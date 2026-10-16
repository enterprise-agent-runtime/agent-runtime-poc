package platform

import (
	"fmt"
	"sync"
)

// Daemon is a wardend this process started with SpawnDaemon. It answers
// whether that process has exited and with which status, so a caller
// waiting for it to come up can tell "exited during start (exit status 3)"
// from "still starting" (WH-4).
type Daemon struct {
	// PID is the process id of the spawned wardend.
	PID int

	mu     sync.Mutex
	done   bool
	closed bool
	status ExitStatus
	poll   func() (ExitStatus, bool)
	close  func()
}

// ExitStatus is how a spawned process ended: Code is its exit code, or -1
// when there is none (a signal ended it, or the status is unknown), and
// then Other says which.
type ExitStatus struct {
	Code  int
	Other string
}

func (s ExitStatus) String() string {
	if s.Code < 0 {
		return s.Other
	}
	return fmt.Sprintf("exit status %d", s.Code)
}

// Exited reports whether the process has exited and, once it has, how.
// On Unix it reaps the child (a zombie still answers kill(pid, 0)); on
// Windows it reads the exit code from a handle held since the spawn, so a
// reused pid is never mistaken for it. After Close it reports what it last
// saw.
func (d *Daemon) Exited() (ExitStatus, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.done && !d.closed {
		d.status, d.done = d.poll()
	}
	return d.status, d.done
}

// Close releases what the Daemon holds (the Windows process handle). The
// process itself keeps running. It is safe to call more than once.
func (d *Daemon) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed && d.close != nil {
		d.close()
	}
	d.closed = true
}
