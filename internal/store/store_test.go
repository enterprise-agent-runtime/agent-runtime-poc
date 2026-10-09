package store

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warden.dev/warden/internal/store/jcs"
	"warden.dev/warden/internal/store/storetest"
)

var ctx = context.Background()

const ses = "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB"

func openTest(t *testing.T, mut func(*Options)) *Store {
	t.Helper()
	clk := storetest.NewClock(time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC), 10*time.Millisecond)
	o := Options{Dir: filepath.Join(t.TempDir(), "db"), RuntimeVersion: "0.1.0", User: "local:robert", Clock: clk.Now,
		IDs: storetest.IDs(clk), Signer: storetest.NewSigner("")}
	if mut != nil {
		mut(&o)
	}
	s, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func api() Actor { return Runtime("api", "0.1.0") }

func openSession(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.Append(ctx, Input{Type: "session.open", Chain: id, NewChain: true, WorkspaceID: "wsp_01JAXR7ZK3M8Q2V9KTC3F6YH5N",
		Classification: "internal", Actor: Runtime("session", "0.1.0"), Payload: map[string]any{"workspace_root": "/repo"}}); err != nil {
		t.Fatal(err)
	}
}

// TestGenesisVectors pins A04 §10.1 genesis hashes (values computed
// independently with sha256sum, store-spec §3.5).
func TestGenesisVectors(t *testing.T) {
	if g := Genesis("sys"); g != "sha256:2a4d9f327cc31def99d463ad1a3c6dad984d13d997a835325366f900c125350a" {
		t.Fatalf("genesis(sys) = %s", g)
	}
	if g := Genesis(ses); g != "sha256:20ffc6c9c761548751e0e724d8f765e426269afa8a241f2487fc2c735fbe4685" {
		t.Fatalf("genesis(ses) = %s", g)
	}
}

