//go:build !windows

// The argv tests build Specs from slash host paths, which is what L1 runs
// on (Linux; the builder is the same code on macOS CI). On Windows a slash
// path is not absolute and the Spec is refused before any argv exists.

package linux_bwrap

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/model/goldentest"
	"warden.dev/warden/internal/sandbox"
)

const (
	testBase   = "/srv/warden-test"
	testID     = "sb_01JAXS2Q7M2V9KTC3F6YH5N0PB"
	testUlid   = "01jaxr8q7m2v9ktc3f6yh5n0pb"
	testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testCommit = "3f9c2e1a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e"
)

// testSpec is the A06 §2 layout of one implement task with two toolchains
// (one under the user's home, as nvm installs it) and three worktree masks
// (two files, one directory).
func testSpec() *sandbox.Spec {
	hostHome := testBase + "/home/ana"
	home := hostHome + "/.warden"
	session := home + "/sessions/" + testUlid
	return &sandbox.Spec{
		ID:             testID,
		SessionID:      "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB",
		TaskKey:        "implement",
		Level:          sandbox.LevelL1,
		Purpose:        sandbox.PurposeTask,
		Mode:           execproto.ModeTool,
		HostHome:       hostHome,
		WardenHome:     home,
		SessionDir:     session,
		Worktree:       session + "/worktree",
		Scratch:        session + "/scratch/implement",
		Cache:          home + "/cache/wsp_01jaxr",
		GitDir:         session + "/git",
		MainObjects:    hostHome + "/project/.git/objects",
		ExecBin:        testBase + "/opt/warden/bin/warden-exec",
		ProfilesFile:   session + "/sandbox/" + testID + "/profiles.json",
		ProfilesDigest: testDigest,
		Toolchains: []sandbox.Toolchain{
			{Root: "/opt/node-v22", BinDirs: []string{"/opt/node-v22/bin"}, Masks: []string{"/opt/node-v22/etc/npmrc"}},
			{Root: hostHome + "/.nvm/versions/node/v20", BinDirs: []string{hostHome + "/.nvm/versions/node/v20/bin"}},
		},
		WorktreeMasks: []sandbox.Mask{
			{RelPath: ".env", Pattern: "**/.env"},
			{RelPath: ".aws", Dir: true, Pattern: "**/.aws/**"},
			{RelPath: "deploy/id_rsa", Pattern: "**/id_rsa"},
		},
		Proxy:         sandbox.ProxyEndpoint{Socket: session + "/scratch/px-1.sock"},
		Limits:        sandbox.DefaultLimits(),
		SessionBranch: "warden/" + testUlid,
		BaseCommit:    testCommit,
	}
}

var (
	features090 = Features{Version: "0.9.0", AddSeccompFD: true, Size: true, DisableUserns: true}
	features061 = Features{Version: "0.6.1", AddSeccompFD: true}
	mergedUsr   = HostLayout{Linked: map[string]bool{"bin": true, "sbin": true, "lib": true, "lib64": true}, SSLPrivate: true}
	splitUsr    = HostLayout{Linked: map[string]bool{}}
	cgroupExec  = ExecFlags{CPUSeconds: 2400, Cgroup: true, MaxProcs: 8}
	rlimitExec  = ExecFlags{CPUSeconds: 2400, Cgroup: false, Nproc: 812, MaxProcs: 8}
)

// identity resolves every toolchain root to itself.
func identity(s *sandbox.Spec) []string {
	var out []string
	for _, tc := range s.Toolchains {
		out = append(out, tc.Root)
	}
	return out
}

func mustBuild(t *testing.T, in buildInput) *Plan {
	t.Helper()
	if in.ToolchainSources == nil {
		in.ToolchainSources = identity(in.Spec)
	}
	p, err := build(in)
	if err != nil {
		t.Fatalf("build() = %v", err)
	}
	return p
}

// op is one bwrap option with its arguments.
type op struct {
	flag string
	args []string
}

