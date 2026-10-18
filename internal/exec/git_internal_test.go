package exec

import (
	"slices"
	"strings"
	"testing"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/execproto/denylist"
)

// These tests pin the pure half of exec.git.run (A14 §6, A06 §4.3, §8):
// the argv the executor builds, the fixed environment, the configuration
// allowlist and the output checks. Each guards a route by which git inside
// the sandbox could run a program nobody authorized or hand the model the
// content of a deny-listed file (INV-I, CLAUDE.md §9 rule 6). The Linux
// tests in git_linux_test.go run the same code against a real git.

var testGitArgs = gitArgs{gitDir: "/git", work: "/work", hooks: "/warden/empty"}

// wantGlobal is the fixed prefix of every git command: the repository and
// worktree come from the launch flags, never from params, and the -c
// overrides (command-line scope, which beats the sandbox-writable
// repository configuration) switch off every program git could run for
// these commands (A14 §2, §4).
var wantGlobal = []string{
	"git", "--git-dir=/git", "--work-tree=/work", "--no-pager", "--no-replace-objects",
	"-c", "core.hooksPath=/warden/empty",
	"-c", "core.fsmonitor=false",
	"-c", "core.untrackedCache=false",
	"-c", "core.attributesFile=/dev/null",
	"-c", "core.quotePath=true",
	"-c", "core.pager=cat",
	"-c", "commit.gpgSign=false",
	"-c", "tag.gpgSign=false",
	"-c", "gc.auto=0",
	"-c", "maintenance.auto=false",
	"-c", "protocol.allow=never",
	"-c", "submodule.recurse=false",
	"-c", "credential.helper=",
	"-c", "safe.directory=/work",
	"-c", "safe.directory=/git",
}

func wantDeny() []string {
	var out []string
	for _, p := range denylist.Patterns {
		out = append(out, ":(exclude,glob,icase)"+p)
	}
	return out
}

