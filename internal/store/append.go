package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"warden.dev/warden/internal/ids"
	"warden.dev/warden/internal/store/jcs"
)

// MaxBoundedPayload is the largest payload kept inline (A04 §6.4).
const MaxBoundedPayload = 64 << 10

// Actor is the envelope actor (A04 §6.2).
type Actor struct {
	Kind    string  `json:"kind"` // user | agent | runtime | harness
	Name    string  `json:"name"`
	Version *string `json:"version"`
	Digest  *string `json:"digest"`
}

// Runtime returns a runtime actor for the named package.
func Runtime(name, version string) Actor {
	return Actor{Kind: "runtime", Name: name, Version: &version}
}

// Redactions totals the replacements applied to a payload.
type Redactions struct {
	Count int      `json:"count"`
	Types []string `json:"types"`
}

// Envelope is a persisted event (A04 §6.2). All keys are always present;
// Hash is omitted only while hashing.
type Envelope struct {
	V              int             `json:"v"`
	Seq            int64           `json:"seq"`
	ID             string          `json:"id"`
	TS             string          `json:"ts"`
	Type           string          `json:"type"`
	Chain          string          `json:"chain"`
	SessionID      *string         `json:"session_id"`
	WorkflowRunID  *string         `json:"workflow_run_id"`
	TaskID         *string         `json:"task_id"`
	ExecutionID    *string         `json:"execution_id"`
	Actor          Actor           `json:"actor"`
	User           string          `json:"user"`
	WorkspaceID    *string         `json:"workspace_id"`
	Classification *string         `json:"classification"`
	Payload        json.RawMessage `json:"payload"`
	Redactions     Redactions      `json:"redactions"`
	PrevHash       string          `json:"prev_hash"`
	Hash           string          `json:"hash,omitempty"`
}

