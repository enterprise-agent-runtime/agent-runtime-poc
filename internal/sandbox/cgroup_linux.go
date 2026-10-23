//go:build linux

package sandbox

// sandboxed: runs only the fixed probe "systemd-run --user --scope --quiet
// --collect -- /bin/true" (A06 §11.6) with the two ScopeEnv variables, for
// system.doctor and the L1 launcher's cgroup decision; never agent input.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CgroupRoot is where the cgroup v2 hierarchy is mounted on a unified host.
const CgroupRoot = "/sys/fs/cgroup"

// systemdRunPaths are the only places systemd-run is taken from: nothing
// is looked up in the daemon's PATH.
var systemdRunPaths = []string{"/usr/bin/systemd-run", "/bin/systemd-run"}

// scopeProbeTimeout bounds the probe scope; systemd-run answers in
// milliseconds when the user manager is up.
const scopeProbeTimeout = 10 * time.Second

// ProbeCgroup observes whether this host can hold an L1 sandbox in a
// systemd user scope with cgroup v2 limits (A06 §11.6): systemd-run exists
// at a fixed path, the bus variables are set, a probe scope running
// /bin/true starts, cgroup v2 is the unified hierarchy, and the user
// manager's cgroup.controllers lists memory, pids and cpu. The probe scope
// is the only process it starts, with only the ScopeEnv variables.
func ProbeCgroup(ctx context.Context) CgroupStatus {
	return probeCgroup(ctx, CgroupRoot, os.Getuid(), os.Getenv, systemdRunPaths)
}

func probeCgroup(ctx context.Context, root string, uid int, getenv func(string) string, candidates []string) CgroupStatus {
	var st CgroupStatus
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 && filepath.IsAbs(p) {
			st.SystemdRun = p
			break
		}
	}
	st.Env, st.EnvErr = ScopeEnv(getenv)
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err == nil {
		st.Unified = true
	}
	st.ServiceDir = UserServiceCgroup(root, uid)
	st.Controllers, st.ControllersErr = readControllers(filepath.Join(st.ServiceDir, "cgroup.controllers"))
	if st.SystemdRun != "" && st.EnvErr == nil {
		st.ScopeTried = true
		st.ScopeErr = runProbeScope(ctx, st.SystemdRun, st.Env)
	}
	return st
}

func readControllers(p string) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxControllersLen+1))
	if err != nil {
		return nil, err
	}
	return ParseControllers(string(b))
}

// runProbeScope starts the fixed probe scope. Its error keeps the first
// line of systemd-run's stderr, capped, so doctor can say why.
func runProbeScope(ctx context.Context, systemdRun string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, scopeProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, systemdRun, "--user", "--scope", "--quiet", "--collect", "--", "/bin/true")
	cmd.Env = append([]string{}, env...)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{buf: &stderr, max: 512}
	err := cmd.Run()
	if err == nil {
		return nil
	}
	line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
	var ee *exec.ExitError
	if errors.As(err, &ee) && line != "" {
		return fmt.Errorf("%w: %s", err, line)
	}
	return err
}

// limitedBuffer keeps the first max bytes written and discards the rest.
type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.buf.Write(p[:room])
	}
	return len(p), nil
}
