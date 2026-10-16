//go:build !linux && !windows

package platform

import "net"

// peerChecked relies on the 0600 socket inside a 0700 directory on macOS;
// getpeereid is not exposed by the standard library (A05 §2.1 peer check is
// recorded as deferred for macOS in docs/PROGRESS.md).
func peerChecked(l net.Listener) net.Listener { return l }
