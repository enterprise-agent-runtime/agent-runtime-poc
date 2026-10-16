package store

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warden.dev/warden/internal/store/jcs"
	"warden.dev/warden/internal/store/storetest"
)

// chainOf builds a small valid session chain and returns parsed events.
func chainOf(t *testing.T) (*Store, []*event) {
	s := openTest(t, nil)
	openSession(t, s, ses)
	for i := 0; i < 2; i++ {
		_, _ = s.Append(ctx, Input{Type: "task.state", Chain: ses, Actor: api(), Payload: map[string]any{"i": i}})
	}
	evs, err := s.loadChain(ctx, ses)
	if err != nil {
		t.Fatal(err)
	}
	return s, evs
}

// rehash recomputes an event's hash after a deliberate change, so that
// only the intended check can fire.
func rehash(t *testing.T, e *event) {
	h, err := e.selfHash()
	if err != nil {
		t.Fatal(err)
	}
	e.hash = h
	e.raw["hash"] = h
}

func runChainPass(evs []*event, chain string) Report {
	rep := Report{}
	v := &verifier{rep: &rep, keyseen: map[string]bool{}, keys: func(string) (ed25519.PublicKey, string, bool) { return nil, "", false }}
	v.chainPass(chain, evs)
	v.finish()
	return rep
}

// TestChainPass_EachViolation exercises every pass-1 kind in isolation.
func TestChainPass_EachViolation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(t *testing.T, evs []*event)
		want string
	}{
		{"unknown type", func(t *testing.T, evs []*event) {
			evs[2].raw["type"], evs[2].typ = "made.up", "made.up"
			rehash(t, evs[2])
			evs[2].prev = evs[1].hash
		}, "unknown_event_type"},
		{"payload not an object", func(t *testing.T, evs []*event) { evs[1].payload = nil }, "payload_invalid"},
		{"missing required field", func(t *testing.T, evs []*event) { evs[0].typ = "tool.exec.start" }, "payload_invalid"},
		{"session id differs", func(t *testing.T, evs []*event) { evs[0].raw["session_id"] = "ses_01JAXR8Q7M2V9KTC3F6YH5N0PC" }, "chain_mismatch"},
		{"wrong chain field", func(t *testing.T, evs []*event) { evs[0].chain = "ses_01JAXR8Q7M2V9KTC3F6YH5N0PC" }, "chain_mismatch"},
		{"seq goes back", func(t *testing.T, evs []*event) { evs[2].seq = 1 }, "seq_not_increasing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, evs := chainOf(t)
			c.mut(t, evs)
			r := runChainPass(evs, ses)
			if !strings.Contains(kinds(r), c.want) || r.ChainOK {
				t.Fatalf("kinds = %s", kinds(r))
			}
		})
	}
	// sys events must not carry session-scoped ids.
	s := openTest(t, nil)
	_, _ = s.Append(ctx, Input{Type: "runtime.start", Chain: SysChain, Actor: api(), Payload: map[string]any{}})
	sys, _ := s.loadChain(ctx, SysChain)
	sys[0].raw["task_id"] = "tsk_01JAXR7ZK3M8Q2V9KTC3F6YH5N"
	if r := runChainPass(sys, SysChain); !strings.Contains(kinds(r), "chain_mismatch") {
		t.Fatalf("sys kinds = %s", kinds(r))
	}
}

func TestCheckpointPass_CountAndUnknownKey(t *testing.T) {
	s, _ := chainOf(t)
	_, _ = s.Checkpoint(ctx, ses, "workflow_end")
	evs, _ := s.loadChain(ctx, ses)
	last := evs[len(evs)-1]
	last.payload["event_count"] = json.Number("99")
	rep := Report{}
	v := &verifier{rep: &rep, keyseen: map[string]bool{}, keys: func(string) (ed25519.PublicKey, string, bool) { return nil, "local", false }}
	v.checkpointPass(ses, evs)
	v.finish()
	if k := kinds(rep); !strings.Contains(k, "checkpoint_mismatch") || !strings.Contains(k, "unknown_key") || rep.CheckpointOK {
		t.Fatalf("kinds = %s", k)
	}
}

