//go:build darwin

package platform

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerChecked relies on the 0600 socket inside a 0700 directory on macOS
// (A05 §2.1 daemon-side peer check recorded as deferred for macOS in
// docs/PROGRESS.md; serverUID below would serve it).
func peerChecked(l net.Listener) net.Listener { return l }

// serverUID returns the uid of the process at the other end of c
// (LOCAL_PEERCRED, the xucred of the peer).
func serverUID(c *net.UnixConn) (int, bool, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, false, err
	}
	var cred *unix.Xucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return -1, false, err
	}
	if cerr != nil {
		return -1, false, cerr
	}
	return int(cred.Uid), true, nil
}
