//go:build linux

package platform

import (
	"errors"
	"math"

	"golang.org/x/sys/unix"
)

func closeOnExecInherited() error { return closeOnExecWith(unix.CloseRange) }

// closeOnExecWith takes close_range as a parameter so the fallback is
// tested on kernels that have the call.
func closeOnExecWith(closeRange func(first, last, flags uint) error) error {
	err := closeRange(3, math.MaxUint32, unix.CLOSE_RANGE_CLOEXEC)
	if err == nil {
		return nil
	}
	// ENOSYS: no close_range (< 5.9); EINVAL: no CLOSE_RANGE_CLOEXEC (< 5.11).
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		return markDirFds("/proc/self/fd")
	}
	return err
}
