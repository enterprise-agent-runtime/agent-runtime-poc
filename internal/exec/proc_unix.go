//go:build linux || darwin

// sandboxed: this is the executor's process table. It runs only inside the
// sandbox, as warden-exec (A02 §5 R4: linked only into cmd/warden-exec), and
// every process it starts is an agent-requested command already decided by
// the policy engine, started through the child shim with the A06 §7.3
// limits, in its own process group, inside that sandbox (INV-H, CLAUDE.md
// §9 rule 1).

package exec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	osexec "os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
)

// Procs is the executor's process table: exec.proc.spawn, wait and signal,
// and exec.shutdown (design A06 §6, §7).
//
// What it guarantees:
//
//   - Spawn runs the A06 §6 checks in order, then §7.1: argv only, never a
//     shell string, also after argv[0] is resolved (IsShellStringResolved);
//     cwd under the worktree (§5.3); the environment exactly the launch
//     environment's §8 subset, plus in harness mode the spec env and the
//     one secret_env variable, whose value is never logged, echoed or put
//     on the shim's command line (INV-C); a new process group; the
//     --child-limits rlimits through the child shim; at most MaxProcs
//     handles running (busy otherwise).
//   - Output is chunked and capped by output (A06 §7.2, §7.5), and a
//     handle's last chunk is on the wire before its wait reports the exit
//     (§4.1, D-050).
//   - No process survives its call (§7.7): when a leader exits, its whole
//     group is killed, then the strays (selectStrays), on every exit path,
//     including timeout, cancel and shutdown. The leader is waited for
//     without being reaped first (Linux), so its pid, which is the group
//     id, cannot be reused by another process before the group is killed.
//   - Timers come from the injected Clock.
type Procs struct {
	cfg    ProcConfig
	pol    spawnPolicy
	env    []string // ChildEnv(cfg.Env)
	notify func(execproto.IONotification) error
	shim   string
	self   int

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // monitors, pumps and tickers

	mu       sync.Mutex
	handles  map[string]*handle
	next     int
	running  int
	shutting bool
}

type handle struct {
	id, callID string
	pid, pgid  int
	proc       *os.Process
	out        *output
	started    time.Time
	cpuLimit   uint64
	pipes      [2]*os.File // read ends; the monitor closes them if the drain bound passes
	pumpsDone  chan struct{}

	// Guarded by Procs.mu.
	exitSeen bool // the leader has exited: no signal may go to its pgid any more
	reaped   bool
	killed   killReason
	timers   []Timer

	done   chan struct{} // closed once result is final
	result execproto.WaitResult
}

// NewProcs builds a process table that sends output through notify. ctx
// bounds every goroutine it starts; Close ends them.
func NewProcs(ctx context.Context, cfg ProcConfig, notify func(execproto.IONotification) error) (*Procs, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	if notify == nil {
		return nil, errors.New("exec: NewProcs needs a notify function")
	}
	shim := cfg.Shim
	if shim == "" {
		var err error
		if shim, err = defaultShim(); err != nil {
			return nil, err
		}
	}
	c, cancel := context.WithCancel(ctx)
	return &Procs{
		cfg:     cfg,
		pol:     spawnPolicy{mode: cfg.Mode, profiles: cfg.Profiles, harness: cfg.Harness},
		env:     ChildEnv(cfg.Env),
		notify:  notify,
		shim:    shim,
		self:    os.Getpid(),
		ctx:     c,
		cancel:  cancel,
		handles: map[string]*handle{},
	}, nil
}

func shellString() error {
	return execproto.NewError(execproto.ErrShellString, "shell interpreter with a command string (CF-19)")
}

