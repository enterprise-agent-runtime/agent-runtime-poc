package sandbox

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"warden.dev/warden/internal/execproto"
)

// testLayout is the core §10 layout of one session in a temp Warden home.
// In the default layout the home is <user home>/.warden, as on Linux and
// macOS, so a test would catch the deny-list's **/.warden/** pattern
// wrongly refusing the task's own roots (CF-17). The custom layout is a
// WARDEN_HOME outside the user's home, where no ".warden" segment exists.
type testLayout struct {
	tmp, hostHome, home, session, objects, execBin string
}

func newLayout(t *testing.T) testLayout { return layoutAt(t, false) }

func layoutAt(t *testing.T, customHome bool) testLayout {
	t.Helper()
	// Validation is pure: nothing here touches the disk. On slash hosts the
	// base is a fixed path outside /tmp, because /tmp is a reserved sandbox
	// path and a toolchain root built under t.TempDir() would be refused for
	// that reason instead of the one a row tests. On Windows t.TempDir()
	// supplies a real absolute drive path.
	tmp := t.TempDir()
	if path.IsAbs(filepath.ToSlash(tmp)) {
		tmp = "/srv/warden-test"
	}
	hostHome := filepath.Join(tmp, "home", "ana")
	home := filepath.Join(hostHome, ".warden")
	if customHome {
		home = filepath.Join(tmp, "wardenhome")
	}
	return testLayout{
		tmp:      tmp,
		hostHome: hostHome,
		home:     home,
		session:  filepath.Join(home, "sessions", "01jaxr8q7m2v9ktc3f6yh5n0pb"),
		objects:  filepath.Join(hostHome, "project", ".git", "objects"),
		execBin:  filepath.Join(tmp, "opt", "warden", "bin", "warden-exec"),
	}
}

// slashHost reports whether host paths are slash paths here. L1 toolchain
// roots are slash paths (L1 exists only on Linux and macOS), so rows that
// build a toolchain root from a host path run only where both agree.
func slashHost(l testLayout) bool { return path.IsAbs(filepath.ToSlash(l.tmp)) }

const (
	testID     = "sb_01JAXS2Q7M2V9KTC3F6YH5N0PB"
	testTask   = "implement"
	testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testBase   = "3f9c2e1a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e"
	testSide   = "warden-proxy-01jaxs2q:3128"
)

// validSpec is the Spec the A06 §2 layout produces for one implement task.
func validSpec(l testLayout, level Level) *Spec {
	s := &Spec{
		ID:             testID,
		SessionID:      "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB",
		TaskKey:        testTask,
		Level:          level,
		Purpose:        PurposeTask,
		Mode:           execproto.ModeTool,
		HostHome:       l.hostHome,
		WardenHome:     l.home,
		SessionDir:     l.session,
		Worktree:       filepath.Join(l.session, "worktree"),
		Scratch:        filepath.Join(l.session, "scratch", testTask),
		Cache:          filepath.Join(l.home, "cache", "wsp_01jaxr"),
		GitDir:         filepath.Join(l.session, "git"),
		MainObjects:    l.objects,
		ExecBin:        l.execBin,
		ProfilesFile:   filepath.Join(l.session, "sandbox", testID, "profiles.json"),
		ProfilesDigest: testDigest,
		WorktreeMasks: []Mask{
			{RelPath: ".env", Pattern: "**/.env"},
			{RelPath: ".aws", Dir: true, Pattern: "**/.aws/**"},
		},
		Limits:        DefaultLimits(),
		SessionBranch: "warden/01jaxr8q7m2v9ktc3f6yh5n0pb",
		BaseCommit:    testBase,
	}
	switch level {
	case LevelL1:
		s.Toolchains = []Toolchain{{
			Root:    "/opt/node-v22",
			BinDirs: []string{"/opt/node-v22/bin"},
			Masks:   []string{"/opt/node-v22/etc/npmrc"},
		}}
		s.Proxy = ProxyEndpoint{Socket: filepath.Join(l.session, "scratch", "px-1.sock")}
	case LevelL2:
		s.Toolchains = []Toolchain{{BinDirs: []string{"/usr/local/go/bin"}}}
		s.Proxy = ProxyEndpoint{Sidecar: testSide}
	}
	return s
}

