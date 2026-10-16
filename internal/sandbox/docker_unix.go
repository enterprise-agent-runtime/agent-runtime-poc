//go:build !windows

package sandbox

import (
	"context"
	"net"
	"os"
	"strings"
)

// dialDocker connects to $DOCKER_HOST (unix:// only) or /var/run/docker.sock.
func dialDocker(ctx context.Context) (net.Conn, error) {
	path := "/var/run/docker.sock"
	if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		path = strings.TrimPrefix(h, "unix://")
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
