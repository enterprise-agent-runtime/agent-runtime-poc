package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrStillRunning reports that a process did not exit within the wait.
var ErrStillRunning = errors.New("process still running")

// exitPoll is how often Watch.Wait looks at the process.
const exitPoll = 50 * time.Millisecond

// WaitExit polls alive until it reports false or timeout has passed on the
// clock given by now and sleep. It returns nil once the process is gone and
// an error wrapping ErrStillRunning at the deadline. The last sleep is
// clipped to the deadline. The decision is kept apart from the OS probe and
// the real clock so it can be tested without either (M2-1).
func WaitExit(alive func() bool, now func() time.Time, sleep func(time.Duration), timeout, poll time.Duration) error {
	deadline := now().Add(timeout)
	for alive() {
		left := deadline.Sub(now())
		if left <= 0 {
			return fmt.Errorf("%w after %s", ErrStillRunning, timeout)
		}
		sleep(min(poll, left))
	}
	return nil
}

// DaemonPID reads the pid wardend wrote to the pid file. Read it before
// asking the daemon to stop: wardend removes the file during shutdown.
func (l Layout) DaemonPID() (int, error) {
	b, err := os.ReadFile(l.PID())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("pid file %s does not hold a pid", l.PID())
	}
	return pid, nil
}

// Watch observes one process so a caller can wait for it to exit. Take it
// before asking the process to stop: on Windows it holds a process handle,
// so a pid reused after the exit cannot be mistaken for the process.
type Watch struct {
	pid     int
	alive   func() bool
	release func()
}

// PID is the process id being watched.
func (w *Watch) PID() int { return w.pid }

// watchByProbe builds the Unix watch from a kill(pid, 0) probe and a reap
// of our own child. It has no build tag so its decisions are tested on every
// OS. wardend runs as the invoking user, so a pid that kill refuses with
// EPERM is not wardend: at the start that means a stale pid file (no watch),
// during the wait it means the pid was reused by another user (ours is gone).
func watchByProbe(pid int, probe func() error, reap func() bool) (*Watch, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("watch process: invalid pid %d", pid)
	}
	switch err := probe(); {
	case errors.Is(err, syscall.EPERM):
		return nil, fmt.Errorf("watch process %d: owned by another user, so the pid file is stale: %w", pid, err)
	case err != nil:
		return nil, fmt.Errorf("watch process %d: %w", pid, err)
	}
	return &Watch{pid: pid, alive: func() bool {
		if reap() {
			return false
		}
		return probe() == nil
	}}, nil
}

// Wait blocks until the process has exited or timeout has passed (then the
// error wraps ErrStillRunning).
func (w *Watch) Wait(timeout time.Duration) error {
	return WaitExit(w.alive, time.Now, time.Sleep, timeout, exitPoll)
}

// Close releases the watch. It is safe to call more than once.
func (w *Watch) Close() {
	if w.release != nil {
		w.release()
		w.release = nil
	}
}
