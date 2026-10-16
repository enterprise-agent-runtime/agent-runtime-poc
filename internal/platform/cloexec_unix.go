//go:build linux || darwin

package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// markDirFds sets FD_CLOEXEC on every descriptor >= 3 listed in dir
// (/proc/self/fd or /dev/fd). The listing's own descriptor is among them;
// it is close-on-exec already and closed by the time it is flagged, which
// is EBADF and ignored, as is any other descriptor that closes meanwhile.
func markDirFds(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("platform: list %s: %w", dir, err)
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd < 3 {
			continue
		}
		fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if errors.Is(err, unix.EBADF) {
			continue
		}
		if err != nil {
			return fmt.Errorf("platform: fd %d: %w", fd, err)
		}
		if fl&unix.FD_CLOEXEC != 0 {
			continue
		}
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, fl|unix.FD_CLOEXEC); err != nil && !errors.Is(err, unix.EBADF) {
			return fmt.Errorf("platform: fd %d: %w", fd, err)
		}
	}
	return nil
}