// arity is every bwrap option the builder may emit, with its argument
// count. An option outside this table fails the parse, so an added flag
// (say --bind-try or --dev-bind) cannot slip past the tests below.
var arity = map[string]int{
	"--unshare-all": 0, "--unshare-user": 0, "--disable-userns": 0, "--die-with-parent": 0,
	"--new-session": 0, "--clearenv": 0,
	"--cap-drop": 1, "--hostname": 1, "--info-fd": 1, "--proc": 1, "--dev": 1, "--tmpfs": 1,
	"--dir": 1, "--remount-ro": 1, "--chdir": 1, "--add-seccomp-fd": 1, "--seccomp": 1, "--size": 1,
	"--bind": 2, "--ro-bind": 2, "--ro-bind-try": 2, "--symlink": 2, "--setenv": 2,
}

// parseArgv splits a bwrap argv into options and the command after "--".
func parseArgv(t *testing.T, argv []string) ([]op, []string) {
	t.Helper()
	if len(argv) == 0 || argv[0] != "bwrap" {
		t.Fatalf("argv does not start with bwrap: %q", argv)
	}
	var ops []op
	for i := 1; i < len(argv); {
		if argv[i] == "--" {
			return ops, argv[i+1:]
		}
		n, ok := arity[argv[i]]
		if !ok {
			t.Fatalf("argv[%d] = %q is not an option the A06 §11.1 argv uses", i, argv[i])
		}
		if i+n >= len(argv) {
			t.Fatalf("option %q at %d is missing arguments", argv[i], i)
		}
		ops = append(ops, op{argv[i], argv[i+1 : i+1+n]})
		i += 1 + n
	}
	t.Fatalf("argv has no \"--\" before the command: %q", argv)
	return nil, nil
}

func hasOp(ops []op, flag string, args ...string) bool {
	return slices.ContainsFunc(ops, func(o op) bool { return o.flag == flag && slices.Equal(o.args, args) })
}

func countFlag(ops []op, flag string) int {
	n := 0
	for _, o := range ops {
		if o.flag == flag {
			n++
		}
	}
	return n
}

// TestBuild_Golden pins the whole argv and fd plan to A06 §11.1 for the two
// hosts that matter: Ubuntu 24.04 (bwrap 0.9.0, merged /usr, systemd
// delegation) and Ubuntu 22.04 (bwrap 0.6.1: no --size, no
// --disable-userns), here with a split /usr and the rlimit fallback. Every
// backend-added mount is in these files; a change to one is a change to the
// sandbox's view and is reviewed against A06 §11.1 as a diff.
// UPDATE_GOLDEN=1 go test ./internal/sandbox/linux_bwrap rewrites them.
func TestBuild_Golden(t *testing.T) {
	cases := []struct {
		name string
		in   buildInput
	}{
		{"bwrap-0.9.0-merged-usr-cgroup", buildInput{Spec: testSpec(), Features: features090, Host: mergedUsr, Exec: cgroupExec}},
		{"bwrap-0.6.1-split-usr-rlimits", buildInput{Spec: testSpec(), Features: features061, Host: splitUsr, Exec: rlimitExec}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			goldentest.JSON(t, filepath.Join("testdata", "golden", c.name+".json"), mustBuild(t, c.in))
		})
	}
}

