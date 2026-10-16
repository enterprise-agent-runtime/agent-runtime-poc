//go:build linux

package linux_bwrap

// SIGSYS deaths as A06 §7.4's seccomp_kill.

import "syscall"

// sigsysExit is how bwrap reports a child killed by SIGSYS: as PID 1 of
// the namespace it exits with 128 + the signal (bubblewrap.c
// propagate_exit_status), like a shell.
const sigsysExit = 128 + int(syscall.SIGSYS)

// SeccompKill reports whether a wait status means the A06 §11.5 filter
// killed the process (KILL_PROCESS delivers SIGSYS). viaBwrap is true when
// ws is bwrap's own status, as the daemon sees it; the executor waits on
// its children directly and passes false. An exit code of 159 from a
// directly waited process is an ordinary exit and is not counted.
func SeccompKill(ws syscall.WaitStatus, viaBwrap bool) bool {
	if ws.Signaled() && ws.Signal() == syscall.SIGSYS {
		return true
	}
	return viaBwrap && ws.Exited() && ws.ExitStatus() == sigsysExit
}
