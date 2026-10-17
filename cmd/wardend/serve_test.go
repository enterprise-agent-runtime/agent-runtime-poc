package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"warden.dev/warden/internal/api"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/secrets"
	"warden.dev/warden/internal/store"
)

// testDeps are serve's dependencies for tests: the real daemon lock, an
// in-memory secrets backend (never the OS keychain, D-043) and the given
// runtime.start.
func testDeps(runtimeStart func(context.Context, *api.Server) error) daemonDeps {
	return daemonDeps{
		lock:         platform.LockDaemon,
		backend:      func(context.Context, string) secrets.Backend { return secrets.NewMemory() },
		runtimeStart: runtimeStart,
	}
}

// entries lists a directory's names, or fails the test.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// TestServe_LockedExitsBeforeAnyWrite guards WH-4 round 1 (F2, CWE-362):
// the daemon lock was taken only at Listen, after the signer, PublicKeys
// and store.Open, so a losing wardend still generated a checkpoint seed,
// wrote keys/checkpoint.pub (possibly moving the winner's key around) and
// ran migrations on the winner's database before exiting 3. With the lock
// held by another daemon, serve now exits 3 straight after Ensure, and
// keys/, db/ and run/token are untouched.
func TestServe_LockedExitsBeforeAnyWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.EnvHome, home)
	l := platform.Layout{Home: home}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	held, err := platform.LockDaemon(home)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	started := false
	code := serve(home, false, testDeps(func(context.Context, *api.Server) error { started = true; return nil }))
	if code != 3 || started {
		t.Fatalf("serve while another daemon holds the lock = exit %d (runtime started %v); want exit 3", code, started)
	}
	if k, d := entries(t, l.Keys()), entries(t, l.DB()); len(k) != 0 || len(d) != 0 {
		t.Fatalf("a losing wardend wrote under keys/ %v and db/ %v", k, d)
	}
	if _, err := os.Lstat(l.Token()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a losing wardend wrote the token file (%v)", err)
	}
}

// TestServe_FailedStartCleansUpThenUnlocks guards WH-4 round 1 (F3, F4):
// when runtime.start fails after the token and pid file are written, both
// were left behind for clients to find; and the lock was released (LIFO
// defers) before the store was closed and the token removed, so a second
// daemon could start in between. Now the token and pid file are gone by the
// time the lock is released, and the lock is free afterwards.
func TestServe_FailedStartCleansUpThenUnlocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.EnvHome, home)
	l := platform.Layout{Home: home}
	deps := testDeps(func(context.Context, *api.Server) error {
		if _, err := os.Lstat(l.Token()); err != nil {
			t.Errorf("runtime.start ran before the token was published: %v", err)
		}
		return errors.New("injected runtime.start failure")
	})
	var leftAtUnlock []string
	deps.lock = func(h string) (func(), error) {
		unlock, err := platform.LockDaemon(h)
		if err != nil {
			return nil, err
		}
		return func() {
			for _, p := range []string{l.Token(), l.PID()} {
				if _, err := os.Lstat(p); err == nil {
					leftAtUnlock = append(leftAtUnlock, filepath.Base(p))
				}
			}
			unlock()
		}, nil
	}
	if code := serve(home, false, deps); code != 1 {
		t.Fatalf("serve with a failing runtime.start = exit %d; want 1", code)
	}
	if len(leftAtUnlock) != 0 {
		t.Fatalf("lock released while %v still existed", leftAtUnlock)
	}
	for _, p := range []string{l.Token(), l.PID()} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind after a failed start (%v)", p, err)
		}
	}
	unlock, err := platform.LockDaemon(home)
	if err != nil {
		t.Fatalf("lock not released after serve returned: %v", err)
	}
	unlock()
}

// TestServe_RefusesInvalidDefaultLevel guards CWE-178 at the daemon: a
// sandbox.default_level that is not a level of this OS (a look-alike with
// a fullwidth L, built numerically; L3; and L1 where the OS offers only L2,
// as on Windows) stops the start with exit 1 before runtime.start and
// before the token is published, instead of the platform default being
// used in its place (CONFLICTS C-06). Before M2-17 wardend never read
// config.yaml at all.
func TestServe_RefusesInvalidDefaultLevel(t *testing.T) {
	bad := []string{string(rune(0xFF2C)) + "2", "L3"}
	if !slices.Contains(platform.SandboxLevels(), "L1") {
		bad = append(bad, "L1")
	}
	for _, level := range bad {
		t.Run(fmt.Sprintf("%+q", level), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv(platform.EnvHome, home)
			l := platform.Layout{Home: home}
			if err := l.Ensure(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(l.Config(), []byte("sandbox:\n  default_level: "+level+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			started := false
			code := serve(home, false, testDeps(func(context.Context, *api.Server) error {
				started = true
				return errors.New("stop: the daemon must not have got this far")
			}))
			if code != 1 || started {
				t.Fatalf("serve with default level %+q = exit %d (runtime started %v); want exit 1", level, code, started)
			}
			if _, err := os.Lstat(l.Token()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("token published although the config was refused (%v)", err)
			}
		})
	}
}

// TestServe_UsesNormalisedDefaultLevel: the level the daemon runs with is
// config.yaml's, in canonical spelling. "l2" is L2 on every OS, so
// runtime.start records "L2" whatever the platform default is; doctor's
// sandbox.l2 is blocking exactly for "L2" (D-026).
func TestServe_UsesNormalisedDefaultLevel(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.EnvHome, home)
	l := platform.Layout{Home: home}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Config(), []byte("sandbox:\n  default_level: \" l2 \"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop after runtime.start")
	code := serve(home, false, testDeps(func(ctx context.Context, srv *api.Server) error {
		if err := srv.RuntimeStart(ctx); err != nil {
			return err
		}
		return stop
	}))
	if code != 1 {
		t.Fatalf("exit %d, want 1 (the injected stop)", code)
	}
	st, err := store.Open(context.Background(), store.Options{Dir: l.DB(), BlobsDir: l.Blobs()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	evs, err := st.Events(context.Background(), store.SysChain, 0, 100, []string{"runtime.start"})
	if err != nil || len(evs) != 1 {
		t.Fatalf("runtime.start events = %d, %v", len(evs), err)
	}
	var ev struct {
		Payload struct {
			Level string `json:"default_sandbox_level"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(evs[0], &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Payload.Level != "L2" {
		t.Fatalf("runtime.start default_sandbox_level = %q, want L2", ev.Payload.Level)
	}
}
