package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// TestSandboxSpecMounts is the INV-B test of CLAUDE.md §5: a sandbox mounts
// exactly the list of CONFLICTS C-24 (interim, needs owner) and nothing
// else, each with its mode: worktree rw (with its deny-list masks, INV-I),
// scratch rw, package cache rw, the session's private gitdir rw, the main
// repository's objects/ ro (objects only, never its .git, CLAUDE.md §9 rule
// 6), toolchains ro, the executor binary ro, the profiles file ro and, on
// L1, the proxy socket. The comparison is exact, so a mount that is added,
// dropped, re-targeted or given another mode fails here first.
//
// What the backend itself creates from no host data (tmpfs /tmp, the empty
// hooks dir, /proc, /dev, generated /etc files) and the L1 system binds of
// A06 §11.1 are not Spec mounts; the bwrap argv test (M2-13) pins them.
func TestSandboxSpecMounts(t *testing.T) {
	l := newLayout(t)
	s1 := validSpec(l, LevelL1)
	worktree := []Mount{
		{Kind: KindWorktree, Source: s1.Worktree, Target: "/work", Mode: ModeRW},
		{Kind: KindMask, Target: "/work/.env", Mode: ModeRO},
		{Kind: KindMask, Target: "/work/.aws", Mode: ModeRO, Dir: true},
		{Kind: KindScratch, Source: s1.Scratch, Target: "/scratch", Mode: ModeRW},
		{Kind: KindCache, Source: s1.Cache, Target: "/cache", Mode: ModeRW},
		{Kind: KindGit, Source: s1.GitDir, Target: "/git", Mode: ModeRW},
	}
	objects := Mount{Kind: KindGitObjects, Source: l.objects, Target: "/git-alt/objects", Mode: ModeRO}
	warden := []Mount{
		{Kind: KindExecutor, Source: l.execBin, Target: "/warden/bin/warden-exec", Mode: ModeRO},
		{Kind: KindGenerated, Source: s1.ProfilesFile, Target: "/warden/etc/profiles.json", Mode: ModeRO},
	}
	toolchain := []Mount{
		{Kind: KindToolchain, Source: "/opt/node-v22", Target: "/opt/node-v22", Mode: ModeRO},
		{Kind: KindMask, Target: "/opt/node-v22/etc/npmrc", Mode: ModeRO},
	}
	proxy := Mount{Kind: KindProxy, Source: s1.Proxy.Socket, Target: "/run/warden/proxy.sock", Mode: ModeConnect}

	cat := func(parts ...[]Mount) []Mount {
		var out []Mount
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	cases := []struct {
		name string
		spec *Spec
		want []Mount
	}{
		{"L1", s1, cat(worktree, []Mount{objects}, toolchain, warden, []Mount{proxy})},
		{"L1 fetched object mode", func() *Spec { s := validSpec(l, LevelL1); s.MainObjects = ""; return s }(),
			cat(worktree, toolchain, warden, []Mount{proxy})},
		{"L1 without toolchains", func() *Spec { s := validSpec(l, LevelL1); s.Toolchains = nil; return s }(),
			cat(worktree, []Mount{objects}, warden, []Mount{proxy})},
		// L2: the toolchain is the image and the proxy is the sidecar on
		// the internal network (C-03), so neither is mounted.
		{"L2", validSpec(l, LevelL2), cat(worktree, []Mount{objects}, warden)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.spec.Mounts()
			if err != nil {
				t.Fatalf("Mounts() error: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("mount table differs from C-24 / INV-B\n got: %s\nwant: %s", fmtMounts(got), fmtMounts(c.want))
			}
			assertINVB(t, c.spec, got)
		})
	}
}

// assertINVB restates INV-B as properties, independent of the exact table:
// it names why a wrong table is wrong.
func assertINVB(t *testing.T, s *Spec, mounts []Mount) {
	t.Helper()
	targets := map[string]bool{}
	for _, m := range mounts {
		if targets[m.Target] {
			t.Errorf("target %s mounted twice", m.Target)
		}
		targets[m.Target] = true
		switch m.Mode {
		case ModeRW:
			if m.Kind != KindWorktree && m.Kind != KindScratch && m.Kind != KindCache && m.Kind != KindGit {
				t.Errorf("%s (%s) is writable; only worktree, scratch, cache and the private gitdir are", m.Target, m.Kind)
			}
		case ModeRO, ModeConnect:
		default:
			t.Errorf("%s has mode %q", m.Target, m.Mode)
		}
		if m.Kind == KindMask {
			if m.Source != "" || m.Mode != ModeRO {
				t.Errorf("mask %s must hide, not expose: source %q mode %q", m.Target, m.Source, m.Mode)
			}
			continue
		}
		if m.Source == "" {
			t.Errorf("%s has no source", m.Target)
		}
		if filepath.Base(m.Source) == ".git" {
			t.Errorf("%s mounts a .git directory %s (CLAUDE.md §9 rule 6)", m.Target, m.Source)
		}
		if overlaps(m.Source, s.WardenHome) && !strictlyWithin(m.Source, filepath.Join(s.WardenHome, "sessions")) &&
			!strictlyWithin(m.Source, filepath.Join(s.WardenHome, "cache")) {
			t.Errorf("%s exposes the Warden home outside sessions/ and cache/: %s", m.Target, m.Source)
		}
	}
	if !targets["/work"] {
		t.Error("no worktree mount")
	}
}

func fmtMounts(ms []Mount) string {
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "\n  %-11s %-3s %-28s <- %s dir=%v", m.Kind, m.Mode, m.Target, m.Source, m.Dir)
	}
	return b.String()
}

