//go:build linux

// sandboxed: these tests spawn the test's own fixed commands through the
// executor's spawn path (see main_linux_test.go); nothing here runs an
// agent-requested process (INV-H).

package exec_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/exec"
	"warden.dev/warden/internal/exec/exectest"
	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/tools"
)

// recordConn records every byte the executor writes, so a test can read the
// exact frame order on the wire and search it for a value that must never
// be there.
type recordConn struct {
	net.Conn
	mu  sync.Mutex
	out bytes.Buffer
}

func (c *recordConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.mu.Lock()
	c.out.Write(b[:n])
	c.mu.Unlock()
	return n, err
}

func (c *recordConn) written() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.out.Bytes()...)
}

// rig is one executor (server, process table, fake clock) over a net.Pipe,
// with the daemon's real client on the other end. The worktree is base/work.
type rig struct {
	t     *testing.T
	base  string
	work  string
	fc    *exectest.FakeClock
	procs *exec.Procs
	c     *tools.Client
	conn  *recordConn

	mu  sync.Mutex
	ios []execproto.IONotification
}

const testProfiles = `{"version":1,"task_key":"implement","profiles":{"echo":[["echo","<args>"]],"sleep":[["sleep","<args>"]]}}`

func baseEnv(base string) []string {
	return []string{"HOME=" + base + "/scratch/home", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TERM=dumb", "TMPDIR=/tmp"}
}

func newRig(t *testing.T, mutate ...func(*exec.ProcConfig)) *rig {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"work/sub", "scratch/home", "git", "outside"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "work", ".env"), []byte("TOKEN=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree, err := exec.OpenTree([]exec.Root{
		{Name: execproto.RootWorktree, Path: base + "/work", Mode: execproto.RootRW},
		{Name: execproto.RootScratch, Path: base + "/scratch", Mode: execproto.RootRW},
		{Name: execproto.RootGit, Path: base + "/git", Mode: execproto.RootRW},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tree.Close() })
	profiles, err := execproto.ParseProfiles([]byte(testProfiles))
	if err != nil {
		t.Fatal(err)
	}
	limits, err := exec.ParseChildLimits("cpu=600,fsize=1073741824,nofile=256,core=0")
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, base: base, work: base + "/work", fc: exectest.NewFakeClock()}
	cfg := exec.ProcConfig{
		Tree: tree, Mode: execproto.ModeTool, Profiles: profiles, Limits: limits, MaxProcs: 4,
		Env: baseEnv(base), Clock: r.fc,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	a, b := net.Pipe()
	r.conn = &recordConn{Conn: a}
	s := newServer(t, r.conn)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.procs, err = exec.RegisterProc(ctx, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop := startServer(t, s, r.conn)
	r.c = tools.New(context.Background(), b, func(n execproto.IONotification) {
		r.mu.Lock()
		r.ios = append(r.ios, n)
		r.mu.Unlock()
	})
	// Cleanups run last-in first-out: kill everything (KILL at once), stop
	// the server, then close the table.
	t.Cleanup(func() {
		r.procs.Close()
	})
	t.Cleanup(func() { _ = stop() })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
		defer cancel()
		_, _ = r.procs.Shutdown(ctx, 0, "")
		_ = r.c.Close()
	})
	if _, err := r.c.Hello(context.Background(), helloParams()); err != nil {
		t.Fatal(err)
	}
	return r
}