func TestBlobs_TamperedContentIsRefused(t *testing.T) {
	s := openTest(t, nil)
	h, err := s.putBlob([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if h2, _ := s.putBlob([]byte("hello")); h2 != h {
		t.Fatal("same content, different hash")
	}
	p, _ := s.blobPath(h)
	_ = os.WriteFile(p, []byte("HELLO"), 0o600)
	if _, err := s.GetBlob(h); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered blob read: %v", err)
	}
	if _, err := s.GetBlob("md5:x"); err == nil {
		t.Fatal("bad hash accepted")
	}
}

func TestEvents_FiltersAndAll(t *testing.T) {
	s, _ := chainOf(t)
	_, _ = s.Append(ctx, Input{Type: "runtime.start", Chain: SysChain, Actor: api(), Payload: map[string]any{}})
	got, _ := s.Events(ctx, ses, 1, 1, []string{"task.state"})
	if len(got) != 1 || !strings.Contains(string(got[0]), `"i":0`) {
		t.Fatalf("filtered = %s", got)
	}
	all, _ := s.AllEvents(ctx, 0, 100)
	if len(all) != 4 {
		t.Fatalf("all = %d", len(all))
	}
	if _, err := s.Chain(ctx, "ses_01JAXR8Q7M2V9KTC3F6YH5N0PC"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown chain: %v", err)
	}
	if _, err := s.Export(ctx, SysChain, t.TempDir()); err == nil {
		t.Fatal("sys chain exported on its own")
	}
	if _, err := s.Checkpoint(ctx, "ses_01JAXR8Q7M2V9KTC3F6YH5N0PC", "export"); err == nil {
		t.Fatal("checkpoint of an unknown chain")
	}
}

// TestExport_IncludesArtifactRecords: artifact records travel with the
// export (A04 §13); the projections need their parent rows.
func TestExport_IncludesArtifactRecords(t *testing.T) {
	s, _ := chainOf(t)
	h, _ := s.putBlob([]byte(`{"summary":"plan"}`))
	for _, q := range []string{
		`INSERT INTO workspaces (id, root, name, created_at, last_opened_at) VALUES ('wsp_01JAXR7ZK3M8Q2V9KTC3F6YH5N', 'D:/repo', 'repo', 'x', 'x')`,
		`INSERT INTO sessions (id, workspace_id, workspace_root, classification, sandbox_level, sandbox_backend, branch, base_commit, worktree_path, gitdir_path, budget_session_usd, status, created_at, last_activity_at)
		 VALUES ('` + ses + `', 'wsp_01JAXR7ZK3M8Q2V9KTC3F6YH5N', 'D:/repo', 'internal', 'L2', 'docker', 'warden/01jaxr8q7m2v9ktc3f6yh5n0pb', '` + strings.Repeat("a", 40) + `', 'w', 'g', 5, 'open', 'x', 'x')`,
		`INSERT INTO blobs (hash, size_bytes, created_at) VALUES ('` + h + `', 18, 'x')`,
		`INSERT INTO artifacts (id, session_id, type, schema, content_hash, size_bytes, media_type, summary, classification, provenance, record, created_at, created_by, event_seq)
		 VALUES ('art_01JAXR7ZK3M8Q2V9KTC3F6YH5N', '` + ses + `', 'plan', 'plan.json', '` + h + `', 18, 'application/json', 'plan', 'internal', '{}', '{"id":"art_01JAXR7ZK3M8Q2V9KTC3F6YH5N"}', 'x', 'agent', 3)`,
	} {
		if _, err := s.w.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Export(ctx, ses, t.TempDir())
	if err != nil || res.Artifacts != 1 {
		t.Fatalf("export = %+v %v", res, err)
	}
	b, _ := os.ReadFile(res.Path)
	if !strings.Contains(string(b), `{"kind":"artifact","record":{"id":"art_01JAXR7ZK3M8Q2V9KTC3F6YH5N"}}`) {
		t.Fatalf("export lacks the artifact line:\n%s", b)
	}
	// Every line is canonical JSON.
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		c, err := jcs.Transform([]byte(l))
		if err != nil || string(c) != l {
			t.Fatalf("line not canonical: %s", l)
		}
	}
}

func TestOpen_RequiresDir(t *testing.T) {
	if _, err := Open(ctx, Options{}); err == nil {
		t.Fatal("empty Dir accepted")
	}
	// Keys passed in are used for verification.
	k := storetest.NewSigner("4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb")
	s, err := Open(ctx, Options{Dir: filepath.Join(t.TempDir(), "db"), PublicKeys: map[string]ed25519.PublicKey{k.KeyID(): k.PublicKey()}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.keys[k.KeyID()]; !ok {
		t.Fatal("archived key not loaded")
	}
}

// TestSetSigner_LateKey: checkpoints written while the keychain is locked
// carry unsigned_reason keychain_locked; after SetSigner they are signed.
func TestSetSigner_LateKey(t *testing.T) {
	s := openTest(t, func(o *Options) { o.Signer = nil })
	s.SetSigner(nil, "keychain_locked")
	openSession(t, s, ses)
	e, _ := s.Checkpoint(ctx, ses, "export")
	if !strings.Contains(string(e.Payload), `"unsigned_reason":"keychain_locked"`) {
		t.Fatalf("payload = %s", e.Payload)
	}
	s.SetSigner(storetest.NewSigner(""), "")
	e, _ = s.Checkpoint(ctx, ses, "export")
	if !strings.Contains(string(e.Payload), `"signature":"`) || strings.Contains(string(e.Payload), "keychain_locked") {
		t.Fatalf("payload = %s", e.Payload)
	}
	if r, _ := s.Verify(ctx, ses, false); !r.OK || len(r.Violations) != 1 || r.Violations[0].Kind != "checkpoint_unsigned" {
		t.Fatalf("report = %+v", r)
	}
}