// TestBuild_HostPathsOnlyFromSpecAndSystem is the INV-B check on the argv:
// every host path bwrap is told to bind is a source of Spec.Mounts() (the
// toolchain roots as resolved), a generated /etc file of this sandbox,
// /dev/null for a mask, or one of the A06 §11.1 system paths, and each is
// bound at the target A06 gives it. Nothing else from the host exists in
// the sandbox (A06 §2 "Nothing else from the host").
func TestBuild_HostPathsOnlyFromSpecAndSystem(t *testing.T) {
	s := testSpec()
	ms, err := s.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	// source -> the one target it may be bound at
	allowed := map[string][]string{"/dev/null": nil}
	for _, m := range ms {
		if m.Source != "" {
			allowed[m.Source] = append(allowed[m.Source], m.Target)
		}
	}
	etc := path.Join(s.SessionDir, "sandbox", s.ID, "etc")
	for _, f := range []string{"passwd", "group", "hosts", "resolv.conf", "nsswitch.conf"} {
		allowed[path.Join(etc, f)] = []string{"/etc/" + f}
	}
	for _, p := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/lib32", "/libx32",
		"/etc/ssl", "/etc/pki", "/etc/ca-certificates", "/etc/crypto-policies", "/etc/alternatives",
		"/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d", "/etc/os-release"} {
		allowed[p] = []string{p}
	}
	for _, host := range []HostLayout{mergedUsr, splitUsr} {
		ops, _ := parseArgv(t, mustBuild(t, buildInput{Spec: s, Features: features090, Host: host, Exec: cgroupExec}).Argv)
		for _, o := range ops {
			switch o.flag {
			case "--bind", "--ro-bind", "--ro-bind-try":
				targets, ok := allowed[o.args[0]]
				if !ok {
					t.Errorf("%s %q %q: host path not from the Spec or the A06 §11.1 system list (INV-B)", o.flag, o.args[0], o.args[1])
					continue
				}
				if o.args[0] != "/dev/null" && !slices.Contains(targets, o.args[1]) {
					t.Errorf("%s %q %q: bound at %q, want one of %q", o.flag, o.args[0], o.args[1], o.args[1], targets)
				}
			case "--symlink":
				// A symlink carries no host data; its target is a fixed
				// usr/<dir> name inside the sandbox.
				if want := "usr" + o.args[1]; o.args[0] != want {
					t.Errorf("--symlink %q %q, want %q", o.args[0], o.args[1], want)
				}
			}
		}
	}
}

// TestBuild_EveryMountFromSpecOnceAndInOrder: the Spec's mounts are
// rendered, not re-derived. Each appears exactly once with the flag its
// mode needs, and each mask comes after the mount it hides (a mask before
// its parent would be covered by the parent bind and hide nothing).
func TestBuild_EveryMountFromSpecOnceAndInOrder(t *testing.T) {
	s := testSpec()
	ms, err := s.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	ops, _ := parseArgv(t, mustBuild(t, buildInput{Spec: s, Features: features090, Host: mergedUsr, Exec: cgroupExec}).Argv)
	index := func(flag string, args ...string) []int {
		var at []int
		for i, o := range ops {
			if o.flag == flag && slices.Equal(o.args, args) {
				at = append(at, i)
			}
		}
		return at
	}
	parentAt := -1
	for _, m := range ms {
		var at []int
		switch {
		case m.Kind == sandbox.KindMask && m.Dir:
			at = index("--tmpfs", m.Target)
			if ro := index("--remount-ro", m.Target); len(ro) != 1 || len(at) != 1 || ro[0] <= at[0] {
				t.Errorf("directory mask %q: tmpfs at %v, remount-ro at %v; want one tmpfs then one remount-ro", m.Target, at, ro)
			}
		case m.Kind == sandbox.KindMask:
			at = index("--ro-bind", "/dev/null", m.Target)
		case m.Mode == sandbox.ModeRO:
			at = index("--ro-bind", m.Source, m.Target)
		default:
			at = index("--bind", m.Source, m.Target)
		}
		if len(at) != 1 {
			t.Errorf("%s mount %q -> %q appears %d times, want once", m.Kind, m.Source, m.Target, len(at))
			continue
		}
		if m.Kind == sandbox.KindMask {
			if at[0] <= parentAt {
				t.Errorf("mask %q at %d is not after its parent mount at %d", m.Target, at[0], parentAt)
			}
		} else {
			parentAt = at[0]
		}
	}
	// No bind or tmpfs targets a task-root path that the Spec did not ask
	// for: the worktree masks are exactly the Spec's.
	var wtMasks []string
	for _, o := range ops {
		if (o.flag == "--ro-bind" && o.args[0] == "/dev/null" || o.flag == "--tmpfs") && strings.HasPrefix(o.args[len(o.args)-1], sandbox.TargetWorktree+"/") {
			wtMasks = append(wtMasks, o.args[len(o.args)-1])
		}
	}
	var want []string
	for _, m := range s.WorktreeMasks {
		want = append(want, path.Join(sandbox.TargetWorktree, m.RelPath))
	}
	if !slices.Equal(wtMasks, want) {
		t.Errorf("worktree masks in argv = %q, want %q", wtMasks, want)
	}
}

