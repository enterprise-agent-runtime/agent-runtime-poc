//go:build !windows

package platform

import "syscall"

// WatchProcess watches pid by polling kill(pid, 0); the decisions are in
// watchByProbe. If pid is a child of this process it is reaped as soon as it
// exits, because a zombie still answers kill(pid, 0). Unlike the Windows
// handle, a pid reused by the same user between the exit and the next poll
// (one exitPoll) would be taken for the process; that only makes the wait
// run to its timeout, never end early (M2-1).
func WatchProcess(pid int) (*Watch, error) {
	probe := func() error { return syscall.Kill(pid, 0) }
	reap := func() bool {
		var ws syscall.WaitStatus
		wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		return err == nil && wpid == pid
	}
	return watchByProbe(pid, probe, reap)
}
