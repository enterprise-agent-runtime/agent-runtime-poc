//go:build darwin

package platform

import (
	"net"

	"golang.org/x/sys/unix"
)

// serverUID returns the uid of the process at the other end of c
// (LOCAL_PEERCRED, the peer's xucred: the client's on the daemon side, the
// server's on the client side; A05 §2.1).
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