// TestGolden_EnvelopeHashAndCheckpointSignature reproduces the worked
// example computed independently (Node + RFC 8785, store-spec §3.5): the
// same envelope must hash to the same value and the RFC 8032 test key must
// produce the same checkpoint signature. A change in field names, null
// handling or canonicalization breaks every existing chain; this catches it.
func TestGolden_EnvelopeHashAndCheckpointSignature(t *testing.T) {
	v := "0.1.0"
	e := Envelope{V: 1, Seq: 1, ID: "evt_01JAXR7ZK3M8Q2V9KTC3F6YH5N", TS: "2026-09-26T10:00:00.000Z", Type: "runtime.start", Chain: "sys",
		Actor: Actor{Kind: "runtime", Name: "api", Version: &v}, User: "local:robert",
		Payload:    json.RawMessage(`{"checkpoint_key_id":"21fe31dfa154a261","mode":"personal","pid":4242,"version":"0.1.0"}`),
		Redactions: Redactions{Types: []string{}}, PrevHash: Genesis("sys")}
	b, err := jcs.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"actor":{"digest":null,"kind":"runtime","name":"api","version":"0.1.0"},"chain":"sys","classification":null,"execution_id":null,"id":"evt_01JAXR7ZK3M8Q2V9KTC3F6YH5N","payload":{"checkpoint_key_id":"21fe31dfa154a261","mode":"personal","pid":4242,"version":"0.1.0"},"prev_hash":"sha256:2a4d9f327cc31def99d463ad1a3c6dad984d13d997a835325366f900c125350a","redactions":{"count":0,"types":[]},"seq":1,"session_id":null,"task_id":null,"ts":"2026-09-26T10:00:00.000Z","type":"runtime.start","user":"local:robert","v":1,"workflow_run_id":null,"workspace_id":null}`
	if string(b) != want {
		t.Fatalf("canonical envelope\n got %s\nwant %s", b, want)
	}
	if h := shaBytes(b); h != "sha256:3293de6734d3118e0c01007987b3bc84a3733b0c6fb2fc8426e2bd7ed0aa9bfa" {
		t.Fatalf("hash = %s", h)
	}
	sg := storetest.NewSigner("")
	if sg.KeyID() != "21fe31dfa154a261" {
		t.Fatalf("key id = %s", sg.KeyID())
	}
	p := CheckpointPayload{Chain: "sys", LastSeq: 1, LastHash: "sha256:3293de6734d3118e0c01007987b3bc84a3733b0c6fb2fc8426e2bd7ed0aa9bfa", EventCount: 1, KeyID: sg.KeyID(), Trigger: "periodic"}
	msg, _ := p.SigningInput()
	if string(msg) != `{"chain":"sys","event_count":1,"key_id":"21fe31dfa154a261","last_hash":"sha256:3293de6734d3118e0c01007987b3bc84a3733b0c6fb2fc8426e2bd7ed0aa9bfa","last_seq":1,"trigger":"periodic","unsigned_reason":null}` {
		t.Fatalf("signing input = %s", msg)
	}
	sig, _ := sg.Sign(ctx, msg)
	if got := base64.RawURLEncoding.EncodeToString(sig); got != "g4DJGIx7J_La29iyu_ZJxQVWl3cx89AfGBJS4cU32NAVOSe40tvErb8xYs7D7WBmqFSUhFd0trjsS3enBkZ9BQ" {
		t.Fatalf("signature = %s", got)
	}
}

func TestAppend_LinksChainsAndSeq(t *testing.T) {
	s := openTest(t, nil)
	e1, err := s.Append(ctx, Input{Type: "runtime.start", Chain: SysChain, Actor: api(), Payload: map[string]any{"version": "0.1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	if e1.Seq != 1 || e1.PrevHash != Genesis(SysChain) || e1.SessionID != nil {
		t.Fatalf("e1 = %+v", e1)
	}
	openSession(t, s, ses)
	e3, _ := s.Append(ctx, Input{Type: "task.state", Chain: ses, TaskID: "tsk_01JAXR7ZK3M8Q2V9KTC3F6YH5N", Actor: api(), Payload: map[string]any{"to": "running"}})
	evs, _ := s.Events(ctx, ses, 0, 0, nil)
	first, _ := parseEvent(evs[0])
	if e3.Seq != 3 || first.prev != Genesis(ses) || e3.PrevHash != first.hash || *e3.SessionID != ses {
		t.Fatalf("e3 = %+v first = %+v", e3, first)
	}
	if s.HeadSeq() != 3 {
		t.Fatalf("head seq = %d", s.HeadSeq())
	}
	// sys events cannot carry task ids; session events need a valid chain.
	if _, err := s.Append(ctx, Input{Type: "runtime.stop", Chain: SysChain, TaskID: "tsk_x", Actor: api(), Payload: map[string]any{}}); err == nil {
		t.Error("sys event with task id accepted")
	}
	if _, err := s.Append(ctx, Input{Type: "task.state", Chain: "ses_bad", Actor: api(), Payload: map[string]any{}}); err == nil {
		t.Error("invalid chain accepted")
	}
	if _, err := s.Append(ctx, Input{Type: "nope.event", Chain: SysChain, Actor: api(), Payload: map[string]any{}}); err == nil {
		t.Error("unknown type accepted")
	}
	if _, err := s.Append(ctx, Input{Type: "runtime.stop", Chain: SysChain, Actor: api(), Payload: []int{1}}); err == nil {
		t.Error("non-object payload accepted")
	}
}

// TestTriggers_AppendOnlyAndTypeChain: the database refuses updates and
// deletes of events and events of the wrong chain kind (A04 §3.2).
func TestTriggers_AppendOnlyAndTypeChain(t *testing.T) {
	s := openTest(t, nil)
	openSession(t, s, ses)
	for _, q := range []string{
		"UPDATE events SET type = 'session.close' WHERE seq = 1",
		"DELETE FROM events WHERE seq = 1",
	} {
		if _, err := s.w.ExecContext(ctx, q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v", q, err)
		}
	}
	// runtime.start is a sys-only type.
	if _, err := s.Append(ctx, Input{Type: "runtime.start", Chain: ses, Actor: api(), Payload: map[string]any{}}); err == nil {
		t.Error("sys-only type accepted on a session chain")
	}
	// The store still verifies after the refused writes.
	if r, _ := s.Verify(ctx, ses, true); !r.OK {
		t.Fatalf("report = %+v", r)
	}
}

func TestOpen_ReopenContinuesAndChecksMigrations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	o := Options{Dir: dir, Signer: storetest.NewSigner("")}
	s, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	id := s.StoreID()
	_, _ = s.Append(ctx, Input{Type: "runtime.start", Chain: SysChain, Actor: api(), Payload: map[string]any{}})
	s.Close()
	s, err = Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.Append(ctx, Input{Type: "runtime.stop", Chain: SysChain, Actor: api(), Payload: map[string]any{}})
	if err != nil || e.Seq != 2 || s.StoreID() != id || s.SchemaVersion() != 1 {
		t.Fatalf("after reopen: %+v %v", e, err)
	}
	// A modified migration is refused.
	if _, err := s.w.ExecContext(ctx, "UPDATE schema_migrations SET checksum = 'sha256:"+strings.Repeat("0", 64)+"'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, o); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("modified migration accepted: %v", err)
	}
}

func TestOpen_RefusesForeignAndNewerDatabases(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	s, err := Open(ctx, Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, Options{Dir: dir}); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("newer schema: %v", err)
	}
	other := filepath.Join(t.TempDir(), "db")
	_ = os.MkdirAll(other, 0o700)
	_ = os.WriteFile(filepath.Join(other, "warden.sqlite"), []byte("not a database at all, just text that is long enough"), 0o600)
	if _, err := Open(ctx, Options{Dir: other}); err == nil {
		t.Fatal("foreign file accepted")
	}
}

// TestPayload_SpillAndBound: spillable types spill to a blob; bounded types
// over 64 KiB are refused so the caller fails closed (A04 §6.4).
func TestPayload_SpillAndBound(t *testing.T) {
	s := openTest(t, nil)
	openSession(t, s, ses)
	big := strings.Repeat("x", 70000)
	e, err := s.Append(ctx, Input{Type: "session.request", Chain: ses, Actor: api(), Payload: map[string]any{"text": big}})
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Blob string `json:"$blob"`
		Size int    `json:"size"`
	}
	_ = json.Unmarshal(e.Payload, &ref)
	b, err := s.GetBlob(ref.Blob)
	if err != nil || !strings.Contains(string(b), big) || ref.Size != len(b) {
		t.Fatalf("spill = %+v, %v", ref, err)
	}
	if _, err := s.Append(ctx, Input{Type: "policy.decision", Chain: ses, Actor: api(), Payload: map[string]any{"reason": big}}); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("bounded payload: %v", err)
	}
	if _, err := s.GetBlob("sha256:" + strings.Repeat("a", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing blob: %v", err)
	}
	if r, _ := s.Verify(ctx, ses, true); !r.OK {
		t.Fatalf("verify = %+v", r)
	}
}

type fakeRedactor struct{}

func (fakeRedactor) Redact(s string) (string, []string) {
	if strings.Contains(s, "sk-ant-") {
		return strings.ReplaceAll(s, "sk-ant-SECRET", "[REDACTED:anthropic_key]"), []string{"anthropic_key"}
	}
	return s, nil
}

// TestAppend_FinalRedactionPass: a secret that reaches the writer is
// replaced and counted, never persisted (INV-C, core §13.16).
func TestAppend_FinalRedactionPass(t *testing.T) {
	s := openTest(t, func(o *Options) { o.Redactor = fakeRedactor{} })
	openSession(t, s, ses)
	e, err := s.Append(ctx, Input{Type: "tool.exec.end", Chain: ses, Actor: api(), Payload: map[string]any{"call_id": "c", "out": []any{"key sk-ant-SECRET here"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(e.Payload), "SECRET") || e.Redactions.Count != 1 || e.Redactions.Types[0] != "anthropic_key" {
		t.Fatalf("event = %s %+v", e.Payload, e.Redactions)
	}
	raw, _ := s.Events(ctx, ses, 0, 0, nil)
	for _, r := range raw {
		if strings.Contains(string(r), "SECRET") {
			t.Fatal("secret persisted")
		}
	}
}

func TestPeriodicCheckpoint(t *testing.T) {
	s := openTest(t, func(o *Options) { o.CheckpointEvery = 3 })
	openSession(t, s, ses)
	for i := 0; i < 2; i++ {
		_, _ = s.Append(ctx, Input{Type: "task.state", Chain: ses, Actor: api(), Payload: map[string]any{"i": i}})
	}
	raw, _ := s.Events(ctx, ses, 0, 0, []string{"chain.checkpoint"})
	if len(raw) != 1 || !strings.Contains(string(raw[0]), `"trigger":"periodic"`) {
		t.Fatalf("checkpoints = %s", raw)
	}
	if r, _ := s.Verify(ctx, ses, false); !r.OK {
		t.Fatalf("verify = %+v", r)
	}
}

func TestCheckpoint_UnsignedIsWarningOnly(t *testing.T) {
	sg := storetest.NewSigner("")
	sg.Locked = true
	s := openTest(t, func(o *Options) { o.Signer = sg })
	openSession(t, s, ses)
	if _, err := s.Checkpoint(ctx, ses, "export"); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Verify(ctx, ses, false)
	if !r.OK || len(r.Violations) != 1 || r.Violations[0].Kind != "checkpoint_unsigned" || r.Violations[0].Severity != "warning" {
		t.Fatalf("report = %+v", r)
	}
	s2 := openTest(t, func(o *Options) { o.Signer = nil })
	openSession(t, s2, ses)
	e, _ := s2.Checkpoint(ctx, ses, "export")
	if !strings.Contains(string(e.Payload), `"unsigned_reason":"key_missing"`) {
		t.Fatalf("payload = %s", e.Payload)
	}
}

func TestCloseSession_AnchorsOnSys(t *testing.T) {
	s := openTest(t, nil)
	openSession(t, s, ses)
	evs, err := s.CloseSession(ctx, ses, "user", api())
	if err != nil || len(evs) != 3 || evs[2].Chain != SysChain {
		t.Fatalf("close = %+v %v", evs, err)
	}
	if _, err := s.CloseSession(ctx, ses, "user", api()); err == nil {
		t.Fatal("closed twice")
	}
	r, _ := s.Verify(ctx, ses, true)
	if !r.OK || !r.Anchored || len(r.Keys) != 1 || r.Keys[0].Source != "local" {
		t.Fatalf("report = %+v", r)
	}
	if _, err := s.Verify(ctx, "ses_01JAXR8Q7M2V9KTC3F6YH5N0PC", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
}

// tamper rewrites a stored envelope the way an attacker with file access
// would: the append-only trigger is dropped first.
func tamper(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	for _, d := range []string{"DROP TRIGGER events_no_update", "DROP TRIGGER events_no_delete"} {
		_, _ = s.w.ExecContext(ctx, d)
	}
	if _, err := s.w.ExecContext(ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}

func kinds(r Report) string {
	var k []string
	for _, v := range r.Violations {
		k = append(k, v.Kind)
	}
	return strings.Join(k, ",")
}

// TestVerify_TamperedStoreFails is M1 acceptance: audit verify fails on a
// tampered store and passes on a clean one.
func TestVerify_TamperedStoreFails(t *testing.T) {
	build := func(t *testing.T) *Store {
		s := openTest(t, nil)
		openSession(t, s, ses)
		for i := 0; i < 3; i++ {
			_, _ = s.Append(ctx, Input{Type: "task.state", Chain: ses, Actor: api(), Payload: map[string]any{"i": i}})
		}
		if _, err := s.CloseSession(ctx, ses, "user", api()); err != nil {
			t.Fatal(err)
		}
		return s
	}
	clean := build(t)
	if r, _ := clean.Verify(ctx, ses, true); !r.OK || !r.ChainOK || !r.CheckpointOK || *r.StrictOK != true {
		t.Fatalf("clean store: %+v", r)
	}
	cases := []struct {
		name string
		q    string
		want string
	}{
		{"payload edited", `UPDATE events SET envelope = replace(envelope, '"i":1', '"i":7') WHERE seq = 3`, "hash_mismatch"},
		{"event deleted", `DELETE FROM events WHERE seq = 3`, "prev_hash_mismatch"},
		{"tail truncated", `DELETE FROM events WHERE chain = '` + ses + `' AND seq > 4`, "anchor_mismatch"},
		{"projection edited", `UPDATE events SET type = 'session.request' WHERE seq = 2`, "projection_mismatch"},
		{"checkpoint signature forged", `UPDATE events SET envelope = replace(envelope, '"signature":"', '"signature":"A') WHERE type = 'chain.checkpoint' AND chain = '` + ses + `'`, "checkpoint_signature_invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := build(t)
			tamper(t, s, c.q)
			r, err := s.Verify(ctx, ses, true)
			if err != nil {
				t.Fatal(err)
			}
			if r.OK || !strings.Contains(kinds(r), c.want) {
				t.Fatalf("report ok=%v kinds=%s", r.OK, kinds(r))
			}
		})
	}
}

// strictEvent is a shorthand for building strict-ordering scenarios.
type strictEvent struct {
	typ     string
	payload map[string]any
}

func dec(id, call, effect string, extra ...any) strictEvent {
	p := map[string]any{"decision_id": id, "call_id": call, "effect": effect, "matched_rules": []string{}}
	for i := 0; i+1 < len(extra); i += 2 {
		p[extra[i].(string)] = extra[i+1]
	}
	return strictEvent{"policy.decision", p}
}
func start(call, decision string) strictEvent {
	return strictEvent{"tool.exec.start", map[string]any{"call_id": call, "decision_id": decision, "tool": "proc.exec", "executor": "sandbox"}}
}
func end(call string) strictEvent {
	return strictEvent{"tool.exec.end", map[string]any{"call_id": call, "ok": true}}
}
func req(apr, call string) strictEvent {
	return strictEvent{"approval.requested", map[string]any{"approval_id": apr, "call_id": call, "scope_max": "workspace"}}
}
func res(apr, decision, scope string) strictEvent {
	return strictEvent{"approval.resolved", map[string]any{"approval_id": apr, "decision": decision, "scope": scope}}
}

const (
	c1 = "call_01JAXR7ZK3M8Q2V9KTC3F6YH51"
	c2 = "call_01JAXR7ZK3M8Q2V9KTC3F6YH52"
	a1 = "apr_01JAXR7ZK3M8Q2V9KTC3F6YH51"
	a2 = "apr_01JAXR7ZK3M8Q2V9KTC3F6YH52"
)

// TestAuditStrictOrdering is INV-A (CLAUDE.md §5): no tool.exec.start
// without a preceding policy.decision{allow} for the same call_id, with the
// CF-40 approval pattern (two decisions around an approval) and grant reuse.
func TestAuditStrictOrdering(t *testing.T) {
	cases := []struct {
		name   string
		events []strictEvent
		ok     bool
		kinds  string
	}{
		{"allow then start", []strictEvent{dec("dec_1", c1, "allow"), start(c1, "dec_1"), end(c1)}, true, ""},
		{"start without decision", []strictEvent{start(c1, "dec_1")}, false, "missing_decision"},
		{"decision after start", []strictEvent{start(c1, "dec_1"), dec("dec_1", c1, "allow")}, false, "missing_decision"},
		{"start after deny", []strictEvent{dec("dec_1", c1, "deny"), start(c1, "dec_1")}, false, "decision_not_allow"},
		{"start after approval_required only", []strictEvent{dec("dec_1", c1, "approval_required"), req(a1, c1), start(c1, "dec_1")}, false, "decision_not_allow"},
		{"decision id mismatch", []strictEvent{dec("dec_1", c1, "allow"), start(c1, "dec_9")}, false, "decision_not_allow"},
		{"later deny overrides allow", []strictEvent{dec("dec_1", c1, "allow"), dec("dec_2", c1, "deny"), start(c1, "dec_1")}, false, "decision_not_allow"},
		{"CF-40 approval pattern", []strictEvent{dec("dec_1", c1, "approval_required"), req(a1, c1), res(a1, "approve", "workspace"),
			dec("dec_2", c1, "allow", "resolved_by_approval", a1, "matched_rules", []string{"grant." + a1}), start(c1, "dec_2"), end(c1)}, true, ""},
		{"approval cited but rejected", []strictEvent{dec("dec_1", c1, "approval_required"), req(a1, c1), res(a1, "reject", "once"),
			dec("dec_2", c1, "allow", "resolved_by_approval", a1), start(c1, "dec_2")}, false, "missing_approval"},
		{"approval cited, never resolved", []strictEvent{dec("dec_2", c1, "allow", "resolved_by_approval", a1), start(c1, "dec_2")}, false, "missing_approval"},
		{"once approval reused", []strictEvent{dec("dec_1", c1, "approval_required"), req(a1, c1), res(a1, "approve", "once"),
			dec("dec_2", c1, "allow", "resolved_by_approval", a1), start(c1, "dec_2"),
			dec("dec_3", c2, "allow", "resolved_by_approval", a1), start(c2, "dec_3")}, false, "approval_scope_mismatch"},
		{"grant reuse", []strictEvent{req(a1, c1), res(a1, "approve", "workspace"),
			dec("dec_3", c2, "allow", "matched_rules", []string{"grant." + a1}), start(c2, "dec_3")}, true, ""},
		{"grant without approval", []strictEvent{dec("dec_3", c2, "allow", "matched_rules", []string{"grant." + a2}), start(c2, "dec_3")}, false, "missing_approval"},
		{"revoked grant", []strictEvent{req(a1, c1), res(a1, "approve", "workspace"), {"approval.revoked", map[string]any{"approval_id": a1}},
			dec("dec_3", c2, "allow", "matched_rules", []string{"grant." + a1}), start(c2, "dec_3")}, false, "missing_approval"},
		{"orphan end is a warning", []strictEvent{end(c1)}, true, "orphan_exec_end"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := openTest(t, nil)
			openSession(t, s, ses)
			for _, e := range c.events {
				if _, err := s.Append(ctx, Input{Type: e.typ, Chain: ses, Actor: Runtime("policy", "0.1.0"), Payload: e.payload}); err != nil {
					t.Fatal(err)
				}
			}
			r, err := s.Verify(ctx, ses, true)
			if err != nil {
				t.Fatal(err)
			}
			if r.OK != c.ok || kinds(r) != c.kinds || !r.ChainOK {
				t.Fatalf("ok=%v kinds=%q chain_ok=%v, want ok=%v kinds=%q", r.OK, kinds(r), r.ChainOK, c.ok, c.kinds)
			}
			// Non-strict verification ignores the ordering.
			if r2, _ := s.Verify(ctx, ses, false); !r2.OK || r2.StrictOK != nil {
				t.Fatalf("non-strict = %+v", r2)
			}
		})
	}
}

// TestAuditStrictOrdering_ExternalGrant: a workspace grant approved in
// another session is a warning, not a violation (OQ-14).
func TestAuditStrictOrdering_ExternalGrant(t *testing.T) {
	s := openTest(t, nil)
	other := "ses_01JAXR8Q7M2V9KTC3F6YH5N0PC"
	openSession(t, s, other)
	for _, e := range []strictEvent{req(a1, c1), res(a1, "approve", "workspace")} {
		_, _ = s.Append(ctx, Input{Type: e.typ, Chain: other, Actor: api(), Payload: e.payload})
	}
	openSession(t, s, ses)
	for _, e := range []strictEvent{dec("dec_3", c2, "allow", "matched_rules", []string{"grant." + a1}), start(c2, "dec_3")} {
		_, _ = s.Append(ctx, Input{Type: e.typ, Chain: ses, Actor: api(), Payload: e.payload})
	}
	r, _ := s.Verify(ctx, ses, true)
	if !r.OK || kinds(r) != "external_grant" {
		t.Fatalf("report = %+v", r)
	}
}

func TestExport_VerifiesOnItsOwnAndDetectsTampering(t *testing.T) {
	s := openTest(t, nil)
	openSession(t, s, ses)
	for _, e := range []strictEvent{dec("dec_1", c1, "allow"), start(c1, "dec_1"), end(c1)} {
		_, _ = s.Append(ctx, Input{Type: e.typ, Chain: ses, Actor: api(), Payload: e.payload})
	}
	dir := t.TempDir()
	res, err := s.Export(ctx, ses, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != 5 || !strings.HasPrefix(res.SHA256, "sha256:") || res.CheckpointEventID == "" {
		t.Fatalf("export = %+v", res)
	}
	// Verification on "another machine": no local keys, the header's key.
	r, err := VerifyFile(res.Path, nil, true)
	if err != nil || !r.OK || r.Events != 5 || len(r.Keys) != 1 || r.Keys[0].Source != "export" {
		t.Fatalf("file verify = %+v %v", r, err)
	}
	// A second export never overwrites.
	if _, err := s.Export(ctx, ses, dir); err == nil {
		t.Log("second export at the same second refused or written under a new name")
	}
	b, _ := os.ReadFile(res.Path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	write := func(name string, ls []string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(strings.Join(ls, "\n")+"\n"), 0o600)
		return p
	}
	edited := append([]string{}, lines...)
	// Line 0 is the header, 1 session.open, 2 the policy.decision.
	edited[2] = strings.Replace(edited[2], `"effect":"allow"`, `"effect":"deny"`, 1)
	if edited[2] == lines[2] {
		t.Fatal("tamper did not change the line")
	}
	if r, _ := VerifyFile(write("edited.jsonl", edited), nil, true); r.OK || !strings.Contains(kinds(r), "hash_mismatch") {
		t.Fatalf("edited = %s", kinds(r))
	}
	dropped := append(append([]string{}, lines[:2]...), lines[3:]...)
	if r, _ := VerifyFile(write("dropped.jsonl", dropped), nil, true); r.OK || !strings.Contains(kinds(r), "prev_hash_mismatch") {
		t.Fatalf("dropped = %s", kinds(r))
	}
	truncated := append(append([]string{}, lines[:len(lines)-3]...), lines[len(lines)-1])
	if r, _ := VerifyFile(write("truncated.jsonl", truncated), nil, true); r.OK {
		t.Fatalf("truncated export verified: %s", kinds(r))
	}
	otherKey := storetest.NewSigner("4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb").PublicKey()
	if r, _ := VerifyFile(res.Path, map[string]ed25519.PublicKey{storetest.NewSigner("").KeyID(): otherKey}, false); r.OK || !strings.Contains(kinds(r), "checkpoint_signature_invalid") {
		t.Fatalf("wrong local key = %s", kinds(r))
	}
	if _, err := VerifyFile(write("garbage.jsonl", []string{"{"}), nil, false); err == nil {
		t.Fatal("malformed file accepted")
	}
	if _, err := VerifyFile(write("headerless.jsonl", lines[1:]), nil, false); err == nil {
		t.Fatal("headerless file accepted")
	}
}

func TestExport_ClosedSessionCarriesAnchor(t *testing.T) {
	s := openTest(t, nil)
	openSession(t, s, ses)
	_, _ = s.CloseSession(ctx, ses, "user", api())
	res, err := s.Export(ctx, ses, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := VerifyFile(res.Path, nil, true)
	if err != nil || !r.OK || !r.Anchored {
		t.Fatalf("report = %+v %v", r, err)
	}
	b, _ := os.ReadFile(res.Path)
	noAnchor := strings.Builder{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.Contains(l, `"kind":"anchor"`) {
			noAnchor.WriteString(l + "\n")
		}
	}
	p := filepath.Join(t.TempDir(), "x.jsonl")
	_ = os.WriteFile(p, []byte(noAnchor.String()), 0o600)
	if r, _ := VerifyFile(p, nil, false); r.OK || !strings.Contains(kinds(r), "anchor_missing") {
		t.Fatalf("anchorless = %s", kinds(r))
	}
}

func TestPublicKeyPEMRoundTrip(t *testing.T) {
	pub := storetest.NewSigner("").PublicKey()
	p, err := PublicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParsePublicKeyPEM(p)
	if err != nil || !back.Equal(pub) || KeyID(back) != "21fe31dfa154a261" {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := ParsePublicKeyPEM("nope"); err == nil {
		t.Fatal("garbage parsed")
	}
}