func cat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// TestGitArgv_Exact pins every command exec.git.run runs, element by
// element. Nothing a caller sends lands before "--" except the validated
// commit id; the deny-list excludes follow every pathspec (A14 §6 DENY).
func TestGitArgv_Exact(t *testing.T) {
	g := testGitArgs
	deny := wantDeny()
	if len(deny) != len(denylist.Patterns) || len(deny) == 0 {
		t.Fatal("deny pathspecs not built")
	}
	if got := denyPathspecs(); !slices.Equal(got, deny) {
		t.Errorf("denyPathspecs = %q, want %q", got, deny)
	}
	base := strings.Repeat("a", 40)
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"status", g.status(), cat(wantGlobal, []string{"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--ignore-submodules=all", "--no-renames", "--", "."}, deny)},
		{"verify", g.verifyCommit(base), cat(wantGlobal, []string{"rev-parse", "--verify", "--quiet", "--end-of-options", base + "^{commit}"})},
		{"diff all", g.diff(base, gitRequest{contextLines: 3}, nil), cat(wantGlobal, []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--ignore-submodules=all", "--src-prefix=a/", "--dst-prefix=b/", "-U3", base, "--", "."}, deny)},
		{"diff stat paths", g.diff(base, gitRequest{contextLines: 0, statOnly: true}, []string{"src/a.go", ""}), cat(wantGlobal, []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--ignore-submodules=all", "--src-prefix=a/", "--dst-prefix=b/", "-U0", "--stat", base, "--", ":(literal)src/a.go", "."}, deny)},
		{"head ref", g.symbolicRef(), cat(wantGlobal, []string{"symbolic-ref", "-q", "HEAD"})},
		{"head commit", g.revParseHead(), cat(wantGlobal, []string{"rev-parse", "--verify", "--quiet", "HEAD"})},
		{"add", g.add([]string{"-c", "x y"}), cat(wantGlobal, []string{"add", "-A", "--", ":(literal)-c", ":(literal)x y"}, deny)},
		{"add all", g.add(nil), cat(wantGlobal, []string{"add", "-A", "--", "."}, deny)},
		{"staged", g.staged(), cat(wantGlobal, []string{"diff", "--cached", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "HEAD", "--"})},
		{"commit", g.commit("--exec-path=/x"), cat(wantGlobal, []string{"commit", "--no-verify", "--no-gpg-sign", "--quiet", "-m", "--exec-path=/x"})},
		{"config", g.configList(), cat(wantGlobal, []string{"config", "--file", "/git/config", "--no-includes", "--list", "--name-only", "-z"})},
	}
	for _, c := range cases {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}

// TestGitEnv_IsFixed: git gets exactly this environment, built from
// nothing but the PATH the executor resolved git with and the launch
// flags; no variable of the executor's own environment reaches it (no
// GIT_DIR, GIT_EXEC_PATH, GIT_CONFIG_PARAMETERS, GIT_TRACE passthrough),
// and HOME is the empty read-only hooks directory, so no ~/.gitconfig or
// ~/.config/git file can exist (A06 §8; A14 §2 profile P). The commit
// identity of A06 l.780 is added only for the commit itself.
func TestGitEnv_IsFixed(t *testing.T) {
	base := []string{
		"PATH=/usr/bin:/bin", "HOME=/warden/empty", "LC_ALL=C", "LANG=C", "TERM=dumb",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_PAGER=cat",
	}
	if got := gitEnv("/usr/bin:/bin", "/warden/empty", ""); !slices.Equal(got, base) {
		t.Errorf("gitEnv =\n %q\nwant\n %q", got, base)
	}
	id := "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB"
	want := append(append([]string(nil), base...), "GIT_AUTHOR_NAME=warden", "GIT_AUTHOR_EMAIL="+id, "GIT_COMMITTER_NAME=warden", "GIT_COMMITTER_EMAIL="+id)
	if got := gitEnv("/usr/bin:/bin", "/warden/empty", id); !slices.Equal(got, want) {
		t.Errorf("gitEnv with identity =\n %q\nwant\n %q", got, want)
	}
}

// TestGitConfig_Check: the launch flags are checked before anything runs;
// the commit identity is derived from the session branch (a launch flag),
// never from params (A06 l.780: <ses_…>).
func TestGitConfig_Check(t *testing.T) {
	ok := GitConfig{GitDir: "/git", SessionBranch: "warden/01jaxr8q7m2v9ktc3f6yh5n0pb", BaseCommit: strings.Repeat("0", 40), HooksDir: "/warden/empty"}
	if err := ok.check(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if got := ok.sessionEmail(); got != "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB" {
		t.Errorf("sessionEmail = %q", got)
	}
	sha256 := ok
	sha256.BaseCommit = strings.Repeat("f", 64)
	if err := sha256.check(); err != nil {
		t.Errorf("sha256 base: %v", err)
	}
	bad := map[string]func(*GitConfig){
		"relative git dir":  func(c *GitConfig) { c.GitDir = "git" },
		"unclean git dir":   func(c *GitConfig) { c.GitDir = "/git/../etc" },
		"root git dir":      func(c *GitConfig) { c.GitDir = "/" },
		"no hooks dir":      func(c *GitConfig) { c.HooksDir = "" },
		"relative hooks":    func(c *GitConfig) { c.HooksDir = "warden/empty" },
		"branch main":       func(c *GitConfig) { c.SessionBranch = "main" },
		"branch short ulid": func(c *GitConfig) { c.SessionBranch = "warden/01jaxr" },
		"branch bad char":   func(c *GitConfig) { c.SessionBranch = "warden/01jaxr8q7m2v9ktc3f6yh5n0pu" },
		"branch suffix":     func(c *GitConfig) { c.SessionBranch = "warden/01jaxr8q7m2v9ktc3f6yh5n0pb/x" },
		"base short":        func(c *GitConfig) { c.BaseCommit = "abc" },
		"base upper":        func(c *GitConfig) { c.BaseCommit = strings.Repeat("A", 40) },
		"base ref":          func(c *GitConfig) { c.BaseCommit = "refs/warden/base" },
	}
	for name, m := range bad {
		c := ok
		m(&c)
		if err := c.check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestParseGitParams: the four A14 §6 operations and nothing else; an op
// that would read objects by path (show, cat-file, grep, log, blame,
// archive) or reach the network does not exist; every option is checked
// against git.json, and against is a commit id or base/HEAD, so nothing a
// caller sends can become a git option (-c, --exec-path, --output).
func TestParseGitParams(t *testing.T) {
	three := 3
	eleven := 11
	neg := -1
	good := []execproto.GitRunParams{
		{Op: execproto.GitStatus},
		{Op: execproto.GitHead},
		{Op: execproto.GitDiff},
		{Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: "HEAD", ContextLines: &three, Paths: []string{"a"}}},
		{Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: strings.Repeat("b", 64)}},
		{Op: execproto.GitCommit, Commit: &execproto.GitCommitOptions{Message: strings.Repeat("m", 4096)}},
		{Op: execproto.GitStatus, TimeoutMS: 1000},
		{Op: execproto.GitStatus, TimeoutMS: 120000},
	}
	for _, p := range good {
		if _, err := parseGitParams(p); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	r, err := parseGitParams(execproto.GitRunParams{Op: execproto.GitDiff})
	if err != nil || r.against != "base" || r.contextLines != 3 || r.timeoutMS != 60000 {
		t.Errorf("defaults: %+v, %v", r, err)
	}
	many := make([]string, 257)
	for i := range many {
		many[i] = "a"
	}
	bad := map[string]execproto.GitRunParams{
		"show":              {Op: "show"},
		"cat-file":          {Op: "cat-file"},
		"grep":              {Op: "grep"},
		"log":               {Op: "log"},
		"blame":             {Op: "blame"},
		"archive":           {Op: "archive"},
		"fetch":             {Op: "fetch"},
		"push":              {Op: "push"},
		"config":            {Op: "config"},
		"option as op":      {Op: "-c"},
		"empty op":          {Op: ""},
		"status with diff":  {Op: execproto.GitStatus, Diff: &execproto.GitDiffOptions{}},
		"head with commit":  {Op: execproto.GitHead, Commit: &execproto.GitCommitOptions{Message: "x"}},
		"diff with commit":  {Op: execproto.GitDiff, Commit: &execproto.GitCommitOptions{Message: "x"}},
		"commit no message": {Op: execproto.GitCommit},
		"commit with diff":  {Op: execproto.GitCommit, Diff: &execproto.GitDiffOptions{}, Commit: &execproto.GitCommitOptions{Message: "x"}},
		"against rev:path":  {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: "HEAD:.env"}},
		"against base:path": {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: "base:.env"}},
		"against -c":        {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: "-c"}},
		"against exec-path": {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: "--exec-path=/tmp"}},
		"against output":    {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: "--output=/work/x"}},
		"against HEAD~1":    {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: "HEAD~1"}},
		"against branch":    {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: "main"}},
		"against tree":      {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: "HEAD^{tree}"}},
		"against short":     {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: strings.Repeat("a", 39)}},
		"against upper":     {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Against: strings.Repeat("A", 40)}},
		"context 11":        {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{ContextLines: &eleven}},
		"context -1":        {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{ContextLines: &neg}},
		"diff 257 paths":    {Op: execproto.GitDiff, Diff: &execproto.GitDiffOptions{Paths: many}},
		"commit 257 paths":  {Op: execproto.GitCommit, Commit: &execproto.GitCommitOptions{Message: "x", Paths: many}},
		"message empty":     {Op: execproto.GitCommit, Commit: &execproto.GitCommitOptions{Message: ""}},
		"message 4097":      {Op: execproto.GitCommit, Commit: &execproto.GitCommitOptions{Message: strings.Repeat("m", 4097)}},
		"message NUL":       {Op: execproto.GitCommit, Commit: &execproto.GitCommitOptions{Message: "a\x00b"}},
		"message not utf-8": {Op: execproto.GitCommit, Commit: &execproto.GitCommitOptions{Message: "a\xffb"}},
		"timeout 999":       {Op: execproto.GitStatus, TimeoutMS: 999},
		"timeout 120001":    {Op: execproto.GitStatus, TimeoutMS: 120001},
	}
	for name, p := range bad {
		_, err := parseGitParams(p)
		if codeOf(err) != execproto.ErrInvalidArgument {
			t.Errorf("%s: %v, want invalid_argument", name, err)
		}
	}
}