// TestBuild_EnvOnlyFromSpec is the INV-C check on the argv: the
// environment is cleared and then holds exactly Spec.Env(), in order, and
// no daemon variable reaches any argv element (the builder reads no
// environment).
func TestBuild_EnvOnlyFromSpec(t *testing.T) {
	const sentinel = "sk-ant-WARDEN-TEST-SENTINEL-not-a-key"
	t.Setenv("ANTHROPIC_API_KEY", sentinel)
	t.Setenv("SSH_AUTH_SOCK", "/run/user/1000/"+sentinel)
	t.Setenv("HOME", "/home/"+sentinel)
	s := testSpec()
	env, err := s.Env()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []buildInput{
		{Spec: s, Features: features090, Host: mergedUsr, Exec: cgroupExec},
		{Spec: s, Features: features061, Host: splitUsr, Exec: rlimitExec},
	} {
		argv := mustBuild(t, in).Argv
		for i, a := range argv {
			if strings.Contains(a, sentinel) {
				t.Errorf("argv[%d] = %q carries a daemon environment value (INV-C)", i, a)
			}
		}
		ops, _ := parseArgv(t, argv)
		var got []sandbox.EnvVar
		clearAt, firstSet := -1, -1
		for i, o := range ops {
			switch o.flag {
			case "--clearenv":
				clearAt = i
			case "--setenv":
				if firstSet < 0 {
					firstSet = i
				}
				got = append(got, sandbox.EnvVar{Key: o.args[0], Value: o.args[1]})
			}
		}
		if countFlag(ops, "--clearenv") != 1 || clearAt > firstSet {
			t.Errorf("--clearenv at %d (count %d), first --setenv at %d; want one --clearenv before every --setenv", clearAt, countFlag(ops, "--clearenv"), firstSet)
		}
		if !slices.Equal(got, env) {
			t.Errorf("--setenv pairs = %v\nwant Spec.Env() = %v", got, env)
		}
	}
}

// TestBuild_Hardening checks the A06 §11.1 namespace and process flags
// that no mount test sees. Each guards a named escape: --die-with-parent
// (a daemon crash leaves no sandbox running, S-8), --new-session (no
// TIOCSTI into the daemon's terminal), --unshare-all (network, pid, ipc,
// uts, cgroup) with --unshare-user (bwrap refuses --disable-userns
// without it; checked against the bwrap 0.8 source), --disable-userns
// when available (unshare -r, escape-check row), --cap-drop ALL, and
// --remount-ro / as the last filesystem operation (writes outside the
// task roots fail with EROFS, escape-check row 4).
func TestBuild_Hardening(t *testing.T) {
	for _, c := range []struct {
		f       Features
		disable bool
	}{{features090, true}, {features061, false}} {
		ops, cmd := parseArgv(t, mustBuild(t, buildInput{Spec: testSpec(), Features: c.f, Host: mergedUsr, Exec: cgroupExec}).Argv)
		for _, flag := range []string{"--unshare-all", "--unshare-user", "--die-with-parent", "--new-session", "--clearenv"} {
			if countFlag(ops, flag) != 1 {
				t.Errorf("bwrap %s: %s appears %d times, want once", c.f.Version, flag, countFlag(ops, flag))
			}
		}
		if hasOp(ops, "--disable-userns") != c.disable {
			t.Errorf("bwrap %s: --disable-userns present = %v, want %v", c.f.Version, !c.disable, c.disable)
		}
		if !hasOp(ops, "--cap-drop", "ALL") || !hasOp(ops, "--hostname", "warden") || !hasOp(ops, "--chdir", sandbox.TargetWorktree) {
			t.Errorf("bwrap %s: missing --cap-drop ALL, --hostname warden or --chdir /work", c.f.Version)
		}
		if hasOp(ops, "--as-pid-1") {
			t.Errorf("--as-pid-1 is not used (A06 §11.1)")
		}
		last := -1
		for i, o := range ops {
			switch o.flag {
			case "--bind", "--ro-bind", "--ro-bind-try", "--symlink", "--tmpfs", "--proc", "--dev", "--dir", "--remount-ro":
				last = i
			}
		}
		if last < 0 || ops[last].flag != "--remount-ro" || ops[last].args[0] != "/" {
			t.Errorf("bwrap %s: last filesystem operation is %v, want --remount-ro /", c.f.Version, ops[last])
		}
		if len(cmd) == 0 || cmd[0] != sandbox.TargetExecBin {
			t.Errorf("command = %q, want %s first", cmd, sandbox.TargetExecBin)
		}
	}
}

