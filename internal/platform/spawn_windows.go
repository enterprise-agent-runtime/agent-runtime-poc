//go:build windows

package platform

import (
	"os"
	"syscall"
	"time"
)

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
	createNoWindow        = 0x08000000
)

// SpawnDaemon starts wardend detached (no console, own process group) with
// output appended to logPath. The returned Daemon holds a handle to the
// process, opened while os.Process still held its own, so it is the same
// process object even if the pid is reused after the exit (WH-4).
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
		Sys:   &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess | createNoWindow, HideWindow: true},
	})
	if err != nil {
		return nil, err
	}
	pid := p.Pid
	h, herr := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	_ = p.Release()
	if herr != nil {
		// The process is running; only its exit status will be unknown.
		// Fall back to the pid probe rather than fail a start that worked.
		return &Daemon{PID: pid, poll: func() (ExitStatus, bool) {
			return ExitStatus{Code: -1, Other: "exit status unknown"}, !Alive(pid)
		}}, nil
	}
	return &Daemon{
		PID: pid,
		poll: func() (ExitStatus, bool) {
			var code uint32
			if err := syscall.GetExitCodeProcess(h, &code); err != nil || code == stillActive {
				return ExitStatus{}, false
			}
			return ExitStatus{Code: int(code)}, true
		},
		close: func() { _ = syscall.CloseHandle(h) },
	}, nil
}

// TerminateGroup ends a process on Windows. Agent processes never run on
// the Windows host (L2 containers are stopped through Docker), so only host
// children reach this path; they are killed after the grace period.
func TerminateGroup(pid int, grace time.Duration) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !Alive(pid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return p.Kill()
}

const processQueryLimitedInformation = 0x1000
const stillActive = 259

// Alive reports whether a process exists and has not exited.
func Alive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
