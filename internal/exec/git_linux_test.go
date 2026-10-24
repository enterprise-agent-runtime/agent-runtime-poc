//go:build linux

// sandboxed: these tests run the real git against a temporary repository
// each test creates. The fixture commands (init, add, commit, checkout,
// rev-parse) are the test's own fixed commands with a fixed environment
// that keeps git away from the user's configuration (GIT_CONFIG_GLOBAL and
// GIT_CONFIG_SYSTEM are /dev/null, HOME is a temp dir); everything else
// runs through exec.git.run and the executor's process table. Nothing here
// runs an agent-requested process (INV-H). The secrets are fake fixture
// strings.

package exec_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/exec"
	"warden.dev/warden/internal/exec/exectest"
	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/tools"
)

const (
	gitULID     = "01jaxr8q7m2v9ktc3f6yh5n0pb"
	gitBranch   = "warden/" + gitULID
	gitEmail    = "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB"
	fakeSecret  = "FAKE-m29-base-secret"
	fakeProd    = "FAKE-m29-prod-secret"
	fakeNew     = "FAKE-m29-new-secret"
	fixtureName = "fixture"
)

// gitTemplate is Warden's configuration of the private gitdir (A14 §3
// step 4), as the daemon writes it.
const gitTemplate = `[core]
	repositoryformatversion = 0
	filemode = true
	bare = false
	logallrefupdates = true
	symlinks = true
	ignorecase = false
	hooksPath = /dev/null
	fsmonitor = false
	untrackedCache = false
	autocrlf = false
[user]
	name = warden
	email = ` + gitEmail + `
[commit]
	gpgSign = false
[tag]
	gpgSign = false
[gc]
	auto = 0
[maintenance]
	auto = false
[protocol]
	allow = never
[advice]
	detachedHead = false
`

type gitRig struct {
	t                         *testing.T
	base, work, gitDir, hooks string
	baseCommit                string
	procs                     *exec.Procs
	c                         *tools.Client
	conn                      *recordConn
}

type gitRigOpts struct {
	env      func(base string) []string // added to the executor's launch environment
	maxProcs int
	harness  bool
	setup    func(r *gitRig) // runs on the fixture before the base commit
}

func gitPath(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/bin/git", "/bin/git", "/usr/local/bin/git"} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	t.Skip("no git binary")
	return ""
}

