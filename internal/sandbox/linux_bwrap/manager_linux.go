//go:build linux

package linux_bwrap

// The L1 sandbox.Manager of design A06 §13 on Linux: create (§13.2),
// destroy (§13.3), the executor-exit watchdog (§13.4) and orphan recovery
// at daemon start (§13.5). It starts processes only through Plan.Start
// (command.go), so the environment, descriptor and wrapper guarantees made
// there hold for every sandbox (INV-B, INV-C, INV-H).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/proxy"
	"warden.dev/warden/internal/sandbox"
	"warden.dev/warden/internal/tools"
)

const (
	stderrCap  = 8192 // bwrap's diagnostics kept for errors
	defaultEnv = "/usr/bin/env"
)

// timing holds the lifecycle bounds (A06 §13.1-§13.4). Tests shorten
// them; nothing asserts on elapsed time.
type timing struct {
	hello      time.Duration // launching -> failed after 5 s; covers the info fd too
	shutdown   time.Duration // draining -> killing after 4 s
	killVerify time.Duration // processes left after this are a root_check violation
	killRounds int           // further SIGKILL rounds after the violation before giving up
	diskEvery  time.Duration // free-space watchdog period
}

func defaultTiming() timing {
	return timing{hello: 5 * time.Second, shutdown: 4 * time.Second, killVerify: time.Second, killRounds: 5, diskEvery: 10 * time.Second}
}

// Config configures a Manager for one Warden home.
type Config struct {
	WardenHome string // the home every Spec must belong to; orphan recovery scans only it
	Bwrap      string // absolute path of bwrap
	Features   Features
	Probes     Probes // DefaultProbes() in production

	// Cgroup is the probe result (sandbox.ProbeCgroup). When Usable, every
	// launch runs in a systemd user scope (A06 §11.6); otherwise the
	// executor applies the rlimit fallback and sandbox.create records
	// cgroup false.
	Cgroup     sandbox.CgroupStatus
	CgroupRoot string // "" is sandbox.CgroupRoot
	EnvBin     string // "" is /usr/bin/env

	Events      sandbox.EventSink                         // required
	ProxyConfig func(*sandbox.Spec) (proxy.Config, error) // required: the task's proxy (allowlist, events)

	CPUSeconds    int // RLIMIT_CPU guard, 4 × call timeout (A06 §7.3)
	MaxProcs      int // concurrent exec.proc.spawn handles
	DaemonVersion string
	Now           func() time.Time
	// FreeBytes reports the free space of the file system holding a path
	// (the disk watchdog, A06 §7.3, §13.4); nil is platform.FreeBytes.
	FreeBytes func(path string) (uint64, error)
}

// Manager is the Linux L1 backend. It is safe for concurrent use.
type Manager struct {
	cfg   Config
	scope bool
	t     timing

	mu   sync.Mutex
	live map[string]*box // nil value: a Create in progress holds the id
}

var _ sandbox.Manager = (*Manager)(nil)

// root is the sandbox root process (bwrap) as the lifecycle sees it;
// *Running in production.
type root interface {
	Pid() int
	Done() <-chan struct{}
	Kill() error
	Exit() (RootExit, bool) // how it ended, once Done is closed
}

// box is one sandbox the manager owns.
type box struct {
	spec      sandbox.Spec
	run       root
	helloOK   bool           // exec.hello succeeded: the executor can be asked to shut down
	counters  CgroupCounters // the scope's counters at launch
	conn      net.Conn
	client    *tools.Client
	proxy     *sandbox.ProxyListener
	cancel    context.CancelFunc
	cgroupDir string
	unit      string
	stderr    *capBuffer
	sbDir     string
	stop      chan struct{} // closed when Destroy takes the box; ends the watchdog
}

