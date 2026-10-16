//go:build linux

package linux_bwrap

import (
	"syscall"
	"testing"
)

func exited(code int) syscall.WaitStatus             { return syscall.WaitStatus(code << 8) }
func signaled(sig syscall.Signal) syscall.WaitStatus { return syscall.WaitStatus(sig) }

// TestSeccompKill maps bwrap's wait status to A06 §7.4's seccomp_kill for
// the daemon. bwrap never dies of the filter's SIGSYS itself: as PID 1 of
// the namespace it exits with 128 + the child's signal (bubblewrap.c
// propagate_exit_status), so 159 is warden-exec killed by the A06 §11.5
// KILL_PROCESS rows. The executor's own check on its children
// (internal/exec, A02 R3) is a separate one-liner and means only "died by
// SIGSYS" (a tool can kill -SYS itself).
func TestSeccompKill(t *testing.T) {
	cases := []struct {
		name string
		ws   syscall.WaitStatus
		want bool
	}{
		{"bwrap reports SIGSYS", exited(128 + int(syscall.SIGSYS)), true},
		{"bwrap reports SIGKILL", exited(128 + int(syscall.SIGKILL)), false},
		{"bwrap exited 0", exited(0), false},
		{"bwrap exited 31", exited(int(syscall.SIGSYS)), false},
		{"bwrap itself killed by SIGSYS", signaled(syscall.SIGSYS), true},
		{"bwrap killed by SIGKILL", signaled(syscall.SIGKILL), false},
	}
	for _, c := range cases {
		if got := SeccompKill(c.ws); got != c.want {
			t.Errorf("%s: SeccompKill(%#x) = %v, want %v", c.name, uint32(c.ws), got, c.want)
		}
	}
}