// TestBuild_SeccompFlag: the program on fd 5 is installed exactly once,
// with --add-seccomp-fd when bwrap has it and the deprecated --seccomp
// otherwise (A06 §11.2 table). Passing the wrong one makes bwrap refuse to
// start, or on a bwrap without --add-seccomp-fd, run with no filter at all
// if the flag were dropped.
func TestBuild_SeccompFlag(t *testing.T) {
	for _, c := range []struct {
		addFD      bool
		want, not_ string
	}{{true, "--add-seccomp-fd", "--seccomp"}, {false, "--seccomp", "--add-seccomp-fd"}} {
		f := features090
		f.AddSeccompFD = c.addFD
		p := mustBuild(t, buildInput{Spec: testSpec(), Features: f, Host: mergedUsr, Exec: cgroupExec})
		ops, _ := parseArgv(t, p.Argv)
		if !hasOp(ops, c.want, fmt.Sprint(FdSeccomp)) || countFlag(ops, c.want) != 1 || countFlag(ops, c.not_) != 0 {
			t.Errorf("AddSeccompFD=%v: want exactly one %s %d and no %s", c.addFD, c.want, FdSeccomp, c.not_)
		}
		if !c.addFD && !slices.Contains(p.Missing, FlagAddSeccompFD) {
			t.Errorf("Missing = %q, want %s recorded", p.Missing, FlagAddSeccompFD)
		}
	}
}

// TestBuild_TmpfsSizes: /tmp is the Spec's tmp limit and /dev/shm 256 MiB
// when bwrap has --size (A06 §11.1, §12); without it both are mounted
// without a size and --size is recorded as missing.
func TestBuild_TmpfsSizes(t *testing.T) {
	s := testSpec()
	s.Limits.TmpMB = 512
	ops, _ := parseArgv(t, mustBuild(t, buildInput{Spec: s, Features: features090, Host: mergedUsr, Exec: cgroupExec}).Argv)
	for i, o := range ops {
		if o.flag == "--tmpfs" && (o.args[0] == "/tmp" || o.args[0] == "/dev/shm") {
			want := map[string]string{"/tmp": "536870912", "/dev/shm": "268435456"}[o.args[0]]
			if i == 0 || ops[i-1].flag != "--size" || ops[i-1].args[0] != want {
				t.Errorf("--tmpfs %s not preceded by --size %s", o.args[0], want)
			}
		}
	}
	p := mustBuild(t, buildInput{Spec: s, Features: features061, Host: mergedUsr, Exec: cgroupExec})
	ops, _ = parseArgv(t, p.Argv)
	if countFlag(ops, "--size") != 0 || !slices.Contains(p.Missing, FlagSize) {
		t.Errorf("bwrap 0.6.1: --size count %d, Missing %q; want none and recorded", countFlag(ops, "--size"), p.Missing)
	}
}

// TestBuild_ExecutorCommand pins the warden-exec command line to A06 §3.1
// and §7.3: tool mode on fd 3, the Spec's branch and base, and the
// per-child rlimits, with RLIMIT_DATA and RLIMIT_NPROC only when no cgroup
// enforces memory and pids.
func TestBuild_ExecutorCommand(t *testing.T) {
	s := testSpec()
	_, cmd := parseArgv(t, mustBuild(t, buildInput{Spec: s, Features: features090, Host: mergedUsr, Exec: cgroupExec}).Argv)
	want := []string{
		"/warden/bin/warden-exec", "serve", "--protocol", "warden.exec/1", "--mode", "tool", "--rpc-fd", "3",
		"--roots", "worktree=/work:rw,scratch=/scratch:rw,cache=/cache:rw,git=/git:rw",
		"--allow-profiles", "/warden/etc/profiles.json", "--git-dir", "/git",
		"--session-branch", s.SessionBranch, "--base-commit", s.BaseCommit, "--hooks-dir", "/warden/empty",
		"--proxy", "/run/warden/proxy.sock", "--proxy-listen", "127.0.0.1:3128",
		"--child-limits", "cpu=2400,fsize=1073741824,nofile=4096,core=0", "--max-procs", "8",
	}
	if !slices.Equal(cmd, want) {
		t.Errorf("command =\n%q\nwant\n%q", cmd, want)
	}
	_, cmd = parseArgv(t, mustBuild(t, buildInput{Spec: s, Features: features090, Host: mergedUsr, Exec: rlimitExec}).Argv)
	if i := slices.Index(cmd, "--child-limits"); i < 0 || cmd[i+1] != "cpu=2400,fsize=1073741824,nofile=4096,core=0,data=3221225472,nproc=812" {
		t.Errorf("rlimit fallback command = %q", cmd)
	}
}