// New checks cfg and returns a Manager.
func New(cfg Config) (*Manager, error) {
	if !filepath.IsAbs(cfg.Bwrap) || !filepath.IsAbs(cfg.WardenHome) {
		return nil, fmt.Errorf("%w: linux_bwrap: bwrap %q and home %q must be absolute", sandbox.ErrUnavailable, cfg.Bwrap, cfg.WardenHome)
	}
	if cfg.Events == nil || cfg.ProxyConfig == nil {
		return nil, fmt.Errorf("%w: linux_bwrap: Events and ProxyConfig are required", sandbox.ErrUnavailable)
	}
	if cfg.CPUSeconds <= 0 || cfg.MaxProcs <= 0 {
		return nil, fmt.Errorf("%w: linux_bwrap: CPUSeconds and MaxProcs must be positive", sandbox.ErrUnavailable)
	}
	if cfg.CgroupRoot == "" {
		cfg.CgroupRoot = sandbox.CgroupRoot
	}
	if cfg.EnvBin == "" {
		cfg.EnvBin = defaultEnv
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.FreeBytes == nil {
		cfg.FreeBytes = platform.FreeBytes
	}
	return &Manager{cfg: cfg, scope: cfg.Cgroup.Usable(), t: defaultTiming(), live: map[string]*box{}}, nil
}

// Cgroup reports whether launches run in a systemd scope with cgroup
// limits (A06 §11.6).
func (m *Manager) Cgroup() bool { return m.scope }

// Client returns the executor client of a ready sandbox.
func (m *Manager) Client(id string) (*tools.Client, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.live[id]
	if b == nil {
		return nil, false
	}
	return b.client, true
}

// Create implements sandbox.Manager (A06 §13.2). The proxy listener is
// opened before anything is launched and closed on every failure path;
// once bwrap has started, a failure tears the sandbox down as Destroy
// does and writes one sandbox.destroy (hello_mismatch for a hello that
// does not match, timeout when exec.hello never came, store_unavailable
// when sandbox.create could not be written, executor_exit otherwise), so every process that ran is on the
// record. sandbox.create is written only after exec.hello matched.
func (m *Manager) Create(ctx context.Context, s *sandbox.Spec) (_ *sandbox.Handle, err error) {
	if s == nil {
		return nil, fmt.Errorf("%w: no spec", sandbox.ErrInvalidSpec)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Level != sandbox.LevelL1 || s.Purpose != sandbox.PurposeTask || s.Mode != execproto.ModeTool {
		return nil, fmt.Errorf("%w: linux_bwrap builds L1 task sandboxes in tool mode, not %s/%s/%s", sandbox.ErrInvalidSpec, s.Level, s.Purpose, s.Mode)
	}
	if filepath.Clean(s.WardenHome) != filepath.Clean(m.cfg.WardenHome) {
		return nil, fmt.Errorf("%w: spec of home %q given to the manager of %q", sandbox.ErrInvalidSpec, s.WardenHome, m.cfg.WardenHome)
	}
	m.mu.Lock()
	if _, dup := m.live[s.ID]; dup {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: sandbox %s already exists", sandbox.ErrInvalidSpec, s.ID)
	}
	m.live[s.ID] = nil
	m.mu.Unlock()

	// The sandbox outlives Create's context: it ends at Destroy.
	sbCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	b := &box{spec: *s, cancel: cancel, stderr: &capBuffer{max: stderrCap}, sbDir: filepath.Dir(s.ProfilesFile), stop: make(chan struct{})}
	reason := sandbox.ReasonExecutorExit
	defer func() {
		if err == nil {
			return
		}
		m.mu.Lock()
		delete(m.live, s.ID)
		m.mu.Unlock()
		if b.run == nil {
			m.release(b) // nothing ran: close the proxy, remove our files, no event
			return
		}
		if terr := m.teardown(context.WithoutCancel(ctx), b, reason); terr != nil {
			err = errors.Join(err, terr)
		}
	}()

	pcfg, err := m.cfg.ProxyConfig(s)
	if err != nil {
		return nil, fmt.Errorf("%w: proxy config: %w", sandbox.ErrUnavailable, err)
	}
	if b.proxy, err = sandbox.OpenProxy(sbCtx, s, pcfg); err != nil {
		return nil, err
	}
	digest, plan, err := m.prepareFiles(s)
	if err != nil {
		return nil, err
	}
	if err := m.launch(sbCtx, b, plan); err != nil {
		return nil, err
	}
	hctx, hcancel := context.WithTimeout(ctx, m.t.hello)
	hello, err := b.client.Hello(hctx, execproto.HelloParams{Protocol: execproto.Protocol, SandboxID: s.ID, SessionID: s.SessionID, DaemonVersion: m.cfg.DaemonVersion})
	timedOut := hctx.Err() != nil
	hcancel()
	if err != nil {
		// No hello: the executor is not known to speak the protocol, so
		// teardown does not wait on exec.shutdown (round 1, item 8). A
		// hello that never came is a timeout (A06 §13.1 "timeout 5 s").
		if timedOut {
			reason = sandbox.ReasonTimeout
		}
		return nil, fmt.Errorf("%w: exec.hello: %w%s", sandbox.ErrUnavailable, err, b.stderr.suffix())
	}
	b.helloOK = true
	if err := CheckHello(s, hello); err != nil {
		reason = sandbox.ReasonHelloMismatch
		b.helloOK = false // an executor that misreports itself is not asked to shut down either
		return nil, err
	}
	ms, err := s.Mounts()
	if err != nil {
		return nil, err
	}
	ev, err := sandbox.NewCreateEvent(s, Backend, Limits(s.Limits, m.scope, m.cfg.Features), CreateEnvKeys(), b.proxy.Path(), digest)
	if err != nil {
		return nil, err
	}
	if err := m.cfg.Events.SandboxCreate(ctx, ev); err != nil {
		reason = sandbox.ReasonStoreUnavailable
		return nil, fmt.Errorf("%w: sandbox.create: %w", sandbox.ErrUnavailable, err)
	}
	m.mu.Lock()
	m.live[s.ID] = b
	m.mu.Unlock()
	go m.watch(b)
	return &sandbox.Handle{ID: s.ID, Level: s.Level, Backend: Backend, Mounts: ms, EnvKeys: CreateEnvKeys(), Created: m.cfg.Now()}, nil
}

// CreateEnvKeys is sandbox.create env_keys for an L1 sandbox: the A06 §8
// allowlist and PWD, which bwrap sets itself after --chdir (M2-13).
func CreateEnvKeys() []string { return append(sandbox.EnvKeys(), "PWD") }

// prepareFiles writes the generated /etc files, builds the plan and
// writes argv.json, all owner-only (A06 §13.2 step 3). The profile digest
// is the sha256 of argv.json, the JSON-serialized bwrap argv.
func (m *Manager) prepareFiles(s *sandbox.Spec) (string, *Plan, error) {
	for _, d := range []string{filepath.Join(s.Scratch, "home"), filepath.Join(s.Scratch, "tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", nil, fmt.Errorf("%w: scratch: %w", sandbox.ErrUnavailable, err)
		}
	}
	etc := filepath.Join(filepath.Dir(s.ProfilesFile), EtcDir)
	for name, content := range EtcFiles(os.Getuid(), os.Getgid()) {
		if err := platform.WriteOwnerOnly(filepath.Join(etc, name), []byte(content)); err != nil {
			return "", nil, fmt.Errorf("%w: %s: %w", sandbox.ErrUnavailable, name, err)
		}
	}
	x := ExecFlags{CPUSeconds: m.cfg.CPUSeconds, Cgroup: m.scope, MaxProcs: m.cfg.MaxProcs}
	if !m.scope {
		n, err := userProcesses(os.Getuid())
		if err != nil {
			return "", nil, fmt.Errorf("%w: counting processes for RLIMIT_NPROC: %w", sandbox.ErrUnavailable, err)
		}
		x.Nproc = n + s.Limits.Pids
	}
	plan, err := Prepare(s, m.cfg.Features, x, m.cfg.Probes)
	if err != nil {
		return "", nil, err
	}
	argv, err := json.Marshal(plan.Argv)
	if err != nil {
		return "", nil, err
	}
	if err := platform.WriteOwnerOnly(filepath.Join(filepath.Dir(s.ProfilesFile), ArgvFile), argv); err != nil {
		return "", nil, fmt.Errorf("%w: argv.json: %w", sandbox.ErrUnavailable, err)
	}
	sum := sha256.Sum256(argv)
	return "sha256:" + hex.EncodeToString(sum[:]), plan, nil
}

// launch starts bwrap (A06 §13.2 step 4), reads its child pid from the
// info fd and writes state.json.
func (m *Manager) launch(ctx context.Context, b *box, plan *Plan) error {
	s := &b.spec
	arch, err := HostSeccompArch()
	if err != nil {
		return err
	}
	prog, err := SeccompFile(arch)
	if err != nil {
		return err
	}
	defer prog.Close()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%w: socketpair: %w", sandbox.ErrUnavailable, err)
	}
	child, ours := os.NewFile(uintptr(pair[0]), "rpc"), os.NewFile(uintptr(pair[1]), "rpc-daemon")
	defer child.Close()
	defer ours.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer null.Close()
	infoR, infoW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer infoR.Close()
	defer infoW.Close()

	l := Launch{Bwrap: m.cfg.Bwrap, Stderr: b.stderr}
	if m.scope {
		b.unit = sandbox.ScopeUnit(s.WardenHome, s.ID)
		l.Scope = &Scope{SystemdRun: m.cfg.Cgroup.SystemdRun, EnvBin: m.cfg.EnvBin, Unit: b.unit, Env: m.cfg.Cgroup.Env, Limits: s.Limits}
	}
	run, err := plan.Start(ctx, l, child, null, prog, infoW)
	if err != nil {
		return err
	}
	b.run = run
	infoW.Close()
	child.Close()
	if err := infoR.SetReadDeadline(time.Now().Add(m.t.hello)); err != nil {
		return err
	}
	childPID, err := ParseInfo(infoR)
	if err != nil {
		return fmt.Errorf("%w%s", err, b.stderr.suffix())
	}
	start, err := procStartTime(run.Pid())
	if err != nil {
		return fmt.Errorf("%w: sandbox root %d: %w", sandbox.ErrUnavailable, run.Pid(), err)
	}
	if m.scope {
		b.cgroupDir = m.scopeDir(run.Pid(), b.unit)
		if b.cgroupDir != "" {
			if b.counters, err = ReadCgroupCounters(b.cgroupDir); err != nil {
				return fmt.Errorf("%w: scope counters: %w", sandbox.ErrUnavailable, err)
			}
		}
	}
	boot, err := bootID()
	if err != nil {
		return fmt.Errorf("%w: boot id: %w", sandbox.ErrUnavailable, err)
	}
	st := State{SandboxID: s.ID, SessionID: s.SessionID, HomeHash: platform.HomeHash(s.WardenHome), Backend: Backend,
		RootPID: run.Pid(), RootStartTime: start, ChildPID: childPID, ScopeUnit: b.unit, BootID: boot}
	sb, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := platform.WriteOwnerOnly(filepath.Join(b.sbDir, StateFile), sb); err != nil {
		return fmt.Errorf("%w: state.json: %w", sandbox.ErrUnavailable, err)
	}
	b.conn, err = net.FileConn(ours)
	if err != nil {
		return fmt.Errorf("%w: rpc socket: %w", sandbox.ErrUnavailable, err)
	}
	b.client = tools.New(ctx, b.conn, nil)
	return nil
}

// scopeDir is the cgroup directory of the root process when it is the
// expected scope below this user's manager; "" otherwise (teardown then
// relies on the PID namespace alone).
func (m *Manager) scopeDir(pid int, unit string) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	rel := ParseProcCgroup(string(b))
	if rel == "" || filepath.Base(rel) != unit {
		return ""
	}
	dir := filepath.Join(m.cfg.CgroupRoot, rel)
	if !strings.HasPrefix(dir, sandbox.UserServiceCgroup(m.cfg.CgroupRoot, os.Getuid())+string(filepath.Separator)) {
		return ""
	}
	return dir
}

