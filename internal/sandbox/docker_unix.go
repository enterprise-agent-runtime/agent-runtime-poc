//go:build !windows

package sandbox

import (
	"context"
	"net"
	"os"
	"syscall"

	"warden.dev/warden/internal/platform"
)

// dialDocker connects to the engine endpoint dockerEndpoint chooses from
// DOCKER_HOST and $XDG_RUNTIME_DIR. Every dial of the engine goes through
// it, M2-21's included.
func dialDocker(ctx context.Context) (net.Conn, error) {
	ep, err := dockerEndpoint(platform.OS(), os.Getenv("DOCKER_HOST"), os.Getenv("XDG_RUNTIME_DIR"), socketOwnedByUs)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	return d.DialContext(ctx, ep.Network, ep.Address)
}

// socketOwnedByUs reports whether path is a socket (not followed if it is
// a symbolic link) owned by this process's user.
func socketOwnedByUs(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
