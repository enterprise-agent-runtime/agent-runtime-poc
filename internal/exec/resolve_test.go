package exec

import (
	"errors"
	"strings"
	"testing"

	"warden.dev/warden/internal/execproto"
)

// testRoots are the sandbox roots of design A06 §2 as the Linux L1 sandbox
// mounts them. The pure part of Resolve only does string work, so the same
// table runs on every OS, Windows included.
func testRoots() []Root {
	return []Root{
		{Name: execproto.RootWorktree, Path: "/work", Mode: execproto.RootRW},
		{Name: execproto.RootScratch, Path: "/scratch", Mode: execproto.RootRW},
		{Name: execproto.RootCache, Path: "/cache", Mode: execproto.RootRW},
		{Name: execproto.RootGit, Path: "/git", Mode: execproto.RootRW},
	}
}

// TestResolve is the lexical half of design A06 §5.1 (steps 1-3 and 6) and
// the per-method root table of §5.3. Every path here comes from the model,
// so the rule under test is CLAUDE.md §9 rule 2: an attempt to leave the
// roots is an error with an A06 §4.4 code, never silently clamped to the
// root (a clamp would turn "../../.ssh/id_ed25519" into a read of something
// the model did not ask for, and hide the attempt from the audit trail).
// The deny-list rows are INV-I's executor layer: the policy engine refuses
// them first, and this layer must refuse them again on its own.
func TestResolve(t *testing.T) {
	const (
		read  = execproto.MethodFSRead
		stat  = execproto.MethodFSStat
		list  = execproto.MethodFSList
		srch  = execproto.MethodFSSearch
		write = execproto.MethodFSWrite
		patch = execproto.MethodFSPatch
		spawn = execproto.MethodSpawn
	)
	type want struct {
		root      execproto.RootName
		rel       string
		canonical string
		deny      string // expected deny_match ("" = null)
	}
	cases := []struct {
		name   string
		method string
		root   execproto.RootName
		input  string
		want   want
		code   execproto.Code // 0 = success expected
	}{
		// Allowed paths, normalised.
		{"plain", read, "", "src/a.ts", want{"worktree", "src/a.ts", "/work/src/a.ts", ""}, 0},
		{"dot slash and doubled slashes", read, "", "./src//a.ts/", want{"worktree", "src/a.ts", "/work/src/a.ts", ""}, 0},
		{"inner dotdot stays inside", read, "", "a/../b", want{"worktree", "b", "/work/b", ""}, 0},
		{"root itself", list, "", ".", want{"worktree", "", "/work", ""}, 0},
		{"absolute under worktree", read, "", "/work/src/a.ts", want{"worktree", "src/a.ts", "/work/src/a.ts", ""}, 0},
		{"absolute equal to worktree", stat, "", "/work", want{"worktree", "", "/work", ""}, 0},
		{"explicit worktree root", write, execproto.RootWorktree, "x.go", want{"worktree", "x.go", "/work/x.go", ""}, 0},
		{"spawn cwd", spawn, "", "pkg/sub", want{"worktree", "pkg/sub", "/work/pkg/sub", ""}, 0},
		{"search", srch, "", "src", want{"worktree", "src", "/work/src", ""}, 0},
		{"segment of 255 bytes", read, "", strings.Repeat("a", 255), want{"worktree", strings.Repeat("a", 255), "/work/" + strings.Repeat("a", 255), ""}, 0},
		{"4096 bytes", read, "", strings.Repeat("a/", 2047) + "bb", want{"worktree", strings.Repeat("a/", 2047) + "bb", "/work/" + strings.Repeat("a/", 2047) + "bb", ""}, 0},

		// Escapes are errors, never clamps (step 3).
		{"dotdot", read, "", "..", want{}, execproto.ErrPathEscape},
		{"dotdot x", read, "", "../x", want{}, execproto.ErrPathEscape},
		{"a dotdot dotdot x", read, "", "a/../../x", want{}, execproto.ErrPathEscape},
		{"ssh key two levels up", stat, "", "../../.ssh/id_ed25519", want{}, execproto.ErrPathEscape},
		{"absolute under root then up", read, "", "/work/../etc/passwd", want{}, execproto.ErrPathEscape},
		{"write escape", write, "", "a/../../x", want{}, execproto.ErrPathEscape},
		{"backslash dotdot", read, "", `..\..\x`, want{}, execproto.ErrPathEscape},
		{"backslash inner escape", read, "", `a\..\..\x`, want{}, execproto.ErrPathEscape},
		// Review of ab63720 (CWE-22): a doubled slash after the root left a
		// tail starting with "/", and path.Clean then ate the "..", so
		// "/work//../../etc/passwd" resolved to RelPath "/etc/passwd": an
		// escape silently clamped, with no path_escape in the audit trail.
		{"doubled slash after root then up", read, "", "/work//../../etc/passwd", want{}, execproto.ErrPathEscape},
		{"doubled slash after root, dotdot", read, "", "/work//..", want{}, execproto.ErrPathEscape},
		{"tripled slash after root, dotdot", stat, "", "/work///../x", want{}, execproto.ErrPathEscape},
		{"doubled slash after root", read, "", "/work//x", want{"worktree", "x", "/work/x", ""}, 0},
		{"doubled backslash after scratch", read, "", `\scratch\\vitest.json`, want{"scratch", "vitest.json", "/scratch/vitest.json", ""}, 0},

		// Absolute paths outside the roots, and the Windows forms (step 2).
		{"etc passwd", read, "", "/etc/passwd", want{}, execproto.ErrOutsideRoots},
		{"root slash", read, "", "/", want{}, execproto.ErrOutsideRoots},
		{"prefix of a root is not the root", read, "", "/workspace/x", want{}, execproto.ErrOutsideRoots},
		{"drive letter backslash", read, "", `C:\x`, want{}, execproto.ErrOutsideRoots},
		{"drive letter slash", read, "", "c:/x", want{}, execproto.ErrOutsideRoots},
		{"drive relative", read, "", "C:x", want{}, execproto.ErrOutsideRoots},
		{"UNC", read, "", `\\server\share`, want{}, execproto.ErrOutsideRoots},
		{"UNC forward", read, "", "//server/share", want{}, execproto.ErrOutsideRoots},
		{"device namespace", read, "", `\\?\C:\x`, want{}, execproto.ErrOutsideRoots},

		// Step 1: invalid input.
		{"empty", read, "", "", want{}, execproto.ErrInvalidArgument},
		{"NUL", read, "", "a\x00b", want{}, execproto.ErrInvalidArgument},
		{"invalid UTF-8", read, "", "a\xffb", want{}, execproto.ErrInvalidArgument},
		{"4097 bytes", read, "", strings.Repeat("a/", 2048) + "b", want{}, execproto.ErrInvalidArgument},
		{"segment of 256 bytes", read, "", "x/" + strings.Repeat("a", 256), want{}, execproto.ErrInvalidArgument},
		{"unknown root name", read, "bogus", "x", want{}, execproto.ErrInvalidArgument},

		// Per-method roots (§5.3).
		{"read scratch by absolute path", read, "", "/scratch/vitest.json", want{"scratch", "vitest.json", "/scratch/vitest.json", ""}, 0},
		{"stat scratch by root name", stat, execproto.RootScratch, "vitest.json", want{"scratch", "vitest.json", "/scratch/vitest.json", ""}, 0},
		{"list scratch refused", list, execproto.RootScratch, "x", want{}, execproto.ErrOutsideRoots},
		{"search scratch refused", srch, "", "/scratch/x", want{}, execproto.ErrOutsideRoots},
		{"write scratch refused", write, "", "/scratch/x", want{}, execproto.ErrOutsideRoots},
		{"spawn cwd scratch refused", spawn, execproto.RootScratch, "x", want{}, execproto.ErrOutsideRoots},
		{"read cache refused", read, "", "/cache/npm/x", want{}, execproto.ErrOutsideRoots},
		{"read git refused", read, "", "/git/config", want{}, execproto.ErrOutsideRoots},
		{"read git by root name refused", read, execproto.RootGit, "config", want{}, execproto.ErrOutsideRoots},
		{"method without paths", execproto.MethodGitRun, "", "x", want{}, execproto.ErrOutsideRoots},

		// Deny-list, root-relative (step 6, CF-17).
		{".env", read, "", ".env", want{}, execproto.ErrDenyList},
		{".env.local nested", read, "", "config/.env.local", want{}, execproto.ErrDenyList},
		{"pem", read, "", "certs/server.pem", want{}, execproto.ErrDenyList},
		{"ssh dir", read, "", ".ssh/id_ed25519", want{}, execproto.ErrDenyList},
		{"aws dir", srch, "", ".aws", want{}, execproto.ErrDenyList},
		{"npmrc", read, "", "pkg/.npmrc", want{}, execproto.ErrDenyList},
		{"repo's own .warden", read, "", ".warden/policy.yaml", want{}, execproto.ErrDenyList},
		{"upper case", read, "", "keys/ID_RSA", want{}, execproto.ErrDenyList},
		{"absolute .env", read, "", "/work/.env", want{}, execproto.ErrDenyList},
		{"denied after clean", read, "", "a/../.env", want{}, execproto.ErrDenyList},
		{"backslash .env", read, "", `sub\.env`, want{}, execproto.ErrDenyList},
		{"write .env", write, "", ".env", want{}, execproto.ErrDenyList},
		{"spawn cwd in .ssh", spawn, "", ".ssh", want{}, execproto.ErrDenyList},
		{"stat reports, does not fail", stat, "", ".env", want{"worktree", ".env", "/work/.env", "**/.env"}, 0},
		{"list reports, does not fail", list, "", ".aws", want{"worktree", ".aws", "/work/.aws", "**/.aws/**"}, 0},
		{"not on the deny-list", read, "", "src/env.ts", want{"worktree", "src/env.ts", "/work/src/env.ts", ""}, 0},

		// The .git write ban (§5.3, CLAUDE.md §9 rule 6).
		{"write .git/config", write, "", ".git/config", want{}, execproto.ErrOutsideRoots},
		{"write .git file", write, "", ".git", want{}, execproto.ErrOutsideRoots},
		{"patch .git hook", patch, "", ".git/hooks/pre-commit", want{}, execproto.ErrOutsideRoots},
		{"write .GIT case-folded", write, "", ".GIT/config", want{}, execproto.ErrOutsideRoots},
		{"write nested .git", write, "", "vendor/lib/.git/hooks/post-checkout", want{}, execproto.ErrOutsideRoots},
		{"write .git via dotdot", write, "", "src/../.git/config", want{}, execproto.ErrOutsideRoots},
		{"write absolute .git", write, "", "/work/.git/config", want{}, execproto.ErrOutsideRoots},
		{"read .git/config allowed", read, "", ".git/config", want{"worktree", ".git/config", "/work/.git/config", ""}, 0},
		{"write .gitignore allowed", write, "", ".gitignore", want{"worktree", ".gitignore", "/work/.gitignore", ""}, 0},
		{"write .github allowed", write, "", ".github/workflows/ci.yml", want{"worktree", ".github/workflows/ci.yml", "/work/.github/workflows/ci.yml", ""}, 0},

		// NTFS 8.3 short names (CWE-41; git's CVE-2014-9390, CVE-2019-1353).
		// On Windows L2 the worktree is a bind mount from NTFS, where ENV~1
		// opens .env and GIT~1 is .git: the deny-list and the .git ban must
		// refuse the alias too. A write cannot tell which long name an 8.3
		// segment stands for, so it refuses every one as a possible .git.
		{"8.3 alias of .env", read, "", "ENV~1", want{}, execproto.ErrDenyList},
		{"8.3 alias of .kube", read, "", "KUBE~1/config", want{}, execproto.ErrDenyList},
		{"8.3 alias of .git, write", write, "", "vendor/x/GIT~1/hooks/post-checkout", want{}, execproto.ErrOutsideRoots},
		{"8.3 segment, write", write, "", "src/LONGFI~1.TS", want{}, execproto.ErrOutsideRoots},
		{"tilde not 8.3, write", write, "", "src/a~b.ts", want{"worktree", "src/a~b.ts", "/work/src/a~b.ts", ""}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(testRoots(), tc.method, tc.root, tc.input)
			if tc.code != 0 {
				if !errors.Is(err, tc.code) {
					t.Fatalf("Resolve(%q, %q, %q) = %+v, %v; want error %s", tc.method, tc.root, tc.input, got, err, tc.code.Name())
				}
				var e *execproto.Error
				if !errors.As(err, &e) || e.Data.Code != tc.code.Name() {
					t.Fatalf("error %v is not an *execproto.Error with data.code %q", err, tc.code.Name())
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q, %q, %q): %v", tc.method, tc.root, tc.input, err)
			}
			if got.Root != tc.want.root || got.RelPath != tc.want.rel || got.Canonical != tc.want.canonical || got.RequestedRelPath != tc.want.rel {
				t.Errorf("Resolve(%q, %q, %q) = %+v; want root %q rel %q canonical %q", tc.method, tc.root, tc.input, got, tc.want.root, tc.want.rel, tc.want.canonical)
			}
			switch {
			case tc.want.deny == "" && got.DenyMatch != nil:
				t.Errorf("deny_match = %q, want null", *got.DenyMatch)
			case tc.want.deny != "" && (got.DenyMatch == nil || *got.DenyMatch != tc.want.deny):
				t.Errorf("deny_match = %v, want %q", got.DenyMatch, tc.want.deny)
			}
		})
	}
}

