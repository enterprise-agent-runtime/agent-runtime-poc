package linux_bwrap

// sandboxed: builds and starts the one process an L1 sandbox launch runs:
// bwrap with a Plan's argv (A06 §11.1), optionally under the fixed
// systemd-run --user --scope wrapper (A06 §11.6). Never agent input; the
// command inside bwrap is the warden-exec path of the Spec.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"warden.dev/warden/internal/platform"
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
	return p.command(ctx, bwrapPath, p.Argv[1:], []string{}, rpc, harnessStdio, seccompProg, info)
}

// Scope is the systemd user scope a launch runs in when cgroup v2
// delegation is available (A06 §11.6). Every field is checked by
// ScopedCommand.
type Scope struct {
	SystemdRun string         // absolute path of systemd-run
	EnvBin     string         // absolute path of env(1): bwrap runs as "env -i bwrap ..."
	Unit       string         // sandbox.ScopeUnit of the plan's sandbox
	Env        []string       // sandbox.ScopeEnv: the only variables systemd-run receives
	Limits     sandbox.Limits // the Spec's limits, rendered as unit properties
}

// ScopedCommand is Command under the A06 §11.6 wrapper:
//
//	systemd-run --user --scope --quiet --collect --unit=<unit>
//	  --property=MemoryMax=<m>M --property=MemorySwapMax=0
//	  --property=TasksMax=<pids> --property=CPUQuota=<cpus×100>%
//	  -- <env> -i <bwrap> <argv...>
//
// With --scope, systemd-run moves itself into the new scope and execs its
// command in place, so the process that becomes bwrap starts with
// systemd-run's environment. systemd-run is given only the ScopeEnv
// variables (it needs them to reach the user manager), and "env -i" clears
// even those before bwrap starts: bwrap's own environment, readable at
// /proc/1/environ inside the sandbox, is empty on this path too (M2-13
// "must hold"; INV-C, CWE-214, CWE-526). The process started is still the
// one that becomes bwrap (systemd-run and env exec in place), so it is a
// direct child of the daemon and --die-with-parent holds.
func (p *Plan) ScopedCommand(ctx context.Context, sc Scope, bwrapPath string, rpc, harnessStdio, seccompProg, info *os.File) (*exec.Cmd, error) {
	args, err := p.scopeArgs(sc, bwrapPath)
	if err != nil {
		return nil, err
	}
	return p.command(ctx, sc.SystemdRun, args, append([]string{}, sc.Env...), rpc, harnessStdio, seccompProg, info)
}

// scopeArgs checks a Scope against the plan and renders systemd-run's
// arguments.
func (p *Plan) scopeArgs(sc Scope, bwrapPath string) ([]string, error) {
	fail := func(format string, a ...any) ([]string, error) {
		return nil, fmt.Errorf("%w: systemd scope: %s", sandbox.ErrUnavailable, fmt.Sprintf(format, a...))
	}
	for _, x := range []struct{ name, path string }{{"bwrap", bwrapPath}, {"systemd-run", sc.SystemdRun}, {"env", sc.EnvBin}} {
		if !filepath.IsAbs(x.path) {
			return fail("%s path %q is not absolute", x.name, x.path)
		}
	}
	if !sandbox.ValidScopeUnit(sc.Unit) || p.SandboxID == "" || sc.Unit != sandbox.ScopeUnit(p.WardenHome, p.SandboxID) {
		return fail("unit %q is not the scope of sandbox %q", sc.Unit, p.SandboxID)
	}
	runtimeDir := false
	for _, e := range sc.Env {
		k, v, ok := strings.Cut(e, "=")
		switch {
		case !ok || v == "":
			return fail("environment entry %q has no value", k)
		case k == "XDG_RUNTIME_DIR":
			runtimeDir = true
		case k == "DBUS_SESSION_BUS_ADDRESS":
		default:
			return fail("variable %q is not one systemd-run needs (only XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS)", k)
		}
	}
	if !runtimeDir {
		return fail("XDG_RUNTIME_DIR is missing")
	}
	l := sc.Limits
	if l.MemoryMB <= 0 || l.Pids <= 0 || l.CPUs <= 0 {
		return fail("limits %+v must be positive (A06 §12)", l)
	}
	args := []string{
		"--user", "--scope", "--quiet", "--collect", "--unit=" + sc.Unit,
		"--property=MemoryMax=" + strconv.Itoa(l.MemoryMB) + "M",
		"--property=MemorySwapMax=0",
		"--property=TasksMax=" + strconv.Itoa(l.Pids),
		"--property=CPUQuota=" + strconv.Itoa(l.CPUs*100) + "%",
		"--", sc.EnvBin, "-i", bwrapPath,
	}
	return append(args, p.Argv[1:]...), nil
}

