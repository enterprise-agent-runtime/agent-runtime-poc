//go:build linux

package platform

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// On kernels without close_range (< 5.9, ENOSYS) or without its CLOEXEC
// flag (< 5.11, EINVAL) the descriptors are flagged one by one; any other
// error is returned, not papered over.
func TestCloseOnExecWith_FallsBackOnOldKernels(t *testing.T) {
	checkStd := stdFlags(t)
	defer checkStd()
	for _, e := range []error{unix.ENOSYS, unix.EINVAL} {
		fds := leakFds(t)
		if err := closeOnExecWith(func(_, _, _ uint) error { return e }); err != nil {
			t.Fatalf("close_range %v: closeOnExecWith() = %v", e, err)
		}
		cloexecFlags(t, fds)
	}
	if err := closeOnExecWith(func(_, _, _ uint) error { return unix.EPERM }); !errors.Is(err, unix.EPERM) {
		t.Errorf("close_range EPERM: closeOnExecWith() = %v, want EPERM", err)
	}
}