// Spawn is exec.proc.spawn: the A06 §6 checks in order (the first failure
// ends the call), then the §7.1 sequence.
func (p *Procs) Spawn(_ context.Context, sp execproto.SpawnParams) (execproto.SpawnResult, error) {
	if p.isShutting() {
		return execproto.SpawnResult{}, execproto.NewError(execproto.ErrShuttingDown, "")
	}
	// §6 step 1, shape.
	if err := validateSpawn(sp); err != nil {
		return execproto.SpawnResult{}, err
	}
	// Step 2, shell strings, on argv as given.
	if execproto.IsShellString(sp.Argv) {
		return execproto.SpawnResult{}, shellString()
	}
	if !execByFD {
		return execproto.SpawnResult{}, execproto.NewError(execproto.ErrIOError, "spawn needs execution by descriptor, which this OS lacks (macOS L1: M7)")
	}
	harness := p.cfg.Mode == execproto.ModeHarness
	if harness && sp.Cwd != "" {
		return execproto.SpawnResult{}, invalid("in harness mode the cwd is the harness spec's")
	}
	if harness && strings.Contains(sp.Argv[0], "/") && !strings.HasPrefix(sp.Argv[0], "/") {
		// A relative harness argv[0] would name a file in the worktree,
		// which the agent writes: refused (stricter than resolving it
		// against the spec cwd).
		return execproto.SpawnResult{}, invalid("a harness argv[0] is a bare name or an absolute path")
	}
	// Step 3, executable resolution, and the shell rule again on the
	// resolved file: a name that is not a shell can be a link to one. The
	// file stays open: the shim executes this descriptor, so what runs is
	// the file these checks were made on (CWE-367).
	exe, exeFile, err := p.resolveExecutable(sp.Argv[0], sp.Cwd)
	if err != nil {
		return execproto.SpawnResult{}, err
	}
	defer exeFile.Close()
	if execproto.IsShellStringResolved(exe, sp.Argv) {
		return execproto.SpawnResult{}, shellString()
	}
	// Steps 4 and 5, profile and authorization consistency, mode, and
	// secret_env.
	profile, err := p.pol.authorize(sp)
	if err != nil {
		return execproto.SpawnResult{}, err
	}
	// Step 6, cwd.
	dir, err := p.openCwd(sp.Cwd)
	if err != nil {
		return execproto.SpawnResult{}, err
	}
	defer dir.Close()
	// (Step 7, the A15 secret-pattern scan of argv, needs a pattern set in
	// execproto that does not exist yet; the PDP's INV-6 check is the
	// authoritative one.)

	h, err := p.start(startSpec{
		callID: sp.CallID, argv: sp.Argv, env: p.childEnv(sp), dir: dir, exe: exeFile,
		timeout: time.Duration(sp.TimeoutMS) * time.Millisecond, cap: outputCap(sp), sink: p.notify,
	})
	if err != nil {
		return execproto.SpawnResult{}, err
	}
	return execproto.SpawnResult{Handle: h.id, PID: h.pid, PGID: h.pgid, Executable: exe, Profile: profile}, nil
}

// startSpec is one checked process for start: its argv and environment,
// the open cwd and executable the checks were made on, the per-call
// timeout, and where its output goes (A06 §7.2, §7.5).
type startSpec struct {
	callID   string
	argv     []string
	env      []string
	dir, exe *os.File
	timeout  time.Duration
	cap      int64
	sink     func(execproto.IONotification) error
}

