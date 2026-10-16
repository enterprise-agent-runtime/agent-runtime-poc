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
		raw, err := uc.SyscallConn()
		if err != nil {
			c.Close()
			continue
		}
		var cred *syscall.Ucred
		var cerr error
		_ = raw.Control(func(fd uintptr) {
			cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		})
		if cerr != nil || cred == nil || int(cred.Uid) != os.Getuid() {
			slog.Warn("rejected a connection from another user on the daemon socket")
			c.Close()
			continue
		}
		return c, nil
	}
}
