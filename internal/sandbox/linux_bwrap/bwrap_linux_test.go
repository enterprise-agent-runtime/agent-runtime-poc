//go:build linux

package linux_bwrap

// sandboxed: runs only the fixed probes "bwrap --version" and "bwrap
// --help", and bwrap with the argv under test whose command is a copy of
// /bin/true or /bin/false placed at the executor path; never agent input.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/sandbox"
)

// realSpec lays out one session under a temp Warden home on disk, with a
// real proxy socket, and puts a copy of the given fixed binary (true or
// false) at the executor path, so bwrap runs nothing but that binary.
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

// TestBwrap_RunsTheBuiltArgv is the one integration test: the argv this
// package builds is accepted by the bwrap installed here (the WSL2 Ubuntu
// in development) and runs the executor path with the seccomp program on
// fd 5 and the info pipe on fd 6. It proves what the unit tests cannot:
// that bwrap takes --unshare-user with --disable-userns, the fd plan, the
// /dev/null and tmpfs masks and the read-only root together. The command
// is a copy of /bin/true (exit 0) and of /bin/false (exit 1, so a zero is
// not bwrap doing nothing). It also runs the fallback argv of a bwrap
// without the optional flags (--seccomp 5, no --size, no
// --disable-userns), which this bwrap still accepts.
func TestBwrap_RunsTheBuiltArgv(t *testing.T) {
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
	probed, err := ParseFeatures(string(ver), string(help))
	if err != nil {
		t.Fatal(err)
	}
	fallback := Features{Version: probed.Version}
	cases := []struct {
		name     string
		f        Features
		exe      string
		wantExit int
	}{
		{"probed " + probed.Version + " true", probed, "/bin/true", 0},
		{"probed " + probed.Version + " false", probed, "/bin/false", 1},
		{"no optional flags", fallback, "/bin/true", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := realSpec(t, c.exe)
			plan, err := Prepare(s, c.f, ExecFlags{CPUSeconds: 60, Cgroup: true, MaxProcs: 8}, DefaultProbes())
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
			files, err := plan.ExtraFiles(rpc, null, prog, infoW)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, bwrap, plan.Argv[1:]...)
			cmd.ExtraFiles = files
			cmd.Env = []string{}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err = cmd.Start()
			infoW.Close()
			if err != nil {
				t.Fatal(err)
			}
			info, _ := io.ReadAll(infoR)
			err = cmd.Wait()
			code := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != c.wantExit {
				t.Fatalf("bwrap exit %d, want %d; stderr: %s", code, c.wantExit, stderr.String())
			}
			if !strings.Contains(string(info), `"child-pid"`) {
				t.Errorf("info fd 6 = %q, want bwrap's child-pid JSON", info)
			}
		})
	}
}