// TestCheckConfigKeys: the private gitdir is mounted read-write (A14 §5),
// so anything in the sandbox can add to its configuration. Keys outside
// Warden's template (A14 §3 step 4) are refused before git runs, because
// some of them name programs git would run that no -c override can reach:
// a filter driver's clean command runs on git add and status, under any
// driver name. include.path is refused rather than followed.
func TestCheckConfigKeys(t *testing.T) {
	tmpl := []string{
		"core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.symlinks",
		"core.ignorecase", "core.precomposeunicode", "core.hookspath", "core.fsmonitor", "core.untrackedcache",
		"core.autocrlf", "extensions.objectformat", "user.name", "user.email", "commit.gpgsign", "tag.gpgsign",
		"gc.auto", "maintenance.auto", "protocol.allow", "advice.detachedhead",
	}
	listing := func(keys ...string) []byte { return []byte(strings.Join(keys, "\x00") + "\x00") }
	if err := checkConfigKeys(listing(tmpl...)); err != nil {
		t.Fatalf("template keys: %v", err)
	}
	if err := checkConfigKeys(nil); err != nil {
		t.Errorf("empty config: %v", err)
	}
	if err := checkConfigKeys(listing("Core.HooksPath")); err != nil {
		t.Errorf("case: %v", err)
	}
	for _, k := range []string{
		"filter.evil.clean", "filter.lfs.process", "diff.external", "diff.x.textconv", "diff.x.command",
		"merge.x.driver", "alias.status", "include.path", "includeif.gitdir:/work/.path", "core.pager",
		"core.editor", "core.sshcommand", "core.askpass", "core.gitproxy", "core.worktree", "core.attributesfile",
		"core.alternaterefscommand", "credential.helper", "gpg.program", "extensions.worktreeconfig",
		"remote.origin.url", "uploadpack.packobjectshook", "submodule.x.update", "sequence.editor", "commit.template",
		"diff.orderfile", "status.showuntrackedfiles", "",
	} {
		if err := checkConfigKeys(listing(append(append([]string(nil), tmpl...), k)...)); codeOf(err) != execproto.ErrInvalidState {
			t.Errorf("%q: %v, want invalid_state", k, err)
		}
	}
	if err := checkConfigKeys([]byte("core.bare\x00filter.x.cle")); codeOf(err) != execproto.ErrInvalidState {
		t.Errorf("unterminated listing: %v, want invalid_state", err)
	}
}

