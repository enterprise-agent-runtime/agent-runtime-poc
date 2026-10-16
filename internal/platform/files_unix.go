//go:build !windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WriteOwnerOnly writes a file readable only by the current user (0600),
// atomically: temp file, fsync, rename (A05 §3.1 token.tmp → token).
func WriteOwnerOnly(path string, data []byte) error {
	return writeOwnerOnly(path, data, writeHooks{})
}

// writeOwnerOnly creates the temp file with a random name and O_EXCL, so a
// file or symlink another account planted at a predictable name is refused
// rather than truncated and reused with its own mode (WH-1+2, security
// review of FX-3, CWE-377).
func writeOwnerOnly(path string, data []byte, h writeHooks) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := h.temp(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := writeSyncClose(f, data, h); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ensureOwnerOnlyDir creates dir 0700.
func ensureOwnerOnlyDir(dir string) error { return os.MkdirAll(dir, 0o700) }

// OwnerOnly reports whether path is owned by the current uid and not
// readable or writable by group or others.
func OwnerOnly(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("%s: no owner information", path)
	}
	return ownerOnlyMode(fi.Mode().Perm(), int(st.Uid), os.Getuid()), nil
}

// ownerOnlyDetail renders the file's permission bits and owner uid for
// failure messages.
func ownerOnlyDetail(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%s uid %d", fi.Mode().Perm(), st.Uid)
	}
	return fi.Mode().Perm().String()
}