func helperBin(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// helper is the argv of one test helper (an absolute path: no profile, so
// the spawn needs authz.approved).
func helper(t *testing.T, args ...string) []string {
	return append([]string{helperBin(t), "helper"}, args...)
}

func spawnParams(argv []string) execproto.SpawnParams {
	return execproto.SpawnParams{
		CallID: "call_1", Argv: argv, TimeoutMS: 600000,
		Authz: execproto.Authz{DecisionID: "dec_1", Approved: true, Kind: execproto.AuthzTool},
	}
}

func (r *rig) spawn(p execproto.SpawnParams) (execproto.SpawnResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	return r.c.Spawn(ctx, p)
}

func (r *rig) mustSpawn(p execproto.SpawnParams) execproto.SpawnResult {
	r.t.Helper()
	res, err := r.spawn(p)
	if err != nil {
		r.t.Fatalf("spawn %q: %v", p.Argv, err)
	}
	return res
}

func (r *rig) wait(h string) execproto.WaitResult {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	res, err := r.c.Wait(ctx, execproto.WaitParams{Handle: h})
	if err != nil {
		r.t.Fatalf("wait %s: %v (a wait that never returns means a process or pipe outlived its call)", h, err)
	}
	if res.State != execproto.StateExited {
		r.t.Fatalf("wait %s: state %s", h, res.State)
	}
	return res
}

// output joins the chunks of handle h per stream (and the marker).
func (r *rig) output(h string) map[execproto.Stream]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[execproto.Stream]string{}
	for _, n := range r.ios {
		if n.Handle != h {
			continue
		}
		d := n.Data
		if n.Encoding == execproto.EncodingBase64 {
			b, _ := base64Decode(d)
			d = string(b)
		}
		out[n.Stream] += d
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hangGuard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// alive reports whether pid exists and is not a zombie.
func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || i+2 >= len(s) || s[i+2] != 'Z'
}

func exitCode(res execproto.WaitResult) int {
	if res.ExitCode == nil {
		return -1
	}
	return *res.ExitCode
}

// TestSpawn_RunsArgvAndReportsExit guards the normal path of design A06
// §6 and §7.1: a profile command found through the sandbox PATH runs with
// its argv as given, its output arrives as exec.proc.io chunks on the
// right streams, and the wait reports exit 0 with reason exit, the
// executor's profile, the resolved executable and the byte counts.
func TestSpawn_RunsArgvAndReportsExit(t *testing.T) {
	r := newRig(t)
	p := spawnParams([]string{"echo", "hello", "world"})
	p.Authz.Profile, p.Authz.Approved = "echo", false
	res := r.mustSpawn(p)
	if res.Profile != "echo" || !strings.HasSuffix(res.Executable, "/echo") || res.PGID != res.PID || !strings.HasPrefix(res.Handle, "ph_") {
		t.Errorf("spawn result %+v", res)
	}
	w := r.wait(res.Handle)
	if exitCode(w) != 0 || w.Reason != execproto.ReasonExit || w.Signal != nil || w.SeccompKill {
		t.Errorf("wait = %+v", w)
	}
	if got := r.output(res.Handle)[execproto.StreamStdout]; got != "hello world\n" {
		t.Errorf("stdout %q", got)
	}
	if w.Bytes == nil || w.Bytes.Stdout != 12 || w.Bytes.Forwarded != 12 || w.Truncated || w.Rusage == nil {
		t.Errorf("bytes %+v truncated %v rusage %+v", w.Bytes, w.Truncated, w.Rusage)
	}

	h := r.mustSpawn(spawnParams(helper(t, "both")))
	r.wait(h.Handle)
	if o := r.output(h.Handle); o[execproto.StreamStdout] != "to stdout" || o[execproto.StreamStderr] != "to stderr" {
		t.Errorf("streams %q", o)
	}
}

// TestSpawn_RefusesShellStrings guards CF-19 (WRD-16 §9 "argv array only,
// no shell string") at the executor, A06 §6 step 2, and the realpath
// re-check this task wires in: a PATH entry can hold a link with an
// innocent name that points at a shell (./tools/xsh -> /bin/sh), which the
// name-only check cannot see. Both are refused with shell_string before
// anything runs, also when the call is approved: a shell string is R6 and
// no approval makes it runnable.
func TestSpawn_RefusesShellStrings(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", filepath.Join(bin, "notashell")); err != nil {
		t.Fatal(err)
	}
	r := newRig(t, func(c *exec.ProcConfig) {
		c.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin"}, c.Env...)
	})
	marker := filepath.Join(r.base, "outside", "ran")
	for _, argv := range [][]string{
		{"sh", "-c", ": > " + marker},
		{"/bin/sh", "-c", ": > " + marker},
		{"env", "bash", "-lc", ": > " + marker},
		{"notashell", "-c", ": > " + marker},
	} {
		_, err := r.spawn(spawnParams(argv))
		if !errors.Is(err, execproto.ErrShellString) {
			t.Errorf("%q: %v, want shell_string", argv, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a shell string ran")
	}
}

// TestSpawn_ProfileConsistency guards A06 §6 steps 4 and 5 through the
// whole spawn path: a command outside every granted profile runs only with
// an approval, and authz.profile must name the executor's own match.
func TestSpawn_ProfileConsistency(t *testing.T) {
	r := newRig(t)
	p := spawnParams([]string{"echo", "x"})
	p.Authz.Approved = false
	if _, err := r.spawn(p); !errors.Is(err, execproto.ErrProfileMismatch) {
		t.Errorf("profile echo matched but authz.profile empty: %v, want profile_mismatch", err)
	}
	p = spawnParams(helper(t, "pwd"))
	p.Authz.Approved = false
	if _, err := r.spawn(p); !errors.Is(err, execproto.ErrNotAllowed) {
		t.Errorf("no profile, not approved: %v, want not_allowed", err)
	}
}

// TestSpawn_CwdUnderWorktree guards A06 §5.3 and §6 step 6: the cwd is
// resolved under the worktree with §5.1 (no escape, no deny-listed
// directory, a directory), and the child starts exactly there.
func TestSpawn_CwdUnderWorktree(t *testing.T) {
	r := newRig(t)
	p := spawnParams(helper(t, "pwd"))
	p.Cwd = "sub"
	h := r.mustSpawn(p)
	r.wait(h.Handle)
	if got := r.output(h.Handle)[execproto.StreamStdout]; got != r.work+"/sub" {
		t.Errorf("cwd %q, want %q", got, r.work+"/sub")
	}
	h = r.mustSpawn(spawnParams(helper(t, "pwd")))
	r.wait(h.Handle)
	if got := r.output(h.Handle)[execproto.StreamStdout]; got != r.work {
		t.Errorf("default cwd %q, want the worktree root", got)
	}
	for cwd, want := range map[string]execproto.Code{
		"../outside":        execproto.ErrPathEscape,
		r.base + "/outside": execproto.ErrOutsideRoots,
		r.base + "/scratch": execproto.ErrOutsideRoots,
		".env":              execproto.ErrDenyList,
		"missing":           execproto.ErrNotFound,
	} {
		p := spawnParams(helper(t, "pwd"))
		p.Cwd = cwd
		if _, err := r.spawn(p); !errors.Is(err, want) {
			t.Errorf("cwd %q: %v, want %s", cwd, err, want.Name())
		}
	}
}

// TestSpawn_EnvironmentIsTheAllowlist guards A06 §7.1 step 2 and §8 with a
// real child (INV-B, INV-C, TestSandboxEnvAllowlist's executor layer): a
// variable the launch environment should not have had, here a fake
// provider key, is not in the child's environment and appears nowhere on
// the wire, while the §8 variables are passed unchanged.
func TestSpawn_EnvironmentIsTheAllowlist(t *testing.T) {
	const canary = "sk-ant-api03-CANARYCANARYCANARY"
	r := newRig(t, func(c *exec.ProcConfig) {
		c.Env = append(c.Env, "ANTHROPIC_API_KEY="+canary, "SSH_AUTH_SOCK=/tmp/agent.sock", "LD_PRELOAD=/work/x.so")
	})
	h := r.mustSpawn(spawnParams(helper(t, "env")))
	r.wait(h.Handle)
	got := r.output(h.Handle)[execproto.StreamStdout]
	want := strings.Join([]string{"HOME=" + r.base + "/scratch/home", "LANG=C.UTF-8", "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=dumb", "TMPDIR=/tmp"}, "\n")
	if got != want {
		t.Errorf("child environment:\n%s\nwant exactly:\n%s", got, want)
	}
	if bytes.Contains(r.conn.written(), []byte(canary)) {
		t.Error("the launch environment's key reached the wire")
	}
}

// TestSpawn_SecretEnv guards HX-1b and INV-C at the executor (A06 §4.3
// secret_env, §6 step 5, §7.1 step 2): in tool mode secret_env is refused;
// in harness mode the one spec key reaches the engine's environment, and
// the value appears nowhere the executor writes (no chunk, no error, no
// result), nor on the shim's command line, which every sandbox process can
// read in /proc/<pid>/cmdline.
func TestSpawn_SecretEnv(t *testing.T) {
	const secret = "gho_CANARYCANARYCANARYCANARY0000"
	tool := newRig(t)
	p := spawnParams(helper(t, "has-env", "COPILOT_TOKEN"))
	p.SecretEnv = execproto.NewSecretEnv(map[string]string{"COPILOT_TOKEN": secret})
	if _, err := tool.spawn(p); !errors.Is(err, execproto.ErrInvalidArgument) {
		t.Errorf("secret_env in tool mode: %v, want invalid_argument", err)
	}
	if bytes.Contains(tool.conn.written(), []byte(secret)) {
		t.Error("tool mode: the secret reached the wire")
	}

	bin := helperBin(t)
	spec := &exec.HarnessSpec{Version: 1, HarnessID: "copilot", RunMode: "colocated",
		Argv: []string{bin, "helper", "has-env", "COPILOT_TOKEN"}, Env: map[string]string{"DISABLE_AUTOUPDATER": "1"}, SecretEnvKeys: []string{"COPILOT_TOKEN"}}
	hr := newRig(t, func(c *exec.ProcConfig) { c.Mode, c.Harness = execproto.ModeHarness, spec })
	p = spawnParams(spec.Argv)
	p.Authz = execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzHarness}
	p.SecretEnv = execproto.NewSecretEnv(map[string]string{"COPILOT_TOKEN": secret})
	h := hr.mustSpawn(p)
	cmdline, _ := os.ReadFile("/proc/" + strconv.Itoa(h.PID) + "/cmdline")
	hr.wait(h.Handle)
	if got := hr.output(h.Handle)[execproto.StreamStdout]; got != "set, "+strconv.Itoa(len(secret))+" bytes" {
		t.Errorf("engine sees %q, want the secret set", got)
	}
	if bytes.Contains(hr.conn.written(), []byte(secret)) {
		t.Error("harness mode: the secret reached the wire")
	}
	if bytes.Contains(cmdline, []byte(secret)) {
		t.Error("the secret is on the shim's command line")
	}
	// Any other argv is refused in harness mode (A06 §3.1, §14).
	other := spawnParams(helper(t, "env"))
	other.Authz = execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzHarness}
	if _, err := hr.spawn(other); !errors.Is(err, execproto.ErrModeForbidden) {
		t.Errorf("non-spec argv in harness mode: %v, want mode_forbidden", err)
	}
}

// TestSpawn_RlimitsApplied guards A06 §7.3 through the child shim: every
// --child-limits entry is the child's soft and hard limit, and RLIMIT_CPU
// is the smaller of the launch value and 4 × the call timeout, with a hard
// limit one second above it (SIGXCPU at the limit, then SIGKILL).
func TestSpawn_RlimitsApplied(t *testing.T) {
	limits, err := exec.ParseChildLimits("cpu=600,fsize=123456789,nofile=200,core=0,data=3221225472,nproc=4000")
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, func(c *exec.ProcConfig) { c.Limits = limits })
	p := spawnParams(helper(t, "rlimits"))
	p.TimeoutMS = 30000 // 4 × 30 s = 120 s < 600 s
	h := r.mustSpawn(p)
	r.wait(h.Handle)
	got := r.output(h.Handle)[execproto.StreamStdout]
	for _, want := range []string{"cpu=120/121\n", "fsize=123456789/123456789\n", "nofile=200/200\n", "core=0/0\n", "data=3221225472/3221225472\n", "nproc=4000/4000\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("child limits:\n%s\nmissing %q", got, want)
		}
	}
}

// TestWait_FileSizeLimitIsResource guards A06 §7.4's SIGXFSZ row with the
// kernel enforcing RLIMIT_FSIZE on a real writer: the agent loop ends the
// execution failed(resource) on it (escape check, WRD-16 §10.7 row 5).
func TestWait_FileSizeLimitIsResource(t *testing.T) {
	limits, err := exec.ParseChildLimits("cpu=600,fsize=4096,nofile=256,core=0")
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, func(c *exec.ProcConfig) { c.Limits = limits })
	h := r.mustSpawn(spawnParams([]string{"/usr/bin/dd", "if=/dev/zero", "of=big.bin", "bs=8192", "count=2"}))
	w := r.wait(h.Handle)
	if w.Reason != execproto.ReasonResource || w.Resource == nil || w.Resource.Kind != execproto.ResourceFsize || w.Signal == nil || *w.Signal != "SIGXFSZ" {
		t.Errorf("wait = %+v (signal %v), want resource fsize after SIGXFSZ", w, ptrStr(w.Signal))
	}
}

func ptrStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// TestWait_SIGSYSIsSeccompKill guards A06 §7.4's last row with a real
// signal: a process killed by SIGSYS (what the seccomp KILL class
// delivers, §11.5) is reported as reason signal with seccomp_kill true, so
// the daemon records the blocked call instead of an ordinary failure.
func TestWait_SIGSYSIsSeccompKill(t *testing.T) {
	r := newRig(t)
	p := spawnParams([]string{"sleep", "60"})
	p.Authz.Profile, p.Authz.Approved = "sleep", false
	h := r.mustSpawn(p)
	if err := unix.Kill(h.PID, unix.SIGSYS); err != nil {
		t.Fatal(err)
	}
	w := r.wait(h.Handle)
	if w.Reason != execproto.ReasonSignal || !w.SeccompKill || ptrStr(w.Signal) != "SIGSYS" || w.ExitCode != nil {
		t.Errorf("wait = %+v (signal %s), want signal SIGSYS with seccomp_kill", w, ptrStr(w.Signal))
	}
}

// TestSignal_TermThenKillAfterGrace guards A06 §7.6: exec.proc.signal goes
// to the handle's process group, the exit is reported as cancelled, and a
// process that ignores TERM is killed once the grace has passed. The grace
// is the fake clock's, so nothing here depends on how long anything takes.
func TestSignal_TermThenKillAfterGrace(t *testing.T) {
	r := newRig(t)
	ready := filepath.Join(r.base, "outside", "ready")
	h := r.mustSpawn(spawnParams(helper(t, "ignore-term", ready)))
	waitFor(t, "the helper to ignore TERM", func() bool { _, err := os.Stat(ready); return err == nil })
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	sr, err := r.c.Signal(ctx, execproto.SignalParams{Handle: h.Handle, Signal: execproto.SignalTERM})
	if err != nil || !sr.Delivered || sr.PGID != h.PGID {
		t.Fatalf("signal = %+v, %v", sr, err)
	}
	// Still running before the grace: a long poll that times out says so.
	n := r.fc.Pending()
	type wr struct {
		res execproto.WaitResult
		err error
	}
	got := make(chan wr, 1)
	go func() {
		res, err := r.c.Wait(ctx, execproto.WaitParams{Handle: h.Handle, TimeoutMS: 1000})
		got <- wr{res, err}
	}()
	if err := r.fc.WaitTimers(ctx, n+1); err != nil {
		t.Fatal(err)
	}
	r.fc.Advance(time.Second)
	if w := <-got; w.err != nil || w.res.State != execproto.StateRunning {
		t.Fatalf("wait before the grace = %+v, %v; want running (TERM is ignored)", w.res, w.err)
	}
	if !alive(h.PID) {
		t.Fatal("the process died before the grace")
	}
	r.fc.Advance(2 * time.Second) // 3 s since TERM
	w := r.wait(h.Handle)
	if w.Reason != execproto.ReasonCancelled || ptrStr(w.Signal) != "SIGKILL" {
		t.Errorf("wait = %+v (signal %s), want cancelled by SIGKILL", w, ptrStr(w.Signal))
	}
}

