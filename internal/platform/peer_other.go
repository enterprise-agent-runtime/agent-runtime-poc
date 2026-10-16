//go:build !linux && !windows && !darwin

package platform

import "net"

// serverUID: the peer credential is not read on Unix systems other than
// Linux and macOS, none of which is a PoC target. Both sides then rely on
// the socket's owner, mode and directory (ensureSocketDir, socketVerdict).
func serverUID(*net.UnixConn) (int, bool, error) { return -1, false, nil }