// watch holds the watchdogs of A06 §13.4 that belong to the sandbox: the
// executor's end (bwrap exits or the channel closes), classified by the
// A06 §7.4 rows the daemon can see (the scope's memory.events and
// pids.events, bwrap's exit 159), and the free-disk watchdog (A06 §7.3).
// It ends when the box is destroyed.
func (m *Manager) watch(b *box) {
	var tick <-chan time.Time
	if b.spec.Limits.DiskFloorMB > 0 && m.t.diskEvery > 0 {
		t := time.NewTicker(m.t.diskEvery)
		defer t.Stop()
		tick = t.C
	}
	var client <-chan struct{}
	if b.client != nil {
		client = b.client.Done()
	}
	for {
		select {
		case <-b.stop:
			return
		case <-b.run.Done():
		case <-client:
		case <-tick:
			free, err := m.cfg.FreeBytes(m.cfg.WardenHome)
			if err != nil || !DiskLow(free, b.spec.Limits.DiskFloorMB) {
				continue
			}
			_ = m.destroy(context.Background(), b.spec.ID, sandbox.ReasonResource, &End{Resource: ClassifyEnd(CgroupCounters{}, CgroupCounters{}, nil, true).Resource})
			return
		}
		break
	}
	_ = m.destroy(context.Background(), b.spec.ID, sandbox.ReasonExecutorExit, nil)
}

