package sandboxtest

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/sandbox"
)

func spec(t *testing.T, id string) *sandbox.Spec {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".warden")
	s := filepath.Join(home, "sessions", "01jaxr")
	return &sandbox.Spec{
		ID: id, SessionID: "ses_01jaxr", TaskKey: "implement", Level: sandbox.LevelL2,
		Purpose: sandbox.PurposeTask, Mode: execproto.ModeTool,
		WardenHome: home, SessionDir: s,
		Worktree: filepath.Join(s, "worktree"), Scratch: filepath.Join(s, "scratch", "implement"),
		Cache: filepath.Join(home, "cache", "wsp_1"), GitDir: filepath.Join(s, "git"),
		ExecBin:        filepath.Join(filepath.Dir(home), "bin", "warden-exec"),
		ProfilesFile:   filepath.Join(s, "sandbox", id, "profiles.json"),
		ProfilesDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Proxy:          sandbox.ProxyEndpoint{Sidecar: "warden-proxy:3128"},
		Limits:         sandbox.DefaultLimits(),
		SessionBranch:  "warden/01jaxr", BaseCommit: "3f9c2e1a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e",
	}
}

// TestFakeSandbox_Lifecycle: every Create is matched by one Destroy, the
// handle carries the mount table and env key names the Spec produces, and
// a second Destroy is an error rather than a silent success.
func TestFakeSandbox_Lifecycle(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	f := &FakeSandbox{Now: func() time.Time { return at }}
	ctx := context.Background()
	s := spec(t, "sb_A")
	h, err := f.Create(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := s.Mounts()
	if h.ID != "sb_A" || h.Backend != "fake" || !h.Created.Equal(at) || !reflect.DeepEqual(h.Mounts, want) ||
		!reflect.DeepEqual(h.EnvKeys, sandbox.EnvKeys()) {
		t.Errorf("handle = %+v", h)
	}
	if _, err := f.Create(ctx, s); !errors.Is(err, sandbox.ErrInvalidSpec) {
		t.Errorf("duplicate id: err = %v", err)
	}
	if got, ok := f.Spec("sb_A"); !ok || got.ID != "sb_A" {
		t.Errorf("Spec(sb_A) = %v, %v", got.ID, ok)
	}
	if got := f.Live(); !reflect.DeepEqual(got, []string{"sb_A"}) {
		t.Errorf("Live() = %v", got)
	}
	if err := f.Destroy(ctx, "sb_A", "because"); err == nil {
		t.Error("Destroy with a reason outside A06 §13.3 succeeded")
	}
	if err := f.Destroy(ctx, "sb_A", sandbox.ReasonTaskEnd); err != nil {
		t.Fatal(err)
	}
	if err := f.Destroy(ctx, "sb_A", sandbox.ReasonTaskEnd); !errors.Is(err, sandbox.ErrUnknownSandbox) {
		t.Errorf("second Destroy: err = %v, want ErrUnknownSandbox", err)
	}
	if got := f.Destroyed(); !reflect.DeepEqual(got, []Destroyed{{"sb_A", sandbox.ReasonTaskEnd}}) {
		t.Errorf("Destroyed() = %v", got)
	}
	if len(f.Live()) != 0 {
		t.Errorf("Live() after destroy = %v", f.Live())
	}
}

// TestFakeSandbox_RefusesWhatTheRealOnesRefuse: the fake builds from the
// same Spec code, so a test above this package cannot get a sandbox the
// invariants forbid.
func TestFakeSandbox_RefusesWhatTheRealOnesRefuse(t *testing.T) {
	f := &FakeSandbox{}
	s := spec(t, "sb_B")
	s.Worktree = filepath.Dir(s.WardenHome) // the user's own directory, not the session worktree
	if _, err := f.Create(context.Background(), s); !errors.Is(err, sandbox.ErrInvalidSpec) {
		t.Errorf("Create(invalid) err = %v, want ErrInvalidSpec", err)
	}
	boom := errors.New("engine down")
	f.CreateErr = boom
	if _, err := f.Create(context.Background(), spec(t, "sb_C")); !errors.Is(err, boom) {
		t.Errorf("CreateErr not returned: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&FakeSandbox{}).Create(ctx, spec(t, "sb_D")); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: err = %v", err)
	}
	if len(f.Live()) != 0 {
		t.Errorf("Live() = %v, want none", f.Live())
	}
}