func TestSpecValidate_AcceptsDesignLayout(t *testing.T) {
	for _, level := range []Level{LevelL1, LevelL2} {
		l := newLayout(t)
		s := validSpec(l, level)
		if err := s.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", level, err)
		}
		s.MainObjects = "" // A14 fetched object mode
		if err := s.Validate(); err != nil {
			t.Errorf("%s without main objects: Validate() = %v, want nil", level, err)
		}
	}
}

// TestSpecValidate_Rejects lists the Specs no backend may build. Most rows
// are a layout mistake that would expose something INV-B keeps out: the
// user's checkout with its .git (CLAUDE.md §9 rule 6), the rest of the
// Warden home (run/token, secrets.enc, C-53), a deny-listed host path
// (INV-I), or a credential in the proxy address (INV-C).
func TestSpecValidate_Rejects(t *testing.T) {
	cases := []struct {
		name  string
		level Level
		edit  func(l testLayout, s *Spec)
		want  error
		msg   string
	}{
		{"relative worktree", LevelL1, func(l testLayout, s *Spec) { s.Worktree = filepath.Join("sessions", "x", "worktree") }, ErrInvalidSpec, "not an absolute path"},
		{"unclean worktree", LevelL1, func(l testLayout, s *Spec) {
			s.Worktree = s.SessionDir + string(filepath.Separator) + ".." + string(filepath.Separator) + "worktree"
		}, ErrInvalidSpec, "not a clean path"},
		{"worktree is the user's checkout", LevelL1, func(l testLayout, s *Spec) { s.Worktree = filepath.Join(l.tmp, "project") }, ErrInvalidSpec, "worktree"},
		{"worktree is the warden home", LevelL1, func(l testLayout, s *Spec) { s.Worktree = l.home }, ErrInvalidSpec, "worktree"},
		{"worktree is the home's run dir", LevelL1, func(l testLayout, s *Spec) { s.Worktree = filepath.Join(l.home, "run") }, ErrInvalidSpec, "worktree"},
		{"session dir outside sessions/", LevelL1, func(l testLayout, s *Spec) { s.SessionDir = filepath.Join(l.home, "x") }, ErrInvalidSpec, "session dir"},
		{"session dir is sessions/ itself", LevelL1, func(l testLayout, s *Spec) { s.SessionDir = filepath.Join(l.home, "sessions") }, ErrInvalidSpec, "session dir"},
		{"git dir is the main repository's .git", LevelL1, func(l testLayout, s *Spec) { s.GitDir = filepath.Dir(l.objects) }, ErrInvalidSpec, "git dir"},
		{"scratch of another task", LevelL1, func(l testLayout, s *Spec) { s.Scratch = filepath.Join(l.session, "scratch", "verify") }, ErrInvalidSpec, "scratch"},
		{"scratch is the whole scratch dir", LevelL1, func(l testLayout, s *Spec) { s.Scratch = filepath.Join(l.session, "scratch") }, ErrInvalidSpec, "scratch"},
		{"cache outside the home", LevelL1, func(l testLayout, s *Spec) { s.Cache = filepath.Join(l.tmp, "cache") }, ErrInvalidSpec, "cache"},
		{"cache is the whole cache dir", LevelL1, func(l testLayout, s *Spec) { s.Cache = filepath.Join(l.home, "cache") }, ErrInvalidSpec, "cache"},
		{"profiles file of another sandbox", LevelL1, func(l testLayout, s *Spec) {
			s.ProfilesFile = filepath.Join(l.session, "sandbox", "sb_OTHER", "profiles.json")
		}, ErrInvalidSpec, "profiles file"},
		{"main objects is the .git dir", LevelL1, func(l testLayout, s *Spec) { s.MainObjects = filepath.Dir(l.objects) }, ErrInvalidSpec, "objects"},
		{"main objects inside the home", LevelL1, func(l testLayout, s *Spec) { s.MainObjects = filepath.Join(l.home, "objects") }, ErrInvalidSpec, "warden home"},
		{"main objects relative", LevelL1, func(l testLayout, s *Spec) { s.MainObjects = filepath.Join("project", ".git", "objects") }, ErrInvalidSpec, "not an absolute path"},
		{"executor binary in the worktree", LevelL1, func(l testLayout, s *Spec) { s.ExecBin = filepath.Join(s.Worktree, "warden-exec") }, ErrInvalidSpec, "writable root"},
		{"executor binary in the cache", LevelL2, func(l testLayout, s *Spec) { s.ExecBin = filepath.Join(s.Cache, "warden-exec") }, ErrInvalidSpec, "writable root"},
		{"executor binary in the warden home", LevelL1, func(l testLayout, s *Spec) { s.ExecBin = filepath.Join(l.home, "bin", "warden-exec") }, ErrInvalidSpec, "deny-listed"},
		{"executor binary under a deny-listed dir", LevelL1, func(l testLayout, s *Spec) { s.ExecBin = filepath.Join(l.tmp, ".ssh", "warden-exec") }, ErrInvalidSpec, "deny-listed"},
		{"L1 without a proxy socket", LevelL1, func(l testLayout, s *Spec) { s.Proxy = ProxyEndpoint{} }, ErrInvalidSpec, "proxy socket"},
		{"L1 with a sidecar", LevelL1, func(l testLayout, s *Spec) { s.Proxy.Sidecar = testSide }, ErrInvalidSpec, "sidecar"},
		{"L1 proxy socket outside the session", LevelL1, func(l testLayout, s *Spec) { s.Proxy.Socket = filepath.Join(l.tmp, "px.sock") }, ErrInvalidSpec, "proxy socket"},
		{"L1 proxy socket in the mounted scratch", LevelL1, func(l testLayout, s *Spec) { s.Proxy.Socket = filepath.Join(s.Scratch, "px.sock") }, ErrInvalidSpec, "proxy socket"},
		{"L2 with a mounted socket", LevelL2, func(l testLayout, s *Spec) { s.Proxy.Socket = filepath.Join(l.session, "scratch", "px-1.sock") }, ErrInvalidSpec, "sidecar"},
		{"L2 sidecar with userinfo", LevelL2, func(l testLayout, s *Spec) { s.Proxy.Sidecar = "user:pw@proxy:3128" }, ErrInvalidSpec, "sidecar"},
		{"L2 sidecar with a scheme", LevelL2, func(l testLayout, s *Spec) { s.Proxy.Sidecar = "http://proxy:3128" }, ErrInvalidSpec, "sidecar"},
		{"L2 sidecar without a port", LevelL2, func(l testLayout, s *Spec) { s.Proxy.Sidecar = "proxy" }, ErrInvalidSpec, "sidecar"},
		{"L2 sidecar port out of range", LevelL2, func(l testLayout, s *Spec) { s.Proxy.Sidecar = "proxy:65536" }, ErrInvalidSpec, "port"},
		{"L2 toolchain mounted from the host", LevelL2, func(l testLayout, s *Spec) { s.Toolchains = []Toolchain{{Root: "/opt/go"}} }, ErrInvalidSpec, "image"},
		{"toolchain root /", LevelL1, func(l testLayout, s *Spec) { s.Toolchains = []Toolchain{{Root: "/"}} }, ErrInvalidSpec, "overlaps"},
		{"toolchain root over /work", LevelL1, func(l testLayout, s *Spec) { s.Toolchains = []Toolchain{{Root: "/work/node"}} }, ErrInvalidSpec, "overlaps"},
		{"toolchain root containing /warden", LevelL1, func(l testLayout, s *Spec) { s.Toolchains = []Toolchain{{Root: "/warden"}} }, ErrInvalidSpec, "overlaps"},
		{"toolchain root deny-listed", LevelL1, func(l testLayout, s *Spec) { s.Toolchains = []Toolchain{{Root: "/home/ana/.aws/cli"}} }, ErrInvalidSpec, "deny-listed"},
		{"toolchain root relative", LevelL1, func(l testLayout, s *Spec) { s.Toolchains = []Toolchain{{Root: "opt/node"}} }, ErrInvalidSpec, "slash path"},
		{"toolchain roots overlap", LevelL1, func(l testLayout, s *Spec) {
			s.Toolchains = append(s.Toolchains, Toolchain{Root: "/opt/node-v22/lib"})
		}, ErrInvalidSpec, "overlap"},
		{"toolchain bin dir outside its root", LevelL1, func(l testLayout, s *Spec) { s.Toolchains[0].BinDirs = []string{"/usr/local/bin"} }, ErrInvalidSpec, "bin dir"},
		{"toolchain bin dir with a colon", LevelL1, func(l testLayout, s *Spec) { s.Toolchains[0].BinDirs = []string{"/opt/node-v22/bin:/work"} }, ErrInvalidSpec, "bin dir"},
		{"L2 bin dir in the worktree", LevelL2, func(l testLayout, s *Spec) { s.Toolchains[0].BinDirs = []string{"/work/node_modules/.bin"} }, ErrInvalidSpec, "task root"},
		{"toolchain mask outside its root", LevelL1, func(l testLayout, s *Spec) { s.Toolchains[0].Masks = []string{"/etc/npmrc"} }, ErrInvalidSpec, "mask"},
		{"worktree mask escaping", LevelL1, func(l testLayout, s *Spec) { s.WorktreeMasks = []Mask{{RelPath: "../.env"}} }, ErrInvalidSpec, "mask"},
		{"worktree mask absolute", LevelL1, func(l testLayout, s *Spec) { s.WorktreeMasks = []Mask{{RelPath: "/work/.env"}} }, ErrInvalidSpec, "mask"},
		{"worktree mask over the whole worktree", LevelL1, func(l testLayout, s *Spec) { s.WorktreeMasks = []Mask{{RelPath: "."}} }, ErrInvalidSpec, "mask"},
		{"worktree mask twice", LevelL1, func(l testLayout, s *Spec) { s.WorktreeMasks = append(s.WorktreeMasks, Mask{RelPath: ".env"}) }, ErrInvalidSpec, "twice"},
		{"more than 256 masks", LevelL1, func(l testLayout, s *Spec) {
			s.WorktreeMasks = nil
			for i := 0; i <= MaxMasks; i++ {
				s.WorktreeMasks = append(s.WorktreeMasks, Mask{RelPath: fmt.Sprintf("k%d.pem", i)})
			}
		}, ErrUnavailable, "too_many_deny_list_matches"},
		{"harness purpose", LevelL1, func(l testLayout, s *Spec) { s.Purpose = PurposeHarness }, ErrInvalidSpec, "purpose"},
		{"harness mode", LevelL1, func(l testLayout, s *Spec) { s.Mode = execproto.ModeHarness }, ErrInvalidSpec, "mode"},
		{"unknown level", LevelL1, func(l testLayout, s *Spec) { s.Level = "L0" }, ErrInvalidSpec, "level"},
		{"bad sandbox id", LevelL1, func(l testLayout, s *Spec) { s.ID = "../x" }, ErrInvalidSpec, "sandbox id"},
		{"bad task key", LevelL1, func(l testLayout, s *Spec) { s.TaskKey = "../verify" }, ErrInvalidSpec, "task key"},
		{"no session id", LevelL1, func(l testLayout, s *Spec) { s.SessionID = "" }, ErrInvalidSpec, "session id"},
		{"zero memory limit", LevelL1, func(l testLayout, s *Spec) { s.Limits.MemoryMB = 0 }, ErrInvalidSpec, "limit"},
		{"bad profiles digest", LevelL1, func(l testLayout, s *Spec) { s.ProfilesDigest = "md5:00" }, ErrInvalidSpec, "digest"},
		{"bad session branch", LevelL1, func(l testLayout, s *Spec) { s.SessionBranch = "main" }, ErrInvalidSpec, "session branch"},
		{"abbreviated base commit", LevelL1, func(l testLayout, s *Spec) { s.BaseCommit = "3f9c2e1" }, ErrInvalidSpec, "base commit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newLayout(t)
			s := validSpec(l, c.level)
			c.edit(l, s)
			err := s.Validate()
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("Validate() = %v, want %v mentioning %q", err, c.want, c.msg)
			}
			if _, merr := s.Mounts(); !errors.Is(merr, c.want) {
				t.Errorf("Mounts() error = %v, want %v: no backend may build an invalid Spec", merr, c.want)
			}
			if _, eerr := s.Env(); !errors.Is(eerr, c.want) {
				t.Errorf("Env() error = %v, want %v: no backend may build an invalid Spec", eerr, c.want)
			}
		})
	}
}