// Destroy implements sandbox.Manager (A06 §13.3).
func (m *Manager) Destroy(ctx context.Context, id string, reason sandbox.DestroyReason) error {
	return m.destroy(ctx, id, reason, nil)
}

// destroy takes the box and tears it down; end, when set, is what a
// watchdog already knows about how the sandbox ended.
func (m *Manager) destroy(ctx context.Context, id string, reason sandbox.DestroyReason, end *End) error {
	if !reason.Valid() {
		return fmt.Errorf("%w: destroy reason %q is not an A06 §13.3 reason", sandbox.ErrInvalidSpec, reason)
	}
	m.mu.Lock()
	b := m.live[id]
	if b == nil {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", sandbox.ErrUnknownSandbox, id)
	}
	delete(m.live, id)
	m.mu.Unlock()
	close(b.stop)
	return m.teardownEnd(ctx, b, reason, end)
}

// teardown is teardownEnd with nothing known in advance about the end.
func (m *Manager) teardown(ctx context.Context, b *box, reason sandbox.DestroyReason) error {
	return m.teardownEnd(ctx, b, reason, nil)
}

// teardownEnd is A06 §13.3 for a box whose process was started: shutdown,
// kill the root (and the scope), verify (a sandbox.violation{root_check}
// when processes survive, and a bounded number of further kills, so
// sandbox.destroy is always written), close the client and then the proxy
// listener (its events precede sandbox.destroy), remove the owner files,
// emit sandbox.destroy with the A06 §7.4 end the daemon observed. An
// executor-exit reason becomes resource when the scope's counters show a
// memory or pids kill; a seccomp kill of warden-exec is also a
// sandbox.violation{seccomp} (A06 §11.5).
func (m *Manager) teardownEnd(ctx context.Context, b *box, reason sandbox.DestroyReason, known *End) error {
	start := m.cfg.Now()
	killed := 0
	var errs []error
	// 1. exec.shutdown, only to an executor that completed exec.hello, then
	// wait for it (and with it bwrap, PID 1 of the namespace) to exit. The
	// executor's own count is its claim: it is bounded by what the sandbox
	// may hold (pids limit), never negative (CWE-20).
	if b.client != nil && b.helloOK {
		grace := 3000
		if reason == sandbox.ReasonCancel {
			grace = 0
		}
		sctx, cancel := context.WithTimeout(ctx, m.t.shutdown)
		if res, err := b.client.Shutdown(sctx, execproto.ShutdownParams{GraceMS: &grace, Reason: shutdownReason(reason)}); err == nil {
			killed += clampCount(res.KilledPIDs, b.spec.Limits.Pids)
		}
		select {
		case <-b.run.Done():
		case <-sctx.Done():
		}
		cancel()
	}
	// The scope's counters, before the scope is killed (and collected).
	var after CgroupCounters
	if b.cgroupDir != "" {
		var err error
		if after, err = ReadCgroupCounters(b.cgroupDir); err != nil {
			errs = append(errs, fmt.Errorf("scope counters: %w", err))
			after = b.counters
		}
	}
	// 2. Kill the root: the scope's cgroup.kill, then SIGKILL to bwrap.
	if b.cgroupDir != "" {
		n, err := KillCgroup(b.cgroupDir, killPid)
		if err != nil {
			errs = append(errs, fmt.Errorf("scope %s: %w", b.unit, err))
		}
		killed += n
	}
	select {
	case <-b.run.Done():
	default:
		if b.cgroupDir == "" {
			killed++
		}
		if err := b.run.Kill(); err != nil {
			errs = append(errs, fmt.Errorf("killing bwrap: %w", err))
		}
	}
	// 3. Verify: bwrap reaped (the PID namespace died with it). Processes
	// left after killVerify are a root_check violation (A06 §13.3 step 3);
	// killing goes on for a bounded number of rounds, whatever ctx says, and
	// sandbox.destroy is written in every case.
	if !waitDone(b.run, m.t.killVerify) {
		m.violation(ctx, b, sandbox.ViolationRootCheck, "processes survived teardown")
		errs = append(errs, fmt.Errorf("%w: sandbox %s: processes survived teardown", sandbox.ErrUnavailable, b.spec.ID))
		for i := 0; i < m.t.killRounds; i++ {
			if b.cgroupDir != "" {
				_, _ = KillCgroup(b.cgroupDir, killPid)
			}
			_ = b.run.Kill()
			if waitDone(b.run, m.t.killVerify) {
				break
			}
		}
	}
	// 4. The executor channel, then the proxy listener: Close waits until
	// the proxy wrote its pending events.
	if b.client != nil {
		b.client.Close()
	}
	if b.proxy != nil {
		if err := b.proxy.Close(); err != nil {
			errs = append(errs, fmt.Errorf("proxy listener: %w", err))
		}
	}
	b.cancel()
	// 5. Owner files: state.json and the generated /etc; argv.json stays.
	errs = append(errs, removeOwnerFiles(b.sbDir, true))
	// 6. sandbox.destroy, with what is known of the end (A06 §7.4).
	var rootExit *RootExit
	if x, ok := b.run.Exit(); ok {
		rootExit = &x
	}
	end := ClassifyEnd(b.counters, after, rootExit, false)
	if known != nil && known.Resource != nil {
		end.Resource = known.Resource
	}
	if end.Resource != nil && reason == sandbox.ReasonExecutorExit {
		reason = sandbox.ReasonResource
	}
	if end.SeccompKill {
		m.violation(ctx, b, sandbox.ViolationSeccomp, "warden-exec was killed by the seccomp filter (SIGSYS)")
	}
	ev := sandbox.DestroyEvent{SessionID: b.spec.SessionID, SandboxID: b.spec.ID, Reason: reason, KilledPIDs: clampCount(killed, b.spec.Limits.Pids+1),
		DurationMS: max(0, m.cfg.Now().Sub(start).Milliseconds()), Resource: end.Resource, SeccompKill: end.SeccompKill}
	if err := m.cfg.Events.SandboxDestroy(context.WithoutCancel(ctx), ev); err != nil {
		errs = append(errs, fmt.Errorf("sandbox.destroy: %w", err))
	}
	return errors.Join(errs...)
}

