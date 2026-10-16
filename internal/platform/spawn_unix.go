//go:build !windows

package platform

import (
	"os"
	"syscall"
	"time"
)

// SpawnDaemon starts wardend detached from the terminal (its own session)
// with stdout/stderr appended to logPath. The returned Daemon reaps the
// child when asked whether it has exited: a released, unreaped child stays
// a zombie that kill(pid, 0) and Alive report as running, so a wardend that
// failed at startup looked alive until the caller's timeout (WH-4). Until
// it is reaped the pid cannot be reused, so the reap is always of our own
// child.
//
// sandboxed: host-trusted self-spawn of wardend for "warden daemon start";
// never an agent-requested process (CONFLICTS C-45, DECISIONS-poc D-006).
func SpawnDaemon(exe string, args []string, logPath string, stdin *os.File) (*Daemon, error) {
	logf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	in := stdin
	if in == nil {
		if in, err = os.Open(os.DevNull); err != nil {
			return nil, err
		}
		defer in.Close()
	}
	p, err := os.StartProcess(exe, append([]string{exe}, args...), &os.ProcAttr{
		Files: []*os.File{in, logf, logf},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return nil, err
	}
	pid := p.Pid
	// Release drops os.Process's own bookkeeping (a pidfd on Linux); the
	// child stays ours to reap with wait4.
	_ = p.Release()
	return &Daemon{PID: pid, poll: func() (ExitStatus, bool) { return reapChild(pid) }}, nil
}

// reapChild reaps pid if it has exited (wait4 with WNOHANG) and says how it
// ended. ECHILD (reaped elsewhere, or not our child) is reported as exited
// with an unknown status rather than as running forever.
func reapChild(pid int) (ExitStatus, bool) {
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	switch {
	case err == syscall.ECHILD:
		return ExitStatus{Code: -1, Other: "exit status unknown (reaped elsewhere)"}, true
	case err != nil || wpid != pid:
		return ExitStatus{}, false
	case ws.Signaled():
		return ExitStatus{Code: -1, Other: "killed by signal " + ws.Signal().String()}, true
	default:
		return ExitStatus{Code: ws.ExitStatus()}, true
	}
}

// TerminateGroup sends SIGTERM to a process group, waits up to grace, then
// SIGKILL (core §13.10).
func TerminateGroup(pgid int, grace time.Duration) error {
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) == syscall.ESRCH {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// Alive reports whether a process exists.
func Alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }
