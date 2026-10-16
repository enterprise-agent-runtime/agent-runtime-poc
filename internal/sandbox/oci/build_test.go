package oci

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/sandbox"
	"warden.dev/warden/internal/sandbox/seccomp"
)

const (
	testID      = "sb_01JAXS2Q7M2V9KTC3F6YH5N0PB"
	testSession = "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB"
	testTask    = "implement"
	testDigest  = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testBase    = "3f9c2e1a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e"
	testSidecar = "warden-proxy-01jaxs2q:3128"
	testSandImg = "warden/sandbox-go@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testProxImg = "warden/proxy@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// hostLayout is the fixed core §10 layout of one session on this host.
// Validation and Build are pure, so the paths need not exist; they are
// fixed so the golden Plans do not depend on the machine. Windows paths can
// be validated only on Windows (sandbox.Spec uses path/filepath), so each
// OS tests its own host form: Windows on Windows, Linux everywhere else.
type hostLayout struct {
	name           string // golden file stem
	hostOS         string
	engine         Engine
	hostHome, home string
	execBin        string
	objects        string
}

func layout() hostLayout {
	if filepath.Separator == '\\' {
		return hostLayout{
			name: "windows-host", hostOS: HostWindows, engine: EngineDesktop,
			hostHome: `C:\Users\ana`, home: `C:\Users\ana\AppData\Local\Warden`,
			execBin: `C:\Program Files\Warden\linux-amd64\warden-exec`,
			objects: `C:\Users\ana\src\my project\.git\objects`,
		}
	}
	return hostLayout{
		name: "linux-host", hostOS: HostLinux, engine: EngineRootless,
		hostHome: "/home/ana", home: "/home/ana/.warden",
		execBin: "/opt/warden/lib/linux-amd64/warden-exec",
		objects: "/home/ana/src/my project/.git/objects",
	}
}

func (l hostLayout) session() string {
	return filepath.Join(l.home, "sessions", "01jaxr8q7m2v9ktc3f6yh5n0pb")
}

// validSpec is the L2 Spec the A06 §2 layout produces for one implement
// task, with a file mask, a directory mask and a nested file mask.
func validSpec(l hostLayout) *sandbox.Spec {
	s := l.session()
	return &sandbox.Spec{
		ID: testID, SessionID: testSession, TaskKey: testTask,
		Level: sandbox.LevelL2, Purpose: sandbox.PurposeTask, Mode: execproto.ModeTool,
		HostHome: l.hostHome, WardenHome: l.home, SessionDir: s,
		Worktree:       filepath.Join(s, "worktree"),
		Scratch:        filepath.Join(s, "scratch", testTask),
		Cache:          filepath.Join(l.home, "cache", "wsp_01jaxr"),
		GitDir:         filepath.Join(s, "git"),
		MainObjects:    l.objects,
		ExecBin:        l.execBin,
		ProfilesFile:   filepath.Join(s, "sandbox", testID, "profiles.json"),
		ProfilesDigest: testDigest,
		Toolchains:     []sandbox.Toolchain{{BinDirs: []string{"/usr/local/go/bin"}}},
		WorktreeMasks: []sandbox.Mask{
			{RelPath: ".env", Pattern: "**/.env"},
			{RelPath: ".aws", Dir: true, Pattern: "**/.aws/**"},
			{RelPath: "config/prod/.env", Pattern: "**/.env"},
		},
		Proxy:         sandbox.ProxyEndpoint{Sidecar: testSidecar},
		Limits:        sandbox.DefaultLimits(),
		SessionBranch: "warden/01jaxr8q7m2v9ktc3f6yh5n0pb",
		BaseCommit:    testBase,
	}
}

// fakeFS is the HostFS double: every directory case-sensitive, no 8.3
// names, every path already final, unless a field says otherwise.
type fakeFS struct {
	insensitive   map[string]bool
	shortCreation bool
	aliases       map[string]bool
	final         map[string]string
	err           error
	asked         []string
}

func (f *fakeFS) CaseSensitiveDir(dir string) (bool, error) {
	f.asked = append(f.asked, "case "+dir)
	return !f.insensitive[dir], f.err
}
func (f *fakeFS) ShortNameCreation(dir string) (bool, error) {
	f.asked = append(f.asked, "8dot3 "+dir)
	return f.shortCreation, nil
}
func (f *fakeFS) HasShortAlias(p string) (bool, error) {
	f.asked = append(f.asked, "alias "+p)
	return f.aliases[p], nil
}
func (f *fakeFS) FinalPath(p string) (string, error) {
	if r, ok := f.final[p]; ok {
		return r, nil
	}
	return p, nil
}

func validOptions(l hostLayout, fs *fakeFS) Options {
	return Options{HostOS: l.hostOS, Engine: l.engine, SandboxImage: testSandImg, ProxyImage: testProxImg, FS: fs}
}

func mustBuild(t *testing.T, s *sandbox.Spec, o Options) *Plan {
	t.Helper()
	p, err := Build(s, o)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

// checkPlan is the independent oracle for every Plan: it re-derives what
// the Plan must hold from the Spec and the contract (A06 §15, C-03, C-24,
// INV-B, INV-C, INV-I) instead of trusting Build.
func checkPlan(t *testing.T, s *sandbox.Spec, o Options, p *Plan) {
	t.Helper()
	sb := p.Sandbox

	// INV-B / C-24: the binds are exactly Spec.Mounts(), in order, each
	// host source in its mapped form; file masks are the engine's
	// /dev/null; directory masks are read-only tmpfs.
	ms, err := s.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	var wantBinds []Bind
	var wantMaskDirs []string
	for _, m := range ms {
		switch {
		case m.Kind == sandbox.KindMask && m.Dir:
			wantMaskDirs = append(wantMaskDirs, m.Target)
		case m.Kind == sandbox.KindMask:
			wantBinds = append(wantBinds, Bind{Kind: m.Kind, Source: "/dev/null", EngineSource: true, Target: m.Target, ReadOnly: true})
		default:
			src, err := MapHostPath(o.HostOS, m.Source)
			if err != nil {
				t.Fatal(err)
			}
			wantBinds = append(wantBinds, Bind{Kind: m.Kind, Source: src, Target: m.Target, ReadOnly: m.Mode == sandbox.ModeRO})
		}
	}
	if !slices.Equal(sb.Binds, wantBinds) {
		t.Errorf("sandbox binds differ from Spec.Mounts()\n got %+v\nwant %+v", sb.Binds, wantBinds)
	}
	for _, m := range s.WorktreeMasks {
		target := path.Join(sandbox.TargetWorktree, m.RelPath)
		found := false
		for _, b := range sb.Binds {
			found = found || (!m.Dir && b.Target == target && b.Source == "/dev/null" && b.ReadOnly)
		}
		for _, tm := range sb.Tmpfs {
			found = found || (m.Dir && tm.Target == target && strings.HasPrefix(tm.Options, "ro,"))
		}
		if !found {
			t.Errorf("mask %q is not in the Plan (INV-I)", m.RelPath)
		}
	}
	var gotTmpfs []string
	for _, tm := range sb.Tmpfs {
		gotTmpfs = append(gotTmpfs, tm.Target)
		if tm.Target != sandbox.TargetTmp && !strings.HasPrefix(tm.Options, "ro,") {
			t.Errorf("tmpfs %s is writable (%s)", tm.Target, tm.Options)
		}
	}
	wantTmpfs := append([]string{sandbox.TargetTmp, sandbox.TargetHooksDir}, wantMaskDirs...)
	if !slices.Equal(gotTmpfs, wantTmpfs) {
		t.Errorf("tmpfs = %v, want %v", gotTmpfs, wantTmpfs)
	}

	// INV-C: the environment is Spec.Env() and nothing else.
	env, err := s.Env()
	if err != nil {
		t.Fatal(err)
	}
	var wantEnv []string
	for _, e := range env {
		wantEnv = append(wantEnv, e.Key+"="+e.Value)
	}
	if !slices.Equal(sb.Env, wantEnv) {
		t.Errorf("sandbox env differs from Spec.Env()\n got %q\nwant %q", sb.Env, wantEnv)
	}
	if len(p.Sidecar.Env) != 0 {
		t.Errorf("sidecar env = %q; the sidecar gets nothing from the Spec environment", p.Sidecar.Env)
	}

	// The hardening every container gets (CLAUDE.md §4 Sandbox, C-03).
	for _, c := range []Container{sb, p.Sidecar} {
		if err := CheckImageRef(c.Image); err != nil {
			t.Errorf("%s: image %q is not pinned by digest", c.Name, c.Image)
		}
		if !slices.Equal(c.CapDrop, []string{"ALL"}) {
			t.Errorf("%s: cap_drop = %v, want [ALL]", c.Name, c.CapDrop)
		}
		if !c.ReadonlyRootfs || !c.NoNewPrivileges || c.Privileged {
			t.Errorf("%s: readonly_rootfs=%v no_new_privileges=%v privileged=%v", c.Name, c.ReadonlyRootfs, c.NoNewPrivileges, c.Privileged)
		}
		if c.Labels["warden.sandbox"] != s.ID || c.Labels["warden.session"] != s.SessionID || c.Labels["warden.home"] != platform.HomeHash(s.WardenHome) {
			t.Errorf("%s: labels %v lack warden.sandbox/warden.session/warden.home", c.Name, c.Labels)
		}
	}
	if sb.Image != o.SandboxImage || p.Sidecar.Image != o.ProxyImage {
		t.Errorf("images %q, %q; want %q, %q", sb.Image, p.Sidecar.Image, o.SandboxImage, o.ProxyImage)
	}
	switch o.Engine {
	case EngineRootless:
		if sb.User != "0:0" {
			t.Errorf("rootless user = %q, want 0:0 (the host user, A06 §15)", sb.User)
		}
	case EngineDesktop:
		if sb.User == "" || sb.User == "0:0" || strings.HasPrefix(sb.User, "0:") || p.Sidecar.User == "" || strings.HasPrefix(p.Sidecar.User, "0:") {
			t.Errorf("Docker Desktop users %q, %q; want non-root (C-03)", sb.User, p.Sidecar.User)
		}
	}

	// Seccomp: the M2-14 Docker profile, at S/sandbox/<sb_id>/seccomp.json.
	prof, err := seccomp.DockerProfile()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(prof)
	wantPath := filepath.Join(s.SessionDir, "sandbox", s.ID, "seccomp.json")
	if p.Seccomp.Path != wantPath || !bytes.Equal(p.Seccomp.Bytes, prof) || p.Seccomp.SHA256 != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("seccomp artefact = %s %s (%d bytes); want %s and DockerProfile()", p.Seccomp.Path, p.Seccomp.SHA256, len(p.Seccomp.Bytes), wantPath)
	}
	if sb.Seccomp != wantPath {
		t.Errorf("sandbox seccomp = %q, want %q", sb.Seccomp, wantPath)
	}

	// Resources (A06 §12).
	l := s.Limits
	const mib = 1 << 20
	r := sb.Resources
	if r.MemoryBytes != int64(l.MemoryMB)*mib || r.MemorySwapBytes != r.MemoryBytes || r.PidsLimit != int64(l.Pids) || r.NanoCPUs != int64(l.CPUs)*1e9 {
		t.Errorf("resources = %+v for limits %+v", r, l)
	}
	wantU := []Ulimit{{"nofile", 4096, 4096}, {"fsize", int64(l.FsizeMB) * mib, int64(l.FsizeMB) * mib}, {"core", 0, 0}}
	if !slices.Equal(r.Ulimits, wantU) {
		t.Errorf("ulimits = %v, want %v", r.Ulimits, wantU)
	}

	// C-03: the sandbox is on the internal network only; the sidecar is on
	// it, reachable under the Spec's sidecar host, and on the egress
	// network, the only way out.
	host := strings.Split(s.Proxy.Sidecar, ":")[0]
	if !p.Internal.Internal || p.Egress.Internal || p.Internal.Name == p.Egress.Name || p.Internal.Name == "" || p.Egress.Name == "" {
		t.Errorf("networks internal=%+v egress=%+v", p.Internal, p.Egress)
	}
	if !slices.EqualFunc(sb.Networks, []Attachment{{Network: p.Internal.Name}}, func(a, b Attachment) bool {
		return a.Network == b.Network && slices.Equal(a.Aliases, b.Aliases)
	}) {
		t.Errorf("sandbox networks = %+v, want the internal network only", sb.Networks)
	}
	var onInternal, onEgress bool
	for _, a := range p.Sidecar.Networks {
		onInternal = onInternal || (a.Network == p.Internal.Name && slices.Contains(a.Aliases, host))
		onEgress = onEgress || a.Network == p.Egress.Name
	}
	if !onInternal || !onEgress || len(p.Sidecar.Networks) != 2 {
		t.Errorf("sidecar networks = %+v; want internal (alias %s) and egress", p.Sidecar.Networks, host)
	}
	if p.Sidecar.Hostname != host {
		t.Errorf("sidecar hostname = %q, want %q", p.Sidecar.Hostname, host)
	}
	for _, n := range []Network{p.Internal, p.Egress} {
		if n.Labels["warden.sandbox"] != s.ID || n.Labels["warden.session"] != s.SessionID || n.Labels["warden.home"] != platform.HomeHash(s.WardenHome) {
			t.Errorf("network %s labels %v", n.Name, n.Labels)
		}
	}

	// The executor: PID 1 over container stdio (A06 §15, C-03).
	if !slices.Equal(sb.Entrypoint, []string{sandbox.TargetExecBin}) || len(sb.Cmd) < 2 || sb.Cmd[0] != "serve" ||
		!slices.Contains(sb.Cmd, "--rpc-stdio") || slices.Contains(sb.Cmd, "--rpc-fd") || slices.Contains(sb.Cmd, "--proxy") {
		t.Errorf("entrypoint %q cmd %q", sb.Entrypoint, sb.Cmd)
	}
	if !sb.OpenStdin || !sb.StdinOnce || !sb.AutoRemove || sb.WorkingDir != sandbox.TargetWorktree {
		t.Errorf("stdio/workdir: open_stdin=%v stdin_once=%v auto_remove=%v workdir=%q", sb.OpenStdin, sb.StdinOnce, sb.AutoRemove, sb.WorkingDir)
	}
}

func TestBuild_PlanHoldsTheContract(t *testing.T) {
	l := layout()
	s := validSpec(l)
	o := validOptions(l, &fakeFS{})
	checkPlan(t, s, o, mustBuild(t, s, o))

	s.MainObjects = "" // A14 fetched object mode
	s.WorktreeMasks = nil
	checkPlan(t, s, o, mustBuild(t, s, o))
}

// The worktree checks of PROGRESS M2-12 "must fix before the L2 backend
// merges": the builder asks about the worktree and every directory on the
// way to a mask, and about every element from the worktree down to each
// masked entry.
func TestBuild_AsksAboutEveryMaskPath(t *testing.T) {
	l := layout()
	s := validSpec(l)
	fs := &fakeFS{}
	mustBuild(t, s, validOptions(l, fs))
	wt := s.Worktree
	j := func(p ...string) string { return filepath.Join(append([]string{wt}, p...)...) }
	for _, want := range []string{
		"case " + wt, "case " + j("config"), "case " + j("config", "prod"),
		"8dot3 " + wt,
		"alias " + j(".env"), "alias " + j(".aws"), "alias " + j("config"), "alias " + j("config", "prod"), "alias " + j("config", "prod", ".env"),
	} {
		if !slices.Contains(fs.asked, want) {
			t.Errorf("Build did not ask %q; asked %q", want, fs.asked)
		}
	}
}

// TestBuild_Refuses lists the Plans that must not exist. The FS rows are
// sandbox_unavailable (the machine cannot host the sandbox safely); the
// others are daemon bugs (ErrInvalidSpec) or bad configuration.
func TestBuild_Refuses(t *testing.T) {
	l := layout()
	wt := validSpec(l).Worktree
	cases := []struct {
		name        string
		mut         func(*sandbox.Spec, *Options, *fakeFS)
		unavailable bool
	}{
		{"L1 spec", func(s *sandbox.Spec, _ *Options, _ *fakeFS) {
			s.Level = sandbox.LevelL1
			s.Toolchains = nil
			s.Proxy = sandbox.ProxyEndpoint{Socket: filepath.Join(s.SessionDir, "scratch", "px-1.sock")}
		}, false},
		{"invalid spec", func(s *sandbox.Spec, _ *Options, _ *fakeFS) { s.ProfilesDigest = "x" }, false},
		{"nil FS", func(_ *sandbox.Spec, o *Options, _ *fakeFS) { o.FS = nil }, false},
		{"sandbox image by tag", func(_ *sandbox.Spec, o *Options, _ *fakeFS) { o.SandboxImage = "warden/sandbox-go:1.23" }, false},
		{"proxy image by tag", func(_ *sandbox.Spec, o *Options, _ *fakeFS) { o.ProxyImage = "warden/proxy:latest" }, false},
		{"unknown host OS", func(_ *sandbox.Spec, o *Options, _ *fakeFS) { o.HostOS = "plan9" }, false},
		{"unknown engine", func(_ *sandbox.Spec, o *Options, _ *fakeFS) { o.Engine = "rootful" }, false},
		{"rootless engine off Linux", func(_ *sandbox.Spec, o *Options, _ *fakeFS) {
			if o.HostOS == HostLinux {
				o.HostOS = HostDarwin
			}
			o.Engine = EngineRootless
		}, false},
		// The sidecar host is a name on the internal network (M2-18 review
		// round 1): the sandbox's own hostname or localhost would point
		// HTTP_PROXY back into the sandbox, and an IP literal pins an
		// address the engine assigns.
		{"sidecar host is the sandbox hostname", func(s *sandbox.Spec, _ *Options, _ *fakeFS) { s.Proxy.Sidecar = "warden:3128" }, false},
		{"sidecar host is localhost", func(s *sandbox.Spec, _ *Options, _ *fakeFS) { s.Proxy.Sidecar = "localhost:3128" }, false},
		{"sidecar host is an IP literal", func(s *sandbox.Spec, _ *Options, _ *fakeFS) { s.Proxy.Sidecar = "172.18.0.2:3128" }, false},
		{"sidecar host is a loopback literal", func(s *sandbox.Spec, _ *Options, _ *fakeFS) { s.Proxy.Sidecar = "127.0.0.1:3128" }, false},
		{"worktree case-insensitive", func(_ *sandbox.Spec, _ *Options, f *fakeFS) { f.insensitive = map[string]bool{wt: true} }, true},
		{"mask parent case-insensitive", func(_ *sandbox.Spec, _ *Options, f *fakeFS) {
			f.insensitive = map[string]bool{filepath.Join(wt, "config", "prod"): true}
		}, true},
		{"case query fails", func(_ *sandbox.Spec, _ *Options, f *fakeFS) { f.err = errors.New("boom") }, true},
		{"8.3 creation enabled", func(_ *sandbox.Spec, _ *Options, f *fakeFS) { f.shortCreation = true }, true},
		{"masked file has an 8.3 alias", func(_ *sandbox.Spec, _ *Options, f *fakeFS) {
			f.aliases = map[string]bool{filepath.Join(wt, ".env"): true}
		}, true},
		{"mask ancestor has an 8.3 alias", func(_ *sandbox.Spec, _ *Options, f *fakeFS) {
			f.aliases = map[string]bool{filepath.Join(wt, "config"): true}
		}, true},
		{"worktree is a junction", func(s *sandbox.Spec, _ *Options, f *fakeFS) {
			f.final = map[string]string{s.Worktree: filepath.Join(l.hostHome, "elsewhere")}
		}, true},
		{"cache in another case on disk", func(s *sandbox.Spec, _ *Options, f *fakeFS) {
			f.final = map[string]string{s.Cache: strings.ToUpper(s.Cache)}
		}, true},
		{"executor behind a link", func(s *sandbox.Spec, _ *Options, f *fakeFS) {
			f.final = map[string]string{s.ExecBin: filepath.Join(filepath.Dir(s.ExecBin), "real-warden-exec")}
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validSpec(l)
			fs := &fakeFS{}
			o := validOptions(l, fs)
			c.mut(s, &o, fs)
			p, err := Build(s, o)
			if err == nil {
				t.Fatalf("Build succeeded: %+v", p)
			}
			if got := errors.Is(err, sandbox.ErrUnavailable); got != c.unavailable {
				t.Fatalf("Build error %v: sandbox_unavailable=%v, want %v", err, got, c.unavailable)
			}
		})
	}
}

// The short_names_enabled refusal tells the owner what to do, in the order
// of the owner's decision of 2026-10-16: move WARDEN_HOME to a volume
// without 8.3 name creation (found with warden doctor, since `fsutil
// 8dot3name query` needs elevation), or have an administrator turn it off
// for that volume, which affects new names only, plus strip.
func TestBuild_ShortNamesHint(t *testing.T) {
	l := layout()
	_, err := Build(validSpec(l), validOptions(l, &fakeFS{shortCreation: true}))
	if err == nil || !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("Build = %v, want sandbox_unavailable", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"short_names_enabled",
		"WARDEN_HOME on a volume without 8.3 name creation",
		"warden doctor",
		"`fsutil 8dot3name query <drive>:` needs elevation",
		"an administrator",
		"`fsutil 8dot3name set <drive>: 1`",
		"affects new names only",
		"`fsutil 8dot3name strip`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("hint lacks %q:\n%s", want, msg)
		}
	}
	if i, j := strings.Index(msg, "WARDEN_HOME"), strings.Index(msg, "administrator"); i < 0 || j < 0 || i > j {
		t.Errorf("hint must offer moving WARDEN_HOME first:\n%s", msg)
	}
}