// waitDone waits up to d for the root to be reaped.
func waitDone(r root, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-r.Done():
		return true
	case <-t.C:
		return false
	}
}

// violation writes sandbox.violation; a failed write is not fatal to the
// teardown that reports it.
func (m *Manager) violation(ctx context.Context, b *box, kind sandbox.ViolationKind, detail string) {
	_ = m.cfg.Events.SandboxViolation(context.WithoutCancel(ctx), sandbox.ViolationEvent{SessionID: b.spec.SessionID, SandboxID: b.spec.ID, Kind: kind, Detail: detail})
}

// clampCount bounds a process count to [0, limit] (sandbox.destroy
// killed_pids has minimum 0; no sandbox holds more than its pids limit).
func clampCount(n, limit int) int {
	return min(max(n, 0), max(limit, 0))
}

// Exit reports how bwrap ended, once it has been reaped.
func (r *Running) Exit() (RootExit, bool) {
	st := r.State()
	if st == nil {
		return RootExit{}, false
	}
	ws, ok := st.Sys().(syscall.WaitStatus)
	if !ok {
		return RootExit{}, false
	}
	if ws.Signaled() {
		return RootExit{Signal: unix.SignalName(ws.Signal())}, true
	}
	return RootExit{Exited: true, Code: ws.ExitStatus()}, true
}

// bootID is this boot's /proc/sys/kernel/random/boot_id.
func bootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" || len(id) > 64 {
		return "", fmt.Errorf("boot_id %q", id)
	}
	return id, nil
}

// release undoes a Create that started nothing.
func (m *Manager) release(b *box) {
	if b.proxy != nil {
		_ = b.proxy.Close()
	}
	b.cancel()
	_ = removeOwnerFiles(b.sbDir, true)
}

func shutdownReason(r sandbox.DestroyReason) execproto.ShutdownReason {
	switch r {
	case sandbox.ReasonTaskEnd:
		return execproto.ShutdownTaskEnd
	case sandbox.ReasonCancel:
		return execproto.ShutdownCancel
	case sandbox.ReasonTimeout:
		return execproto.ShutdownTimeout
	case sandbox.ReasonResource:
		return execproto.ShutdownResource
	case sandbox.ReasonDaemonStop:
		return execproto.ShutdownDaemonStop
	}
	return execproto.ShutdownError
}

// Recovered is what RecoverOrphans found and cleaned.
type Recovered struct {
	Sandboxes []string // sandbox ids whose state file was found (and destroyed with reason orphan)
	Scopes    []string // scope units killed that no state file named
	Skipped   []string // state files left alone, with the reason
}

// RecoverOrphans is A06 §13.5 for this manager's home, at daemon start and
// before any Create. For every <home>/sessions/*/sandbox/*/state.json that
// is the user's own (platform.ReadOwnerOnly) and describes a sandbox of
// this home (State.Check): if the recorded root is alive with the recorded
// start time it is killed, its scope too; then sandbox.destroy{orphan} is
// written to its session and its owner files are removed. Afterwards every
// scope of this home still present below the user manager's cgroup is
// killed. Nothing of another home is read or touched: the scan is below
// this home only, and scopes match only by this home's hash (D-042).
//
// The proxy sockets of earlier runs are not removed here: wardend calls
// sandbox.SweepProxySockets once its endpoint is bound (D-053, D-065).
func (m *Manager) RecoverOrphans(ctx context.Context) (Recovered, error) {
	var rec Recovered
	var errs []error
	home := m.cfg.WardenHome
	scopes, err := FindScopes(sandbox.UserServiceCgroup(m.cfg.CgroupRoot, os.Getuid()), home)
	if err != nil {
		errs = append(errs, fmt.Errorf("scopes: %w", err))
	}
	sessions := filepath.Join(home, "sessions")
	for _, p := range []string{home, sessions} {
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return rec, m.killScopes(scopes, &rec, errs)
		}
		if err != nil || !fi.IsDir() {
			return rec, errors.Join(append(errs, fmt.Errorf("%s is not a directory (%v)", p, err))...)
		}
	}
	ses, err := os.ReadDir(sessions)
	if err != nil {
		return rec, errors.Join(append(errs, err)...)
	}
	for _, se := range ses {
		if !se.IsDir() {
			continue
		}
		sbRoot := filepath.Join(sessions, se.Name(), "sandbox")
		// A symlinked sandbox directory would lead the sweep outside this
		// home (round 1, item 7): only a real directory is read.
		if fi, err := os.Lstat(sbRoot); err != nil || !fi.IsDir() {
			if err == nil {
				rec.Skipped = append(rec.Skipped, sbRoot+": not a directory ("+fi.Mode().Type().String()+")")
			}
			continue
		}
		sbs, err := os.ReadDir(sbRoot)
		if err != nil {
			continue // no sandbox directory, or not a directory: nothing of ours
		}
		for _, sb := range sbs {
			if !sb.IsDir() {
				continue
			}
			dir := filepath.Join(sbRoot, sb.Name())
			raw, err := platform.ReadOwnerOnly(filepath.Join(dir, StateFile), maxStateFile)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			var st State
			if err == nil {
				st, err = ParseState(raw)
			}
			if err == nil {
				err = st.Check(home, se.Name(), sb.Name())
			}
			if err != nil {
				rec.Skipped = append(rec.Skipped, dir+": "+err.Error())
				continue
			}
			killed, kerr := m.killOrphan(st, scopes[st.SandboxID])
			delete(scopes, st.SandboxID)
			errs = append(errs, kerr, removeOwnerFiles(dir, true))
			ev := sandbox.DestroyEvent{SessionID: st.SessionID, SandboxID: st.SandboxID, Reason: sandbox.ReasonOrphan, KilledPIDs: killed}
			if err := m.cfg.Events.SandboxDestroy(ctx, ev); err != nil {
				errs = append(errs, fmt.Errorf("sandbox.destroy %s: %w", st.SandboxID, err))
			}
			rec.Sandboxes = append(rec.Sandboxes, st.SandboxID)
		}
	}
	return rec, m.killScopes(scopes, &rec, errs)
}