// TestTimeout_KillsTheCall guards A06 §7.3's wall-clock row and §7.4's
// timeout row: when timeout_ms passes, the watchdog stops the group and the
// exit is reported as timeout, not as the signal.
func TestTimeout_KillsTheCall(t *testing.T) {
	r := newRig(t)
	p := spawnParams([]string{"sleep", "60"})
	p.Authz.Profile, p.Authz.Approved, p.TimeoutMS = "sleep", false, 1000
	h := r.mustSpawn(p)
	r.fc.Advance(time.Second)
	w := r.wait(h.Handle)
	if w.Reason != execproto.ReasonTimeout || ptrStr(w.Signal) != "SIGTERM" {
		t.Errorf("wait = %+v (signal %s), want timeout", w, ptrStr(w.Signal))
	}
}

// TestNoProcessSurvivesItsCall guards A06 §7.7: when the leader exits, its
// whole process group is killed, so a grandchild that keeps the output
// pipe open neither survives nor holds the wait; and a process that left
// the group with setsid (a daemon a build script started) is killed by the
// stray sweep and counted in strays_killed.
func TestNoProcessSurvivesItsCall(t *testing.T) {
	r := newRig(t)
	h := r.mustSpawn(spawnParams(helper(t, "fork-sleep")))
	w := r.wait(h.Handle)
	pid, err := strconv.Atoi(r.output(h.Handle)[execproto.StreamStdout])
	if err != nil || exitCode(w) != 0 {
		t.Fatalf("helper: %q, %+v", r.output(h.Handle), w)
	}
	if alive(pid) {
		_ = unix.Kill(pid, unix.SIGKILL)
		t.Error("a grandchild in the leader's group survived the call")
	}
	if w.StraysKilled != 0 {
		t.Errorf("strays_killed %d: the leader's own group is killed as the group, not counted as strays", w.StraysKilled)
	}

	h = r.mustSpawn(spawnParams(helper(t, "setsid-sleep")))
	w = r.wait(h.Handle)
	pid, err = strconv.Atoi(r.output(h.Handle)[execproto.StreamStdout])
	if err != nil {
		t.Fatalf("helper: %q", r.output(h.Handle))
	}
	waitFor(t, "the setsid daemon to be killed", func() bool { return !alive(pid) })
	if alive(pid) || w.StraysKilled != 1 {
		_ = unix.Kill(pid, unix.SIGKILL)
		t.Errorf("setsid daemon alive %v, strays_killed %d; want killed and counted", alive(pid), w.StraysKilled)
	}
}

