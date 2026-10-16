package linux_bwrap

// sandboxed: builds the exec.Cmd that starts bwrap with a Plan's argv (A06
// §11.1); the launcher (M2-15) starts it. Never agent input.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"warden.dev/warden/internal/sandbox"
)

// Command returns the exec.Cmd that starts bwrap with this plan: bwrapPath
// (absolute, so nothing is looked up in the daemon's PATH), the plan's argv,
// the four files as fds 3-6, and an empty environment.
//
// The environment is part of the plan, not the launcher's choice:
// --clearenv clears only what bwrap passes to warden-exec, while bwrap's
// own PID 1 inside the namespace keeps the environment bwrap was started
// with, and /proc/1/environ is readable by the sandboxed processes (same
// uid). An exec.Cmd with Env == nil inherits the daemon's environment, an
// exported API key included (M2-13 review; INV-C, CWE-214, CWE-526). bwrap
// needs no variable of its own.
//
// Stdin, stdout and stderr are left nil (/dev/null); the launcher may set
// them before Start.
func (p *Plan) Command(ctx context.Context, bwrapPath string, rpc, harnessStdio, seccompProg, info *os.File) (*exec.Cmd, error) {
	if !filepath.IsAbs(bwrapPath) {
		return nil, fmt.Errorf("%w: bwrap path %q is not absolute", sandbox.ErrUnavailable, bwrapPath)
	}
	files, err := p.extraFiles(rpc, harnessStdio, seccompProg, info)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bwrapPath, p.Argv[1:]...)
	cmd.Env = []string{}
	cmd.ExtraFiles = files
	return cmd, nil
}
