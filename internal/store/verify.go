package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"warden.dev/warden/internal/ids"
	"warden.dev/warden/internal/store/jcs"
)

// Violation is one finding of audit verify (A05 types.json violation).
type Violation struct {
	Seq      *int64 `json:"seq"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"` // error | warning
	Detail   string `json:"detail"`
}

// KeyReport names a checkpoint key and where it came from.
type KeyReport struct {
	KeyID  string `json:"key_id"`
	Source string `json:"source"` // local | export
	Known  bool   `json:"known"`
}

// Report is the result of audit verify (A05 §8.10 audit.verify).
type Report struct {
	OK               bool        `json:"ok"`
	ChainOK          bool        `json:"chain_ok"`
	StrictOK         *bool       `json:"strict_ok"`
	CheckpointOK     bool        `json:"checkpoint_ok"`
	Events           int         `json:"events"`
	Violations       []Violation `json:"violations"`
	SessionID        string      `json:"session_id,omitempty"`
	Anchored         bool        `json:"anchored"`
	ToolCallsChecked int         `json:"tool_calls_checked"`
	Keys             []KeyReport `json:"keys"`
}

// Violation kinds and their pass (A04 §12).
var errorKindPass = map[string]int{
	"hash_mismatch": 1, "prev_hash_mismatch": 1, "seq_not_increasing": 1, "chain_mismatch": 1, "unknown_event_type": 1,
	"payload_invalid": 1, "projection_mismatch": 1, "blob_mismatch": 1, "artifact_record_mismatch": 1, "anchor_missing": 1, "anchor_mismatch": 1,
	"checkpoint_mismatch": 2, "checkpoint_signature_invalid": 2, "unknown_key": 2,
	"missing_decision": 3, "decision_not_allow": 3, "missing_approval": 3, "approval_scope_mismatch": 3,
}

// event is a parsed envelope.
type event struct {
	raw     map[string]any
	seq     int64
	id      string
	typ     string
	chain   string
	hash    string
	prev    string
	payload map[string]any
}

func parseEvent(b []byte) (*event, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	e := &event{raw: m}
	e.seq = num(m["seq"])
	e.id, _ = m["id"].(string)
	e.typ, _ = m["type"].(string)
	e.chain, _ = m["chain"].(string)
	e.hash, _ = m["hash"].(string)
	e.prev, _ = m["prev_hash"].(string)
	e.payload, _ = m["payload"].(map[string]any)
	return e, nil
}

func num(v any) int64 {
	if n, ok := v.(json.Number); ok {
		i, _ := strconv.ParseInt(string(n), 10, 64)
		return i
	}
	return 0
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// selfHash recomputes sha256(JCS(envelope minus hash)).
func (e *event) selfHash() (string, error) {
	cp := make(map[string]any, len(e.raw))
	for k, v := range e.raw {
		if k != "hash" {
			cp[k] = v
		}
	}
	b, err := jcs.Marshal(cp)
	if err != nil {
		return "", err
	}
	return shaBytes(b), nil
}

// requiredPayload lists the payload fields verify requires per type: the
// fields --strict and the chain checks depend on (A04 §6.5). Full payload
// schema validation lands with the M3 event producers.
var requiredPayload = map[string][]string{
	"policy.decision":    {"decision_id", "effect"},
	"tool.exec.start":    {"call_id", "decision_id", "tool"},
	"tool.exec.end":      {"call_id"},
	"approval.requested": {"approval_id", "scope_max"},
	"approval.resolved":  {"approval_id", "decision"},
	"approval.revoked":   {"approval_id"},
	"chain.checkpoint":   {"chain", "last_seq", "last_hash", "event_count", "key_id"},
	"session.close":      {"reason"},
}

// KnownEventTypes is the PoC registry (design core §5, A04 seed).
var KnownEventTypes = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`runtime.start runtime.stop policy.reload provider.configured workspace.classification session.purged
		session.open session.resume session.request session.close budget.changed workflow.start workflow.end workflow.gate.presented
		workflow.gate.resolved workflow.delivered task.state routing.decision routing.fallback model.call.start model.call.end
		context.assembled context.compacted policy.decision approval.requested approval.resolved approval.revoked tool.exec.start
		tool.exec.end sandbox.create sandbox.destroy sandbox.violation proxy.connect proxy.denied secret.access redaction
		artifact.created artifact.edited worktree.create worktree.checkpoint worktree.remove harness.session.start harness.hook
		harness.session.end chain.checkpoint`) {
		KnownEventTypes[t] = true
	}
}

