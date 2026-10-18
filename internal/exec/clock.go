package exec

import "time"

// Clock is the executor's time source for the process table (design A06
// §7.2 chunk interval, §7.5 per-call timeout, §7.6 TERM→KILL grace, §7.8
// shutdown grace). It is injected so that tests decide when time passes
// and assert no wall-clock durations (CLAUDE.md §8.6: flaky tests are
// bugs). RealClock is the production clock; exectest.FakeClock is the
// test double.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine once d has passed, unless
	// the timer is stopped first.
	AfterFunc(d time.Duration, f func()) Timer
	// NewTicker delivers ticks every d until stopped.
	NewTicker(d time.Duration) Ticker
}

// Timer is a stoppable AfterFunc timer.
type Timer interface {
	// Stop prevents the call if it has not started; it reports whether
	// it did.
	Stop() bool
}

// Ticker is a stoppable ticker.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// RealClock returns the wall clock.
func RealClock() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

func (realClock) NewTicker(d time.Duration) Ticker { return realTicker{time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }
