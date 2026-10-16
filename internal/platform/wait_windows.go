//go:build windows

package platform

import (
	"fmt"
	"syscall"
)

// WatchProcess opens pid with SYNCHRONIZE and watches that handle. Holding
// the handle keeps the process object alive, so the watch follows this
// process even if its pid is reused after it exits (M2-1).
func WatchProcess(pid int) (*Watch, error) {
	h, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("open process %d: %w", pid, err)
	}
	return &Watch{
		Pid: pid,
		alive: func() bool {
			ev, err := syscall.WaitForSingleObject(h, 0)
			// A failed wait is treated as still running: the caller then
			// times out and says so, rather than claiming an exit it never saw.
			return err != nil || ev == syscall.WAIT_TIMEOUT
		},
		release: func() { _ = syscall.CloseHandle(h) },
	}, nil
}