// start is A06 §7.1 steps 1, 3 and 5 for a process whose checks are done:
// a slot (--max-procs), the child shim with the --child-limits rlimits, a
// new process group, a handle in the table, the pumps, the per-call
// timeout and the monitor. exec.proc.spawn and exec.git.run both start
// their processes here, so every child of the executor is a handle the
// stray sweep sees, runs through the same shim and execveat, and is killed
// with its call (A06 §7.7).
func (p *Procs) start(st startSpec) (*handle, error) {
	limits := p.cfg.Limits.ForCall(st.timeout)
	lj, err := json.Marshal(limits)
	if err != nil {
		return nil, execproto.NewError(execproto.ErrIOError, "limits could not be encoded")
	}
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, execproto.NewError(execproto.ErrIOError, "cannot open /dev/null")
	}
	defer devnull.Close()
	var rd, wr [2]*os.File
	for i := range rd {
		if rd[i], wr[i], err = os.Pipe(); err != nil {
			closeFiles(rd[:i]...)
			closeFiles(wr[:i]...)
			return nil, execproto.NewError(execproto.ErrIOError, "cannot create a pipe")
		}
	}
	// §7.1 step 3: the child shim. The command line carries the limits,
	// the cwd descriptor (3), the executable's descriptor (4) and argv; the environment
	// (which may hold the harness secret) travels only as the
	// environment, never as an argument.
	args := append([]string{"warden-exec", "child", "--limits", string(lj), "--cwd-fd", "3", "--exec-fd", "4", "--"}, st.argv...)
	cmd := &osexec.Cmd{
		Path:        p.shim,
		Args:        args,
		Env:         st.env,
		Stdin:       devnull,
		Stdout:      wr[0],
		Stderr:      wr[1],
		ExtraFiles:  []*os.File{st.dir, st.exe},
		SysProcAttr: sysProcAttr(),
	}

	p.mu.Lock()
	switch {
	case p.shutting:
		p.mu.Unlock()
		closeFiles(rd[0], rd[1], wr[0], wr[1])
		return nil, execproto.NewError(execproto.ErrShuttingDown, "")
	case p.running >= p.cfg.MaxProcs:
		// §7.1 step 1: acquire a slot (--max-procs).
		p.mu.Unlock()
		closeFiles(rd[0], rd[1], wr[0], wr[1])
		return nil, execproto.NewError(execproto.ErrBusy, "--max-procs handles are running")
	}
	// Start under the lock: a stray sweep, which holds it too, can never
	// see this process before it is in the table.
	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		closeFiles(rd[0], rd[1], wr[0], wr[1])
		return nil, execproto.NewError(execproto.ErrIOError, "cannot start the process")
	}
	p.next++
	id := "ph_" + strconv.Itoa(p.next)
	h := &handle{
		id: id, callID: st.callID, pid: cmd.Process.Pid, pgid: cmd.Process.Pid, proc: cmd.Process,
		started: p.cfg.Clock.Now(), cpuLimit: limits.CPU, pipes: rd,
		pumpsDone: make(chan struct{}), done: make(chan struct{}),
	}
	h.out = newOutput(id, st.callID, st.cap, st.sink)
	h.timers = append(h.timers, p.cfg.Clock.AfterFunc(st.timeout, func() { p.kill(h, killTimeout, unix.SIGTERM) }))
	p.handles[id] = h
	p.running++
	p.wg.Add(1)
	p.mu.Unlock()
	closeFiles(wr[0], wr[1]) // the child has them; EOF comes when every holder is gone

	p.startPumps(h)
	go p.monitor(h)
	return h, nil
}

func closeFiles(fs ...*os.File) {
	for _, f := range fs {
		if f != nil {
			_ = f.Close()
		}
	}
}

func (p *Procs) isShutting() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shutting
}

// childEnv is §7.1 step 2: the launch environment's §8 subset; in harness
// mode also the spec env (sorted, for a stable environment) and the one
// secret variable. Spec keys never replace §8 keys (ParseHarnessSpec).
func (p *Procs) childEnv(sp execproto.SpawnParams) []string {
	env := append([]string(nil), p.env...)
	if p.cfg.Mode != execproto.ModeHarness {
		return env
	}
	keys := make([]string, 0, len(p.cfg.Harness.Env))
	for k := range p.cfg.Harness.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+p.cfg.Harness.Env[k])
	}
	for _, k := range sp.SecretEnv.Keys() {
		v, _ := sp.SecretEnv.Value(k)
		env = append(env, k+"="+v)
	}
	return env
}

