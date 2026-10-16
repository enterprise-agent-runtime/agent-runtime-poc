package store

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"warden.dev/warden/internal/store/jcs"
)

// ExportFormat identifies the export file (A04 §13).
const ExportFormat = "warden.audit-export"

// ExportResult is the result of audit.export (A05 §8.10).
type ExportResult struct {
	Path              string `json:"path"`
	Events            int    `json:"events"`
	Artifacts         int    `json:"artifacts"`
	SHA256            string `json:"sha256"`
	Bytes             int64  `json:"bytes"`
	CheckpointEventID string `json:"checkpoint_event_id"`
}

type pubKey struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	PEM       string `json:"pem"`
}

// PublicKeyPEM encodes an Ed25519 public key as PKIX "PUBLIC KEY" PEM.
func PublicKeyPEM(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// ParsePublicKeyPEM decodes a PKIX Ed25519 public key.
func ParsePublicKeyPEM(s string) (ed25519.PublicKey, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		return nil, errors.New("no PEM block")
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an Ed25519 key")
	}
	return pub, nil
}

// KeyID is the first 16 hex characters of SHA-256(raw public key) (A04 §11.2).
func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])[:16]
}

// Export writes a session's audit export as JSON Lines into dir (A04 §13):
// header, events in seq order, artifact records, the newest sys anchor of a
// closed session, trailer. An export checkpoint is appended first when the
// chain head is not already a checkpoint, so the file ends signed. Every
// line is canonical JSON; the file is never overwritten.
func (s *Store) Export(ctx context.Context, sessionID, dir string) (ExportResult, error) {
	var res ExportResult
	info, err := s.Chain(ctx, sessionID)
	if err != nil {
		return res, err
	}
	if info.Chain == SysChain {
		return res, errors.New("store: the sys chain is not exported on its own")
	}
	evs, err := s.loadChain(ctx, sessionID)
	if err != nil {
		return res, err
	}
	if len(evs) > 0 && evs[len(evs)-1].typ != "chain.checkpoint" {
		if info.Status != "open" {
			return res, fmt.Errorf("store: closed session %s does not end with a checkpoint", sessionID)
		}
		if _, err := s.Checkpoint(ctx, sessionID, "export"); err != nil {
			return res, err
		}
		if evs, err = s.loadChain(ctx, sessionID); err != nil {
			return res, err
		}
	}
	var anchor *event
	if info.Status != "open" {
		sys, err := s.loadChain(ctx, SysChain)
		if err != nil {
			return res, err
		}
		for _, e := range sys {
			if e.typ == "chain.checkpoint" && str(e.payload, "chain") == sessionID {
				anchor = e
			}
		}
	}
	artifacts, err := s.artifactRecords(ctx, sessionID)
	if err != nil {
		return res, err
	}
	keys := map[string]bool{}
	var keyList []pubKey
	addKey := func(e *event) {
		id := str(e.payload, "key_id")
		if keys[id] {
			return
		}
		keys[id] = true
		if pub, ok := s.keys[id]; ok {
			p, _ := PublicKeyPEM(pub)
			keyList = append(keyList, pubKey{KeyID: id, Algorithm: "ed25519", PEM: p})
		}
	}
	for _, e := range evs {
		if e.typ == "chain.checkpoint" {
			addKey(e)
		}
	}
	if anchor != nil {
		addKey(anchor)
	}
	if keyList == nil {
		keyList = []pubKey{}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, err
	}
	now := s.opts.Clock().UTC()
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.jsonl", sessionID, now.Format("20060102T150405Z")))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return res, err
	}
	h := sha256.New()
	w := bufio.NewWriter(io.MultiWriter(f, h))
	var n int64
	line := func(v any) error {
		b, err := jcs.Marshal(v)
		if err != nil {
			return err
		}
		n += int64(len(b) + 1)
		_, err = w.Write(append(b, '\n'))
		return err
	}
	last := evs[len(evs)-1]
	err = func() error {
		if err := line(map[string]any{"kind": "header", "format": ExportFormat, "format_version": 1, "session_id": sessionID,
			"exported_at": ts(now), "runtime_version": s.opts.RuntimeVersion, "store_id": s.storeID, "schema_version": s.schemaV,
			"with_blobs": false, "public_keys": keyList}); err != nil {
			return err
		}
		for _, e := range evs {
			if err := line(map[string]any{"kind": "event", "event": e.raw}); err != nil {
				return err
			}
		}
		for _, a := range artifacts {
			if err := line(map[string]any{"kind": "artifact", "record": a}); err != nil {
				return err
			}
		}
		if anchor != nil {
			if err := line(map[string]any{"kind": "anchor", "event": anchor.raw}); err != nil {
				return err
			}
		}
		return line(map[string]any{"kind": "trailer", "events": len(evs), "artifacts": len(artifacts), "last_seq": last.seq,
			"last_hash": last.hash, "checkpoint_event_id": last.id})
	}()
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return res, err
	}
	return ExportResult{Path: path, Events: len(evs), Artifacts: len(artifacts), SHA256: "sha256:" + hex.EncodeToString(h.Sum(nil)),
		Bytes: n, CheckpointEventID: last.id}, nil
}

