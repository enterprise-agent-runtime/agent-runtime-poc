//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
)

// LockDaemon takes the per-home daemon lock: an exclusive flock on
// run/wardend.lock, held until the returned unlock is called or the
// process ends (the kernel drops it on exit, so a crash leaves no stale
// lock). wardend holds it for its whole lifetime, from before Listen until
// after the shutdown cleanup, so Listen's stale-socket removal and the
// token write are exclusive per home: after a crash two wardends could
// both remove the stale socket and bind, the second removing the first
// one's fresh socket (WH-4, CWE-367). A lock another process holds is
// ErrDaemonRunning (wardend exits 3, as for a lost bind).
//
// The file is opened without following a symlink (O_NOFOLLOW) and without
// blocking on a FIFO, must be a regular file, and must be owner-only on
// that descriptor; it is created 0600 in the owner-only run directory
// (Ensure) and never removed, since removing a lock file lets a second
// process lock a new inode while the first still holds the old one.
func LockDaemon(home string) (unlock func(), err error) {
	p := Layout{Home: home}.Lock()
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("the daemon lock %s is a symbolic link; remove it: %w", p, errNotOwnerOnly)
		}
		return nil, fmt.Errorf("open the daemon lock: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("the daemon lock: %w", err)
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("the daemon lock %s is not a regular file (%v); remove it: %w", p, fi.Mode().Type(), errNotOwnerOnly)
	}
	if ok, err := statOwnerOnly(fi); err != nil || !ok {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, notOwnerOnly(p, statDetail(fi))
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrDaemonRunning
		}
		return nil, fmt.Errorf("lock %s: %w", p, err)
	}
	var once sync.Once
	return func() { once.Do(func() { f.Close() }) }, nil
}
