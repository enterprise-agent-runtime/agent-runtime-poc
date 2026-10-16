//go:build windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLockDaemon_RefusesJunction is TestLockDaemon_RefusesSymlink for
// Windows, where creating a symlink needs a privilege the test usually
// lacks: a junction planted at run/wardend.lock is opened as itself
// (FILE_FLAG_OPEN_REPARSE_POINT), refused, and nothing is created where it
// points (WH-4).
func TestLockDaemon_RefusesJunction(t *testing.T) {
	l := Layout{Home: filepath.Join(t.TempDir(), "home")}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, l.Lock(), target)
	unlock, err := LockDaemon(l.Home)
	if err == nil {
		unlock()
		t.Fatal("LockDaemon accepted a junction as the lock file")
	}
	if errors.Is(err, ErrDaemonRunning) || !strings.Contains(err.Error(), "junction") {
		t.Fatalf("LockDaemon on a junction = %v; want a junction refusal", err)
	}
	if ents, _ := os.ReadDir(target); len(ents) != 0 {
		t.Fatalf("junction target changed: %v", ents)
	}
}
