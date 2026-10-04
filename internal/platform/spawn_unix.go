//go:build !windows

package platform

import (
	"os"
	"syscall"
	"time"
)

// SpawnDaemon starts wardend detached from the terminal (its own session)
// with stdout/stderr appended to logPath, and returns its pid.
//
// sandboxed: host-trusted self-spawn of wardend for "warden daemon start";
// never an agent-requested process (CONFLICTS C-45, DECISIONS-poc D-006).
func SpawnDaemon(exe string, args []string, logPath string, stdin *os.File) (int, error) {
	logf, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	in := stdin
	if in == nil {
		if in, err = os.Open(os.DevNull); err != nil {
			return 0, err
		}
		defer in.Close()
	}
	p, err := os.StartProcess(exe, append([]string{exe}, args...), &os.ProcAttr{
		Files: []*os.File{in, logf, logf},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return 0, err
	}
	pid := p.Pid
	_ = p.Release()
	return pid, nil
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
