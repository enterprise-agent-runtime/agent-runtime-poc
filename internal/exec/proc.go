package exec

// The process methods of warden.exec/1 (WRD-04 §5, WRD-16 §9; design A06
// §4.2 exec.proc.spawn/io/wait/signal and exec.shutdown, §6, §7): the
// configuration and the method registration, shared by every OS. The
// process table itself is proc_unix.go (Linux and macOS); on other systems
// the executor does not run (it runs in the L2 Linux container on a
// Windows host, CLAUDE.md §3), and proc_other.go refuses. The package doc
// is in server.go.

import (
	"context"
	"errors"
	"time"

	"warden.dev/warden/internal/execproto"
)

// Defaults of A06 §7.2, §7.6 and §7.8.
const (
	chunkInterval   = 100 * time.Millisecond // §7.2 "at most every 100 ms"
	defaultGrace    = 3 * time.Second        // §7.6 TERM, then KILL after 3 s
	defaultEOFGrace = time.Second            // §7.8 "1 s on EOF"
	defaultDrain    = time.Second            // after the group is killed, how long the pipes may stay open
	maxWaitMS       = 600000
	maxShutdownMS   = 10000
)

// ProcConfig configures the process table. Everything in it comes from the
// launch (A06 §3.1), not from the model.
type ProcConfig struct {
	// Tree holds the roots: cwd and worktree executables are resolved
	// through it (A06 §5.1, §6 steps 3 and 6).
	Tree *Tree
	// Mode is --mode: in harness mode only Harness's argv is spawned.
	Mode     execproto.Mode
	Profiles *execproto.ProfileSet // --allow-profiles
	Harness  *HarnessSpec          // --harness-spec; required in harness mode
	Limits   ChildLimits           // --child-limits
	MaxProcs int                   // --max-procs: concurrent handles; more is busy
	// Env is the executor's launch environment (os.Environ() at start).
	// Children get its A06 §8 subset only (ChildEnv).
	Env []string
	// Shim is the warden-exec binary that runs "child" (A06 §7.1 step 3):
	// "" means /proc/self/exe on Linux and os.Executable() on macOS.
	Shim string
	// Clock defaults to RealClock.
	Clock Clock
	// Grace is the TERM→KILL delay of a timeout or a TERM signal (A06 §7.6,
	// 3 s by default). Drain bounds how long the pipes may stay open once
	// the group is killed (1 s by default): a process outside every tree
	// the executor can see could otherwise hold them and the wait forever.
	Grace, Drain time.Duration
	// PIDNamespace is true when the executor runs in the sandbox's own
	// PID namespace (hello features.pid_namespace): the stray sweep of A06
	// §7.7 then covers every process but the namespace init and the
	// executor's ancestors. Outside one it covers only the executor's
	// descendants.
	PIDNamespace bool
}

// RegisterProc builds the process table and registers exec.proc.spawn,
// exec.proc.wait, exec.proc.signal and exec.shutdown on s, with output
// sent as exec.proc.io notifications through s.Notify. Notify returns once
// the frame is written, and a handle's last chunk is written before its
// wait returns, so every exec.proc.io for a handle precedes the response
// that reports its exit (A06 §4.1; D-050).
//
// The caller must call Close on the table when Serve returns (A06 §7.8:
// RPC EOF kills every child), and exits the process after a successful
// exec.shutdown.
func RegisterProc(ctx context.Context, s *Server, cfg ProcConfig) (*Procs, error) {
	p, err := NewProcs(ctx, cfg, func(n execproto.IONotification) error { return s.Notify(execproto.MethodIO, n) })
	if err != nil {
		return nil, err
	}
	regs := []error{
		Handle(s, execproto.MethodSpawn, p.Spawn),
		Handle(s, execproto.MethodWait, p.Wait),
		Handle(s, execproto.MethodSignal, p.Signal),
		Handle(s, execproto.MethodShutdown, func(ctx context.Context, sp execproto.ShutdownParams) (execproto.ShutdownResult, error) {
			grace := int(defaultGrace / time.Millisecond)
			if sp.GraceMS != nil {
				grace = *sp.GraceMS
			}
			if grace < 0 || grace > maxShutdownMS {
				return execproto.ShutdownResult{}, invalid("grace_ms must be 0 to 10000")
			}
			switch sp.Reason {
			case "", execproto.ShutdownCancel, execproto.ShutdownTaskEnd, execproto.ShutdownTimeout, execproto.ShutdownResource, execproto.ShutdownDaemonStop, execproto.ShutdownError:
			default:
				return execproto.ShutdownResult{}, invalid("unknown shutdown reason")
			}
			return p.Shutdown(ctx, time.Duration(grace)*time.Millisecond, sp.Reason)
		}),
	}
	if err := errors.Join(regs...); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}