// Windows only: the host path mapping is applied to every host source, so a
// Spec whose (valid) host paths the mapper refuses is not built. RUNNER~1
// is the GitHub runner TEMP the D-055 amendment names: the Spec accepts it,
// the mapper refuses it, because the caller must expand it first.
func TestBuild_RefusesUnmappableSource(t *testing.T) {
	l := layout()
	if l.hostOS != HostWindows {
		t.Skip("Windows host paths")
	}
	s := validSpec(l)
	s.ExecBin = `C:\Users\RUNNER~1\bin\warden-exec`
	if _, err := Build(s, validOptions(l, &fakeFS{})); err == nil {
		t.Fatal("Build accepted an 8.3 bind source")
	}
}

// The golden Plan of this host (CLAUDE.md §8.1 L2): Linux everywhere but
// Windows, Windows on Windows. Rewrite with UPDATE_GOLDEN=1 go test and
// review the diff like code; the Linux file is regenerated in WSL2 or on a
// Linux or macOS machine.
func TestGolden_Plan(t *testing.T) {
	l := layout()
	s := validSpec(l)
	o := validOptions(l, &fakeFS{})
	p := mustBuild(t, s, o)
	checkPlan(t, s, o, p)
	got, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	golden := filepath.Join("testdata", "golden", "plan-"+l.name+".json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden %s (run with UPDATE_GOLDEN=1): %v", golden, err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from Build()\n--- got\n%s", golden, got)
	}
}