// TestResolve_ReadOnlyRoot: a write method never writes into a root mounted
// read-only, even one the method table allows (A06 §2 Mode column). The
// sandbox mount would refuse the write too; the executor refuses first, with
// a code, instead of an EROFS io_error.
func TestResolve_ReadOnlyRoot(t *testing.T) {
	roots := []Root{{Name: execproto.RootWorktree, Path: "/work", Mode: execproto.RootRO}}
	if _, err := Resolve(roots, execproto.MethodFSWrite, "", "x.go"); !errors.Is(err, execproto.ErrOutsideRoots) {
		t.Fatalf("write into a read-only worktree: %v, want outside_roots", err)
	}
	if _, err := Resolve(roots, execproto.MethodFSRead, "", "x.go"); err != nil {
		t.Fatalf("read from a read-only worktree: %v", err)
	}
}

// TestResolve_MissingRoot: a root the method allows but the sandbox does not
// have (no scratch configured) is outside the roots, not a panic or a path
// relative to "".
func TestResolve_MissingRoot(t *testing.T) {
	roots := []Root{{Name: execproto.RootWorktree, Path: "/work", Mode: execproto.RootRW}}
	if _, err := Resolve(roots, execproto.MethodFSRead, execproto.RootScratch, "x"); !errors.Is(err, execproto.ErrOutsideRoots) {
		t.Fatalf("read from an absent scratch root: %v, want outside_roots", err)
	}
	if _, err := Resolve(roots, execproto.MethodFSRead, "", "/scratch/x"); !errors.Is(err, execproto.ErrOutsideRoots) {
		t.Fatalf("absolute path into an absent scratch root: %v, want outside_roots", err)
	}
}

