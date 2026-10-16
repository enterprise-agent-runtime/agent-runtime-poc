//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"warden.dev/warden/internal/api"
	"warden.dev/warden/internal/buildinfo"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/secrets"
)

// builtWardend finds the wardend binary from "make build" ($WARDEN_BIN, else
// <repo>/bin). The test does not build it itself: compiling would mean
// starting a process outside the INV-H packages (CLAUDE.md §5).
func builtWardend(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("WARDEN_BIN")
	if dir == "" {
		dir = filepath.Join("..", "..", "bin")
	}
	p, err := filepath.Abs(filepath.Join(dir, platform.ExeName("wardend")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("wardend not found at %s; run make build (or make integration) first, or set WARDEN_BIN", p)
	}
	return p
}

// TestDaemonStop_HomeRemovableImmediately guards M2-1 (preflight finding
// 2026-10-16): on Windows "warden daemon stop" returned before wardend had
// exited, so removing WARDEN_HOME right after stop failed with "Device or
// resource busy" and succeeded seconds later. Every script and test that
// stops the daemon and then cleans up raced. After stop returns, the home
// must be removable at once, with no retry.
//
// The test is meaningful on Windows only: on Linux and macOS RemoveAll
// succeeds even while wardend still has the files open, so there it passes
// with or without the wait (the Unix wait itself is covered at L1 by
// TestWatchByProbe_EPERMIsNotOurs and TestWaitExit_TimesOutAndReports). CI
// does not run make integration yet; run it by hand on Windows. The temp
// home has its own keychain namespace (platform.KeychainService), so the
// daemon here never reads or writes the user's real Warden secrets, and the
// cleanup deletes what it created there (cleanTestKeychain).
func TestDaemonStop_HomeRemovableImmediately(t *testing.T) {
	exe := builtWardend(t)
	home, err := os.MkdirTemp("", "warden-stop-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(platform.EnvHome, home)
	l := platform.Layout{Home: home}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	// Refuse before the daemon touches anything if this home would share
	// the real keychain namespace.
	if _, err := testKeychainService(home); err != nil {
		t.Fatal(err)
	}
	// Starting wardend is the C-45 / D-006 path (platform.SpawnDaemon), the
	// same one "warden daemon start" uses.
	pid, err := platform.SpawnDaemon(exe, nil, filepath.Join(l.Logs(), "wardend.log"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if platform.Alive(pid) {
			_ = platform.TerminateGroup(pid, 0)
		}
		// The daemon generated a checkpoint seed in this home's own keychain
		// namespace; it outlives the home unless deleted here.
		cleanTestKeychain(t, home)
		// Cleanup only, never the assertion: when the test fails, the home
		// is still busy, so retry for a while instead of leaking it.
		for i := 0; i < 100 && os.RemoveAll(home) != nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		c, err := api.Dial(ctx, l, "warden-cli", buildinfo.Version, nil)
		if err == nil {
			c.Close()
			break
		}
		if ctx.Err() != nil || !platform.Alive(pid) {
			log, _ := os.ReadFile(filepath.Join(l.Logs(), "wardend.log"))
			t.Fatalf("wardend did not come up: %v\n%s", err, log)
		}
		time.Sleep(100 * time.Millisecond)
	}

	stop := daemonCmd()
	stop.SetArgs([]string{"stop"})
	if err := stop.ExecuteContext(ctx); err != nil {
		t.Fatalf("warden daemon stop: %v", err)
	}
	if err := os.RemoveAll(home); err != nil {
		t.Fatalf("WARDEN_HOME not removable right after warden daemon stop: %v", err)
	}
}

// daemonKeychainAccounts are the keychain items wardend creates. go-keyring
// cannot list a service's items, so cleanup deletes these known accounts:
// the checkpoint signing seed (generated on first start) and doctor/probe
// (written and deleted by the backend probe; deleted again in case a run
// was interrupted between the two).
func daemonKeychainAccounts(t *testing.T) []string {
	t.Helper()
	ref, err := secrets.Parse(secrets.CheckpointRef)
	if err != nil {
		t.Fatal(err)
	}
	return []string{ref.Account, "doctor/probe"}
}

// cleanTestKeychain deletes the daemon's items in the test home's own
// keychain namespace, and only there.
func cleanTestKeychain(t *testing.T, home string) {
	t.Helper()
	svc, err := testKeychainService(home)
	if err != nil {
		t.Errorf("keychain cleanup: %v", err)
		return
	}
	k := secrets.NewKeyring(svc)
	for _, acct := range daemonKeychainAccounts(t) {
		if err := k.Delete(context.Background(), acct); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			t.Errorf("keychain cleanup: delete %s/%s: %v", svc, acct, err)
		}
	}
}
