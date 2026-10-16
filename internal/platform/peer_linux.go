//go:build linux

package platform

import (
	"log/slog"
	"net"
	"os"
	"syscall"
)

// peerChecked closes connections from other users (SO_PEERCRED, A05 §2.1).
func peerChecked(l net.Listener) net.Listener { return &peerListener{l} }

type peerListener struct{ net.Listener }

func (p *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := p.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			return c, nil
		}
		if uid, _, err := serverUID(uc); err != nil || uid != os.Getuid() {
			slog.Warn("rejected a connection from another user on the daemon socket")
			c.Close()
			continue
		}
		return c, nil
	}
}

// serverUID returns the uid of the process at the other end of c
// (SO_PEERCRED: the client's on the daemon side, the server's on the
// client side). Linux always reports it.
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