// TestWorktreeMasks_A06Walk checks the A06 §11.3 walk: deny-listed files
// and directories are masked (a masked directory's content is not walked),
// symlinks are not (a bind onto a symlink would mask its target instead),
// and the generated dependency trees are skipped.
func TestWorktreeMasks_A06Walk(t *testing.T) {
	fsys := fstest.MapFS{
		"src/app.ts":                    {Data: []byte("x")},
		".env":                          {Data: []byte("x")},
		"config/.ENV.production":        {Data: []byte("x")},
		".aws/credentials":              {Data: []byte("x")},
		".aws/config":                   {Data: []byte("x")},
		"pkg/.warden/policy.yaml":       {Data: []byte("x")},
		"link.pem":                      {Data: []byte("src/app.ts"), Mode: fs.ModeSymlink},
		"node_modules/x/.env":           {Data: []byte("x")},
		"sub/node_modules/y/server.key": {Data: []byte("x")},
		".venv/lib/id_rsa":              {Data: []byte("x")},
		"venv/.netrc":                   {Data: []byte("x")},
		"__pycache__/a.pem":             {Data: []byte("x")},
		"docs/keys.md":                  {Data: []byte("x")},
	}
	got, err := WorktreeMasks(context.Background(), fsys)
	if err != nil {
		t.Fatal(err)
	}
	want := []Mask{
		{RelPath: ".aws", Dir: true, Pattern: "**/.aws/**"},
		{RelPath: ".env", Pattern: "**/.env"},
		{RelPath: "config/.ENV.production", Pattern: "**/.env.*"},
		{RelPath: "pkg/.warden", Dir: true, Pattern: "**/.warden/**"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorktreeMasks()\n got %+v\nwant %+v", got, want)
	}
}

// TestWorktreeMasks_FailsClosed: a walk that cannot finish could miss a
// deny-listed file, so it fails sandbox creation instead of mounting an
// unmasked worktree (A06 §11.3 bounds; the fail-closed choice is ours).
func TestWorktreeMasks_FailsClosed(t *testing.T) {
	many := fstest.MapFS{}
	for i := 0; i <= MaxMasks; i++ {
		many[fmt.Sprintf("k/%03d.pem", i)] = &fstest.MapFile{Data: []byte("x")}
	}
	if _, err := WorktreeMasks(context.Background(), many); !errors.Is(err, ErrUnavailable) ||
		!strings.Contains(err.Error(), "too_many_deny_list_matches") {
		t.Errorf("257 matches: err = %v, want sandbox_unavailable too_many_deny_list_matches", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WorktreeMasks(ctx, fstest.MapFS{"a.txt": {}}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("expired walk: err = %v, want sandbox_unavailable", err)
	}

	if _, err := WorktreeMasks(context.Background(), brokenFS{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unreadable worktree: err = %v, want sandbox_unavailable", err)
	}
}

// brokenFS fails every open, as an unreadable directory would.
type brokenFS struct{}

func (brokenFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }
