//go:build linux

package linux_bwrap

// sandboxed: runs only the fixed probes "bwrap --version" and "bwrap
// --help", and bwrap with the argv under test (directly or under the fixed
// systemd-run scope) whose command is a copy of this test binary in probe
// mode (it reports what it sees and exits) or in stand-in executor mode
// (it answers exec.hello and exec.shutdown, M2-15, until M2-10 exists);
// never agent input.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/sandbox"
)

// TestMain runs the package tests, or, when this binary was started as
// warden-exec inside a test sandbox, the probe.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "warden-exec" {
		os.Exit(probeMain())
	}
	os.Exit(m.Run())
}

// probeReport is what the probe sees inside the sandbox.
type probeReport struct {
	Fds         []int    `json:"fds"`          // open descriptors, besides the one listing them
	Pid1Environ string   `json:"pid1_environ"` // bwrap's PID 1 environment
	Pid1Err     string   `json:"pid1_err,omitempty"`
	Environ     []string `json:"environ"` // the probe's own environment
	DotEnv      string   `json:"dot_env"` // content of /work/.env (masked: empty)
	DotEnvErr   string   `json:"dot_env_err,omitempty"`
	AwsEntries  []string `json:"aws_entries"` // /work/.aws (masked: empty)
	WriteRoot   string   `json:"write_root"`  // error writing outside the task roots
}

func probeMain() int {
	var r probeReport
	// List descriptors first and with raw calls, before the os package can
	// open its poller descriptors.
	dfd, err := unix.Open("/proc/self/fd", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 70
	}
	buf := make([]byte, 4096)
	n, err := unix.ReadDirent(dfd, buf)
	if err != nil {
		return 71
	}
	_, _, names := unix.ParseDirent(buf[:n], -1, nil)
	for _, name := range names {
		if fd, err := strconv.Atoi(name); err == nil && fd != dfd {
			r.Fds = append(r.Fds, fd)
		}
	}
	unix.Close(dfd)
	slices.Sort(r.Fds)
	// The stand-in executor of the manager tests is chosen by the profiles
	// file, the one input the test controls (the environment is cleared).
	if b, err := os.ReadFile(sandbox.TargetProfiles); err == nil && bytes.HasPrefix(b, []byte(helperMarker)) {
		return serveMain(b)
	}
	if b, err := os.ReadFile("/proc/1/environ"); err != nil {
		r.Pid1Err = err.Error()
	} else {
		r.Pid1Environ = string(b)
	}
	r.Environ = os.Environ()
	if b, err := os.ReadFile("/work/.env"); err != nil {
		r.DotEnvErr = err.Error()
	} else {
		r.DotEnv = string(b)
	}
	if es, err := os.ReadDir("/work/.aws"); err == nil {
		for _, e := range es {
			r.AwsEntries = append(r.AwsEntries, e.Name())
		}
	}
	if err := os.WriteFile("/warden-probe-escape", []byte("x"), 0o600); err != nil {
		r.WriteRoot = err.Error()
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		return 72
	}
	return 0
}

// realSpec lays out one session under a temp Warden home on disk, with a
// real proxy socket and a masked .env file and .aws directory, and puts a
// copy of exe (this test binary) at the executor path, so bwrap runs
// nothing but the probe.
func realSpec(t *testing.T, exe string) *sandbox.Spec {
	t.Helper()
	s := realLayout(t, exe, "{}\n")
	for name, content := range EtcFiles(os.Getuid(), os.Getgid()) {
		if err := os.WriteFile(filepath.Join(filepath.Dir(s.ProfilesFile), "etc", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l, err := net.Listen("unix", s.Proxy.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return s
}

// realLayout is realSpec without the generated /etc files and the proxy
// socket, which the Manager creates itself; profiles is the content of
// the profiles file, and ProfilesDigest is its sha256.
func realLayout(t *testing.T, exe, profiles string) *sandbox.Spec {
	t.Helper()
	// A short base: a Unix socket path is limited to 107 bytes, and
	// t.TempDir() names are long.
	tmp, err := os.MkdirTemp("", "m213")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	home := filepath.Join(tmp, "wh")
	session := filepath.Join(home, "sessions", testUlid)
	s := &sandbox.Spec{
		ID: testID, SessionID: "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB", TaskKey: "implement",
		Level: sandbox.LevelL1, Purpose: sandbox.PurposeTask, Mode: execproto.ModeTool,
		HostHome: filepath.Join(tmp, "home", "ana"), WardenHome: home, SessionDir: session,
		Worktree: filepath.Join(session, "worktree"), Scratch: filepath.Join(session, "scratch", "implement"),
		Cache: filepath.Join(home, "cache", "wsp_01jaxr"), GitDir: filepath.Join(session, "git"),
		ExecBin:        filepath.Join(tmp, "opt", "warden", "bin", "warden-exec"),
		ProfilesFile:   filepath.Join(session, "sandbox", testID, "profiles.json"),
		ProfilesDigest: testDigest,
		WorktreeMasks:  []sandbox.Mask{{RelPath: ".env", Pattern: "**/.env"}, {RelPath: ".aws", Dir: true, Pattern: "**/.aws/**"}},
		Proxy:          sandbox.ProxyEndpoint{Socket: filepath.Join(session, "scratch", "px-1.sock")},
		Limits:         sandbox.DefaultLimits(),
		SessionBranch:  "warden/" + testUlid, BaseCommit: testCommit,
	}
	for _, d := range []string{s.Worktree, filepath.Join(s.Worktree, ".aws"), s.Scratch, s.Cache, s.GitDir,
		filepath.Join(filepath.Dir(s.ProfilesFile), "etc"), filepath.Dir(s.ExecBin)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, content string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(s.Worktree, ".env"), "NOT_A_SECRET=1\n", 0o600)
	write(s.ProfilesFile, profiles, 0o600)
	sum := sha256.Sum256([]byte(profiles))
	s.ProfilesDigest = "sha256:" + hex.EncodeToString(sum[:])
	bin, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	write(s.ExecBin, string(bin), 0o755)
	return s
}

// TestBwrap_RunsTheBuiltArgv is the integration test: the argv this
// package builds, started through Plan.Command, is accepted by the bwrap
// installed here (WSL2 Ubuntu in development) and the probe at the
// executor path sees exactly the A06 §2/§11.1 view:
//
//   - descriptors 0-4 only: fd 3 (RPC) and 4 (stdio) passed through, the
//     seccomp program (5) and the info pipe (6) consumed by bwrap; no
//     descriptor the test process inherited or leaked without close-on-exec
//     (M2-13b, CWE-403);
//   - /proc/1/environ empty: bwrap's PID 1 keeps the environment bwrap was
//     started with, so a daemon variable there is readable by the sandbox
//     (M2-13 review, INV-C); the parent's environment here carries a fake
//     one that Plan.Command must keep out;
//   - its own environment exactly Spec.Env() (INV-C), plus PWD=/work,
//     which bwrap sets itself after --chdir and no option can remove;
//   - /work/.env unreadable (the /dev/null mask is on a nodev bind, so
//     open fails with EACCES) and /work/.aws empty (INV-I masks);
//   - writing outside the task roots fails with EROFS (--remount-ro /).
//
// It runs the probed argv and the fallback argv of a bwrap without the
// optional flags (--seccomp 5, no --size, no --disable-userns), which this
// bwrap still accepts.
func TestBwrap_RunsTheBuiltArgv(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap is not installed")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DAEMON_SECRET", "fake-not-a-secret")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ver, err := exec.CommandContext(ctx, bwrap, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	help, err := exec.CommandContext(ctx, bwrap, "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	probed, err := ParseFeatures(string(ver), string(help))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Features{probed, {Version: probed.Version}} {
		t.Run(strings.Join(append([]string{"bwrap", f.Version, "missing"}, f.Missing()...), " "), func(t *testing.T) {
			runProbe(t, ctx, bwrap, self, f, nil)
		})
	}
}

// runProbe starts the probe at the executor path with the built argv,
// directly (scope nil, Plan.Command) or under the systemd-run wrapper
// (Plan.ScopedCommand), and checks the A06 §2/§11.1 view listed on
// TestBwrap_RunsTheBuiltArgv.
func runProbe(t *testing.T, ctx context.Context, bwrap, self string, f Features, scope *Scope) {
	t.Helper()
	s := realSpec(t, self)
	plan, err := Prepare(s, f, ExecFlags{CPUSeconds: 60, Cgroup: true, MaxProcs: 8}, DefaultProbes())
	if err != nil {
		t.Fatalf("Prepare() = %v", err)
	}
	arch, err := HostSeccompArch()
	if err != nil {
		t.Fatal(err)
	}
	prog, err := SeccompFile(arch)
	if err != nil {
		t.Fatal(err)
	}
	defer prog.Close()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	rpc, ours := os.NewFile(uintptr(pair[0]), "rpc"), os.NewFile(uintptr(pair[1]), "rpc-daemon")
	defer rpc.Close()
	defer ours.Close()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	infoR, infoW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer infoR.Close()
	// A descriptor this process inherited without close-on-exec, as the
	// GitHub runner hands the test process (CI saw fds 142 and 145
	// inside) and as a shell or service manager can hand wardend. Here
	// it is the worktree's .env, opened and flagged inheritable.
	leak, err := os.Open(filepath.Join(s.Worktree, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	defer leak.Close()
	if _, err := unix.FcntlInt(leak.Fd(), unix.F_SETFD, 0); err != nil {
		t.Fatal(err)
	}
	cmd, err := plan.Command(ctx, bwrap, rpc, null, prog, infoW)
	if scope != nil {
		sc := *scope
		sc.Unit = sandbox.ScopeUnit(s.WardenHome, s.ID)
		sc.Limits = s.Limits
		cmd, err = plan.ScopedCommand(ctx, sc, bwrap, rpc, null, prog, infoW)
	}
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Start()
	infoW.Close()
	if err != nil {
		t.Fatal(err)
	}
	info, _ := io.ReadAll(infoR)
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("bwrap exit %d; stderr: %s", ee.ExitCode(), stderr.String())
		}
		t.Fatal(err)
	}
	if !strings.Contains(string(info), `"child-pid"`) {
		t.Errorf("info fd 6 = %q, want bwrap's child-pid JSON", info)
	}
	var r probeReport
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		t.Fatalf("probe output %q: %v; stderr: %s", stdout.String(), err, stderr.String())
	}
	if !slices.Equal(r.Fds, []int{0, 1, 2, FdRPC, FdHarnessStdio}) {
		t.Errorf("descriptors inside = %v, want [0 1 2 3 4]", r.Fds)
	}
	if r.Pid1Err != "" || r.Pid1Environ != "" {
		t.Errorf("/proc/1/environ = %q (err %q), want readable and empty (INV-C)", r.Pid1Environ, r.Pid1Err)
	}
	env, err := s.Env()
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, e := range env {
		want = append(want, e.Key+"="+e.Value)
	}
	want = append(want, "PWD="+sandbox.TargetWorktree)
	if !slices.Equal(r.Environ, want) {
		t.Errorf("environment inside =\n%q\nwant Spec.Env() =\n%q", r.Environ, want)
	}
	if r.DotEnv != "" || !strings.Contains(r.DotEnvErr, "permission denied") {
		t.Errorf("/work/.env = %q (err %q), want the /dev/null mask: EACCES on the nodev bind", r.DotEnv, r.DotEnvErr)
	}
	if len(r.AwsEntries) != 0 {
		t.Errorf("/work/.aws lists %q, want the empty tmpfs mask", r.AwsEntries)
	}
	if !strings.Contains(r.WriteRoot, "read-only file system") {
		t.Errorf("write outside the task roots: %q, want EROFS", r.WriteRoot)
	}
}

// hostBwrap returns the installed bwrap and its probed features, or skips.
func hostBwrap(t *testing.T) (string, Features) {
	t.Helper()
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ver, err := exec.CommandContext(ctx, bwrap, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	help, err := exec.CommandContext(ctx, bwrap, "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseFeatures(string(ver), string(help))
	if err != nil {
		t.Fatal(err)
	}
	return bwrap, f
}

// hostScope is the systemd-run wrapper this host can run, or a skip that
// says why not. Only whether a user scope starts matters here, not whether
// cgroup v2 delegates controllers: the wrapper's environment is what is
// tested.
func hostScope(t *testing.T) (sandbox.CgroupStatus, *Scope) {
	t.Helper()
	st := sandbox.ProbeCgroup(context.Background())
	switch {
	case st.SystemdRun == "":
		t.Skip("systemd-run --user is unavailable here: systemd-run not found")
	case st.EnvErr != nil:
		t.Skipf("systemd-run --user is unavailable here: %v", st.EnvErr)
	case st.ScopeErr != nil:
		t.Skipf("systemd-run --user is unavailable here: a probe scope did not start: %v", st.ScopeErr)
	}
	return st, &Scope{SystemdRun: st.SystemdRun, EnvBin: "/usr/bin/env", Env: st.Env}
}

// TestBwrap_RunsTheBuiltArgvInScope repeats every assertion of
// TestBwrap_RunsTheBuiltArgv on the cgroup path, the argv started under
// "systemd-run --user --scope ... -- env -i bwrap" (A06 §11.6). It exists
// for the M2-13 "must hold": with --scope, systemd-run execs its command
// in place, so without the wrapper owning the environment bwrap's PID 1
// would carry systemd-run's environment (and with the default, the whole
// daemon's), readable at /proc/1/environ (INV-C, CWE-214/526). The parent
// here holds a fake daemon variable, and the user manager's bus variables
// reach systemd-run, yet /proc/1/environ inside must be empty. Skips, with
// the reason, where systemd-run --user cannot start a scope (WSL2 with
// systemd=true runs one; the CI job enables a user manager for it).
func TestBwrap_RunsTheBuiltArgvInScope(t *testing.T) {
	bwrap, f := hostBwrap(t)
	_, scope := hostScope(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DAEMON_SECRET", "fake-not-a-secret")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	runProbe(t, ctx, bwrap, self, f, scope)
}

// helperMarker starts a profiles file that turns the probe into the
// stand-in executor of the manager tests: {"warden_test_executor": mode}
// with mode "serve" (answer exec.hello and exec.shutdown), "mismatch"
// (report a wrong profiles digest) or "exit" (exit right after the hello).
// Until M2-10 builds warden-exec, this is the only process a test sandbox
// runs: a copy of this test binary, never anything agent-requested.
const helperMarker = `{"warden_test_executor":`

func serveMain(profiles []byte) int {
	var cfg struct {
		Mode string `json:"warden_test_executor"`
	}
	if err := json.Unmarshal(profiles, &cfg); err != nil {
		return 73
	}
	sum := sha256.Sum256(profiles)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if cfg.Mode == "mismatch" {
		digest = "sha256:" + strings.Repeat("0", 64)
	}
	var mode execproto.Mode
	var roots []execproto.HelloRoot
	for i := 0; i+1 < len(os.Args); i++ {
		switch os.Args[i] {
		case "--mode":
			mode = execproto.Mode(os.Args[i+1])
		case "--roots":
			for _, r := range strings.Split(os.Args[i+1], ",") {
				name, rest, _ := strings.Cut(r, "=")
				p, m, _ := strings.Cut(rest, ":")
				roots = append(roots, execproto.HelloRoot{Name: name, Path: p, Mode: execproto.RootMode(m)})
			}
		}
	}
	rpc := os.NewFile(FdRPC, "rpc")
	r, w := execproto.NewFrameReader(rpc), execproto.NewFrameWriter(rpc)
	for {
		b, err := r.ReadFrame()
		if err != nil {
			return 0 // EOF: the daemon closed the channel (A06 §7.8)
		}
		m, err := execproto.DecodeMessage(b)
		if err != nil {
			return 74
		}
		var result any
		switch m.Method {
		case execproto.MethodHello:
			result = execproto.HelloResult{Protocol: execproto.Protocol, ExecutorVersion: "stand-in", OS: "linux", Arch: runtime.GOARCH,
				Mode: mode, Roots: roots, ProfilesDigest: digest, PID: os.Getpid(), UID: os.Getuid()}
		case execproto.MethodShutdown:
			result = execproto.ShutdownResult{}
		default:
			return 75
		}
		res, err := json.Marshal(result)
		if err != nil {
			return 76
		}
		out, err := json.Marshal(execproto.Message{JSONRPC: "2.0", ID: m.ID, Result: res})
		if err != nil {
			return 77
		}
		if err := w.WriteFrame(out); err != nil {
			return 78
		}
		if m.Method == execproto.MethodShutdown || (m.Method == execproto.MethodHello && cfg.Mode == "exit") {
			return 0
		}
	}
}