// FuzzResolve checks the properties every successful Resolve must have,
// whatever the input (A06 §5.1 step 5): RelPath is root-relative (no leading
// "/"), has no ".." segment, and Canonical is exactly the root's path joined
// with it. It exists because a doubled slash after the root once produced
// RelPath "/etc/passwd" (review of ab63720, CWE-22), which no table row had
// thought of. It must also never panic.
func FuzzResolve(f *testing.F) {
	for _, s := range []string{
		"src/a.ts", "..", "/work//../../etc/passwd", "/work//..", "/work//x", `\work\\..\x`,
		"/scratch//vitest.json", "a/../../x", `C:\x`, `\server\share`, ".env", "ENV~1", "/work", "/", ".",
	} {
		for _, m := range []string{execproto.MethodFSRead, execproto.MethodFSWrite, execproto.MethodFSList} {
			f.Add(m, s)
		}
	}
	roots := testRoots()
	f.Fuzz(func(t *testing.T, method, input string) {
		r, err := Resolve(roots, method, "", input)
		if err != nil {
			var e *execproto.Error
			if !errors.As(err, &e) || e.Data.Code == "" {
				t.Fatalf("Resolve(%q, %q): error %v has no executor code", method, input, err)
			}
			return
		}
		root, ok := findRoot(roots, r.Root)
		if !ok {
			t.Fatalf("Resolve(%q, %q) = %+v: unknown root", method, input, r)
		}
		for _, p := range []string{r.RelPath, r.RequestedRelPath} {
			if strings.HasPrefix(p, "/") || p == "." {
				t.Fatalf("Resolve(%q, %q) = %+v: path %q is not root-relative", method, input, r, p)
			}
			for _, seg := range strings.Split(p, "/") {
				if seg == ".." || (seg == "" && p != "") {
					t.Fatalf("Resolve(%q, %q) = %+v: path %q has segment %q", method, input, r, p, seg)
				}
			}
		}
		want := root.Path
		if r.RelPath != "" {
			want += "/" + r.RelPath
		}
		if r.Canonical != want {
			t.Fatalf("Resolve(%q, %q) = %+v: canonical %q, want %q", method, input, r, r.Canonical, want)
		}
	})
}