// verifier holds one verification run.
type verifier struct {
	rep     *Report
	keys    func(keyID string) (ed25519.PublicKey, string, bool)
	keyseen map[string]bool
	strict  bool
	// external maps approval_id → approval.resolved events from other
	// sessions (export "external" lines or store lookups).
	external func(approvalID string) (*event, bool)
}

func (v *verifier) add(seq int64, kind, sev, detail string) {
	var p *int64
	if seq > 0 {
		s := seq
		p = &s
	}
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	v.rep.Violations = append(v.rep.Violations, Violation{Seq: p, Kind: kind, Severity: sev, Detail: detail})
}

// chainPass checks linkage, order, hashes, types and required payload
// fields of one chain (pass 1).
func (v *verifier) chainPass(chain string, evs []*event) {
	prevHash := Genesis(chain)
	var prevSeq int64
	for _, e := range evs {
		if e.chain != chain {
			v.add(e.seq, "chain_mismatch", "error", fmt.Sprintf("event %s is on chain %q, verifying %q", e.id, e.chain, chain))
		}
		sid, _ := e.raw["session_id"].(string)
		if chain == SysChain {
			for _, k := range []string{"session_id", "workflow_run_id", "task_id", "execution_id"} {
				if e.raw[k] != nil {
					v.add(e.seq, "chain_mismatch", "error", "sys event carries "+k)
				}
			}
		} else if sid != chain {
			v.add(e.seq, "chain_mismatch", "error", "session_id differs from the chain")
		}
		if e.seq <= prevSeq {
			v.add(e.seq, "seq_not_increasing", "error", fmt.Sprintf("seq %d after %d", e.seq, prevSeq))
		}
		if e.prev != prevHash {
			v.add(e.seq, "prev_hash_mismatch", "error", fmt.Sprintf("prev_hash %s, expected %s", e.prev, prevHash))
		}
		if h, err := e.selfHash(); err != nil || h != e.hash {
			v.add(e.seq, "hash_mismatch", "error", fmt.Sprintf("stored hash %s, recomputed %s", e.hash, h))
		}
		if !KnownEventTypes[e.typ] {
			v.add(e.seq, "unknown_event_type", "error", "unknown event type "+e.typ)
		}
		if e.payload == nil {
			v.add(e.seq, "payload_invalid", "error", "payload is not an object")
		} else if _, spilled := e.payload["$blob"]; !spilled {
			for _, f := range requiredPayload[e.typ] {
				if _, ok := e.payload[f]; !ok {
					v.add(e.seq, "payload_invalid", "error", e.typ+" payload lacks "+f)
				}
			}
		}
		prevHash, prevSeq = e.hash, e.seq
	}
}

// checkpointPass checks every checkpoint of `chain` written on it (pass 2).
func (v *verifier) checkpointPass(chain string, evs []*event) {
	hashBySeq := map[int64]string{}
	for i, e := range evs {
		hashBySeq[e.seq] = e.hash
		if e.typ != "chain.checkpoint" || str(e.payload, "chain") != chain {
			continue
		}
		last := num(e.payload["last_seq"])
		if hashBySeq[last] != str(e.payload, "last_hash") {
			v.add(e.seq, "checkpoint_mismatch", "error", fmt.Sprintf("checkpoint last_hash does not match event %d", last))
		}
		var count int64
		for _, x := range evs[:i] {
			if x.seq <= last {
				count++
			}
		}
		if count != num(e.payload["event_count"]) {
			v.add(e.seq, "checkpoint_mismatch", "error", fmt.Sprintf("checkpoint counts %s events, chain has %d", e.payload["event_count"], count))
		}
		v.signature(e)
	}
}

// signature verifies a checkpoint's Ed25519 signature.
func (v *verifier) signature(e *event) {
	keyID := str(e.payload, "key_id")
	sig, signed := e.payload["signature"].(string)
	if !signed {
		v.add(e.seq, "checkpoint_unsigned", "warning", "checkpoint is unsigned: "+str(e.payload, "unsigned_reason"))
		return
	}
	pub, source, ok := v.keys(keyID)
	if !v.keyseen[keyID] {
		v.keyseen[keyID] = true
		v.rep.Keys = append(v.rep.Keys, KeyReport{KeyID: keyID, Source: source, Known: ok})
	}
	if !ok {
		v.add(e.seq, "unknown_key", "error", "no public key for key_id "+keyID)
		return
	}
	p := CheckpointPayload{Chain: str(e.payload, "chain"), LastSeq: num(e.payload["last_seq"]), LastHash: str(e.payload, "last_hash"),
		EventCount: num(e.payload["event_count"]), KeyID: keyID, Trigger: str(e.payload, "trigger")}
	if r, ok := e.payload["unsigned_reason"].(string); ok {
		p.UnsignedReason = &r
	}
	msg, _ := p.SigningInput()
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !ed25519.Verify(pub, msg, raw) {
		v.add(e.seq, "checkpoint_signature_invalid", "error", "signature does not verify with key "+keyID)
	}
}