// TestSpawn_BusyAtMaxProcs guards A06 §3.1 --max-procs: further spawns
// fail with busy while that many handles run, and a slot is free again
// once a handle has ended.
func TestSpawn_BusyAtMaxProcs(t *testing.T) {
	r := newRig(t, func(c *exec.ProcConfig) { c.MaxProcs = 1 })
	p := spawnParams([]string{"sleep", "60"})
	p.Authz.Profile, p.Authz.Approved = "sleep", false
	h := r.mustSpawn(p)
	if _, err := r.spawn(p); !errors.Is(err, execproto.ErrBusy) {
		t.Fatalf("second spawn at --max-procs 1: %v, want busy", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	if _, err := r.c.Signal(ctx, execproto.SignalParams{Handle: h.Handle, Signal: execproto.SignalKILL}); err != nil {
		t.Fatal(err)
	}
	r.wait(h.Handle)
	h2 := r.mustSpawn(p)
	_, _ = r.c.Signal(ctx, execproto.SignalParams{Handle: h2.Handle, Signal: execproto.SignalKILL})
	r.wait(h2.Handle)
}

// TestOutputCap_RealProcess guards A06 §7.5 end to end: a process that
// writes far more than max_output_bytes is not blocked (it exits), the
// daemon receives at most the cap, the marker, and the tail with the
// summary the process printed last; wait reports truncated and the counts.
func TestOutputCap_RealProcess(t *testing.T) {
	r := newRig(t)
	p := spawnParams(helper(t, "spew", "2000000", "\nSUMMARY: 3 passed\n"))
	p.MaxOutputBytes = 8192
	h := r.mustSpawn(p)
	w := r.wait(h.Handle)
	o := r.output(h.Handle)
	if exitCode(w) != 0 || !w.Truncated || w.Bytes == nil || w.Bytes.Forwarded != 8192 || w.Bytes.Stdout != 2000000+19 || w.Bytes.Discarded != 2000000+19-8192 {
		t.Errorf("wait = %+v bytes %+v", w, w.Bytes)
	}
	if !strings.Contains(o[execproto.StreamMarker], "output truncated: "+strconv.Itoa(2000000+19-8192)+" bytes omitted") {
		t.Errorf("marker %q", o[execproto.StreamMarker])
	}
	if !strings.HasSuffix(o[execproto.StreamStdout], "SUMMARY: 3 passed\n") || len(o[execproto.StreamStdout]) != 8192 {
		t.Errorf("stdout: %d bytes, ends %q", len(o[execproto.StreamStdout]), o[execproto.StreamStdout][max(0, len(o[execproto.StreamStdout])-30):])
	}
}

// TestIOBeforeWait_RealProcesses guards A06 §4.1 and D-050 with the real
// table: on the wire, every exec.proc.io frame of a handle precedes the
// response of the wait that reports its exit. The frames are read back from
// exactly what the executor wrote, after everything has stopped, so a chunk
// sent late would be seen.
func TestIOBeforeWait_RealProcesses(t *testing.T) {
	r := newRig(t)
	var hs []execproto.SpawnResult
	for i := 0; i < 3; i++ {
		p := spawnParams(helper(t, "spew", "100000", "\nend\n"))
		p.CallID = "call_" + strconv.Itoa(i+1)
		hs = append(hs, r.mustSpawn(p))
	}
	var wg sync.WaitGroup
	for _, h := range hs {
		wg.Add(1)
		go func() { defer wg.Done(); r.wait(h.Handle) }()
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	_, _ = r.procs.Shutdown(ctx, 0, "")
	r.procs.Close()

	fr := execproto.NewFrameReader(bytes.NewReader(r.conn.written()))
	waited := map[string]bool{}
	ios := map[string]int{}
	for {
		b, err := fr.ReadFrame()
		if err != nil {
			break
		}
		m, err := execproto.DecodeMessage(b)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case m.Method == execproto.MethodIO:
			var n execproto.IONotification
			if err := json.Unmarshal(m.Params, &n); err != nil {
				t.Fatal(err)
			}
			if waited[n.Handle] {
				t.Fatalf("exec.proc.io seq %d of %s after its wait response", n.Seq, n.Handle)
			}
			ios[n.Handle]++
		case m.Result != nil:
			var res execproto.WaitResult
			if json.Unmarshal(m.Result, &res) == nil && res.State == execproto.StateExited {
				waited[res.Handle] = true
			}
		}
	}
	for _, h := range hs {
		if !waited[h.Handle] || ios[h.Handle] == 0 {
			t.Errorf("%s: waited %v, %d chunks", h.Handle, waited[h.Handle], ios[h.Handle])
		}
		if o := r.output(h.Handle)[execproto.StreamStdout]; len(o) != 100005 {
			t.Errorf("%s: %d bytes of stdout, want 100005", h.Handle, len(o))
		}
	}
}

// TestShutdown_KillsEverything guards A06 §7.8: exec.shutdown stops every
// running group (TERM, then KILL after grace_ms), every wait reports
// executor_shutdown, and no spawn is accepted afterwards.
func TestShutdown_KillsEverything(t *testing.T) {
	r := newRig(t)
	ready := filepath.Join(r.base, "outside", "ready")
	stubborn := r.mustSpawn(spawnParams(helper(t, "ignore-term", ready)))
	p := spawnParams([]string{"sleep", "60"})
	p.Authz.Profile, p.Authz.Approved = "sleep", false
	plain := r.mustSpawn(p)
	waitFor(t, "the helper to ignore TERM", func() bool { _, err := os.Stat(ready); return err == nil })

	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	grace := 3000
	type sr struct {
		res execproto.ShutdownResult
		err error
	}
	got := make(chan sr, 1)
	go func() {
		res, err := r.c.Shutdown(ctx, execproto.ShutdownParams{GraceMS: &grace, Reason: execproto.ShutdownTaskEnd})
		got <- sr{res, err}
	}()
	// The grace timer is one of several the table arms (a drain bound per
	// ending handle), so the clock is moved in steps past the grace, to 10 s
	// of fake time and no further: the helpers' own call timeouts (600 s)
	// must not be what ends them. Then the call must return.
	var res sr
	for i := 0; i < 20 && res.err == nil && res.res == (execproto.ShutdownResult{}); i++ {
		select {
		case res = <-got:
		default:
			r.fc.Advance(500 * time.Millisecond)
			time.Sleep(2 * time.Millisecond)
		}
	}
	if res.err == nil && res.res == (execproto.ShutdownResult{}) {
		select {
		case res = <-got:
		case <-ctx.Done():
			t.Fatal("exec.shutdown did not return after its grace: a process that ignores TERM was never killed")
		}
	}
	if res.err != nil || res.res.KilledPIDs < 2 || res.res.Remaining != 0 {
		t.Fatalf("shutdown = %+v, %v", res.res, res.err)
	}
	for _, h := range []execproto.SpawnResult{stubborn, plain} {
		if alive(h.PID) {
			t.Errorf("%s survived exec.shutdown", h.Handle)
		}
		if w := r.wait(h.Handle); w.Reason != execproto.ReasonExecutorShutdown {
			t.Errorf("%s: reason %s, want executor_shutdown", h.Handle, w.Reason)
		}
	}
	if _, err := r.spawn(p); !errors.Is(err, execproto.ErrShuttingDown) {
		t.Errorf("spawn after exec.shutdown: %v, want shutting_down", err)
	}
}

// TestSpawn_NoInheritedDescriptors guards the shim's close-on-exec sweep:
// the executor holds descriptors a child must never get (the RPC channel on
// fd 3 is inherited without close-on-exec, the root directories, the proxy
// socket). One such descriptor is opened here without close-on-exec, and
// the child must not have it.
func TestSpawn_NoInheritedDescriptors(t *testing.T) {
	r := newRig(t)
	null, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(null)
	// F_DUPFD clears close-on-exec, like a descriptor inherited from the
	// sandbox launch; 100 and up stays clear of the descriptors the
	// helper's own runtime opens at start.
	fd, err := unix.FcntlInt(uintptr(null), unix.F_DUPFD, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	h := r.mustSpawn(spawnParams(helper(t, "fd-open", strconv.Itoa(fd))))
	r.wait(h.Handle)
	if got := r.output(h.Handle)[execproto.StreamStdout]; got != "closed" {
		t.Errorf("the executor's fd %d in the child: %q, want closed", fd, got)
	}
}

func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func copyFile(t *testing.T, from, to string, mode os.FileMode) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, mode); err != nil {
		t.Fatal(err)
	}
}

// TestSpawn_ExecutableResolution guards A06 §6 step 3. An argv[0] with a
// "/" is resolved under the worktree with §5.1, relative to the cwd as
// execve would, following links that stay inside the root
// (./node_modules/.bin/x); a link that leaves the root, a deny-listed
// file, a file that is not executable and a file under the private gitdir
// are refused. A bare name is looked up in the sandbox PATH, and a PATH
// entry inside the worktree is skipped, so a repository cannot shadow
// echo, npm or git with its own file.
func TestSpawn_ExecutableResolution(t *testing.T) {
	r := newRig(t, func(c *exec.ProcConfig) {
		// The worktree's bin directory first in PATH: it must be ignored.
		c.Env = append([]string{"PATH=" + rigBase(c) + "/work/bin:/usr/bin:/bin"}, c.Env...)
	})
	copyFile(t, "/bin/echo", r.work+"/bin/echo", 0o700) // would shadow echo if PATH were trusted
	copyFile(t, "/bin/echo", r.work+"/tools/myecho", 0o700)
	copyFile(t, "/bin/echo", r.work+"/tools/noexec", 0o600)
	copyFile(t, "/bin/echo", r.base+"/git/hooks/post-checkout", 0o700)
	if err := os.MkdirAll(r.work+"/sub/node_modules/.bin", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../tools/myecho", r.work+"/sub/node_modules/.bin/x"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/echo", r.work+"/tools/outlink"); err != nil {
		t.Fatal(err)
	}

	p := spawnParams([]string{"./node_modules/.bin/x", "inside"})
	p.Cwd = "sub"
	h := r.mustSpawn(p)
	if h.Executable != r.work+"/tools/myecho" {
		t.Errorf("executable %q, want the link's target inside the worktree", h.Executable)
	}
	r.wait(h.Handle)
	if got := r.output(h.Handle)[execproto.StreamStdout]; got != "inside\n" {
		t.Errorf("stdout %q", got)
	}

	p = spawnParams([]string{"echo", "x"})
	p.Authz.Profile, p.Authz.Approved = "echo", false
	h = r.mustSpawn(p)
	r.wait(h.Handle)
	if h.Executable != "/usr/bin/echo" {
		t.Errorf("echo resolved to %q: a PATH entry inside the worktree was used", h.Executable)
	}

	for argv0, want := range map[string]execproto.Code{
		"tools/outlink":                     execproto.ErrPathEscape,
		"./.env":                            execproto.ErrDenyList,
		"tools/noexec":                      execproto.ErrInvalidArgument,
		"tools/missing":                     execproto.ErrNotFound,
		"tools":                             execproto.ErrNotRegular,
		r.base + "/git/hooks/post-checkout": execproto.ErrOutsideRoots,
		"no-such-command-anywhere":          execproto.ErrNotFound,
	} {
		if argv0 == "tools" {
			argv0 = "./tools"
		}
		if _, err := r.spawn(spawnParams([]string{argv0})); !errors.Is(err, want) {
			t.Errorf("argv[0] %q: %v, want %s", argv0, err, want.Name())
		}
	}
}

// TestRunChild_RefusesBadUsage: the shim fails closed on a command line it
// does not fully understand, before it sets any limit or executes anything.
// (Only these early refusals can run in the test process itself; the shim's
// limits and execve are covered through real spawns above.)
func TestRunChild_RefusesBadUsage(t *testing.T) {
	good := []string{"--limits", `{"cpu":1,"fsize":1,"nofile":16,"core":0}`, "--cwd-fd", "3", "--", "/bin/true", "true"}
	for name, mutate := range map[string]func([]string) []string{
		"too short":       func(a []string) []string { return a[:6] },
		"no --limits":     func(a []string) []string { a[0] = "--limit"; return a },
		"limits not json": func(a []string) []string { a[1] = "cpu=1"; return a },
		"unknown limit":   func(a []string) []string { a[1] = `{"cpu":1,"fsize":1,"nofile":16,"core":0,"as":1}`; return a },
		"no cpu":          func(a []string) []string { a[1] = `{"cpu":0,"fsize":1,"nofile":16,"core":0}`; return a },
		"fd 2":            func(a []string) []string { a[3] = "2"; return a },
		"fd not decimal":  func(a []string) []string { a[3] = "03"; return a },
		"no --":           func(a []string) []string { a[4] = "-"; return a },
		"empty path":      func(a []string) []string { a[5] = ""; return a },
	} {
		args := mutate(append([]string(nil), good...))
		if err := exec.RunChild(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// rigBase is the rig's base directory, for options that run before newRig
// returns (baseEnv puts HOME at base/scratch/home).
func rigBase(c *exec.ProcConfig) string {
	for _, kv := range c.Env {
		if home, ok := strings.CutPrefix(kv, "HOME="); ok {
			return strings.TrimSuffix(home, "/scratch/home")
		}
	}
	return ""
}

// TestWaitSignal_Handles guards the handle rules of A06 §4.3 and §4.4: a
// malformed handle or signal is invalid_argument, an unknown handle is
// invalid_state, and a handle whose exit has been reported is forgotten
// (its result was delivered once; a second wait is invalid_state), so a
// late signal can never reach a process group whose id was reused.
func TestWaitSignal_Handles(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	for _, h := range []string{"ph_", "ph_x", "1", "ph_12345678901"} {
		if _, err := r.c.Wait(ctx, execproto.WaitParams{Handle: h}); !errors.Is(err, execproto.ErrInvalidArgument) {
			t.Errorf("wait %q: %v, want invalid_argument", h, err)
		}
	}
	if _, err := r.c.Wait(ctx, execproto.WaitParams{Handle: "ph_1", TimeoutMS: 600001}); !errors.Is(err, execproto.ErrInvalidArgument) {
		t.Errorf("wait timeout over 600 s: %v, want invalid_argument", err)
	}
	if _, err := r.c.Signal(ctx, execproto.SignalParams{Handle: "ph_1", Signal: "HUP"}); !errors.Is(err, execproto.ErrInvalidArgument) {
		t.Errorf("signal HUP: %v, want invalid_argument", err)
	}
	if _, err := r.c.Signal(ctx, execproto.SignalParams{Handle: "ph_9", Signal: execproto.SignalTERM}); !errors.Is(err, execproto.ErrInvalidState) {
		t.Errorf("signal to an unknown handle: %v, want invalid_state", err)
	}
	h := r.mustSpawn(spawnParams(helper(t, "pwd")))
	r.wait(h.Handle)
	if _, err := r.c.Wait(ctx, execproto.WaitParams{Handle: h.Handle}); !errors.Is(err, execproto.ErrInvalidState) {
		t.Errorf("second wait after the exit was reported: %v, want invalid_state", err)
	}
	if _, err := r.c.Signal(ctx, execproto.SignalParams{Handle: h.Handle, Signal: execproto.SignalKILL}); !errors.Is(err, execproto.ErrInvalidState) {
		t.Errorf("signal after the exit was reported: %v, want invalid_state", err)
	}
}