// Input is an event to append.
type Input struct {
	Type           string
	Chain          string // "sys" or the session id
	RunID          string
	TaskID         string
	ExecutionID    string
	WorkspaceID    string
	Classification string
	Actor          Actor
	Payload        any // a JSON object
	// NewChain creates the session chain row in the same transaction; set it
	// on the first event of a session (session.open).
	NewChain bool
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Append writes one event.
func (s *Store) Append(ctx context.Context, in Input) (Envelope, error) {
	evs, err := s.AppendGroup(ctx, []Input{in})
	if err != nil {
		return Envelope{}, err
	}
	return evs[0], nil
}

// AppendGroup writes several events in one transaction; heads and
// sequence numbers advance only on commit (A04 §10.6). A chain that reaches
// the periodic threshold gets a chain.checkpoint right after.
func (s *Store) AppendGroup(ctx context.Context, ins []Input) ([]Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Envelope
	err := s.inTx(ctx, func(t *txn) error {
		for _, in := range ins {
			e, err := t.append(in)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.periodicLocked(ctx, out); err != nil {
		return out, err
	}
	return out, nil
}

// txn is one writer transaction; seq advances locally and is committed to
// the store only when the transaction commits.
type txn struct {
	s   *Store
	ctx context.Context
	tx  *sql.Tx
	seq int64
}

func (s *Store) inTx(ctx context.Context, fn func(*txn) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	t := &txn{s: s, ctx: ctx, tx: tx, seq: s.nextSeq}
	if err := fn(t); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.nextSeq = t.seq
	return nil
}

func (t *txn) head(chain string) (seq int64, hash string, count int64, status string, err error) {
	err = t.tx.QueryRowContext(t.ctx, "SELECT head_seq, head_hash, event_count, status FROM chains WHERE chain = ?", chain).Scan(&seq, &hash, &count, &status)
	if errors.Is(err, sql.ErrNoRows) {
		err = fmt.Errorf("%w: chain %s", ErrNotFound, chain)
	}
	return
}

func (t *txn) append(in Input) (Envelope, error) {
	s := t.s
	et, ok := s.types[in.Type]
	if !ok {
		return Envelope{}, fmt.Errorf("store: unknown event type %q", in.Type)
	}
	if in.Chain != SysChain && !ids.Valid(ids.Session, in.Chain) {
		return Envelope{}, fmt.Errorf("store: invalid chain %q", in.Chain)
	}
	if in.NewChain {
		g := Genesis(in.Chain)
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO chains (chain, genesis_hash, head_seq, head_hash, event_count, status, created_at) VALUES (?, ?, 0, ?, 0, 'open', ?)",
			in.Chain, g, g, ts(s.opts.Clock())); err != nil {
			return Envelope{}, fmt.Errorf("store: create chain: %w", err)
		}
	}
	_, prev, _, _, err := t.head(in.Chain)
	if err != nil {
		return Envelope{}, err
	}
	payload, red, err := s.preparePayload(in.Payload)
	if err != nil {
		return Envelope{}, err
	}
	var blob string
	blobSize := len(payload)
	if len(payload) > MaxBoundedPayload {
		if !et.spillable {
			return Envelope{}, fmt.Errorf("%w: %s is %d bytes", ErrPayloadTooLarge, in.Type, len(payload))
		}
		blob, err = s.putBlob(payload)
		if err != nil {
			return Envelope{}, err
		}
		payload, _ = jcs.Marshal(map[string]any{"$blob": blob, "size": len(payload)})
	}
	e := Envelope{
		V: 1, Seq: t.seq, ID: s.opts.IDs.New(ids.Event), TS: ts(s.opts.Clock()), Type: in.Type, Chain: in.Chain,
		WorkflowRunID: ptr(in.RunID), TaskID: ptr(in.TaskID), ExecutionID: ptr(in.ExecutionID),
		Actor: in.Actor, User: s.opts.User, Payload: payload, Redactions: red, PrevHash: prev,
	}
	if in.Chain != SysChain {
		e.SessionID = ptr(in.Chain)
		e.WorkspaceID = ptr(in.WorkspaceID)
		e.Classification = ptr(in.Classification)
	} else if in.RunID != "" || in.TaskID != "" || in.ExecutionID != "" {
		return Envelope{}, errors.New("store: sys events carry no run, task or execution")
	}
	canon, err := jcs.Marshal(e)
	if err != nil {
		return Envelope{}, err
	}
	e.Hash = shaBytes(canon)
	stored, err := jcs.Marshal(e)
	if err != nil {
		return Envelope{}, err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO events (seq, id, v, ts, type, chain, session_id, workflow_run_id, task_id, execution_id,
		workspace_id, classification, actor_kind, actor_name, prev_hash, hash, envelope, payload_blob)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Seq, e.ID, e.TS, e.Type, e.Chain, e.SessionID, e.WorkflowRunID, e.TaskID, e.ExecutionID,
		e.WorkspaceID, e.Classification, e.Actor.Kind, e.Actor.Name, e.PrevHash, e.Hash, string(stored), ptr(blob))
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: append %s: %v", ErrUnavailable, in.Type, err)
	}
	if blob != "" {
		if _, err := t.tx.ExecContext(t.ctx, "INSERT OR IGNORE INTO blobs (hash, size_bytes, created_at) VALUES (?, ?, ?)", blob, blobSize, e.TS); err != nil {
			return Envelope{}, err
		}
		if _, err := t.tx.ExecContext(t.ctx, "INSERT OR IGNORE INTO blob_refs (hash, owner_kind, owner_id, session_id) VALUES (?, 'event_payload', ?, ?)", blob, e.ID, e.SessionID); err != nil {
			return Envelope{}, err
		}
	}
	t.seq++
	return e, nil
}

// preparePayload marshals the payload, runs the final redaction pass over
// every string value and returns canonical bytes (core §13.16).
func (s *Store) preparePayload(p any) (json.RawMessage, Redactions, error) {
	red := Redactions{Types: []string{}}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, red, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, red, err
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, red, errors.New("store: payload must be a JSON object")
	}
	if s.opts.Redactor != nil {
		types := map[string]bool{}
		v = redactValue(v, s.opts.Redactor, &red.Count, types)
		for t := range types {
			red.Types = append(red.Types, t)
		}
		sort.Strings(red.Types)
	}
	out, err := jcs.Marshal(v)
	return out, red, err
}

func redactValue(v any, r Redactor, count *int, types map[string]bool) any {
	switch x := v.(type) {
	case string:
		out, ts := r.Redact(x)
		*count += len(ts)
		for _, t := range ts {
			types[t] = true
		}
		return out
	case []any:
		for i := range x {
			x[i] = redactValue(x[i], r, count, types)
		}
	case map[string]any:
		for k := range x {
			x[k] = redactValue(x[k], r, count, types)
		}
	}
	return v
}

// periodicLocked appends periodic checkpoints for chains that reached the
// threshold (A04 §11.1).
func (s *Store) periodicLocked(ctx context.Context, appended []Envelope) error {
	seen := map[string]bool{}
	for _, e := range appended {
		if seen[e.Chain] {
			continue
		}
		seen[e.Chain] = true
		var since int
		if err := s.w.QueryRowContext(ctx, "SELECT events_since_checkpoint FROM chains WHERE chain = ?", e.Chain).Scan(&since); err != nil {
			return err
		}
		if since >= s.opts.CheckpointEvery {
			if err := s.inTx(ctx, func(t *txn) error { _, err := t.checkpoint(e.Chain, e.Chain, "periodic"); return err }); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckpointPayload is the chain.checkpoint payload (A04 §11.2).
type CheckpointPayload struct {
	Chain          string  `json:"chain"`
	LastSeq        int64   `json:"last_seq"`
	LastHash       string  `json:"last_hash"`
	EventCount     int64   `json:"event_count"`
	KeyID          string  `json:"key_id"`
	Trigger        string  `json:"trigger"`
	Signature      *string `json:"signature"`
	UnsignedReason *string `json:"unsigned_reason"`
}

// SigningInput returns JCS(payload without "signature").
func (p CheckpointPayload) SigningInput() ([]byte, error) {
	m := map[string]any{"chain": p.Chain, "last_seq": p.LastSeq, "last_hash": p.LastHash, "event_count": p.EventCount,
		"key_id": p.KeyID, "trigger": p.Trigger, "unsigned_reason": p.UnsignedReason}
	return jcs.Marshal(m)
}

// noKeyID is the key_id of a checkpoint written without any signing key.
const noKeyID = "0000000000000000"

// checkpoint writes chain.checkpoint on chain `on` describing chain `of`
// (equal for a normal checkpoint; on = sys, of = ses_X for an anchor).
func (t *txn) checkpoint(on, of, trigger string) (Envelope, error) {
	seq, hash, count, _, err := t.head(of)
	if err != nil {
		return Envelope{}, err
	}
	if count == 0 {
		return Envelope{}, fmt.Errorf("store: chain %s has no events to checkpoint", of)
	}
	p := CheckpointPayload{Chain: of, LastSeq: seq, LastHash: hash, EventCount: count, KeyID: noKeyID, Trigger: trigger}
	if sg := t.s.opts.Signer; sg != nil {
		p.KeyID = sg.KeyID()
		msg, err := p.SigningInput()
		if err != nil {
			return Envelope{}, err
		}
		if sig, err := sg.Sign(t.ctx, msg); err == nil {
			enc := base64.RawURLEncoding.EncodeToString(sig)
			p.Signature = &enc
		} else {
			r := "keychain_locked"
			p.UnsignedReason = &r
		}
	} else {
		r := "key_missing"
		p.UnsignedReason = &r
	}
	return t.append(Input{Type: "chain.checkpoint", Chain: on, Actor: Runtime("store", t.s.opts.RuntimeVersion), Payload: p})
}

// Checkpoint writes a checkpoint of a chain with the given trigger
// (workflow_end, export, …).
func (s *Store) Checkpoint(ctx context.Context, chain, trigger string) (Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var e Envelope
	err := s.inTx(ctx, func(t *txn) error {
		var err error
		e, err = t.checkpoint(chain, chain, trigger)
		return err
	})
	return e, err
}

// CloseSession writes the session-close group of A04 §10.6:
// session.close (S), chain.checkpoint (S, session_close) and the sys anchor
// (Y), then marks the chain closed.
func (s *Store) CloseSession(ctx context.Context, sessionID, reason string, actor Actor) ([]Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Envelope
	err := s.inTx(ctx, func(t *txn) error {
		_, _, _, status, err := t.head(sessionID)
		if err != nil {
			return err
		}
		if status != "open" {
			return fmt.Errorf("store: session %s is %s", sessionID, status)
		}
		e, err := t.append(Input{Type: "session.close", Chain: sessionID, Actor: actor, Payload: map[string]any{"reason": reason}})
		if err != nil {
			return err
		}
		out = append(out, e)
		if e, err = t.checkpoint(sessionID, sessionID, "session_close"); err != nil {
			return err
		}
		out = append(out, e)
		if e, err = t.checkpoint(SysChain, sessionID, "anchor"); err != nil {
			return err
		}
		out = append(out, e)
		_, err = t.tx.ExecContext(ctx, "UPDATE chains SET status = 'closed' WHERE chain = ?", sessionID)
		return err
	})
	return out, err
}

// ChainInfo describes a chain.
type ChainInfo struct {
	Chain      string
	HeadSeq    int64
	HeadHash   string
	EventCount int64
	Status     string
}

// Chain returns a chain's head.
func (s *Store) Chain(ctx context.Context, chain string) (ChainInfo, error) {
	c := ChainInfo{Chain: chain}
	err := s.r.QueryRowContext(ctx, "SELECT head_seq, head_hash, event_count, status FROM chains WHERE chain = ?", chain).Scan(&c.HeadSeq, &c.HeadHash, &c.EventCount, &c.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("%w: chain %s", ErrNotFound, chain)
	}
	return c, err
}

// Events returns up to limit events of a chain with seq > after, in order.
// An empty types list means all types.
func (s *Store) Events(ctx context.Context, chain string, after int64, limit int, types []string) ([]json.RawMessage, error) {
	q := "SELECT envelope FROM events WHERE chain = ? AND seq > ?"
	args := []any{chain, after}
	if len(types) > 0 {
		q += " AND type IN (" + strings.TrimSuffix(strings.Repeat("?,", len(types)), ",") + ")"
		for _, t := range types {
			args = append(args, t)
		}
	}
	q += " ORDER BY seq"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var env string
		if err := rows.Scan(&env); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(env))
	}
	return out, rows.Err()
}

// AllEvents returns every event with seq > after across chains (for
// event.subscribe with session "*").
func (s *Store) AllEvents(ctx context.Context, after int64, limit int) ([]json.RawMessage, error) {
	rows, err := s.r.QueryContext(ctx, fmt.Sprintf("SELECT envelope FROM events WHERE seq > ? ORDER BY seq LIMIT %d", limit), after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var env string
		if err := rows.Scan(&env); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(env))
	}
	return out, rows.Err()
}

// HeadSeq returns the highest committed sequence number.
func (s *Store) HeadSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextSeq - 1
}