// rootPath returns the sandbox path of a root, if the executor has it.
func (p *Procs) rootPath(name execproto.RootName) (string, bool) {
	for _, r := range p.cfg.Tree.plainRoots() {
		if r.Name == name {
			return r.Path, true
		}
	}
	return "", false
}

func under(pth, root string) bool { return pth == root || strings.HasPrefix(pth, root+"/") }

func notExecutable() error {
	return execproto.NewError(execproto.ErrInvalidArgument, "argv[0] is not an executable regular file")
}

// resolveExecutable is A06 §6 step 3. It returns the executable's real
// path (no symlink left in it), on which the shell re-check is made and
// which SpawnResult reports, and the file itself, open: the shim executes
// that descriptor, so the file that runs is the file every check was made
// on, whatever a concurrent rename or symlink swap does to the name
// between the check and the execve (CWE-367; CF-19). The caller closes it.
//
//   - argv[0] with a "/" that is relative, or absolute under the worktree,
//     is resolved under the worktree with §5.1 (Tree.Resolve: no escape,
//     the deny-list applies), relative to cwd as execve would, and opened
//     with OpenCanonical, which follows no link.
//   - Without a "/", it is looked up in the child's PATH (§8), skipping
//     relative entries and any entry under the worktree or the private
//     gitdir, so a repository cannot shadow npm or git.
//   - Anything else is a system or toolchain file: inside the sandbox
//     those are the only other files there are. It is opened first, and
//     the checks are made on the path the kernel reports for the open
//     file: one that turns out to be in the worktree is resolved again as
//     a worktree path; one under the git root is refused (A06 §6 step 3
//     lists system, toolchain, worktree, scratch and cache, not git).
func (p *Procs) resolveExecutable(argv0, cwd string) (string, *os.File, error) {
	work, hasWork := p.rootPath(execproto.RootWorktree)
	if strings.Contains(argv0, "/") {
		switch {
		case !strings.HasPrefix(argv0, "/"):
			return p.worktreeExecutable(path.Join(cwd, argv0))
		case hasWork && under(path.Clean(argv0), work):
			return p.worktreeExecutable(argv0)
		}
		return p.systemExecutable(argv0)
	}
	pathv, _ := envValue(p.env, "PATH")
	gitRoot, hasGit := p.rootPath(execproto.RootGit)
	for _, dir := range strings.Split(pathv, ":") {
		if !strings.HasPrefix(dir, "/") {
			continue
		}
		dir = path.Clean(dir)
		if (hasWork && under(dir, work)) || (hasGit && under(dir, gitRoot)) {
			continue
		}
		cand := dir + "/" + argv0
		if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p.systemExecutable(cand)
		}
	}
	return "", nil, execproto.NewError(execproto.ErrNotFound, "argv[0] not found in PATH")
}

// checkExecutable requires an open file to be a regular file with an
// execute bit.
func checkExecutable(f *os.File) error {
	fi, err := f.Stat()
	switch {
	case err != nil:
		return execproto.NewError(execproto.ErrIOError, "cannot stat argv[0]")
	case !fi.Mode().IsRegular():
		return execproto.NewError(execproto.ErrNotRegular, "argv[0] is not a regular file")
	case fi.Mode().Perm()&0o111 == 0:
		return notExecutable()
	}
	return nil
}

func (p *Procs) worktreeExecutable(input string) (string, *os.File, error) {
	r, err := p.cfg.Tree.Resolve(execproto.MethodSpawn, execproto.RootWorktree, input)
	if err != nil {
		return "", nil, err
	}
	f, err := p.cfg.Tree.OpenCanonical(r, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return "", nil, err
	}
	if err := checkExecutable(f); err != nil {
		_ = f.Close()
		return "", nil, err
	}
	return r.Canonical, f, nil
}

