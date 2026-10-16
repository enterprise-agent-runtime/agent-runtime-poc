//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"syscall"
)

// WatchProcess watches pid by polling kill(pid, 0). If pid is a child of
// this process it is reaped as soon as it exits, because a zombie still
// answers kill(pid, 0). Unlike the Windows handle, a pid reused between the
// exit and the next poll (one exitPoll) would be taken for the process; that
// only makes the wait run to its timeout, never end early (M2-1).
func WatchProcess(pid int) (*Watch, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("watch process: invalid pid %d", pid)
	}
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return nil, fmt.Errorf("watch process %d: %w", pid, err)
	}
	return &Watch{
		Pid: pid,
		alive: func() bool {
			var ws syscall.WaitStatus
			if wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); err == nil && wpid == pid {
				return false
			}
			err := syscall.Kill(pid, 0)
			return err == nil || errors.Is(err, syscall.EPERM)
		},
	}, nil
}