func (m *Manager) killScopes(scopes map[string]string, rec *Recovered, errs []error) error {
	for _, dir := range scopes {
		if _, err := KillCgroup(dir, killPid); err != nil {
			errs = append(errs, err)
		}
		rec.Scopes = append(rec.Scopes, filepath.Base(dir))
	}
	return errors.Join(errs...)
}

// killOrphan kills a recorded root that is still the same process (same
// boot, pid and start time), and its scope.
func (m *Manager) killOrphan(st State, scopeDir string) (int, error) {
	killed := 0
	var errs []error
	if scopeDir != "" {
		n, err := KillCgroup(scopeDir, killPid)
		killed += n
		errs = append(errs, err)
	}
	boot, err := bootID()
	if err != nil {
		return killed, errors.Join(append(errs, fmt.Errorf("boot id: %w", err))...)
	}
	if boot != st.BootID {
		return killed, errors.Join(errs...) // another boot: the pid names another process
	}
	if t, err := procStartTime(st.RootPID); err == nil && t == st.RootStartTime {
		if err := killPid(st.RootPID); err == nil && scopeDir == "" {
			killed++
		}
		deadline := time.Now().Add(m.t.killVerify)
		for time.Now().Before(deadline) {
			if t, err := procStartTime(st.RootPID); err != nil || t != st.RootStartTime || procZombie(st.RootPID) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return killed, errors.Join(errs...)
}

func killPid(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func procStat(pid int) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	return string(b), err
}

func procStartTime(pid int) (uint64, error) {
	s, err := procStat(pid)
	if err != nil {
		return 0, err
	}
	return ParseStartTime(s)
}

// procZombie reports a process that has exited and waits to be reaped.
func procZombie(pid int) bool {
	s, err := procStat(pid)
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && strings.HasPrefix(strings.TrimSpace(s[i+1:]), "Z")
}

// userProcesses counts the processes of uid (RLIMIT_NPROC counts per real
// uid, A06 §7.3).
func userProcesses(uid int) (int, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		fi, err := os.Lstat("/proc/" + e.Name())
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) == uid {
			n++
		}
	}
	return n, nil
}

