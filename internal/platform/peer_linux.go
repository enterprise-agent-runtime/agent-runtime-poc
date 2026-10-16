//go:build linux

package platform

import (
	"net"
	"syscall"
)

// serverUID returns the uid of the process at the other end of c
// (SO_PEERCRED: the client's on the daemon side, the server's on the
// client side; A05 §2.1). Linux always reports it.
func serverUID(c *net.UnixConn) (int, bool, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, false, err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return -1, false, err
	}
	if cerr != nil {
		return -1, false, cerr
	}
	return int(cred.Uid), true, nil
}
