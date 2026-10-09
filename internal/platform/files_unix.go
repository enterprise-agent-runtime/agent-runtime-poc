//go:build !windows

package platform

import (
	"os"
	"path/filepath"
)

// WriteOwnerOnly writes a file readable only by the current user (0600),
// atomically: temp file, fsync, rename (A05 §3.1 token.tmp → token).
func WriteOwnerOnly(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// OwnerOnly reports whether path is not readable by group or others.
func OwnerOnly(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return fi.Mode().Perm()&0o077 == 0, nil
}

// ownerOnlyDetail renders the file's permission bits for failure messages.
func ownerOnlyDetail(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return fi.Mode().Perm().String()
}