func (p *Procs) systemExecutable(name string) (string, *os.File, error) {
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, execproto.NewError(execproto.ErrNotFound, "argv[0] does not exist")
	}
	f := os.NewFile(uintptr(fd), name)
	real, err := fdPath(f)
	if err != nil {
		_ = f.Close()
		return "", nil, execproto.NewError(execproto.ErrIOError, "cannot tell which file argv[0] is")
	}
	if work, ok := p.rootPath(execproto.RootWorktree); ok && under(real, work) {
		_ = f.Close()
		return p.worktreeExecutable(real)
	}
	if g, ok := p.rootPath(execproto.RootGit); ok && under(real, g) {
		_ = f.Close()
		return "", nil, execproto.NewError(execproto.ErrOutsideRoots, "argv[0] lies under the git root")
	}
	if err := checkExecutable(f); err != nil {
		_ = f.Close()
		return "", nil, err
	}
	return real, f, nil
}

// openCwd is A06 §6 step 6: the cwd is resolved with §5.1 under the
// worktree, must be a directory and not deny-listed, and is opened, so
// the shim changes into exactly the directory that was checked (fchdir),
// not whatever the path names later. In harness mode the cwd is the spec's
// (a launch file the daemon wrote), or the worktree root. The spec's path
// is opened one component at a time without following a link
// (openDirNoFollow), as the tool-mode cwd is resolved without one: a link a
// sandbox process planted on the way cannot move the engine elsewhere.
func (p *Procs) openCwd(cwd string) (*os.File, error) {
	if p.cfg.Mode == execproto.ModeHarness && p.cfg.Harness.Cwd != "" {
		return openDirNoFollow(p.cfg.Harness.Cwd)
	}
	if cwd == "" {
		cwd = "."
	}
	r, err := p.cfg.Tree.Resolve(execproto.MethodSpawn, execproto.RootWorktree, cwd)
	if err != nil {
		return nil, err
	}
	f, err := p.cfg.Tree.OpenCanonical(r, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// startPumps reads both pipes into the handle's output (A06 §7.2: a 16 KiB
// buffer each) and ticks it every chunk interval until both reach EOF.
func (p *Procs) startPumps(h *handle) {
	var pumps sync.WaitGroup
	for i, f := range h.pipes {
		pumps.Add(1)
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer pumps.Done()
			buf := make([]byte, chunkMax)
			s := streamOf(i)
			for {
				n, err := f.Read(buf)
				if n > 0 {
					h.out.write(s, buf[:n])
				}
				if err != nil {
					return
				}
			}
		}()
	}
	p.wg.Add(2)
	go func() {
		defer p.wg.Done()
		pumps.Wait()
		close(h.pumpsDone)
	}()
	go func() {
		defer p.wg.Done()
		t := p.cfg.Clock.NewTicker(chunkInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C():
				h.out.tick()
			case <-h.pumpsDone:
				return
			case <-p.ctx.Done():
				return
			}
		}
	}()
}

