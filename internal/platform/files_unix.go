//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"io/fs"
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

// mkdirOwnerOnly creates dir (not its parents) 0700; an existing dir is
// fs.ErrExist.
func mkdirOwnerOnly(dir string) error { return os.Mkdir(dir, 0o700) }

// protectDir chmods an existing directory 0700 through a descriptor opened
// on the directory itself (O_NOFOLLOW|O_DIRECTORY), after checking on that
// descriptor that uid owns it: another account's directory is refused, not
// re-permissioned (WH-1+2 review of c8f81ca).
func protectDir(dir string, me identity) error {
	uid := me.uid
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: no owner information", dir)
	}
	if int(st.Uid) != uid {
		return fmt.Errorf("%s is owned by uid %d, not by you (uid %d); use a directory of your own", dir, st.Uid, uid)
	}
	return f.Chmod(0o700)
}

// identity is the account Ensure protects directories for: the uid.
type identity struct{ uid int }

func currentIdentity() (identity, error) { return identity{uid: os.Getuid()}, nil }

// isLink reports a symbolic link.
func isLink(fi fs.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 }

// OwnerOnly reports whether path, not followed if it is a symlink, is owned
// by the current uid and not readable or writable by group or others. A
// symlink is never owner-only (WH-1+2 review of c8f81ca: os.Stat followed
// run/token to the user's own 0600 SSH key).
func OwnerOnly(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	return statOwnerOnly(fi)
}

func statOwnerOnly(fi os.FileInfo) (bool, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("%s: no owner information", fi.Name())
	}
	return ownerOnlyMode(fi.Mode().Perm(), int(st.Uid), os.Getuid()), nil
}

// ReadOwnerOnly opens path once, without following a symlink (O_NOFOLLOW)
// and without blocking on a FIFO, decides on that descriptor that it is a
// regular owner-only file, and reads at most max bytes from it. Checking by
// path and then reading by path let the file be swapped in between (WH-1+2
// review of c8f81ca, CWE-367/59).
func ReadOwnerOnly(path string, max int) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a symbolic link, not an owner-only file: %w", path, errNotOwnerOnly)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file: %w", path, errNotOwnerOnly)
	}
	ok, err := statOwnerOnly(fi)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, notOwnerOnly(path, statDetail(fi))
	}
	return readLimited(f, path, max)
}

// ownerOnlyDetail renders the file's permission bits and owner uid for
// failure messages.
func ownerOnlyDetail(path string) string {
	fi, err := os.Lstat(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return statDetail(fi)
}

func statDetail(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%s uid %d", fi.Mode(), st.Uid)
	}
	return fi.Mode().String()
}

// ReadOwnerWritable opens path once, without following a symlink and
// without blocking on a FIFO, decides on that descriptor that it is a
// regular file owned by the user that neither group nor others can write
// (ownerWritableMode), and reads at most max bytes from it. It is the
// integrity check for files whose content anyone may read but only the
// user may have written, such as the checkpoint public keys: ReadOwnerOnly
// is the secrecy check and refuses the 0644 key files every build before
// WH-4 wrote (WH-4 round 1, CWE-1188).
func ReadOwnerWritable(path string, max int) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a symbolic link, not a regular file: %w", path, errNotOwnerWritable)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (%v): %w", path, fi.Mode().Type(), errNotOwnerWritable)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("%s: no owner information", path)
	}
	if int(st.Uid) != os.Getuid() {
		return nil, notOwnerWritable(path, "is owned by another account", statDetail(fi),
			fmt.Sprintf("only if you trust its contents, replace it with a copy you own (cp %q %q.mine && mv %q.mine %q)", path, path, path, path))
	}
	if !ownerWritableMode(fi.Mode().Perm(), int(st.Uid), os.Getuid()) {
		return nil, notOwnerWritable(path, "is writable by group or others", statDetail(fi), fmt.Sprintf("if you trust its contents, run chmod go-w %q", path))
	}
	return readLimited(f, path, max)
}

// tightenMoved chmods a moved-aside file 0600 through a descriptor opened
// without following a link, if it is a regular file the user owns.
func tightenMoved(path string) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) == os.Getuid() {
		_ = f.Chmod(0o600)
	}
}