// TestCheckStatusOutput: the deny-list excludes keep denied paths out of
// status (A14 §6); this check fails closed when one is there anyway (an
// NTFS 8.3 alias, which no pathspec expresses) or the output has a shape
// it cannot read.
func TestCheckStatusOutput(t *testing.T) {
	z := func(recs ...string) []byte { return []byte(strings.Join(recs, "\x00") + "\x00") }
	h := strings.Repeat("0", 40)
	ok := z("# branch.oid "+h, "# branch.head warden/x",
		"1 .M N... 100644 100644 100644 "+h+" "+h+" src/sp ace.txt",
		"2 R. N... 100644 100644 100644 "+h+" "+h+" R100 new.txt", "old.txt",
		"u UU N... 100644 100644 100644 100644 "+h+" "+h+" "+h+" conflict.txt",
		"? untracked.txt", "! ignored.txt")
	if err := checkStatusOutput(ok); err != nil {
		t.Fatalf("clean status: %v", err)
	}
	if err := checkStatusOutput(nil); err != nil {
		t.Errorf("empty: %v", err)
	}
	denied := map[string][]byte{
		"modified .env":   z("1 .M N... 100644 100644 100644 " + h + " " + h + " .env"),
		"untracked key":   z("? certs/server.KEY"),
		"ignored aws":     z("! home/.aws/credentials"),
		"rename to":       z("2 R. N... 100644 100644 100644 "+h+" "+h+" R100 .env", "x"),
		"rename from":     z("2 R. N... 100644 100644 100644 "+h+" "+h+" R100 x", "id_rsa"),
		"unmerged":        z("u UU N... 100644 100644 100644 100644 " + h + " " + h + " " + h + " .npmrc"),
		"8.3 short alias": z("1 .M N... 100644 100644 100644 " + h + " " + h + " ENV~1"),
	}
	for name, out := range denied {
		if err := checkStatusOutput(out); codeOf(err) != execproto.ErrDenyList {
			t.Errorf("%s: %v, want deny_list", name, err)
		}
	}
	for name, out := range map[string][]byte{
		"unknown record": z("x what"),
		"short record":   z("1 .M N..."),
		"rename no orig": []byte("2 R. N... 100644 100644 100644 " + h + " " + h + " R100 x\x00"),
	} {
		if err := checkStatusOutput(out); codeOf(err) != execproto.ErrIOError {
			t.Errorf("%s: %v, want io_error", name, err)
		}
	}
}

