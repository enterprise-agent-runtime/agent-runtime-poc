//go:build windows

package sandbox

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// dialDocker connects to Docker Desktop's engine pipe.
func dialDocker(ctx context.Context) (net.Conn, error) {
	return winio.DialPipeContext(ctx, `\\.\pipe\docker_engine`)
}
