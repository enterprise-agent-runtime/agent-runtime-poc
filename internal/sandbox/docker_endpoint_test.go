package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestDockerEndpoint_Selects guards the review finding on M2-17: a
// DOCKER_HOST with a non-unix scheme (tcp://, ssh://) silently fell back to
// /var/run/docker.sock, which may be a rootful engine the user did not
// mean, and Windows ignored DOCKER_HOST altogether. The endpoint is chosen
// by one pure function, and the gate and M2-21 dial through it:
//
//   - a non-local scheme is refused (engine_unreachable), never replaced
//     by a default: L2 uses the local socket or pipe only (CLAUDE.md §4);
//   - unix:// with an absolute path on Linux and macOS, as before;
//   - npipe:// on Windows only, and only naming a local pipe;
//   - unset on Linux/macOS: $XDG_RUNTIME_DIR/docker.sock (the rootless
//     socket) when it is a socket owned by this user, else
//     /var/run/docker.sock; unset on Windows: Docker Desktop's pipe.
func TestDockerEndpoint_Selects(t *testing.T) {
	ours := func(owned ...string) func(string) bool {
		return func(p string) bool {
			for _, o := range owned {
				if p == o {
					return true
				}
			}
			return false
		}
	}
	none := ours()
	cases := []struct {
		name, host, dockerHost, xdg string
		owned                       func(string) bool
		network, address            string // "" = refused
		reason                      string // refusal must contain
	}{
		{"unset linux", "linux", "", "", none, "unix", "/var/run/docker.sock", ""},
		{"unset linux, rootless socket ours", "linux", "", "/run/user/1000", ours("/run/user/1000/docker.sock"), "unix", "/run/user/1000/docker.sock", ""},
		{"unset linux, rootless socket not ours", "linux", "", "/run/user/1000", none, "unix", "/var/run/docker.sock", ""},
		{"unset linux, XDG relative", "linux", "", "run/user/1000", ours("run/user/1000/docker.sock"), "unix", "/var/run/docker.sock", ""},
		{"unset darwin", "darwin", "", "", none, "unix", "/var/run/docker.sock", ""},
		{"unix linux", "linux", "unix:///run/user/1000/docker.sock", "", none, "unix", "/run/user/1000/docker.sock", ""},
		{"unix darwin", "darwin", "unix:///Users/dev/.docker/run/docker.sock", "", none, "unix", "/Users/dev/.docker/run/docker.sock", ""},
		{"unix relative", "linux", "unix://docker.sock", "", none, "", "", "absolute"},
		{"unix empty", "linux", "unix://", "", none, "", "", "absolute"},
		{"tcp", "linux", "tcp://127.0.0.1:2375", "", none, "", "", "DOCKER_HOST scheme tcp is not supported; L2 uses the local socket"},
		{"tcp on windows", "windows", "tcp://localhost:2375", "", none, "", "", "DOCKER_HOST scheme tcp is not supported"},
		{"ssh", "linux", "ssh://dev@build-host", "", none, "", "", "DOCKER_HOST scheme ssh is not supported"},
		{"http", "darwin", "http://localhost:2375", "", none, "", "", "DOCKER_HOST scheme http is not supported"},
		{"fd", "linux", "fd://", "", none, "", "", "DOCKER_HOST scheme fd is not supported"},
		{"no scheme", "linux", "/var/run/docker.sock", "", none, "", "", "without a scheme"},
		{"scheme with escape not shown", "linux", "t\x1b[2Jcp://x", "", none, "", "", "(not shown)"},
		{"npipe on linux", "linux", "npipe:////./pipe/docker_engine", "", none, "", "", "DOCKER_HOST scheme npipe is not supported"},
		{"unset windows", "windows", "", "", none, "npipe", `\\.\pipe\docker_engine`, ""},
		{"npipe windows, slashes", "windows", "npipe:////./pipe/docker_engine", "", none, "npipe", `\\.\pipe\docker_engine`, ""},
		{"npipe windows, backslashes", "windows", `npipe://\\.\pipe\dockerDesktopLinuxEngine`, "", none, "npipe", `\\.\pipe\dockerDesktopLinuxEngine`, ""},
		{"npipe windows, remote host", "windows", "npipe:////build-host/pipe/docker_engine", "", none, "", "", "local pipe"},
		{"npipe windows, no pipe", "windows", "npipe:////./pipe/", "", none, "", "", "local pipe"},
		{"npipe windows, nested", "windows", "npipe:////./pipe/a/b", "", none, "", "", "local pipe"},
		{"npipe windows, not a pipe", "windows", "npipe:////./C:/x", "", none, "", "", "local pipe"},
		{"unix on windows", "windows", "unix:///tmp/docker.sock", "", none, "", "", "DOCKER_HOST scheme unix is not supported"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ep, err := dockerEndpoint(c.host, c.dockerHost, c.xdg, c.owned)
			if c.network == "" {
				var ee *EngineError
				if !errors.As(err, &ee) || ee.Code != EngineUnreachableCode || !errors.Is(err, ErrUnavailable) {
					t.Fatalf("dockerEndpoint = %+v, %v; want engine_unreachable", ep, err)
				}
				if !strings.Contains(ee.Reason, c.reason) {
					t.Errorf("reason %q lacks %q", ee.Reason, c.reason)
				}
				if strings.ContainsAny(ee.Reason, "\x1b") {
					t.Errorf("reason %q carries a control character", ee.Reason)
				}
				return
			}
			if err != nil || ep.Network != c.network || ep.Address != c.address {
				t.Fatalf("dockerEndpoint = %+v, %v; want %s %s", ep, err, c.network, c.address)
			}
		})
	}
}

// TestDockerEndpoint_RefusalReachesVerdict: a refused endpoint is the
// gate's engine_unreachable with the endpoint reason, not "not reachable:
// sandbox_unavailable: ..." nested inside another one.
func TestDockerEndpoint_RefusalReachesVerdict(t *testing.T) {
	refuse := func(context.Context) (EngineFacts, error) {
		_, err := dockerEndpoint("linux", "tcp://127.0.0.1:2375", "", nil)
		return EngineFacts{}, err
	}
	v := engineVerdict(context.Background(), "linux", refuse)
	if v.Code != EngineUnreachableCode || !strings.HasPrefix(v.Reason, "DOCKER_HOST scheme tcp") {
		t.Fatalf("verdict %+v", v)
	}
}
