//go:build linux

package linux_bwrap

// The program on fd 5 (design A06 §11.1, §11.5): a sealed memfd.

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/sandbox"
	"warden.dev/warden/internal/sandbox/seccomp"
)

// SeccompFile renders the seccomp program for a into a memfd, seals it
// against every change (write, grow, shrink, and further seal changes) and
// returns it at offset 0, ready for exec.Cmd.ExtraFiles at fd 5. The fd is
// close-on-exec in the daemon; ExtraFiles dup2s it for bwrap only. The
// caller closes it once bwrap has started.
func SeccompFile(a seccomp.Arch) (*os.File, error) {
	prog, err := seccomp.Render(a)
	if err != nil {
		return nil, fmt.Errorf("%w: seccomp program: %w", sandbox.ErrUnavailable, err)
	}
	fd, err := unix.MemfdCreate("warden-seccomp", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("%w: memfd_create: %w", sandbox.ErrUnavailable, err)
	}
	f := os.NewFile(uintptr(fd), "warden-seccomp")
	fail := func(step string, err error) (*os.File, error) {
		f.Close()
		return nil, fmt.Errorf("%w: seccomp memfd %s: %w", sandbox.ErrUnavailable, step, err)
	}
	if _, err := f.Write(prog); err != nil {
		return fail("write", err)
	}
	if _, err := unix.FcntlInt(f.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE); err != nil {
		return fail("seal", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fail("seek", err)
	}
	return f, nil
}
