//go:build windows

package sandbox

import (
	"context"
	"net"
	"os"

	"github.com/Microsoft/go-winio"

	"warden.dev/warden/internal/platform"
)

// dialDocker connects to the engine pipe dockerEndpoint chooses from
// DOCKER_HOST (Docker Desktop's pipe when unset). Every dial of the engine
// goes through it, M2-21's included.
func dialDocker(ctx context.Context) (net.Conn, error) {
	ep, err := dockerEndpoint(platform.OS(), os.Getenv("DOCKER_HOST"), "", nil)
	if err != nil {
		return nil, err
	}
	return winio.DialPipeContext(ctx, ep.Address)
}
