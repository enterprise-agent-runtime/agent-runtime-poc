package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// dockerPing asks the local Docker engine for its version over its own
// endpoint (Unix socket or named pipe; never TCP) and returns the engine
// version and OS type. The full Engine API client (docker/docker/client)
// arrives with the L2 backend in M2.
func dockerPing(ctx context.Context) (version, osType string, err error) {
	return dockerPingVia(ctx, dialDocker)
}

// dockerPingVia is dockerPing over an injected dialer, so a test can stand
// in for whatever answers on the Docker endpoint.
func dockerPingVia(ctx context.Context, dial func(context.Context) (net.Conn, error)) (version, osType string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dial(ctx)
	}}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/version", nil)
	resp, err := hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Only the numeric code: the reason phrase is chosen by whatever
		// answers on the endpoint, unbounded and unescaped (CWE-150, M2-3).
		return "", "", fmt.Errorf("engine answered HTTP %d", resp.StatusCode)
	}
	var v struct {
		Version string `json:"Version"`
		Os      string `json:"Os"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return "", "", err
	}
	return v.Version, v.Os, nil
}
