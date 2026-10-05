package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warden.dev/warden/internal/model/goldentest"
	"warden.dev/warden/internal/store/storetest"
)

// Audit fixtures (M1 acceptance: "audit verify fails on a tampered fixture
// and passes on a clean one"). testdata/audit holds a deterministic export
// of a closed session with the CF-40 approval pattern, signed with the
// RFC 8032 test key, plus tampered variants. They are rebuilt with
// UPDATE_GOLDEN=1; `warden audit verify --file` can be run on them.
const fixtureDir = "testdata/audit"

func buildFixture(t *testing.T) []byte {
	clk := storetest.NewClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), 7*time.Millisecond)
	s, err := Open(ctx, Options{Dir: filepath.Join(t.TempDir(), "db"), RuntimeVersion: "0.1.0", User: "local:fixture", Clock: clk.Now,
		IDs: storetest.IDs(clk), Signer: storetest.NewSigner("")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sid := "ses_01K6E3Y0000000000000000000"
	openSession(t, s, sid)
	for _, e := range []strictEvent{
		dec("dec_01K6E3Y0000000000000000001", c1, "allow"), start(c1, "dec_01K6E3Y0000000000000000001"), end(c1),
		dec("dec_01K6E3Y0000000000000000002", c2, "approval_required"), req(a1, c2), res(a1, "approve", "workspace"),
		dec("dec_01K6E3Y0000000000000000003", c2, "allow", "resolved_by_approval", a1, "matched_rules", []string{"grant." + a1}),
		start(c2, "dec_01K6E3Y0000000000000000003"), end(c2),
	} {
		if _, err := s.Append(ctx, Input{Type: e.typ, Chain: sid, Actor: Runtime("policy", "0.1.0"), Payload: e.payload}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CloseSession(ctx, sid, "user", Runtime("session", "0.1.0")); err != nil {
		t.Fatal(err)
	}
	r, err := s.Export(ctx, sid, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGolden_AuditFixtures(t *testing.T) {
	clean := buildFixture(t)
	lines := strings.Split(strings.TrimSpace(string(clean)), "\n")
	variants := map[string]string{"clean.jsonl": string(clean)}
	// Payload of the first decision changed from allow to deny.
	edited := append([]string{}, lines...)
	edited[2] = strings.Replace(edited[2], `"effect":"allow"`, `"effect":"deny"`, 1)
	variants["tampered-edited.jsonl"] = strings.Join(edited, "\n") + "\n"
	// The first policy.decision removed (and nothing else).
	variants["tampered-dropped.jsonl"] = strings.Join(append(append([]string{}, lines[:2]...), lines[3:]...), "\n") + "\n"
	if goldentest.Update() {
		_ = os.MkdirAll(fixtureDir, 0o755)
		for name, body := range variants {
			if err := os.WriteFile(filepath.Join(fixtureDir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	committed, err := os.ReadFile(filepath.Join(fixtureDir, "clean.jsonl"))
	if err != nil {
		t.Fatalf("missing fixture (run with UPDATE_GOLDEN=1): %v", err)
	}
	if strings.ReplaceAll(string(committed), "\r\n", "\n") != string(clean) {
		t.Fatal("the clean fixture no longer matches what the store exports: hashing or export format changed")
	}
	want := map[string]struct {
		ok   bool
		kind string
	}{
		"clean.jsonl":            {true, ""},
		"tampered-edited.jsonl":  {false, "hash_mismatch"},
		"tampered-dropped.jsonl": {false, "prev_hash_mismatch"},
	}
	for name, w := range want {
		r, err := VerifyFile(filepath.Join(fixtureDir, name), nil, true)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.OK != w.ok || (w.kind != "" && !strings.Contains(kinds(r), w.kind)) {
			t.Errorf("%s: ok=%v kinds=%s", name, r.OK, kinds(r))
		}
		if name == "clean.jsonl" && (!r.Anchored || r.ToolCallsChecked != 2) {
			t.Errorf("clean: anchored=%v tool calls=%d", r.Anchored, r.ToolCallsChecked)
		}
	}
}
