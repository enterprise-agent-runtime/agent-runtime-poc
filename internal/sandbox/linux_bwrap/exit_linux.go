//go:build linux

package linux_bwrap

// SIGSYS deaths as A06 §7.4's seccomp_kill, on the daemon side.

import "syscall"

// sigsysExit is how bwrap reports a child killed by SIGSYS: as PID 1 of
// the namespace it exits with 128 + the signal (bubblewrap.c
// propagate_exit_status), like a shell.
const sigsysExit = 128 + int(syscall.SIGSYS)

// SeccompKill reports whether bwrap's wait status means the A06 §11.5
// filter killed warden-exec (KILL_PROCESS delivers SIGSYS, bwrap reports
// 159). bwrap itself runs unfiltered; a SIGSYS death of bwrap is counted
// too. The executor's report for its own children (internal/exec keeps its
// own one-line check, A02 R3) means only "died by SIGSYS": a tool can send
// itself SIGSYS, so that event is a hint, not proof of a filter hit.
func SeccompKill(ws syscall.WaitStatus) bool {
	if ws.Signaled() && ws.Signal() == syscall.SIGSYS {
		return true
	}
	return ws.Exited() && ws.ExitStatus() == sigsysExit
}