// fixture runs one fixed git command of the test itself, outside the
// executor, with an environment that never reads the user's git
// configuration.
func (r *gitRig) fixture(args ...string) string {
	r.t.Helper()
	cmd := osexec.Command(gitPath(r.t), append([]string{"--git-dir=" + r.gitDir, "--work-tree=" + r.work, "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = r.work
	cmd.Env = []string{
		"HOME=" + r.base + "/fixture-home", "PATH=/usr/bin:/bin", "LC_ALL=C",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=" + fixtureName, "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=" + fixtureName, "GIT_COMMITTER_EMAIL=fixture@example.invalid",
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("fixture git %q: %v: %s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func (r *gitRig) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.work, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *gitRig) appendTo(rel, content string) {
	r.t.Helper()
	f, err := os.OpenFile(filepath.Join(r.work, rel), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		r.t.Fatal(err)
	}
}

// setConfig replaces the private gitdir's configuration with the template
// plus extra, as a sandboxed process with write access to /git could.
func (r *gitRig) setConfig(extra string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.gitDir, "config"), []byte(gitTemplate+extra), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func newGitRig(t *testing.T, o gitRigOpts) *gitRig {
	t.Helper()
	gitPath(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &gitRig{t: t, base: base, work: base + "/work", gitDir: base + "/git", hooks: base + "/empty"}
	for _, d := range []string{"work", "git", "scratch/home", "empty", "outside", "fixture-home", "tmpl"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The fixture: the private repository of A14 §1 (gitdir S/git, worktree
	// S/worktree with a relative .git file), created from an empty
	// template, with Warden's configuration and a base commit that holds
	// committed deny-listed files (as injection-lab does).
	cmd := osexec.Command(gitPath(t), "init", "-q", "--template="+base+"/tmpl", "--initial-branch="+gitBranch, "--separate-git-dir="+r.gitDir, r.work)
	cmd.Env = []string{"HOME=" + base + "/fixture-home", "PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	r.setConfig("")
	if err := os.WriteFile(filepath.Join(r.work, ".git"), []byte("gitdir: ../git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.write("src/app.txt", "hello\n")
	r.write("notes.txt", "notes\n")
	r.write(".env", "API_TOKEN="+fakeSecret+"\n")
	r.write("config/.env.prod", fakeProd+"\n")
	if o.setup != nil {
		o.setup(r)
	}
	r.fixture("add", "-A")
	r.fixture("commit", "-q", "-m", "base")
	r.baseCommit = r.fixture("rev-parse", "HEAD")
	if err := os.Chmod(r.hooks, 0o500); err != nil {
		t.Fatal(err)
	}

	tree, err := exec.OpenTree([]exec.Root{
		{Name: execproto.RootWorktree, Path: r.work, Mode: execproto.RootRW},
		{Name: execproto.RootScratch, Path: base + "/scratch", Mode: execproto.RootRW},
		{Name: execproto.RootGit, Path: r.gitDir, Mode: execproto.RootRW},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tree.Close() })
	limits, err := exec.ParseChildLimits("cpu=600,fsize=1073741824,nofile=256,core=0")
	if err != nil {
		t.Fatal(err)
	}
	maxProcs := o.maxProcs
	if maxProcs == 0 {
		maxProcs = 4
	}
	pcfg := exec.ProcConfig{
		Tree: tree, Mode: execproto.ModeTool, Limits: limits, MaxProcs: maxProcs,
		Env: launchEnv(base, o.env), Clock: exectest.NewFakeClock(),
	}
	if o.harness {
		pcfg.Mode = execproto.ModeHarness
		pcfg.Harness = &exec.HarnessSpec{Version: 1, HarnessID: "copilot", RunMode: "colocated", Argv: []string{helperBin(t), "helper", "pwd"}}
	}
	a, b := net.Pipe()
	r.conn = &recordConn{Conn: a}
	s := newServer(t, r.conn)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if r.procs, err = exec.RegisterProc(ctx, s, pcfg); err != nil {
		t.Fatal(err)
	}
	gcfg := exec.GitConfig{GitDir: r.gitDir, SessionBranch: gitBranch, BaseCommit: r.baseCommit, HooksDir: r.hooks}
	if err := exec.RegisterGit(s, r.procs, gcfg); err != nil {
		t.Fatal(err)
	}
	stop := startServer(t, s, r.conn)
	r.c = tools.New(context.Background(), b, func(execproto.IONotification) {})
	t.Cleanup(func() { r.procs.Close() })
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

func (r *gitRig) run(p execproto.GitRunParams) (execproto.GitRunResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	return r.c.Git(ctx, p)
}

func (r *gitRig) mustRun(p execproto.GitRunParams) execproto.GitRunResult {
	r.t.Helper()
	res, err := r.run(p)
	if err != nil {
		r.t.Fatalf("git.run %+v: %v", p, err)
	}
	return res
}

func diffOp(o execproto.GitDiffOptions) execproto.GitRunParams {
	return execproto.GitRunParams{Op: execproto.GitDiff, Diff: &o}
}

func commitOp(msg string, paths ...string) execproto.GitRunParams {
	return execproto.GitRunParams{Op: execproto.GitCommit, Commit: &execproto.GitCommitOptions{Message: msg, Paths: paths}}
}

var statusOp = execproto.GitRunParams{Op: execproto.GitStatus}

// noSecretOnWire: no fixture secret ever reaches the daemon, in a result,
// an error or a notification.
func (r *gitRig) noSecretOnWire() {
	r.t.Helper()
	w := r.conn.written()
	for _, s := range []string{fakeSecret, fakeProd, fakeNew, "API_TOKEN"} {
		if bytes.Contains(w, []byte(s)) {
			r.t.Errorf("%q reached the wire", s)
		}
	}
}

func detailOf(err error) string {
	var e *execproto.Error
	if errors.As(err, &e) {
		return e.Data.Detail
	}
	return ""
}

// TestGitRun_StatusLeavesOutDenyListed: A14 §6 status with the DENY
// excludes. A modified committed .env, an untracked key and an AWS
// credentials file are not in the output; everything else is.
func TestGitRun_StatusLeavesOutDenyListed(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	r.appendTo(".env", fakeNew+"\n")
	r.appendTo("config/.env.prod", fakeNew+"\n")
	r.appendTo("src/app.txt", "more\n")
	r.write("new.txt", "n\n")
	r.write("id_rsa", "k\n")
	r.write("home/.aws/credentials", "c\n")
	res := r.mustRun(statusOp)
	if res.ExitCode != 0 || res.Encoding != execproto.EncodingUTF8 || res.Stderr != "" {
		t.Fatalf("status: %+v", res)
	}
	for _, want := range []string{"src/app.txt", "new.txt", "# branch.head " + gitBranch} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("status lacks %q: %q", want, res.Stdout)
		}
	}
	for _, not := range []string{".env", "id_rsa", ".aws"} {
		if strings.Contains(res.Stdout, not) {
			t.Errorf("status names %q: %q", not, res.Stdout)
		}
	}
	r.noSecretOnWire()
}

// TestGitRun_DiffNeverCarriesDenyListedContent is INV-I for git object
// reads (the M2-12 follow-up): the masks hide only the working tree, but
// the committed .env is in the objects, and a diff against base would
// print both its old and its new content. With every form of against, a
// full diff leaves the deny-listed files out; a diff that names one is
// refused, however the name is spelled (case, "..", a symlink).
func TestGitRun_DiffNeverCarriesDenyListedContent(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	r.appendTo(".env", fakeNew+"\n")
	r.appendTo("config/.env.prod", fakeNew+"\n")
	r.appendTo("src/app.txt", "more\n")
	if err := os.Symlink(".env", filepath.Join(r.work, "envlink")); err != nil {
		t.Fatal(err)
	}
	three := 3
	for _, against := range []string{"", "base", "HEAD", r.baseCommit} {
		for _, stat := range []bool{false, true} {
			res := r.mustRun(diffOp(execproto.GitDiffOptions{Against: against, StatOnly: stat, ContextLines: &three}))
			if !strings.Contains(res.Stdout, "src/app.txt") {
				t.Errorf("against %q stat %v: no src/app.txt: %q", against, stat, res.Stdout)
			}
			if strings.Contains(res.Stdout, ".env") || strings.Contains(res.Stdout, "FAKE-m29") {
				t.Errorf("against %q stat %v: deny-listed content: %q", against, stat, res.Stdout)
			}
		}
	}
	if res := r.mustRun(diffOp(execproto.GitDiffOptions{Paths: []string{"src"}})); !strings.Contains(res.Stdout, "+more") {
		t.Errorf("diff of src: %q", res.Stdout)
	}
	for _, p := range []string{".env", ".ENV", "config/.env.prod", "src/../.env", "/work/../.env", "envlink", "config"} {
		res, err := r.run(diffOp(execproto.GitDiffOptions{Paths: []string{"src/app.txt", p}}))
		if p == "config" {
			// A directory that holds a deny-listed file is not itself
			// denied; the excludes keep the file out.
			if err != nil || strings.Contains(res.Stdout, "FAKE-m29") {
				t.Errorf("paths [config]: %v %q", err, res.Stdout)
			}
			continue
		}
		if p == "/work/../.env" {
			// Not under the test's worktree path: refused as outside or escape.
			if err == nil {
				t.Errorf("paths %q accepted", p)
			}
			continue
		}
		if !errors.Is(err, execproto.ErrDenyList) {
			t.Errorf("paths %q: %v, want deny_list", p, err)
		}
	}
	if _, err := r.run(diffOp(execproto.GitDiffOptions{Paths: []string{"../outside"}})); !errors.Is(err, execproto.ErrPathEscape) {
		t.Errorf("paths ../outside: %v, want path_escape", err)
	}
	r.noSecretOnWire()
}

// TestGitRun_OutputCheckCatchesWhatPathspecsMiss: an NTFS 8.3 alias
// (ENV~1 opens .env on a volume with short names, D-055) matches no
// pathspec exclude, so only the output check stands between its content
// and the model. It must refuse, not filter silently or pass.
func TestGitRun_OutputCheckCatchesWhatPathspecsMiss(t *testing.T) {
	r := newGitRig(t, gitRigOpts{setup: func(r *gitRig) { r.write("ENV~1", fakeSecret+"\n") }})
	r.appendTo("ENV~1", fakeNew+"\n")
	if _, err := r.run(diffOp(execproto.GitDiffOptions{})); !errors.Is(err, execproto.ErrDenyList) {
		t.Errorf("diff: %v, want deny_list", err)
	}
	if _, err := r.run(statusOp); !errors.Is(err, execproto.ErrDenyList) {
		t.Errorf("status: %v, want deny_list", err)
	}
	// --stat lists names too (review of 41a3818). The alias also sits under
	// a directory long enough that git's default stat width would print it
	// as ".../ENV~1"; the explicit name widths keep it whole, so it is
	// refused as deny_list, not merely as uncheckable.
	if _, err := r.run(diffOp(execproto.GitDiffOptions{StatOnly: true})); !errors.Is(err, execproto.ErrDenyList) {
		t.Errorf("diff --stat: %v, want deny_list", err)
	}
	long := strings.Repeat("d", 120)
	r.write(long+"/ENV~1", fakeSecret+"\n")
	r.fixture("add", long+"/ENV~1")
	r.fixture("commit", "-q", "-m", "long")
	if err := os.Remove(filepath.Join(r.work, "ENV~1")); err != nil {
		t.Fatal(err)
	}
	r.fixture("commit", "-q", "-a", "-m", "drop the short one")
	r.appendTo(long+"/ENV~1", fakeNew+"\n")
	if _, err := r.run(diffOp(execproto.GitDiffOptions{Against: "HEAD", StatOnly: true})); !errors.Is(err, execproto.ErrDenyList) {
		t.Errorf("diff --stat of a long path: %v, want deny_list", err)
	}
	r.noSecretOnWire()
}

// TestGitRun_StatPassesLongNames: the stat check must not refuse a long
// name that is not deny-listed (git would abbreviate it without the
// explicit widths, and the check refuses abbreviated names).
func TestGitRun_StatPassesLongNames(t *testing.T) {
	long := strings.Repeat("d", 120)
	r := newGitRig(t, gitRigOpts{setup: func(r *gitRig) { r.write(long+"/app.txt", "x\n") }})
	r.appendTo(long+"/app.txt", "y\n")
	three := 3
	res := r.mustRun(diffOp(execproto.GitDiffOptions{StatOnly: true, ContextLines: &three}))
	if !strings.Contains(res.Stdout, long+"/app.txt") {
		t.Errorf("stat: %q", res.Stdout)
	}
	// stat_only is the stat alone: -U<n> would make git print the patch
	// as well (it implies -p), content the caller did not ask for.
	if strings.Contains(res.Stdout, "diff --git") || strings.Contains(res.Stdout, "@@") || strings.Contains(res.Stdout, "+y") {
		t.Errorf("stat_only returned the patch: %q", res.Stdout)
	}
}

// TestGitRun_ObjectReadsAndOptionsRefused: show HEAD:.env, cat-file -p
// HEAD:.env and every other object read by path have no exec.git.run
// operation, params carry no argv, and against cannot become a revision
// with a path or an option (-c, --exec-path).
func TestGitRun_ObjectReadsAndOptionsRefused(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	for _, op := range []string{"show", "cat-file", "grep", "log", "blame", "archive", "config", "fetch", "push", "-c", "--exec-path=/tmp"} {
		if _, err := r.run(execproto.GitRunParams{Op: execproto.GitOp(op)}); !errors.Is(err, execproto.ErrInvalidArgument) {
			t.Errorf("op %q: %v, want invalid_argument", op, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	for _, raw := range []map[string]any{
		{"op": "status", "argv": []string{"show", "HEAD:.env"}},
		{"op": "diff", "diff": map[string]any{"against": "base", "args": []string{"-c", "diff.external=/x"}}},
		{"op": "status", "git_dir": "/elsewhere"},
	} {
		var res execproto.GitRunResult
		err := r.c.Call(ctx, execproto.MethodGitRun, raw, &res)
		var re *execproto.RPCError
		if !errors.As(err, &re) || re.Code != execproto.RPCInvalidParams {
			t.Errorf("%v: %v, want invalid params", raw, err)
		}
	}
	for _, a := range []string{"HEAD:.env", "base:.env", ":.env", "-c", "--exec-path=/tmp", "--output=/work/x", "HEAD~1", gitBranch} {
		if _, err := r.run(diffOp(execproto.GitDiffOptions{Against: a})); !errors.Is(err, execproto.ErrInvalidArgument) {
			t.Errorf("against %q: %v, want invalid_argument", a, err)
		}
	}
	// A path that looks like an option is a literal pathspec after "--".
	if res, err := r.run(diffOp(execproto.GitDiffOptions{Paths: []string{"--output=x", "-c"}})); err != nil || res.Stdout != "" {
		t.Errorf("option-shaped paths: %v %q", err, res.Stdout)
	}
	if _, err := os.Stat(filepath.Join(r.work, "x")); err == nil {
		t.Error("--output=x was taken as an option")
	}
	r.noSecretOnWire()
}

// TestGitRun_AgainstMustBeACommit: an object id that is a blob (the
// committed .env's) or does not exist is refused before any diff runs,
// with a fixed detail that quotes neither the id nor git's message.
func TestGitRun_AgainstMustBeACommit(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	blob := r.fixture("rev-parse", "HEAD:.env")
	tree := r.fixture("rev-parse", "HEAD^{tree}")
	for _, id := range []string{blob, tree, strings.Repeat("0", 40)} {
		_, err := r.run(diffOp(execproto.GitDiffOptions{Against: id}))
		if !errors.Is(err, execproto.ErrInvalidArgument) {
			t.Errorf("against %s: %v, want invalid_argument", id, err)
		}
		if d := detailOf(err); strings.Contains(d, id) || strings.Contains(d, "fatal") || strings.Contains(d, "error:") {
			t.Errorf("detail quotes git: %q", d)
		}
	}
	r.noSecretOnWire()
}

// TestGitRun_CommitOnlyOnTheSessionBranch: A06 §3.1 "exec.git.run commit
// refuses unless HEAD is refs/heads/<session-branch>"; identity warden
// <ses_…> (A06 l.780) whatever the configuration says; deny-listed files
// are never committed, also not when staged by hand; nothing to commit is
// exit 1, never an empty commit.
func TestGitRun_CommitOnlyOnTheSessionBranch(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	r.setConfig("[user]\n\tname = mallory\n\temail = mallory@example.invalid\n")
	r.appendTo(".env", fakeNew+"\n")
	r.appendTo("src/app.txt", "more\n")
	res := r.mustRun(commitOp("m29 change"))
	if res.ExitCode != 0 || res.Head == nil || res.Head.Branch != gitBranch || len(res.Head.Commit) != 40 {
		t.Fatalf("commit: %+v", res)
	}
	if got := r.fixture("log", "-1", "--format=%an <%ae>|%cn <%ce>|%s"); got != "warden <"+gitEmail+">|warden <"+gitEmail+">|m29 change" {
		t.Errorf("identity: %q", got)
	}
	if got := r.fixture("rev-parse", "HEAD"); got != res.Head.Commit {
		t.Errorf("head.commit %s, HEAD %s", res.Head.Commit, got)
	}
	if files := r.fixture("show", "--name-only", "--format=", "HEAD"); files != "src/app.txt" {
		t.Errorf("committed files: %q", files)
	}

	// Nothing left to commit (the .env change is excluded).
	res = r.mustRun(commitOp("again"))
	if res.ExitCode != 1 || res.Stdout != "nothing to commit\n" {
		t.Errorf("nothing to commit: %+v", res)
	}
	if got := r.fixture("rev-parse", "HEAD"); got != r.fixture("rev-parse", gitBranch) {
		t.Error("branch moved")
	}

	// A deny-listed file staged by an approved command is refused.
	tip := r.fixture("rev-parse", "HEAD")
	r.fixture("add", ".env")
	if _, err := r.run(commitOp("staged secret")); !errors.Is(err, execproto.ErrDenyList) {
		t.Errorf("staged .env: %v, want deny_list", err)
	}
	if got := r.fixture("rev-parse", "HEAD"); got != tip {
		t.Error("a commit was made with .env staged")
	}
	r.fixture("reset", "-q", "--", ".env")

	// Another branch, and a detached HEAD.
	r.fixture("checkout", "-q", "-b", "main")
	r.appendTo("src/app.txt", "on main\n")
	if _, err := r.run(commitOp("on main")); !errors.Is(err, execproto.ErrBranchMismatch) {
		t.Errorf("on main: %v, want branch_mismatch", err)
	}
	if got := r.fixture("rev-parse", "main"); got != tip {
		t.Error("main moved")
	}
	r.fixture("checkout", "-q", "--detach")
	if _, err := r.run(commitOp("detached")); !errors.Is(err, execproto.ErrBranchMismatch) {
		t.Errorf("detached: %v, want branch_mismatch", err)
	}
	if got := r.fixture("rev-parse", "HEAD"); got != tip {
		t.Error("detached HEAD moved")
	}
	r.noSecretOnWire()
}

// TestGitRun_CommitMessage: A14 §6 bounds the message at 4096 bytes; a
// message that looks like options is a message.
func TestGitRun_CommitMessage(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	for _, m := range []string{"", strings.Repeat("m", 4097), "a\x00b"} {
		r.appendTo("src/app.txt", "x\n")
		if _, err := r.run(commitOp(m)); !errors.Is(err, execproto.ErrInvalidArgument) {
			t.Errorf("message of %d bytes: %v, want invalid_argument", len(m), err)
		}
	}
	msg := "--exec-path=/tmp -c core.hooksPath=/work --amend"
	r.mustRun(commitOp(msg))
	if got := r.fixture("log", "-1", "--format=%B"); got != msg {
		t.Errorf("message %q", got)
	}
	if got := r.fixture("rev-list", "--count", "HEAD"); got != "2" {
		t.Errorf("%s commits, want 2 (no amend)", got)
	}
	r.appendTo("src/app.txt", "y\n")
	r.mustRun(commitOp(strings.Repeat("m", 4096)))
}

// TestGitRun_NoHookRuns is the S4 trace of A14 §11 for exec.git.run:
// hooks in the gitdir's hooks/ and under a core.hooksPath that a package
// script wrote into the configuration never run, on status, diff or
// commit (core.hooksPath is overridden on the command line with the empty
// --hooks-dir).
func TestGitRun_NoHookRuns(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	names := []string{"pre-commit", "commit-msg", "prepare-commit-msg", "post-commit", "reference-transaction", "post-index-change", "pre-auto-gc"}
	for _, dir := range []string{filepath.Join(r.gitDir, "hooks"), filepath.Join(r.work, "hooks")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			script := "#!/bin/sh\ntouch " + r.base + "/outside/marker-" + n + "\n"
			if err := os.WriteFile(filepath.Join(dir, n), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.setConfig("[core]\n\thooksPath = " + r.work + "/hooks\n")
	r.appendTo("src/app.txt", "more\n")
	r.mustRun(statusOp)
	r.mustRun(diffOp(execproto.GitDiffOptions{}))
	r.mustRun(commitOp("with hooks around"))
	ents, err := os.ReadDir(filepath.Join(r.base, "outside"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		t.Errorf("hook ran: %s", e.Name())
	}
}

// TestGitRun_RepositoryConfigCannotRunPrograms: the gitdir is writable from
// the sandbox, so a process an approved command left behind can add a
// filter driver (run by git add and status under any driver name, which no
// -c override can name), an alias, a pager, an include or a commondir. Each
// is refused with invalid_state before git runs, and nothing runs.
func TestGitRun_RepositoryConfigCannotRunPrograms(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	evil := filepath.Join(r.base, "outside", "evil.sh")
	marker := filepath.Join(r.base, "outside", "marker")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\ntouch "+marker+"\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	inc := filepath.Join(r.base, "outside", "inc")
	if err := os.WriteFile(inc, []byte("[filter \"evil\"]\n\tclean = "+evil+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.write(".gitattributes", "* filter=evil diff=evil\n")
	r.appendTo("src/app.txt", "more\n")
	cases := map[string]string{
		"filter":       "[filter \"evil\"]\n\tclean = " + evil + "\n\tsmudge = " + evil + "\n",
		"filter case":  "[Filter \"evil\"]\n\tClean = " + evil + "\n",
		"diff driver":  "[diff \"evil\"]\n\ttextconv = " + evil + "\n\tcommand = " + evil + "\n",
		"external":     "[diff]\n\texternal = " + evil + "\n",
		"alias":        "[alias]\n\tstatus = !" + evil + "\n",
		"pager":        "[core]\n\tpager = " + evil + "\n",
		"include":      "[include]\n\tpath = " + inc + "\n",
		"includeIf":    "[includeIf \"gitdir:/\"]\n\tpath = " + inc + "\n",
		"worktree cfg": "[extensions]\n\tworktreeConfig = true\n",
	}
	ops := []execproto.GitRunParams{statusOp, diffOp(execproto.GitDiffOptions{}), commitOp("x"), {Op: execproto.GitHead}}
	for name, extra := range cases {
		r.setConfig(extra)
		for _, op := range ops {
			if _, err := r.run(op); !errors.Is(err, execproto.ErrInvalidState) {
				t.Errorf("%s, %s: %v, want invalid_state", name, op.Op, err)
			}
		}
	}
	r.setConfig("")
	if err := os.WriteFile(filepath.Join(r.gitDir, "commondir"), []byte(r.base+"/outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(statusOp); !errors.Is(err, execproto.ErrInvalidState) {
		t.Errorf("commondir: %v, want invalid_state", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a program named in the repository configuration ran")
	}
	if err := os.Remove(filepath.Join(r.gitDir, "commondir")); err != nil {
		t.Fatal(err)
	}
	// With the template alone the same tree works (the attributes name a
	// driver no configuration defines).
	r.mustRun(statusOp)
	if _, err := os.Stat(marker); err == nil {
		t.Error("evil.sh ran with the template configuration")
	}
}

func launchEnv(base string, extra func(string) []string) []string {
	env := baseEnv(base)
	if extra != nil {
		env = append(env, extra(base)...)
	}
	return env
}

// TestGitRun_EnvironmentIsFixed: nothing of the executor's environment
// reaches git except PATH (A06 §8, A14 §2 "clears the environment first"):
// a GIT_TRACE would write a file, GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE
// would move the repository, GIT_AUTHOR_NAME would forge the identity, and
// GIT_CONFIG_PARAMETERS / GIT_CONFIG_COUNT would add configuration. The
// launch environment is the one place these could come from, since
// exec.git.run params carry none.
func TestGitRun_EnvironmentIsFixed(t *testing.T) {
	r := newGitRig(t, gitRigOpts{env: func(base string) []string {
		out := base + "/outside"
		return []string{
			"GIT_TRACE=" + out + "/trace", "GIT_TRACE2_EVENT=" + out + "/trace2",
			"GIT_DIR=" + out, "GIT_WORK_TREE=" + out, "GIT_INDEX_FILE=" + out + "/index",
			"GIT_CONFIG_PARAMETERS='alias.status'='!touch " + out + "/alias'",
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=" + base + "/work/hooks",
			"GIT_EXEC_PATH=" + out, "GIT_AUTHOR_NAME=mallory", "GIT_COMMITTER_EMAIL=mallory@example.invalid",
			"GIT_CONFIG_GLOBAL=" + out + "/gc", "GIT_CONFIG_NOSYSTEM=0",
		}
	}})
	if err := os.MkdirAll(filepath.Join(r.work, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.work, "hooks", "post-commit"), []byte("#!/bin/sh\ntouch "+r.base+"/outside/hook\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.base, "outside", "gc"), []byte("[core]\n\tpager = touch "+r.base+"/outside/pager\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.appendTo("src/app.txt", "more\n")
	if res := r.mustRun(statusOp); !strings.Contains(res.Stdout, "src/app.txt") {
		t.Errorf("status ran elsewhere: %q", res.Stdout)
	}
	r.mustRun(commitOp("env"))
	if got := r.fixture("log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != "warden <"+gitEmail+">|warden <"+gitEmail+">" {
		t.Errorf("identity: %q", got)
	}
	ents, err := os.ReadDir(filepath.Join(r.base, "outside"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.Name() != "gc" {
			t.Errorf("the launch environment reached git: %s exists", e.Name())
		}
	}
}

// TestGitRun_ChildrenAreHandles: git runs as a handle of the process table
// (PROGRESS M2-8, "Left for M2-9"), so it takes a --max-procs slot like
// any child, the stray sweep sees it, and it is killed with its call. With
// the one slot taken, exec.git.run is busy; once it is free, it runs.
func TestGitRun_ChildrenAreHandles(t *testing.T) {
	r := newGitRig(t, gitRigOpts{maxProcs: 1})
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	sp, err := r.c.Spawn(ctx, spawnParams(helper(t, "ignore-term", filepath.Join(r.base, "outside", "started"))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.run(statusOp); !errors.Is(err, execproto.ErrBusy) {
		t.Errorf("status with the only slot taken: %v, want busy", err)
	}
	if _, err := r.c.Signal(ctx, execproto.SignalParams{Handle: sp.Handle, Signal: execproto.SignalKILL}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Wait(ctx, execproto.WaitParams{Handle: sp.Handle}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r.mustRun(statusOp) // the slot is released after every run
	}
	r.appendTo("src/app.txt", "more\n")
	if res := r.mustRun(commitOp("six processes, one slot", "src")); res.ExitCode != 0 { // config, head, add, staged, commit, rev-parse
		t.Errorf("commit: %+v", res)
	}
}

// TestGitRun_HarnessModeForbidden: A06 §4.2, exec.git.run is not available
// in harness mode.
func TestGitRun_HarnessModeForbidden(t *testing.T) {
	r := newGitRig(t, gitRigOpts{harness: true})
	if _, err := r.run(statusOp); !errors.Is(err, execproto.ErrModeForbidden) {
		t.Errorf("harness mode: %v, want mode_forbidden", err)
	}
}

// TestGitRun_Head: the branch lookup for the PDP (A14 §6 head).
func TestGitRun_Head(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	res := r.mustRun(execproto.GitRunParams{Op: execproto.GitHead})
	if res.Head == nil || res.Head.Commit != r.baseCommit || res.Head.Branch != gitBranch || res.Stdout != "" {
		t.Errorf("head: %+v", res)
	}
	r.fixture("checkout", "-q", "--detach")
	res = r.mustRun(execproto.GitRunParams{Op: execproto.GitHead})
	if res.Head == nil || res.Head.Commit != r.baseCommit || res.Head.Branch != "" {
		t.Errorf("detached head: %+v", res)
	}
}

// TestGitRun_ErrorDetailsAreFixed: A06 §4.4, an error's detail is fixed
// text. Git's stderr quotes paths, object ids and content; none of it
// reaches the daemon.
func TestGitRun_ErrorDetailsAreFixed(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	r.write("ignored/x", "x\n")
	r.write(".gitignore", "ignored/\n")
	blob := r.fixture("rev-parse", "HEAD:.env")
	var errs []error
	_, err := r.run(commitOp("ignored", "ignored/x")) // git add refuses an ignored path
	errs = append(errs, err)
	_, err = r.run(diffOp(execproto.GitDiffOptions{Against: blob}))
	errs = append(errs, err)
	r.setConfig("[filter \"x\"]\n\tclean = cat\n")
	_, err = r.run(statusOp)
	errs = append(errs, err)
	for i, err := range errs {
		if err == nil {
			t.Errorf("case %d: no error", i)
			continue
		}
		d := detailOf(err)
		for _, bad := range []string{r.base, blob, "fatal", "error:", "ignored/x", "filter.x", "hint"} {
			if strings.Contains(d, bad) {
				t.Errorf("case %d: detail %q quotes %q", i, d, bad)
			}
		}
	}
	if bytes.Contains(r.conn.written(), []byte(r.base)) {
		t.Error("a sandbox path reached the wire")
	}
}

// TestGitRun_PathsGoToGitResolved: git gets the canonical root-relative
// path that Tree.Resolve produced and checked, not the caller's spelling.
// An absolute sandbox path and a path through a symlinked directory name
// the same file as src/app.txt; given to git as typed, the first is
// outside the repository and the second "beyond a symbolic link", and the
// path git used would not be the path the deny-list was checked on.
func TestGitRun_PathsGoToGitResolved(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	r.appendTo("src/app.txt", "more\n")
	if err := os.Symlink("src", filepath.Join(r.work, "srclink")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{r.work + "/src/app.txt", "srclink/app.txt", "./src//app.txt"} {
		res, err := r.run(diffOp(execproto.GitDiffOptions{Paths: []string{p}}))
		if err != nil || !strings.Contains(res.Stdout, "+more") {
			t.Errorf("paths [%s]: %v %q", p, err, res.Stdout)
		}
	}
	res := r.mustRun(commitOp("through the link", "srclink/app.txt"))
	if res.ExitCode != 0 {
		t.Fatalf("commit: %+v", res)
	}
	if files := r.fixture("show", "--name-only", "--format=", "HEAD"); files != "src/app.txt" {
		t.Errorf("committed %q", files)
	}
	if _, err := r.run(commitOp("x", ".git/config")); !errors.Is(err, execproto.ErrOutsideRoots) {
		t.Errorf("commit .git/config: %v, want outside_roots", err)
	}
}

// TestGitRun_StderrNeverForwarded: A06 §4.4, git's own messages quote
// paths and content and are never forwarded, also not on a run that
// succeeds. With core.autocrlf (a key of Warden's template, which tooling
// may set) git warns on stderr for every LF file it compares; the result
// carries git's stdout only, and stderr is empty.
func TestGitRun_StderrNeverForwarded(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	r.setConfig("[core]\n\tautocrlf = true\n")
	r.appendTo("src/app.txt", "more\n")
	for _, op := range []execproto.GitRunParams{statusOp, diffOp(execproto.GitDiffOptions{})} {
		res := r.mustRun(op)
		if res.Stderr != "" || strings.Contains(res.Stdout, "warning") || strings.Contains(res.Stdout, "CRLF") {
			t.Errorf("%s: git's stderr reached the result: %q", op.Op, res.Stdout)
		}
		if !strings.Contains(res.Stdout, "src/app.txt") {
			t.Errorf("%s: %q", op.Op, res.Stdout)
		}
	}
}

// TestGitRun_StagedGitlinkAtDenyListedPath (review of 41a3818): a gitlink
// (mode 160000) staged at a deny-listed path is a change a commit would
// record there. The staged-names check runs with --ignore-submodules=none,
// so neither the default nor a .gitmodules "ignore = all" hides it.
func TestGitRun_StagedGitlinkAtDenyListedPath(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	r.appendTo("src/app.txt", "more\n")
	tip := r.fixture("rev-parse", "HEAD")
	for _, p := range []string{".env", "keys/id_rsa"} {
		r.fixture("update-index", "--add", "--cacheinfo", "160000,"+tip+","+p)
		for _, gm := range []bool{false, true} {
			if gm {
				r.write(".gitmodules", "[submodule \"x\"]\n\tpath = "+p+"\n\tignore = all\n")
			}
			if _, err := r.run(commitOp("gitlink at " + p)); !errors.Is(err, execproto.ErrDenyList) {
				t.Errorf("gitlink at %s (.gitmodules %v): %v, want deny_list", p, gm, err)
			}
			if got := r.fixture("rev-parse", "HEAD"); got != tip {
				t.Fatalf("gitlink at %s was committed", p)
			}
		}
		if err := os.Remove(filepath.Join(r.work, ".gitmodules")); err != nil {
			t.Fatal(err)
		}
		r.fixture("reset", "-q", "--", p, ".gitmodules")
	}
	if res := r.mustRun(commitOp("clean")); res.ExitCode != 0 {
		t.Errorf("commit after the gitlinks are gone: %+v", res)
	}
}

// fixtureGitIn runs one fixed fixture git command in dir, with the same
// environment as gitRig.fixture.
func fixtureGitIn(t *testing.T, base, dir string, args ...string) {
	t.Helper()
	cmd := osexec.Command(gitPath(t), append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{
		"HOME=" + base + "/fixture-home", "PATH=/usr/bin:/bin", "LC_ALL=C",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=" + fixtureName, "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=" + fixtureName, "GIT_COMMITTER_EMAIL=fixture@example.invalid",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture git %q: %v: %s", args, err, out)
	}
}

// TestGitRun_NestedGitdirRunsNoDriver (review evidence): a subdirectory of
// the worktree holds a .git file pointing at a gitdir outside, whose own
// configuration defines a filter, a textconv driver, an fsmonitor and a
// hooks path, and a .gitattributes there uses them. exec.git.run never
// starts a git in that repository (status and diff ignore submodules, add
// records the nested repository as a gitlink by reading its HEAD), so
// nothing it names runs: not on status, diff or commit, before or after
// the gitlink is committed.
func TestGitRun_NestedGitdirRunsNoDriver(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	out := filepath.Join(r.base, "outside")
	evil := filepath.Join(out, "evil.sh")
	marker := filepath.Join(out, "marker")
	if err := os.WriteFile(evil, []byte("#!/bin/sh\ntouch "+marker+"\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(r.work, "sub")
	fixtureGitIn(t, r.base, r.base, "init", "-q", "--template="+r.base+"/tmpl", "--separate-git-dir="+out+"/evilgit", sub)
	r.write("sub/file.txt", "inner\n")
	fixtureGitIn(t, r.base, sub, "add", "file.txt")
	fixtureGitIn(t, r.base, sub, "commit", "-q", "-m", "inner")
	hooks := filepath.Join(out, "evilhooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"pre-commit", "post-commit", "post-index-change", "reference-transaction"} {
		if err := os.WriteFile(filepath.Join(hooks, n), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(filepath.Join(out, "evilgit", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("[filter \"x\"]\n\tclean = " + evil + "\n\tsmudge = " + evil + "\n\tprocess = " + evil + "\n" +
		"[diff \"x\"]\n\ttextconv = " + evil + "\n\tcommand = " + evil + "\n" +
		"[core]\n\tfsmonitor = " + evil + "\n\thooksPath = " + hooks + "\n\tpager = " + evil + "\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r.write("sub/.gitattributes", "* filter=x diff=x\n")
	r.appendTo("sub/file.txt", "changed\n")
	r.appendTo("src/app.txt", "more\n")

	for round := 0; round < 2; round++ {
		r.mustRun(statusOp)
		r.mustRun(diffOp(execproto.GitDiffOptions{}))
		r.mustRun(diffOp(execproto.GitDiffOptions{Paths: []string{"sub"}}))
		r.mustRun(commitOp("with a nested repository"))
		r.appendTo("sub/file.txt", "again\n")
		r.appendTo("src/app.txt", "again\n")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a program named by the nested repository ran")
	}
	if got := r.fixture("ls-files", "-s", "sub"); !strings.HasPrefix(got, "160000 ") {
		t.Errorf("sub is not a gitlink: %q", got)
	}
}

// TestGitRun_HandlesAreNotTheModelsToSignal (review, optional): git's
// processes are handles of the table, but not handles the daemon was
// given: exec.proc.signal and exec.proc.wait treat them as unknown, so a
// model-driven signal cannot cut a git call short (and leave index.lock
// behind). A FIFO at objects/info/alternates holds git while the test
// signals every handle id it could have.
func TestGitRun_HandlesAreNotTheModelsToSignal(t *testing.T) {
	r := newGitRig(t, gitRigOpts{})
	fifo := filepath.Join(r.gitDir, "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		res execproto.GitRunResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.run(statusOp)
		done <- outcome{res, err}
	}()
	var w *os.File
	for until := time.Now().Add(hangGuard); w == nil; {
		fd, err := unix.Open(fifo, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		switch {
		case err == nil:
			w = os.NewFile(uintptr(fd), fifo)
		case time.Now().After(until):
			t.Fatal("git never opened objects/info/alternates")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	for i := 1; i <= 8; i++ {
		h := "ph_" + strconv.Itoa(i)
		if _, err := r.c.Signal(ctx, execproto.SignalParams{Handle: h, Signal: execproto.SignalKILL}); !errors.Is(err, execproto.ErrInvalidState) {
			t.Errorf("signal %s during a git run: %v, want invalid_state (unknown handle)", h, err)
		}
	}
	_ = w.Close() // git reads an empty alternates file and goes on
	o := <-done
	if o.err != nil || !strings.Contains(o.res.Stdout, "# branch.head") {
		t.Errorf("status after the signals: %v %+v", o.err, o.res)
	}
}