// command is the one place an exec.Cmd for a launch is built.
func (p *Plan) command(ctx context.Context, path string, args, env []string, rpc, harnessStdio, seccompProg, info *os.File) (*exec.Cmd, error) {
	files, err := p.extraFiles(rpc, harnessStdio, seccompProg, info)
	if err != nil {
		return nil, err
	}
	// Nothing this process inherited without close-on-exec may follow
	// bwrap into the sandbox: Go flags only the descriptors it opened, and
	// bwrap closes none (M2-13b, CWE-403). Done here, right before the Cmd
	// exists, so no caller can skip it.
	if err := platform.CloseOnExecInherited(); err != nil {
		return nil, fmt.Errorf("%w: marking inherited descriptors close-on-exec: %w", sandbox.ErrUnavailable, err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.ExtraFiles = files
	return cmd, nil
}

// Launch says how Start runs a plan.
type Launch struct {
	Bwrap  string    // absolute path of bwrap
	Scope  *Scope    // nil: no cgroup scope, the rlimit fallback (A06 §11.6)
	Stderr io.Writer // bwrap's and the executor's stderr; nil is /dev/null
}

// Running is a started launch: the process that is bwrap (directly, or
// after systemd-run and env exec'd into it).
type Running struct {
	proc *os.Process
	done chan struct{}

	mu    sync.Mutex
	state *os.ProcessState
}

// Start builds the launch with Command or ScopedCommand and starts it. A
// goroutine reaps the process; it ends when the process does, and ctx
// ending kills the process (exec.CommandContext). The caller closes its
// copies of the four files once Start returns.
func (p *Plan) Start(ctx context.Context, l Launch, rpc, harnessStdio, seccompProg, info *os.File) (*Running, error) {
	var cmd *exec.Cmd
	var err error
	if l.Scope != nil {
		cmd, err = p.ScopedCommand(ctx, *l.Scope, l.Bwrap, rpc, harnessStdio, seccompProg, info)
	} else {
		cmd, err = p.Command(ctx, l.Bwrap, rpc, harnessStdio, seccompProg, info)
	}
	if err != nil {
		return nil, err
	}
	cmd.Stderr = l.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: starting bwrap: %w", sandbox.ErrUnavailable, err)
	}
	r := &Running{proc: cmd.Process, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait() // the exit is read from ProcessState
		r.mu.Lock()
		r.state = cmd.ProcessState
		r.mu.Unlock()
		close(r.done)
	}()
	return r, nil
}

// Pid is the process id of bwrap on the host (the sandbox root).
func (r *Running) Pid() int { return r.proc.Pid }

// Done is closed once the process has exited and been reaped.
func (r *Running) Done() <-chan struct{} { return r.done }

// Kill sends SIGKILL to bwrap; the kernel then kills every process of the
// sandbox's PID namespace (A06 §11.1, §13.3 step 2). A process already
// reaped is not an error.
func (r *Running) Kill() error {
	select {
	case <-r.done:
		return nil
	default:
	}
	if err := r.proc.Kill(); err != nil && err != os.ErrProcessDone {
		return err
	}
	return nil
}

// State is the exit state once Done is closed, else nil.
func (r *Running) State() *os.ProcessState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}
