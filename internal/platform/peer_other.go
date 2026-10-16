//go:build !linux && !windows && !darwin

package platform

import "net"

// peerChecked relies on the 0600 socket inside a 0700 directory on Unix
// systems other than Linux and macOS, none of which is a PoC target.
func peerChecked(l net.Listener) net.Listener { return l }

// serverUID: the peer credential is not read on these systems; the client
// relies on the socket's owner, mode and directory (socketVerdict).
func serverUID(*net.UnixConn) (int, bool, error) { return -1, false, nil }
