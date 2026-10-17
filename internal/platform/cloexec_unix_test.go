//go:build linux || darwin

package platform

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// leakFds opens files and makes inheritable copies of them, one at a low
// number and one above 1000, as a process can inherit them from whatever
// started it (the GitHub runner hands its test process fds 142 and 145
// without close-on-exec, M2-13b). It returns the inheritable fds.
func leakFds(t *testing.T) []int {
	t.Helper()
	var fds []int
	for i, min := range []int{3, 1000, 4000} {
		f, err := os.Create(filepath.Join(t.TempDir(), "leak"+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD, min) // F_DUPFD: no close-on-exec
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { unix.Close(fd) })
		if fl, _ := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); fl&unix.FD_CLOEXEC != 0 {
			t.Fatalf("fd %d already close-on-exec", fd)
		}
		fds = append(fds, fd)
	}
	return fds
}

func cloexecFlags(t *testing.T, fds []int) {
	t.Helper()
	for _, fd := range fds {
		fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			t.Errorf("fd %d: %v (closed? it must only be flagged)", fd, err)
			continue
		}
		if fl&unix.FD_CLOEXEC == 0 {
			t.Errorf("fd %d is not close-on-exec", fd)
		}
	}
}

// stdFlags returns a check that fds 0-2 keep their flags: they are the
// process's stdio, which children are meant to get.
func stdFlags(t *testing.T) func() {
	var std [3]int
	for i := range std {
		std[i], _ = unix.FcntlInt(uintptr(i), unix.F_GETFD, 0)
	}
	return func() {
		for i := range std {
			if fl, _ := unix.FcntlInt(uintptr(i), unix.F_GETFD, 0); fl != std[i] {
				t.Errorf("fd %d flags changed from %#x to %#x: 0-2 must be left alone", i, std[i], fl)
			}
		}
	}
}

func TestCloseOnExecInherited_FlagsEveryInheritedFd(t *testing.T) {
	checkStd := stdFlags(t)
	fds := leakFds(t)
	if err := CloseOnExecInherited(); err != nil {
		t.Fatalf("CloseOnExecInherited() = %v", err)
	}
	cloexecFlags(t, fds)
	checkStd()
}

// The fallback (kernels before close_range's CLOEXEC flag, and macOS)
// flags each listed descriptor; it is tested on its own so the
// close_range path cannot hide it.
func TestMarkDirFds_FlagsEveryListedFd(t *testing.T) {
	dir := "/dev/fd"
	if _, err := os.Stat("/proc/self/fd"); err == nil {
		dir = "/proc/self/fd"
	}
	checkStd := stdFlags(t)
	fds := leakFds(t)
	if err := markDirFds(dir); err != nil {
		t.Fatalf("markDirFds(%s) = %v", dir, err)
	}
	cloexecFlags(t, fds)
	checkStd()
}