// monitor follows one handle from its leader's exit to its final result
// (A06 §7.7, §7.4).
func (p *Procs) monitor(h *handle) {
	defer p.wg.Done()
	var state *os.ProcessState
	var werr error
	// Wait for the exit without reaping where the OS allows it, so that the
	// zombie keeps the pid (= pgid) from being reused while the group is
	// killed. Elsewhere the leader is reaped first and killGroup refuses a
	// pgid that a live handle now uses.
	if !waitExited(h.pid) {
		state, werr = h.proc.Wait()
	}
	end := p.cfg.Clock.Now()
	p.mu.Lock()
	h.exitSeen = true
	for _, t := range h.timers {
		t.Stop()
	}
	h.timers = nil
	p.killGroup(h, unix.SIGKILL)
	p.mu.Unlock()
	strays := p.sweep(h)
	if state == nil && werr == nil {
		state, werr = h.proc.Wait()
	}
	p.mu.Lock()
	h.reaped = true
	p.mu.Unlock()

	// Every holder of the pipes is dead or gone now, unless something
	// outside every tree the executor can see kept them; the drain bound
	// stops that from holding the wait forever.
	drained := make(chan struct{})
	dt := p.cfg.Clock.AfterFunc(p.cfg.Drain, func() { close(drained) })
	select {
	case <-h.pumpsDone:
	case <-drained:
		closeFiles(h.pipes[0], h.pipes[1])
		<-h.pumpsDone
	}
	dt.Stop()
	closeFiles(h.pipes[0], h.pipes[1])
	h.out.finish()

	res := execproto.WaitResult{Handle: h.id, State: execproto.StateExited, DurationMS: end.Sub(h.started).Milliseconds(), StraysKilled: strays}
	p.mu.Lock()
	obs := exitObs{Killed: h.killed, CPULimit: h.cpuLimit}
	p.mu.Unlock()
	if werr == nil && state != nil {
		if ws, ok := state.Sys().(syscall.WaitStatus); ok {
			switch {
			case ws.Exited():
				code := ws.ExitStatus()
				res.ExitCode, obs.Exited, obs.Code = &code, true, code
			case ws.Signaled():
				name := unix.SignalName(ws.Signal())
				if name == "" {
					name = "SIG" + strconv.Itoa(int(ws.Signal()))
				}
				res.Signal, obs.Signal = &name, name
			}
		}
		if ru, ok := state.SysUsage().(*syscall.Rusage); ok {
			user := time.Duration(ru.Utime.Nano())
			sys := time.Duration(ru.Stime.Nano())
			obs.CPUTime = user + sys
			res.Rusage = &execproto.Rusage{UserMS: user.Milliseconds(), SysMS: sys.Milliseconds(), MaxRSSKB: maxRSSKB(ru)}
		}
	}
	res.Reason, res.Resource, res.SeccompKill = classify(obs)
	b, truncated := h.out.stats()
	res.Bytes, res.Truncated = &b, truncated

	p.mu.Lock()
	h.result = res
	p.running--
	close(h.done)
	p.mu.Unlock()
}

// killGroup signals the handle's process group, with p.mu held. The pgid
// is the leader's pid; once the leader is reaped that pid may belong to a
// new handle's leader, so a pgid a running handle uses is never signalled
// on another handle's behalf.
func (p *Procs) killGroup(h *handle, sig unix.Signal) bool {
	if h.reaped {
		return false
	}
	for _, o := range p.handles {
		if o != h && !o.exitSeen && o.pgid == h.pgid {
			return false
		}
	}
	return unix.Kill(-h.pgid, sig) == nil
}

// kill is the executor's own signal to a running handle (timeout, cancel,
// shutdown), with TERM followed by KILL after the grace (A06 §7.6).
func (p *Procs) kill(h *handle, why killReason, sig unix.Signal) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if h.exitSeen {
		return false
	}
	if h.killed == killNone {
		h.killed = why
	}
	ok := p.killGroup(h, sig)
	if sig == unix.SIGTERM {
		h.timers = append(h.timers, p.cfg.Clock.AfterFunc(p.cfg.Grace, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if !h.exitSeen {
				p.killGroup(h, unix.SIGKILL)
			}
		}))
	}
	return ok
}

// sweep is A06 §7.7 after h's leader exited: every stray is killed, and
// zombies the executor inherited (as child subreaper) are reaped. It
// returns the number killed.
func (p *Procs) sweep(ending *handle) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	procs, err := listProcs()
	if err != nil {
		return 0
	}
	live := liveSet{leaders: map[int]bool{}, pgids: map[int]bool{}}
	for _, o := range p.handles {
		if !o.reaped {
			live.leaders[o.pid] = true
		}
		if o != ending && !o.exitSeen {
			live.pgids[o.pgid] = true
		}
	}
	if ending != nil {
		// The ending handle's own group has just been killed as a group;
		// strays_killed counts what is outside every handle's group.
		live.pgids[ending.pgid] = true
	}
	strays := selectStrays(procs, p.self, live, p.cfg.PIDNamespace)
	killed := 0
	for _, pid := range strays {
		if unix.Kill(pid, unix.SIGKILL) == nil {
			killed++
		}
	}
	for _, pr := range procs {
		if pr.PPID == p.self && pr.State == 'Z' && !live.leaders[pr.PID] {
			var ws unix.WaitStatus
			_, _ = unix.Wait4(pr.PID, &ws, unix.WNOHANG, nil)
		}
	}
	return killed
}