// anchorPass checks a closed session against its newest sys anchor.
func (v *verifier) anchorPass(chain string, evs []*event, anchor *event) {
	if anchor == nil {
		v.add(0, "anchor_missing", "error", "closed session has no sys anchor")
		return
	}
	v.rep.Anchored = true
	if h, err := anchor.selfHash(); err != nil || h != anchor.hash {
		v.add(anchor.seq, "hash_mismatch", "error", "anchor event hash does not verify")
	}
	v.signature(anchor)
	last := evs[len(evs)-1]
	if str(anchor.payload, "last_hash") != last.hash || num(anchor.payload["event_count"]) != int64(len(evs)) {
		v.add(anchor.seq, "anchor_mismatch", "error", fmt.Sprintf("anchor names %s/%s events, chain head is %s/%d events",
			str(anchor.payload, "last_hash"), anchor.payload["event_count"], last.hash, len(evs)))
	}
}

// strictPass enforces INV-A / CF-40 over one session chain (pass 3).
func (v *verifier) strictPass(evs []*event) {
	decisions := map[string]*event{} // call_id → latest policy.decision
	resolved := map[string]*event{}  // approval_id → approval.resolved
	requested := map[string]*event{} // approval_id → approval.requested
	revoked := map[string]bool{}
	usedOnce := map[string]bool{}
	started := map[string]bool{}
	for _, e := range evs {
		p := e.payload
		switch e.typ {
		case "policy.decision":
			if c := str(p, "call_id"); c != "" {
				decisions[c] = e
			}
		case "approval.requested":
			requested[str(p, "approval_id")] = e
		case "approval.resolved":
			resolved[str(p, "approval_id")] = e
		case "approval.revoked":
			revoked[str(p, "approval_id")] = true
		case "tool.exec.end":
			if !started[str(p, "call_id")] {
				v.add(e.seq, "orphan_exec_end", "warning", "tool.exec.end without tool.exec.start for "+str(p, "call_id"))
			}
		case "tool.exec.start":
			v.rep.ToolCallsChecked++
			call := str(p, "call_id")
			started[call] = true
			d, ok := decisions[call]
			if !ok {
				v.add(e.seq, "missing_decision", "error", "tool.exec.start without an earlier policy.decision for "+call)
				continue
			}
			if str(d.payload, "effect") != "allow" || str(d.payload, "decision_id") != str(p, "decision_id") {
				v.add(e.seq, "decision_not_allow", "error", fmt.Sprintf("latest decision for %s is %s %s; tool.exec.start cites %s",
					call, str(d.payload, "decision_id"), str(d.payload, "effect"), str(p, "decision_id")))
				continue
			}
			if a := str(d.payload, "resolved_by_approval"); a != "" {
				r, ok := resolved[a]
				if !ok || str(r.payload, "decision") != "approve" || r.seq > d.seq {
					v.add(e.seq, "missing_approval", "error", "decision cites approval "+a+" without an earlier approval.resolved(approve)")
				} else if str(r.payload, "scope") == "once" {
					rq := requested[a]
					if usedOnce[a] || (rq != nil && str(rq.payload, "call_id") != "" && str(rq.payload, "call_id") != call) {
						v.add(e.seq, "approval_scope_mismatch", "error", "once approval "+a+" used for another call or reused")
					}
					usedOnce[a] = true
				}
			}
			rules, _ := d.payload["matched_rules"].([]any)
			for _, r := range rules {
				rule, _ := r.(string)
				a, ok := strings.CutPrefix(rule, "grant.")
				if !ok || a == str(d.payload, "resolved_by_approval") {
					continue
				}
				if res, ok := resolved[a]; ok {
					if str(res.payload, "decision") != "approve" || res.seq > d.seq || revoked[a] {
						v.add(e.seq, "missing_approval", "error", "grant "+a+" is not an earlier, unrevoked approval")
					}
				} else if ext, ok := v.external(a); ok && str(ext.payload, "decision") == "approve" {
					v.add(e.seq, "external_grant", "warning", "grant "+a+" originated in another session")
				} else {
					v.add(e.seq, "missing_approval", "error", "grant "+a+" has no approval.resolved(approve)")
				}
			}
		}
	}
}

