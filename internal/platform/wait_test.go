package platform

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeClock is a clock whose Sleep only advances Now, so the wait loop is
// tested without ever sleeping on a real clock.
type fakeClock struct {
	t      time.Time
	sleeps int
}

func (c *fakeClock) Now() time.Time                     { return c.t }
func (c *fakeClock) Sleep(d time.Duration)              { c.t = c.t.Add(d); c.sleeps++ }
func (c *fakeClock) elapsed(t0 time.Time) time.Duration { return c.t.Sub(t0) }

// TestWaitExit_TimesOutAndReports guards the decision behind "warden daemon
// stop" (M2-1, preflight finding 2026-10-16): on Windows stop returned while
// wardend still held its files, so "rm -rf $WARDEN_HOME" right after stop
// failed with "Device or resource busy". Stop now waits for the process to
// exit; this test pins down when that wait ends and what it reports.
func TestWaitExit_TimesOutAndReports(t *testing.T) {
	const timeout, poll = 10 * time.Second, 50 * time.Millisecond
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	t.Run("returns once the process is gone", func(t *testing.T) {
		clk := &fakeClock{t: t0}
		calls := 0
		alive := func() bool { calls++; return calls < 4 } // exits on the 4th look
		if err := WaitExit(alive, clk.Now, clk.Sleep, timeout, poll); err != nil {
			t.Fatalf("WaitExit = %v, want nil", err)
		}
		if calls != 4 {
			t.Errorf("alive called %d times, want 4 (stop as soon as it reports false)", calls)
		}
		if got := clk.elapsed(t0); got != 3*poll {
			t.Errorf("waited %v, want %v", got, 3*poll)
		}
	})

	t.Run("already exited does not sleep", func(t *testing.T) {
		clk := &fakeClock{t: t0}
		if err := WaitExit(func() bool { return false }, clk.Now, clk.Sleep, timeout, poll); err != nil {
			t.Fatalf("WaitExit = %v, want nil", err)
		}
		if clk.sleeps != 0 {
			t.Errorf("slept %d times for a process that had already exited", clk.sleeps)
		}
	})

	t.Run("times out with a clear error after the deadline", func(t *testing.T) {
		clk := &fakeClock{t: t0}
		err := WaitExit(func() bool { return true }, clk.Now, clk.Sleep, timeout, poll)
		if !errors.Is(err, ErrStillRunning) {
			t.Fatalf("WaitExit = %v, want ErrStillRunning", err)
		}
		if !strings.Contains(err.Error(), "10s") {
			t.Errorf("error %q does not name the timeout", err)
		}
		got := clk.elapsed(t0)
		if got < timeout || got > timeout+poll {
			t.Errorf("gave up after %v, want within [%v, %v]", got, timeout, timeout+poll)
		}
	})

	t.Run("never sleeps past the deadline", func(t *testing.T) {
		clk := &fakeClock{t: t0}
		_ = WaitExit(func() bool { return true }, clk.Now, clk.Sleep, 120*time.Millisecond, poll)
		if got := clk.elapsed(t0); got != 120*time.Millisecond {
			t.Errorf("waited %v, want exactly the 120ms deadline (last sleep is clipped)", got)
		}
	})
}