// TestCheckDiffOutput: a section of a deny-listed path in a diff is
// refused, whichever way git spelled its name (plain, with spaces,
// C-quoted, combined, a rename header); a "diff " line that cannot be read
// is refused too, so the check cannot be talked past by an odd name. A
// content line can never start with "diff " (hunk lines carry a prefix).
func TestCheckDiffOutput(t *testing.T) {
	ok := "diff --git a/src/sp ace.txt b/src/sp ace.txt\nindex 1..2 100644\n--- a/src/sp ace.txt\t\n+++ b/src/sp ace.txt\t\n@@ -1 +1 @@\n-diff --git a/.env b/.env\n+x\n" +
		"diff --git \"a/n\\303\\251.txt\" \"b/n\\303\\251.txt\"\n" +
		" src/x | 1 +\n 1 file changed\n"
	if err := checkDiffOutput([]byte(ok)); err != nil {
		t.Fatalf("clean diff: %v", err)
	}
	denied := map[string]string{
		"plain":        "diff --git a/.env b/.env\n+SECRET\n",
		"nested":       "diff --git a/config/.env.prod b/config/.env.prod\n",
		"case":         "diff --git a/.ENV b/.ENV\n",
		"quoted":       "diff --git \"a/sub/\\151d_rsa\" \"b/sub/\\151d_rsa\"\n",
		"quoted space": "diff --git \"a/my dir/.env\\t\" \"b/my dir/.env\\t\"\nx\ndiff --git \"a/k\\\".pem\" \"b/k\\\".pem\"\n",
		"combined":     "diff --cc .npmrc\n",
		"combined q":   "diff --cc \"x/\\151d_rsa\"\n",
		// Rename and copy headers do not occur with --no-renames; they are
		// checked anyway, after a header the check can read.
		"rename from":   "diff --git a/y b/y\nrename from .env\nrename to y\n",
		"copy to":       "diff --git a/x b/x\ncopy from x\ncopy to id_ed25519\n",
		"8.3 alias":     "diff --git a/ENV~1 b/ENV~1\n",
		"second header": "diff --git a/ok b/ok\n+x\ndiff --git a/.aws/config b/.aws/config\n",
	}
	for name, out := range denied {
		if err := checkDiffOutput([]byte(out)); codeOf(err) != execproto.ErrDenyList {
			t.Errorf("%s: %v, want deny_list", name, err)
		}
	}
	for name, out := range map[string]string{
		"names differ":      "diff --git a/x b/.env\n",
		"no prefix":         "diff --git x x\n",
		"unknown diff line": "diff -r a b\n",
		"bad quote":         "diff --git \"a/x \"b/x\"\n",
		"bad escape":        "diff --git \"a/\\q\" \"b/\\q\"\n",
		"trailing junk":     "diff --git \"a/x\" \"b/x\" z\n",
	} {
		if err := checkDiffOutput([]byte(out)); codeOf(err) != execproto.ErrIOError {
			t.Errorf("%s: %v, want io_error", name, err)
		}
	}
}

// TestCheckNameList: the staged-names check before a commit (A14 §6: DENY
// keeps denied paths out of every commit, also one an approved command
// staged by hand).
func TestCheckNameList(t *testing.T) {
	if err := checkNameList([]byte("src/a.go\x00docs/b.md\x00")); err != nil {
		t.Errorf("clean: %v", err)
	}
	if err := checkNameList([]byte("src/a.go\x00.env\x00")); codeOf(err) != execproto.ErrDenyList {
		t.Errorf(".env staged: %v, want deny_list", err)
	}
	if err := checkNameList([]byte("src/a.go")); codeOf(err) != execproto.ErrIOError {
		t.Errorf("unterminated: %v, want io_error", err)
	}
}

func TestUnquoteC(t *testing.T) {
	cases := []struct{ in, val, rest string }{
		{`"a/b" x`, "a/b", " x"},
		{`"n\303\251"`, "n\u00e9", ""},
		{`"\a\b\t\n\v\f\r\"\\"`, "\a\b\t\n\v\f\r\"\\", ""},
		{`"\001x"`, "\x01x", ""},
	}
	for _, c := range cases {
		v, r, ok := unquoteC(c.in)
		if !ok || v != c.val || r != c.rest {
			t.Errorf("unquoteC(%q) = %q, %q, %v", c.in, v, r, ok)
		}
	}
	for _, in := range []string{`a`, `"abc`, `"\x41"`, `"\4"`, `"\400"`, `"\`, ``} {
		if _, _, ok := unquoteC(in); ok {
			t.Errorf("unquoteC(%q) accepted", in)
		}
	}
}

// TestCompleteLines: output cut by the cap is checked and returned only up
// to its last complete line or record, so a cut never leaves half a header
// that the check could misread.
func TestCompleteLines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\nb\n", "a\nb\n"},
		{"a\nb", "a\n"},
		{"diff --git a/.e", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := string(completeLines([]byte(c.in), '\n')); got != c.want {
			t.Errorf("completeLines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := string(completeLines([]byte("? a\x00? .e"), 0)); got != "? a\x00" {
		t.Errorf("NUL records: %q", got)
	}
}
