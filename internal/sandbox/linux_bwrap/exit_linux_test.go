//go:build linux

package linux_bwrap

import (
	"syscall"
	"testing"
)

func exited(code int) syscall.WaitStatus             { return syscall.WaitStatus(code << 8) }
func signaled(sig syscall.Signal) syscall.WaitStatus { return syscall.WaitStatus(sig) }

// TestSeccompKill maps a death by SIGSYS (the filter's KILL_PROCESS rows,
// A06 §11.5) to A06 §7.4's seccomp_kill. The executor sees its children's
// status directly. The daemon sees bwrap, which never dies of the signal
// itself: as PID 1 of the namespace it exits with 128 + the child's signal
// (propagate_exit_status in bubblewrap.c), so 159 from bwrap is a seccomp
// kill of warden-exec, while 159 from a process waited on directly is
// just an exit code.
func TestSeccompKill(t *testing.T) {
	cases := []struct {
		name     string
		ws       syscall.WaitStatus
		viaBwrap bool
		want     bool
	}{
		{"child killed by SIGSYS", signaled(syscall.SIGSYS), false, true},
		{"child killed by SIGKILL", signaled(syscall.SIGKILL), false, false},
		{"child killed by SIGXFSZ", signaled(syscall.SIGXFSZ), false, false},
		{"child exited 159", exited(128 + int(syscall.SIGSYS)), false, false},
		{"child exited 0", exited(0), false, false},
		{"bwrap reports SIGSYS", exited(128 + int(syscall.SIGSYS)), true, true},
		{"bwrap reports SIGKILL", exited(128 + int(syscall.SIGKILL)), true, false},
		{"bwrap exited 0", exited(0), true, false},
		{"bwrap exited 31", exited(int(syscall.SIGSYS)), true, false},
		{"bwrap itself killed by SIGSYS", signaled(syscall.SIGSYS), true, true},
		{"bwrap killed by SIGKILL", signaled(syscall.SIGKILL), true, false},
	}
	for _, c := range cases {
		if got := SeccompKill(c.ws, c.viaBwrap); got != c.want {
			t.Errorf("%s: SeccompKill(%#x, %v) = %v, want %v", c.name, uint32(c.ws), c.viaBwrap, got, c.want)
		}
	}
}
