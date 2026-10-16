package exec

import (
	"errors"

	"golang.org/x/sys/unix"
)

// resolveFlags are the openat2 resolve flags of design A06 §5.2: stay
// beneath the root fd, follow no symlink at all (the canonical path has
// none), no /proc magic links, no mount crossing (which also refuses a
// deny-list mask mount, A06 §11.3).
const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV

// probeOpenat2 returns openat2 when the kernel supports it with these
// flags (Linux >= 5.6, A06 features.openat2), nil otherwise. Any failure to
// open the root itself counts as unsupported: ENOSYS on an old kernel, EPERM
// where a container's seccomp profile filters the call, EINVAL where a flag
// is unknown. The component walk then does the same job.
func probeOpenat2(rootfd int) func(int, string, int) (int, error) {
	fd, err := openat2(rootfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC)
	if err != nil {
		return nil
	}
	_ = unix.Close(fd)
	return openat2
}

func openat2(dirfd int, rel string, flags int) (int, error) {
	how := unix.OpenHow{Flags: uint64(flags), Resolve: resolveFlags}
	// openat2 refuses a mode without O_CREAT or O_TMPFILE (EINVAL), and
	// O_TMPFILE contains the O_DIRECTORY bit, so it is tested as a whole.
	if flags&unix.O_CREAT != 0 || flags&unix.O_TMPFILE == unix.O_TMPFILE {
		how.Mode = createMode
	}
	for {
		fd, err := unix.Openat2(dirfd, rel, &how)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}
