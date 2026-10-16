//go:build linux

package linux_bwrap

// The host side of Probes.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// DefaultProbes observes this host: filepath.EvalSymlinks, lstat(2),
// readlink(2) and the daemon's uid.
func DefaultProbes() Probes {
	return Probes{
		EvalSymlinks: filepath.EvalSymlinks,
		LstatSocket:  lstatSocket,
		Lstat:        os.Lstat,
		Readlink:     os.Readlink,
		UID:          os.Getuid(),
	}
}

// lstatSocket returns the type and owner of p itself, not of a link target.
func lstatSocket(p string) (fs.FileMode, int, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return 0, 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("lstat %s: no owner", p)
	}
	return fi.Mode(), int(st.Uid), nil
}