func (s *Store) artifactRecords(ctx context.Context, sessionID string) ([]json.RawMessage, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT record FROM artifacts WHERE session_id = ? ORDER BY event_seq", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(r))
	}
	return out, rows.Err()
}

// VerifyFile verifies an export file (file mode, A04 §12): the session
// chain, its checkpoints against the header's public keys (plus any keys
// the caller trusts locally), the anchor of a closed session, and with
// strict the INV-A ordering. Grants from other sessions are checked against
// the "external" lines and reported as warnings (OQ-14).
func VerifyFile(path string, localKeys map[string]ed25519.PublicKey, strict bool) (Report, error) {
	var rep Report
	f, err := os.Open(path)
	if err != nil {
		return rep, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var evs []*event
	var anchor *event
	external := map[string]*event{}
	exportKeys := map[string]ed25519.PublicKey{}
	var sessionID string
	sawHeader, sawTrailer := false, false
	var trailer map[string]any
	lineNo := 0
	for sc.Scan() {
		lineNo++
		var l struct {
			Kind       string          `json:"kind"`
			Format     string          `json:"format"`
			SessionID  string          `json:"session_id"`
			PublicKeys []pubKey        `json:"public_keys"`
			Event      json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return rep, fmt.Errorf("line %d: malformed: %w", lineNo, err)
		}
		switch l.Kind {
		case "header":
			if l.Format != ExportFormat || lineNo != 1 {
				return rep, fmt.Errorf("line %d: not a %s header", lineNo, ExportFormat)
			}
			sawHeader, sessionID = true, l.SessionID
			for _, k := range l.PublicKeys {
				if pub, err := ParsePublicKeyPEM(k.PEM); err == nil && KeyID(pub) == k.KeyID {
					exportKeys[k.KeyID] = pub
				}
			}
		case "event", "anchor", "external":
			e, err := parseEvent(l.Event)
			if err != nil {
				return rep, fmt.Errorf("line %d: malformed event: %w", lineNo, err)
			}
			switch l.Kind {
			case "event":
				evs = append(evs, e)
			case "anchor":
				anchor = e
			case "external":
				if h, err := e.selfHash(); err == nil && h == e.hash && e.typ == "approval.resolved" {
					external[str(e.payload, "approval_id")] = e
				}
			}
		case "artifact":
		case "trailer":
			sawTrailer = true
			dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
			dec.UseNumber()
			_ = dec.Decode(&trailer)
		default:
			return rep, fmt.Errorf("line %d: unknown kind %q", lineNo, l.Kind)
		}
	}
	if err := sc.Err(); err != nil {
		return rep, err
	}
	if !sawHeader || !sawTrailer || len(evs) == 0 {
		return rep, errors.New("export is incomplete (header, events and trailer are required)")
	}
	rep.SessionID = sessionID
	rep.Events = len(evs)
	v := &verifier{rep: &rep, strict: strict, keyseen: map[string]bool{},
		keys: func(id string) (ed25519.PublicKey, string, bool) {
			if k, ok := localKeys[id]; ok {
				return k, "local", true
			}
			k, ok := exportKeys[id]
			return k, "export", ok
		},
		external: func(a string) (*event, bool) { e, ok := external[a]; return e, ok },
	}
	v.chainPass(sessionID, evs)
	v.checkpointPass(sessionID, evs)
	last := evs[len(evs)-1]
	if num(trailer["events"]) != int64(len(evs)) || str(trailer, "last_hash") != last.hash {
		v.add(last.seq, "anchor_mismatch", "error", "trailer does not match the events in the file (truncated export)")
	}
	closed := false
	for _, e := range evs {
		if e.typ == "session.close" {
			closed = true
		}
	}
	if closed {
		v.anchorPass(sessionID, evs, anchor)
	}
	if strict {
		v.strictPass(evs)
	}
	v.finish()
	return rep, nil
}
