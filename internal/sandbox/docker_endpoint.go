package sandbox

import (
	"fmt"
	"regexp"
	"strings"
)

// dockerEndpointAddr is where the Docker engine is dialled: a Unix socket
// path ("unix") or a local named pipe ("npipe").
type dockerEndpointAddr struct {
	Network string // "unix" | "npipe"
	Address string
}

// Default endpoints.
const (
	defaultDockerSocket = "/var/run/docker.sock"
	defaultDockerPipe   = `\\.\pipe\docker_engine`
)

// dockerScheme is a URL scheme short and plain enough to show.
var dockerScheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]{0,15}$`)

// localPipeName is the last element of a local pipe: no separators, no
// controls, nothing a path could reinterpret.
var localPipeName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// dockerEndpoint chooses the engine endpoint from DOCKER_HOST and, on
// Linux and macOS, $XDG_RUNTIME_DIR (CLAUDE.md §4: the engine is reached
// over its own Unix socket or named pipe, never TCP). It is pure: the
// caller passes the host OS, both variables and ownedByUs, which reports
// whether a path is a socket owned by this process's user.
//
// Every dial of the Docker engine goes through this function (dialDocker):
// the gate, doctor, and the L2 backend of M2-21, which must not choose an
// endpoint of its own, or the gate would vouch for one engine while the
// sandbox is created on another.
//
//   - DOCKER_HOST set: unix://<absolute path> on Linux and macOS;
//     npipe://<local pipe> on Windows (npipe:////./pipe/<name> or
//     npipe://\\.\pipe\<name>); anything else, tcp:// and ssh:// included,
//     is refused rather than replaced by a default, so the user never gets
//     a different engine from the one they named.
//   - DOCKER_HOST unset: on Windows Docker Desktop's pipe; on Linux and
//     macOS $XDG_RUNTIME_DIR/docker.sock (rootless Docker) when it is an
//     absolute path to a socket owned by this user, else
//     /var/run/docker.sock.
//
// A refusal is an *EngineError with engine_unreachable. It shows the
// scheme only when it has a plain shape: DOCKER_HOST is the user's, but a
// value copied from elsewhere could still carry escape sequences.
func dockerEndpoint(hostOS, dockerHost, xdgRuntimeDir string, ownedByUs func(string) bool) (dockerEndpointAddr, error) {
	refuse := func(format string, a ...any) (dockerEndpointAddr, error) {
		return dockerEndpointAddr{}, &EngineError{Code: EngineUnreachableCode, Reason: fmt.Sprintf(format, a...)}
	}
	if dockerHost == "" {
		if hostOS == "windows" {
			return dockerEndpointAddr{"npipe", defaultDockerPipe}, nil
		}
		if strings.HasPrefix(xdgRuntimeDir, "/") && ownedByUs != nil {
			p := strings.TrimRight(xdgRuntimeDir, "/") + "/docker.sock"
			if ownedByUs(p) {
				return dockerEndpointAddr{"unix", p}, nil
			}
		}
		return dockerEndpointAddr{"unix", defaultDockerSocket}, nil
	}
	scheme, rest, ok := strings.Cut(dockerHost, "://")
	if !ok {
		return refuse("DOCKER_HOST without a scheme is not supported; L2 uses the local socket (unix:// or, on Windows, npipe://)")
	}
	switch {
	case scheme == "unix" && hostOS != "windows":
		if !strings.HasPrefix(rest, "/") {
			return refuse("DOCKER_HOST unix:// needs an absolute socket path")
		}
		return dockerEndpointAddr{"unix", rest}, nil
	case scheme == "npipe" && hostOS == "windows":
		p := strings.ReplaceAll(rest, "/", `\`)
		name, ok := strings.CutPrefix(p, `\\.\pipe\`)
		if !ok || !localPipeName.MatchString(name) {
			return refuse(`DOCKER_HOST npipe:// must name a local pipe (npipe:////./pipe/<name>)`)
		}
		return dockerEndpointAddr{"npipe", `\\.\pipe\` + name}, nil
	}
	shown := "(not shown)"
	if dockerScheme.MatchString(scheme) {
		shown = scheme
	}
	return refuse("DOCKER_HOST scheme %s is not supported; L2 uses the local socket or pipe of the engine, never a remote one", shown)
}
