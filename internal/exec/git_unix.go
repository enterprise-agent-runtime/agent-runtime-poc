//go:build linux || darwin

// sandboxed: exec.git.run runs git inside the sandbox, as warden-exec (A02
// §5 R4), with an argv the executor builds itself from four fixed
// operations (A14 §6), never one a model wrote. Every git process is a
// handle of the process table (Procs.start): the child shim, the
// --child-limits rlimits, its own process group, the stray sweep, killed
// with its call (INV-H, CLAUDE.md §9 rules 1 and 6).

package exec

// The process side of exec.git.run (design A06 §4.2, §4.3, §4.4; A14 §6).
// The argv, the environment and the checks are git.go; the operations are
// git_ops_unix.go. The package doc is in server.go.

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
)

// Git serves exec.git.run.
type Git struct {
	p    *Procs
	cfg  GitConfig
	args gitArgs
	path string // the child PATH (A06 §8), where git is looked up
	byFD bool   // execByFD; a field so the Linux tests can take the macOS path
}

// RegisterGit registers exec.git.run on s, running git through p's process
// table with the launch flags in cfg. In harness mode the method fails
// mode_forbidden (A06 §4.2, last column).
func RegisterGit(s *Server, p *Procs, cfg GitConfig) error {
	if p == nil {
		return errors.New("exec: RegisterGit needs the process table")
	}
	if err := cfg.check(); err != nil {
		return err
	}
	if p.cfg.Mode == execproto.ModeHarness {
		return Handle(s, execproto.MethodGitRun, func(context.Context, execproto.GitRunParams) (execproto.GitRunResult, error) {
			return execproto.GitRunResult{}, execproto.NewError(execproto.ErrModeForbidden, "exec.git.run is not available in harness mode")
		})
	}
	work, ok := p.rootPath(execproto.RootWorktree)
	if !ok {
		return errors.New("exec: RegisterGit needs a worktree root")
	}
	pathv, _ := envValue(p.env, "PATH")
	g := &Git{p: p, cfg: cfg, args: gitArgs{gitDir: cfg.GitDir, work: work, hooks: cfg.HooksDir}, path: pathv, byFD: execByFD}
	return Handle(s, execproto.MethodGitRun, g.Run)
}

// gitOut is what one git process left: stdout up to the cap's head
// (anything after it, the tail included, is dropped, so a cut never
// separates content from the header that names its file), and its exit
// code.
type gitOut struct {
	stdout    []byte
	truncated bool
	exit      int
}

// collector keeps the head chunks of one process's stdout. stderr is
// counted by the output cap but never kept: git's messages quote paths,
// ids and content (A06 §4.4).
type collector struct {
	mu  sync.Mutex
	out []byte
}

func (c *collector) sink(n execproto.IONotification) error {
	if n.Stream != execproto.StreamStdout || n.Segment == execproto.SegmentTail {
		return nil
	}
	b := []byte(n.Data)
	if n.Encoding == execproto.EncodingBase64 {
		var err error
		if b, err = base64.StdEncoding.DecodeString(n.Data); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.out = append(c.out, b...)
	c.mu.Unlock()
	return nil
}

// git runs one git command as a handle of the process table and waits for
// it; email adds the commit identity. The handle is forgotten once it has
// ended. A cancelled call kills the process group.
func (g *Git) git(ctx context.Context, deadline time.Time, argv []string, email string) (gitOut, error) {
	if !g.byFD {
		// As Spawn: the shim executes only the checked file, by descriptor.
		return gitOut{}, noExecByFD()
	}
	left := deadline.Sub(g.p.cfg.Clock.Now())
	if left <= 0 {
		return gitOut{}, execproto.NewError(execproto.ErrTimeout, "git timed out")
	}
	if g.p.isShutting() {
		return gitOut{}, execproto.NewError(execproto.ErrShuttingDown, "")
	}
	// git is a system executable, looked up in the child PATH and checked
	// like any other (A06 §6 step 3): never a file under the worktree or
	// the gitdir; the shim executes the very file that was checked.
	_, exe, err := g.p.resolveExecutable(argv[0], "")
	if err != nil {
		return gitOut{}, err
	}
	defer exe.Close()
	dir, err := g.p.openCwd("")
	if err != nil {
		return gitOut{}, err
	}
	defer dir.Close()
	var c collector
	h, err := g.p.start(startSpec{
		argv: argv, env: gitEnv(g.path, g.cfg.HooksDir, email), dir: dir, exe: exe, internal: true,
		timeout: left, cap: defaultOutput, sink: c.sink,
	})
	if err != nil {
		return gitOut{}, err
	}
	select {
	case <-h.done:
	case <-ctx.Done():
		g.p.kill(h, killCancel, unix.SIGKILL)
		<-h.done
	}
	g.p.mu.Lock()
	if g.p.handles[h.id] == h {
		delete(g.p.handles, h.id)
	}
	res := h.result
	g.p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return gitOut{}, err
	}
	switch {
	case res.Reason == execproto.ReasonTimeout:
		return gitOut{}, execproto.NewError(execproto.ErrTimeout, "git timed out")
	case res.ExitCode == nil:
		return gitOut{}, execproto.NewError(execproto.ErrIOError, "git did not exit normally")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return gitOut{stdout: c.out, truncated: res.Truncated, exit: *res.ExitCode}, nil
}
