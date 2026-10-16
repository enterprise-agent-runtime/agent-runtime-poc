package wire

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Watchdog cancels a request that sends no first event within firstByte or
// goes silent for idle between lines (design A11 §7.2). It is distinguished
// from caller cancellation by Fired.
type Watchdog struct {
	mu     sync.Mutex
	timer  *time.Timer
	idle   time.Duration
	fired  atomic.Bool
	cancel context.CancelFunc
}

// StartWatchdog arms a watchdog with firstByte and returns the derived
// context to bind the request to.
func StartWatchdog(parent context.Context, firstByte, idle time.Duration) (context.Context, *Watchdog) {
	ctx, cancel := context.WithCancel(parent)
	w := &Watchdog{idle: idle, cancel: cancel}
	w.timer = time.AfterFunc(firstByte, func() {
		w.fired.Store(true)
		cancel()
	})
	return ctx, w
}

// Touch re-arms the watchdog with the idle timeout; call it on every line.
func (w *Watchdog) Touch() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fired.Load() {
		return
	}
	w.timer.Reset(w.idle)
}

// Fired reports whether the watchdog (not the caller) cancelled the request.
func (w *Watchdog) Fired() bool { return w.fired.Load() }

// Stop disarms the watchdog and releases its context.
func (w *Watchdog) Stop() {
	w.mu.Lock()
	w.timer.Stop()
	w.mu.Unlock()
	w.cancel()
}

// Timeouts returns the effective connect, first-byte and idle timeouts for
// a provider (A11 §7.2): connect 10 s; first byte 120 s for T0/T1 (model
// loading) and 60 s otherwise; idle 60 s; each overridable.
func Timeouts(tier string, connectMS, firstByteMS, idleMS int) (connect, firstByte, idle time.Duration) {
	connect, idle = 10*time.Second, 60*time.Second
	firstByte = 60 * time.Second
	if tier == "T0" || tier == "T1" {
		firstByte = 120 * time.Second
	}
	if connectMS > 0 {
		connect = time.Duration(connectMS) * time.Millisecond
	}
	if firstByteMS > 0 {
		firstByte = time.Duration(firstByteMS) * time.Millisecond
	}
	if idleMS > 0 {
		idle = time.Duration(idleMS) * time.Millisecond
	}
	return connect, firstByte, idle
}
