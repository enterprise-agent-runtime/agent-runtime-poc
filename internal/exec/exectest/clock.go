// Package exectest holds test doubles for the executor (internal/exec,
// design A06 §7): FakeClock, so that process-table tests decide when the
// chunk interval, the per-call timeout and the TERM→KILL grace pass,
// without asserting wall-clock durations (CLAUDE.md §8.4, §8.6).
package exectest

import (
	"context"
	"sort"
	"sync"
	"time"

	"warden.dev/warden/internal/exec"
)

// FakeClock is a manual clock. Time moves only through Advance, which
// fires every timer and ticker that falls due, in deadline order.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer
	tickers []*fakeTicker
	changed chan struct{} // closed and replaced whenever a timer or ticker is added
}

// NewFakeClock starts at a fixed instant.
func NewFakeClock() *FakeClock {
	return &FakeClock{now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), changed: make(chan struct{})}
}

var _ exec.Clock = (*FakeClock)(nil)

// Now returns the fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

type fakeTimer struct {
	c       *FakeClock
	when    time.Time
	f       func()
	stopped bool
	fired   bool
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

type fakeTicker struct {
	c       *FakeClock
	every   time.Duration
	next    time.Time
	ch      chan time.Time
	stopped bool
}

func (t *fakeTicker) C() <-chan time.Time { return t.ch }

func (t *fakeTicker) Stop() {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	t.stopped = true
}

// AfterFunc registers f to run once the fake time reaches now+d.
func (c *FakeClock) AfterFunc(d time.Duration, f func()) exec.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, when: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	c.signal()
	return t
}

// NewTicker registers a ticker; like time.Ticker it drops ticks nobody
// has taken.
func (c *FakeClock) NewTicker(d time.Duration) exec.Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTicker{c: c, every: d, next: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.tickers = append(c.tickers, t)
	c.signal()
	return t
}

func (c *FakeClock) signal() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// Pending is the number of timers neither fired nor stopped.
func (c *FakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingLocked()
}

func (c *FakeClock) pendingLocked() int {
	n := 0
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

// WaitTimers blocks until at least n timers are pending or ctx ends. It is
// how a test knows that the code under test has armed the timer it is
// about to fire.
func (c *FakeClock) WaitTimers(ctx context.Context, n int) error {
	for {
		c.mu.Lock()
		ok := c.pendingLocked() >= n
		ch := c.changed
		c.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Advance moves the time forward by d and fires what falls due: timer
// functions run synchronously, in deadline order, without the clock's
// lock held; tickers get one tick each (dropped if the last is untaken).
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.when.After(now) {
			t.fired = true
			due = append(due, t)
		}
	}
	live := c.timers[:0]
	for _, t := range c.timers {
		if !t.fired && !t.stopped {
			live = append(live, t)
		}
	}
	c.timers = live
	for _, t := range c.tickers {
		if t.stopped || t.next.After(now) {
			continue
		}
		for !t.next.After(now) {
			t.next = t.next.Add(t.every)
		}
		select {
		case t.ch <- now:
		default:
		}
	}
	c.mu.Unlock()
	sort.SliceStable(due, func(i, j int) bool { return due[i].when.Before(due[j].when) })
	for _, t := range due {
		t.f()
	}
}