// Wait is exec.proc.wait, a long poll: timeout_ms 0 waits for the exit,
// otherwise at most timeout_ms (state running). The exited result is
// reported once; the handle is then forgotten.
func (p *Procs) Wait(ctx context.Context, wp execproto.WaitParams) (execproto.WaitResult, error) {
	if !validHandle(wp.Handle) || wp.TimeoutMS < 0 || wp.TimeoutMS > maxWaitMS {
		return execproto.WaitResult{}, invalid("handle must match ^ph_[0-9]{1,10}$ and timeout_ms be 0 to 600000")
	}
	p.mu.Lock()
	h := p.handles[wp.Handle]
	p.mu.Unlock()
	if h == nil {
		return execproto.WaitResult{}, unknownHandle()
	}
	var expired chan struct{}
	if wp.TimeoutMS > 0 {
		expired = make(chan struct{})
		t := p.cfg.Clock.AfterFunc(time.Duration(wp.TimeoutMS)*time.Millisecond, func() { close(expired) })
		defer t.Stop()
	}
	select {
	case <-h.done:
	case <-expired:
		return execproto.WaitResult{Handle: h.id, State: execproto.StateRunning}, nil
	case <-ctx.Done():
		return execproto.WaitResult{}, ctx.Err()
	}
	p.mu.Lock()
	if p.handles[h.id] == h {
		delete(p.handles, h.id)
	}
	res := h.result
	p.mu.Unlock()
	return res, nil
}

// Signal is exec.proc.signal: the signal goes to the handle's process group
// (-pgid, A06 §7.6), and the handle's exit is then reported as cancelled.
// After TERM the executor sends KILL itself once the grace has passed, in
// case the daemon's own KILL never comes. A handle whose leader has exited
// gets nothing (delivered false): its group is already being killed.
func (p *Procs) Signal(_ context.Context, sp execproto.SignalParams) (execproto.SignalResult, error) {
	var sig unix.Signal
	switch sp.Signal {
	case execproto.SignalINT:
		sig = unix.SIGINT
	case execproto.SignalTERM:
		sig = unix.SIGTERM
	case execproto.SignalKILL:
		sig = unix.SIGKILL
	default:
		return execproto.SignalResult{}, invalid("signal must be INT, TERM or KILL")
	}
	if !validHandle(sp.Handle) {
		return execproto.SignalResult{}, invalid("handle must match ^ph_[0-9]{1,10}$")
	}
	p.mu.Lock()
	h := p.handles[sp.Handle]
	p.mu.Unlock()
	if h == nil {
		return execproto.SignalResult{}, unknownHandle()
	}
	return execproto.SignalResult{Delivered: p.kill(h, killCancel, sig), PGID: h.pgid}, nil
}

