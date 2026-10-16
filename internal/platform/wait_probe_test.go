package platform

import (
	"errors"
	"syscall"
	"testing"
)

// TestWatchByProbe_EPERMIsNotOurs guards the M2-1 review finding
// (2026-10-16): on Unix, a stale pid file can name a pid that now belongs to
// another user's process. kill(pid, 0) then fails with EPERM, and the old
// watch counted EPERM as "still running", so "warden daemon stop" would wait
// the full 10 s and report a timeout for a process that was never wardend.
// wardend runs as the invoking user, so EPERM means "not ours": the first
// probe refuses to watch, and EPERM later in the wait means the pid has been
// reused by someone else, so ours is gone.
func TestWatchByProbe_EPERMIsNotOurs(t *testing.T) {
	noReap := func() bool { return false }

	t.Run("first probe EPERM: stale pid file, no watch", func(t *testing.T) {
		w, err := watchByProbe(4242, func() error { return syscall.EPERM }, noReap)
		if err == nil {
			t.Fatalf("watchByProbe = %+v, nil; want an error for a pid owned by another user", w)
		}
		if !errors.Is(err, syscall.EPERM) {
			t.Errorf("error %v does not wrap EPERM", err)
		}
	})

	t.Run("first probe ESRCH: already gone, no watch", func(t *testing.T) {
		if _, err := watchByProbe(4242, func() error { return syscall.ESRCH }, noReap); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("watchByProbe error = %v, want ESRCH", err)
		}
	})

	t.Run("ours, then EPERM during the wait: gone", func(t *testing.T) {
		results := []error{nil, nil, syscall.EPERM}
		probe := func() error { e := results[0]; results = results[1:]; return e }
		w, err := watchByProbe(4242, probe, noReap)
		if err != nil {
			t.Fatal(err)
		}
		if !w.alive() {
			t.Fatal("alive = false while kill(pid, 0) succeeds")
		}
		if w.alive() {
			t.Error("alive = true after the pid passed to another user")
		}
	})

	t.Run("reaped child: gone without probing", func(t *testing.T) {
		w, err := watchByProbe(4242, func() error { return nil }, func() bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if w.alive() {
			t.Error("alive = true for a child that was just reaped")
		}
	})
}