// removeOwnerFiles removes state.json and the generated /etc files of a
// sandbox directory, each only if it is a regular file owned by the user
// (never through a link), then the etc directory if empty. argv.json and
// profiles.json stay (A06 §13.3 step 5).
func removeOwnerFiles(sbDir string, etc bool) error {
	var errs []error
	paths := []string{filepath.Join(sbDir, StateFile)}
	if etc {
		for _, n := range EtcFileNames() {
			paths = append(paths, filepath.Join(sbDir, EtcDir, n))
		}
	}
	for _, p := range paths {
		errs = append(errs, removeOwnFile(p))
	}
	if etc {
		if fi, err := os.Lstat(filepath.Join(sbDir, EtcDir)); err == nil && fi.IsDir() {
			_ = os.Remove(filepath.Join(sbDir, EtcDir))
		}
	}
	return errors.Join(errs...)
}

func removeOwnFile(p string) error {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not the user's regular file; left in place", p)
	}
	return os.Remove(p)
}

// capBuffer keeps the first max bytes of a stream for error messages.
type capBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// suffix renders bwrap's stderr for an error, or nothing.
func (c *capBuffer) suffix() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := strings.TrimSpace(c.buf.String())
	if s == "" {
		return ""
	}
	return "; bwrap: " + execproto.Neutralise(s)
}

var _ io.Writer = (*capBuffer)(nil)
