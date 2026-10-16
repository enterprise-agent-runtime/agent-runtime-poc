package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLockDaemon_Exclusive guards WH-4 (WH-1+2 review, CWE-367): after a
// crash two wardends could both find the stale socket, and both remove it
// and bind (B removing A's fresh socket), so "exclusively" in Listen's
// comment was not true. The run/wardend.lock lock, held for the daemon's
// lifetime, makes the stale-socket path exclusive: while one holder has
// it, a second gets ErrDaemonRunning (exit 3); once released it is free.
func TestLockDaemon_Exclusive(t *testing.T) {
	l := Layout{Home: filepath.Join(t.TempDir(), "home")}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	unlock, err := LockDaemon(l.Home)
	if err != nil {
		t.Fatal(err)
	}
	if u2, err := LockDaemon(l.Home); !errors.Is(err, ErrDaemonRunning) {
		if u2 != nil {
			u2()
		}
		t.Fatalf("second LockDaemon while held = %v; want ErrDaemonRunning", err)
	}
	unlock()
	u3, err := LockDaemon(l.Home)
	if err != nil {
		t.Fatalf("LockDaemon after release = %v; want the lock", err)
	}
	u3()
	if ok, err := OwnerOnly(l.Lock()); err != nil || !ok {
		t.Fatalf("%s owner-only %v %v (%s)", l.Lock(), ok, err, ownerOnlyDetail(l.Lock()))
	}
}

// TestLockDaemon_RefusesSymlink: the lock file is opened without following
// a link, so one planted at run/wardend.lock is refused and its target is
// neither created nor changed.
func TestLockDaemon_RefusesSymlink(t *testing.T) {
	l := Layout{Home: filepath.Join(t.TempDir(), "home")}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Symlink(target, l.Lock()); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	unlock, err := LockDaemon(l.Home)
	if err == nil {
		unlock()
		t.Fatal("LockDaemon followed or accepted a symlinked lock file")
	}
	if errors.Is(err, ErrDaemonRunning) || !strings.Contains(err.Error(), "link") {
		t.Fatalf("LockDaemon on a symlink = %v; want a link refusal", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target created (%v)", err)
	}
}