// Shutdown is exec.shutdown and the executor's exit path (A06 §7.8): no
// new spawn is accepted; every running group gets TERM, and after grace
// KILL; the call returns once every handle has its final result (its
// output flushed), with the number of handles signalled plus the strays a
// final sweep killed, and the number still running.
func (p *Procs) Shutdown(ctx context.Context, grace time.Duration, reason execproto.ShutdownReason) (execproto.ShutdownResult, error) {
	why := killShutdown
	if reason == execproto.ShutdownCancel {
		why = killCancel
	}
	p.mu.Lock()
	p.shutting = true
	var live []*handle
	for _, h := range p.handles {
		select {
		case <-h.done:
			continue
		default:
		}
		live = append(live, h)
		if !h.exitSeen {
			if h.killed == killNone {
				h.killed = why
			}
			p.killGroup(h, unix.SIGTERM)
		}
	}
	p.mu.Unlock()

	all := func() <-chan struct{} {
		ch := make(chan struct{})
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for _, h := range live {
				select {
				case <-h.done:
				case <-p.ctx.Done():
					return
				}
			}
			close(ch)
		}()
		return ch
	}()
	if grace > 0 {
		expired := make(chan struct{})
		t := p.cfg.Clock.AfterFunc(grace, func() { close(expired) })
		select {
		case <-all:
		case <-expired:
		case <-ctx.Done():
		}
		t.Stop()
	}
	p.mu.Lock()
	for _, h := range live {
		if !h.exitSeen {
			p.killGroup(h, unix.SIGKILL)
		}
	}
	p.mu.Unlock()
	select {
	case <-all:
	case <-ctx.Done():
	case <-p.ctx.Done():
	}
	strays := p.sweep(nil)
	remaining := 0
	for _, h := range live {
		select {
		case <-h.done:
		default:
			remaining++
		}
	}
	return execproto.ShutdownResult{KilledPIDs: len(live) + strays, Remaining: remaining}, nil
}

// Close is the end of the executor (RPC EOF, A06 §7.8): everything is
// killed with the 1 s EOF grace, and every goroutine of the table has
// returned when Close does. Safe to call more than once.
func (p *Procs) Close() {
	_, _ = p.Shutdown(p.ctx, defaultEOFGrace, "")
	p.cancel()
	p.wg.Wait()
	_ = p.sweep(nil)
}

// envValue returns the value of key in env (first occurrence).
func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// outputCap is max_output_bytes with its default.
func outputCap(p execproto.SpawnParams) int64 {
	if p.MaxOutputBytes == 0 {
		return defaultOutput
	}
	return p.MaxOutputBytes
}

// check validates the configuration and fills in the defaults.
func (c *ProcConfig) check() error {
	switch {
	case c.Tree == nil:
		return errors.New("exec: ProcConfig.Tree is required")
	case c.MaxProcs < 1:
		return errors.New("exec: ProcConfig.MaxProcs must be at least 1")
	case c.Mode != execproto.ModeTool && c.Mode != execproto.ModeHarness:
		return errors.New("exec: ProcConfig.Mode must be tool or harness")
	case c.Mode == execproto.ModeHarness && c.Harness == nil:
		return errors.New("exec: harness mode needs a harness spec")
	case c.Limits.CPU == 0 || c.Limits.Nofile == 0:
		return errors.New("exec: ProcConfig.Limits must come from ParseChildLimits")
	}
	if c.Clock == nil {
		c.Clock = RealClock()
	}
	if c.Grace <= 0 {
		c.Grace = defaultGrace
	}
	if c.Drain <= 0 {
		c.Drain = defaultDrain
	}
	return nil
}

// validHandle is ^ph_[0-9]{1,10}$.
func validHandle(h string) bool {
	rest, ok := strings.CutPrefix(h, "ph_")
	if !ok || len(rest) < 1 || len(rest) > 10 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return false
		}
	}
	return true
}

func unknownHandle() error {
	return execproto.NewError(execproto.ErrInvalidState, "unknown handle")
}

// openDirNoFollow opens an absolute, clean directory path from "/", one
// component at a time with O_DIRECTORY | O_NOFOLLOW, so no symlink on the
// way is followed.
func openDirNoFollow(p string) (*os.File, error) {
	refuse := func() (*os.File, error) {
		return nil, execproto.NewError(execproto.ErrNotFound, "harness cwd is not a directory reachable without a symlink")
	}
	dir, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return refuse()
	}
	for _, c := range strings.Split(strings.Trim(p, "/"), "/") {
		if c == "" {
			continue
		}
		next, err := unix.Openat(dir, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(dir)
		if err != nil {
			return refuse()
		}
		dir = next
	}
	return os.NewFile(uintptr(dir), p), nil
}