func TestBuild_RefusesBadInput(t *testing.T) {
	cases := []struct {
		name string
		edit func(in *buildInput)
	}{
		{"invalid spec", func(in *buildInput) { in.Spec.Worktree = "/etc" }},
		{"L2 spec", func(in *buildInput) {
			in.Spec.Level = sandbox.LevelL2
			in.Spec.Toolchains = nil
			in.Spec.Proxy = sandbox.ProxyEndpoint{Sidecar: "proxy:3128"}
		}},
		{"toolchain sources do not match", func(in *buildInput) { in.ToolchainSources = in.ToolchainSources[:1] }},
		{"no cpu limit", func(in *buildInput) { in.Exec.CPUSeconds = 0 }},
		{"no max procs", func(in *buildInput) { in.Exec.MaxProcs = 0 }},
		{"no nproc without a cgroup", func(in *buildInput) { in.Exec.Cgroup, in.Exec.Nproc = false, 0 }},
		{"bwrap below the minimum", func(in *buildInput) { in.Features.Version = "0.5.0" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := testSpec()
			in := buildInput{Spec: s, Features: features090, Host: mergedUsr, Exec: cgroupExec, ToolchainSources: identity(s)}
			c.edit(&in)
			if p, err := build(in); err == nil {
				t.Fatalf("build() = %q, want an error", p.Argv)
			}
		})
	}
}

// TestBuild_NixStoreOnlyForNixToolchains: /nix/store is bound whole only
// when a toolchain resolves into it (A06 §11.1), since a store path's
// dependencies are other store paths.
func TestBuild_NixStoreOnlyForNixToolchains(t *testing.T) {
	s := testSpec()
	ops, _ := parseArgv(t, mustBuild(t, buildInput{Spec: s, Features: features090, Host: mergedUsr, Exec: cgroupExec}).Argv)
	if hasOp(ops, "--ro-bind-try", "/nix/store", "/nix/store") {
		t.Error("/nix/store bound without a Nix toolchain")
	}
	src := identity(s)
	src[1] = "/nix/store/abc-nodejs-20"
	ops, _ = parseArgv(t, mustBuild(t, buildInput{Spec: s, Features: features090, Host: mergedUsr, Exec: cgroupExec, ToolchainSources: src}).Argv)
	if !hasOp(ops, "--ro-bind-try", "/nix/store", "/nix/store") {
		t.Error("/nix/store not bound for a toolchain in the store")
	}
	if !hasOp(ops, "--ro-bind", src[1], s.Toolchains[1].Root) {
		t.Errorf("toolchain not bound from its resolved source %q at %q", src[1], s.Toolchains[1].Root)
	}
}

func TestPlan_FdPlanMatchesArgv(t *testing.T) {
	p := mustBuild(t, buildInput{Spec: testSpec(), Features: features090, Host: mergedUsr, Exec: cgroupExec})
	want := []Fd{{3, "rpc", "warden-exec"}, {4, "harness_stdio", "warden-exec"}, {5, "seccomp", "bwrap"}, {6, "info", "bwrap"}}
	if !slices.Equal(p.Fds, want) {
		t.Errorf("Fds = %v, want %v", p.Fds, want)
	}
	ops, cmd := parseArgv(t, p.Argv)
	if !hasOp(ops, "--info-fd", "6") || !hasOp(ops, "--add-seccomp-fd", "5") {
		t.Error("argv does not use fd 5 for seccomp and fd 6 for info")
	}
	if i := slices.Index(cmd, "--rpc-fd"); i < 0 || cmd[i+1] != "3" {
		t.Errorf("executor does not read RPC on fd 3: %q", cmd)
	}
	if _, err := p.Command(context.Background(), "/usr/bin/bwrap", nil, nil, nil, nil); err == nil {
		t.Error("Command accepted missing files")
	}
}