// finish computes the ok flags from the violations.
func (v *verifier) finish() {
	r := v.rep
	r.ChainOK, r.CheckpointOK = true, true
	strictOK := true
	for _, x := range r.Violations {
		if x.Severity != "error" {
			continue
		}
		switch errorKindPass[x.Kind] {
		case 1:
			r.ChainOK = false
		case 2:
			r.CheckpointOK = false
		case 3:
			strictOK = false
		}
	}
	if v.strict {
		r.StrictOK = &strictOK
	}
	r.OK = r.ChainOK && r.CheckpointOK && (!v.strict || strictOK)
	if r.Violations == nil {
		r.Violations = []Violation{}
	}
	if r.Keys == nil {
		r.Keys = []KeyReport{}
	}
}

// Verify checks one session chain in the store (store mode, A04 §12):
// linkage and hashes, projections, checkpoints and signatures, the sys
// anchor of a closed session (verifying the whole sys chain), and with
// strict the INV-A ordering of every tool.exec.start.
func (s *Store) Verify(ctx context.Context, sessionID string, strict bool) (Report, error) {
	rep := Report{SessionID: sessionID}
	info, err := s.Chain(ctx, sessionID)
	if err != nil {
		return rep, err
	}
	evs, err := s.loadChain(ctx, sessionID)
	if err != nil {
		return rep, err
	}
	rep.Events = len(evs)
	v := &verifier{rep: &rep, strict: strict, keyseen: map[string]bool{},
		keys:     func(id string) (ed25519.PublicKey, string, bool) { k, ok := s.keys[id]; return k, "local", ok },
		external: func(a string) (*event, bool) { return s.findResolved(ctx, a) },
	}
	v.chainPass(sessionID, evs)
	if err := s.projectionPass(ctx, v, sessionID); err != nil {
		return rep, err
	}
	v.checkpointPass(sessionID, evs)
	if info.Status != "open" {
		sys, err := s.loadChain(ctx, SysChain)
		if err != nil {
			return rep, err
		}
		v.chainPass(SysChain, sys)
		v.checkpointPass(SysChain, sys)
		var anchor *event
		for _, e := range sys {
			if e.typ == "chain.checkpoint" && str(e.payload, "chain") == sessionID {
				anchor = e
			}
		}
		if len(evs) > 0 {
			v.anchorPass(sessionID, evs, anchor)
		}
	}
	if strict {
		v.strictPass(evs)
	}
	v.finish()
	return rep, nil
}

func (s *Store) loadChain(ctx context.Context, chain string) ([]*event, error) {
	raws, err := s.Events(ctx, chain, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	out := make([]*event, 0, len(raws))
	for _, r := range raws {
		e, err := parseEvent(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// projectionPass compares indexed columns with the envelope (store only).
func (s *Store) projectionPass(ctx context.Context, v *verifier, chain string) error {
	rows, err := s.r.QueryContext(ctx, `SELECT seq, id, type, chain, coalesce(session_id,''), coalesce(task_id,''), actor_kind, prev_hash, hash, envelope
		FROM events WHERE chain = ? ORDER BY seq`, chain)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var id, typ, ch, sid, tid, actor, prev, hash, env string
		if err := rows.Scan(&seq, &id, &typ, &ch, &sid, &tid, &actor, &prev, &hash, &env); err != nil {
			return err
		}
		e, err := parseEvent([]byte(env))
		if err != nil {
			v.add(seq, "projection_mismatch", "error", "envelope is not JSON")
			continue
		}
		actorKind := ""
		if a, ok := e.raw["actor"].(map[string]any); ok {
			actorKind = str(a, "kind")
		}
		tidEnv, _ := e.raw["task_id"].(string)
		sidEnv, _ := e.raw["session_id"].(string)
		if e.seq != seq || e.id != id || e.typ != typ || e.chain != ch || sidEnv != sid || tidEnv != tid || actorKind != actor || e.prev != prev || e.hash != hash {
			v.add(seq, "projection_mismatch", "error", "indexed columns differ from the envelope of "+id)
		}
	}
	return rows.Err()
}

// findResolved looks up an approval.resolved for an approval id on any
// session chain of the store, checking its self-hash.
func (s *Store) findResolved(ctx context.Context, approvalID string) (*event, bool) {
	if !ids.Valid(ids.Approval, approvalID) {
		return nil, false
	}
	var env string
	err := s.r.QueryRowContext(ctx, "SELECT envelope FROM events WHERE approval_id = ? AND type = 'approval.resolved' ORDER BY seq DESC LIMIT 1", approvalID).Scan(&env)
	if err != nil {
		return nil, false
	}
	e, err := parseEvent([]byte(env))
	if err != nil {
		return nil, false
	}
	if h, err := e.selfHash(); err != nil || h != e.hash {
		return nil, false
	}
	return e, true
}
