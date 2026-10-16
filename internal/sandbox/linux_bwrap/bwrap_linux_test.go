//go:build linux

package linux_bwrap

// sandboxed: runs only the fixed probes "bwrap --version" and "bwrap
// --help", and bwrap with the argv under test whose command is a copy of
// this test binary in probe mode (it reports what it sees and exits);
// never agent input.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	write(s.ProfilesFile, "{}\n", 0o600)
	for name, content := range EtcFiles(os.Getuid(), os.Getgid()) {
		write(filepath.Join(filepath.Dir(s.ProfilesFile), "etc", name), content, 0o644)
	}
	bin, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	write(s.ExecBin, string(bin), 0o755)
	l, err := net.Listen("unix", s.Proxy.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return s
}

// TestBwrap_RunsTheBuiltArgv is the integration test: the argv this
// package builds, started through Plan.Command, is accepted by the bwrap
// installed here (WSL2 Ubuntu in development) and the probe at the
// executor path sees exactly the A06 §2/§11.1 view:
//
//   - descriptors 0-4 only: fd 3 (RPC) and 4 (stdio) passed through, the
//     seccomp program (5) and the info pipe (6) consumed by bwrap;
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
			cmd, err := plan.Command(ctx, bwrap, rpc, null, prog, infoW)
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
		})
	}
}