// TestPlan_CommandOwnsTheEnvironment exists for the M2-13 review finding
// (INV-C; CWE-214, CWE-526): --clearenv clears only what bwrap passes to
// the command. bwrap's own PID 1 in the namespace keeps the environment it
// was started with, and /proc/1/environ is readable by the sandboxed
// process (same uid). An exec.Cmd with Env == nil inherits the daemon's
// environment, so every daemon variable, an exported ANTHROPIC_API_KEY
// included, was readable inside. The Plan builds the Cmd and gives bwrap
// an empty environment; it is started by absolute path, so it needs no
// PATH.
func TestPlan_CommandOwnsTheEnvironment(t *testing.T) {
	t.Setenv("FAKE_DAEMON_SECRET", "fake-not-a-secret")
	p := mustBuild(t, buildInput{Spec: testSpec(), Features: features090, Host: mergedUsr, Exec: cgroupExec})
	var files []*os.File
	for range 2 {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		files = append(files, r, w)
	}
	cmd, err := p.Command(context.Background(), "/usr/bin/bwrap", files[0], files[1], files[2], files[3])
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Env == nil || len(cmd.Env) != 0 {
		t.Errorf("Cmd.Env = %#v, want a non-nil empty slice (nil inherits the daemon's environment)", cmd.Env)
	}
	if cmd.Path != "/usr/bin/bwrap" || !slices.Equal(cmd.Args[1:], p.Argv[1:]) {
		t.Errorf("Cmd = %q %q, want /usr/bin/bwrap with the plan's argv", cmd.Path, cmd.Args)
	}
	if !slices.Equal(cmd.ExtraFiles, files) {
		t.Errorf("Cmd.ExtraFiles = %v, want the four files as fds 3-6", cmd.ExtraFiles)
	}
	if _, err := p.Command(context.Background(), "bwrap", files[0], files[1], files[2], files[3]); err == nil {
		t.Error("Command accepted a relative bwrap path (it would be looked up in the daemon's PATH)")
	}
}

// TestBuild_MasksSSLPrivate exists for the M2-13 review finding (CWE-250):
// /etc/ssl/private is inside the /etc/ssl bind, and the sandboxed process
// keeps the user's supplementary groups, so a member of ssl-cert (Debian:
// keys 0640 root:ssl-cert) could read TLS private keys. When the host has
// the directory it is covered by an empty read-only tmpfs right after the
// /etc/ssl bind.
func TestBuild_MasksSSLPrivate(t *testing.T) {
	for _, private := range []bool{true, false} {
		host := HostLayout{Linked: mergedUsr.Linked, SSLPrivate: private}
		ops, _ := parseArgv(t, mustBuild(t, buildInput{Spec: testSpec(), Features: features090, Host: host, Exec: cgroupExec}).Argv)
		i := slices.IndexFunc(ops, func(o op) bool { return o.flag == "--ro-bind-try" && o.args[0] == "/etc/ssl" })
		masked := i >= 0 && i+2 < len(ops) && hasOp(ops[i+1:i+2], "--tmpfs", "/etc/ssl/private") && hasOp(ops[i+2:i+3], "--remount-ro", "/etc/ssl/private")
		if masked != private {
			t.Errorf("SSLPrivate=%v: /etc/ssl/private masked right after the /etc/ssl bind = %v", private, masked)
		}
		if !private && (hasOp(ops, "--tmpfs", "/etc/ssl/private") || hasOp(ops, "--remount-ro", "/etc/ssl/private")) {
			t.Error("/etc/ssl/private masked on a host without it (bwrap would create it)")
		}
	}
}
