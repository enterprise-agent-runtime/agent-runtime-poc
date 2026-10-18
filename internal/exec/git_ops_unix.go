//go:build linux || darwin

package exec

// The four operations of exec.git.run (design A14 §6; A06 §3.1, §4.3,
// §4.4, l.780). Each git process runs through Git.git (git_unix.go). The
// package doc is in server.go.

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"warden.dev/warden/internal/execproto"
)

func gitFailed(what string) error {
	return execproto.NewError(execproto.ErrIOError, "git "+what+" failed")
}

// Run is exec.git.run: params checked, paths resolved, the repository
// checked, then the operation.
func (g *Git) Run(ctx context.Context, params execproto.GitRunParams) (execproto.GitRunResult, error) {
	r, err := parseGitParams(params)
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	rels, err := g.resolvePaths(r.paths, r.op == execproto.GitCommit)
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	deadline := g.p.cfg.Clock.Now().Add(time.Duration(r.timeoutMS) * time.Millisecond)
	if err := g.checkRepo(ctx, deadline); err != nil {
		return execproto.GitRunResult{}, err
	}
	switch r.op {
	case execproto.GitStatus:
		return g.status(ctx, deadline)
	case execproto.GitDiff:
		return g.diff(ctx, deadline, r, rels)
	case execproto.GitHead:
		commit, branch, err := g.head(ctx, deadline)
		if err != nil {
			return execproto.GitRunResult{}, err
		}
		res := result(nil, false)
		res.Head = &execproto.GitHeadInfo{Commit: commit, Branch: branch}
		return res, nil
	}
	return g.commit(ctx, deadline, r, rels)
}

// result is a successful run's result. stderr is always empty: git's
// messages are never forwarded (A06 §4.4).
func result(stdout []byte, truncated bool) execproto.GitRunResult {
	res := execproto.GitRunResult{Stdout: string(stdout), Encoding: execproto.EncodingUTF8, Truncated: truncated}
	if !utf8.Valid(stdout) {
		res.Stdout, res.Encoding = base64.StdEncoding.EncodeToString(stdout), execproto.EncodingBase64
	}
	return res
}

// resolvePaths resolves every path under the worktree (A06 §5.1: no
// escape, the deny-list on the requested and the canonical path, symlinks
// followed inside the root). Paths a commit stages are held to the write
// ban too (no .git segment, no 8.3 alias).
func (g *Git) resolvePaths(paths []string, commit bool) ([]string, error) {
	rels := make([]string, 0, len(paths))
	for _, p := range paths {
		res, err := g.p.cfg.Tree.Resolve(methodGitPaths, execproto.RootWorktree, p)
		if err != nil {
			return nil, err
		}
		if commit {
			for _, rel := range []string{res.RelPath, res.RequestedRelPath} {
				if d := writeBan(rel); d != "" {
					return nil, execproto.NewError(execproto.ErrOutsideRoots, d)
				}
			}
		}
		rels = append(rels, res.RelPath)
	}
	return rels, nil
}

// checkRepo refuses a private gitdir that would make git run a program or
// read configuration from elsewhere: a commondir file (git would read the
// common directory's config), or a key outside Warden's template
// (checkConfigKeys). It runs before every operation.
func (g *Git) checkRepo(ctx context.Context, deadline time.Time) error {
	if _, err := os.Lstat(g.cfg.GitDir + "/commondir"); !errors.Is(err, os.ErrNotExist) {
		return execproto.NewError(execproto.ErrInvalidState, "the private repository has a commondir file")
	}
	out, err := g.git(ctx, deadline, g.args.configList(), "")
	if err != nil {
		return err
	}
	if out.exit != 0 || out.truncated {
		return configRefused()
	}
	return checkConfigKeys(out.stdout)
}

func (g *Git) status(ctx context.Context, deadline time.Time) (execproto.GitRunResult, error) {
	out, err := g.git(ctx, deadline, g.args.status(), "")
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	if out.exit != 0 {
		return execproto.GitRunResult{}, gitFailed("status")
	}
	stdout := completeLines(out.stdout, 0)
	if err := checkStatusOutput(stdout); err != nil {
		return execproto.GitRunResult{}, err
	}
	return result(stdout, out.truncated), nil
}

// head is A14 §6 head: HEAD's commit id and, when HEAD is a branch, its
// name ("" when detached or not under refs/heads/).
func (g *Git) head(ctx context.Context, deadline time.Time) (commit, branch string, err error) {
	out, err := g.git(ctx, deadline, g.args.revParseHead(), "")
	if err != nil {
		return "", "", err
	}
	commit = strings.TrimSuffix(string(out.stdout), "\n")
	if out.exit != 0 || !objectID(commit) {
		return "", "", gitFailed("rev-parse")
	}
	ref, err := g.headRef(ctx, deadline)
	if err != nil {
		return "", "", err
	}
	return commit, strings.TrimPrefix(ref, "refs/heads/"), nil
}

// headRef is the full branch ref HEAD points at, "" when detached.
func (g *Git) headRef(ctx context.Context, deadline time.Time) (string, error) {
	out, err := g.git(ctx, deadline, g.args.symbolicRef(), "")
	if err != nil {
		return "", err
	}
	switch out.exit {
	case 0:
		ref := strings.TrimSuffix(string(out.stdout), "\n")
		if !strings.HasPrefix(ref, "refs/heads/") {
			return "", nil
		}
		return ref, nil
	case 1:
		return "", nil
	}
	return "", gitFailed("symbolic-ref")
}

// diff is A14 §6 diff. against is resolved to a commit id first (base is
// the --base-commit id, never refs/warden/base, A06 §3.1), so a blob, a
// tree or an unknown id is refused before anything reads content.
func (g *Git) diff(ctx context.Context, deadline time.Time, r gitRequest, rels []string) (execproto.GitRunResult, error) {
	rev := r.against
	if rev == "base" {
		rev = g.cfg.BaseCommit
	}
	out, err := g.git(ctx, deadline, g.args.verifyCommit(rev), "")
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	id := strings.TrimSuffix(string(out.stdout), "\n")
	if out.exit != 0 || !objectID(id) {
		return execproto.GitRunResult{}, invalidGit("against is not a commit in this repository")
	}
	out, err = g.git(ctx, deadline, g.args.diff(id, r, rels), "")
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	if out.exit != 0 {
		return execproto.GitRunResult{}, gitFailed("diff")
	}
	stdout := completeLines(out.stdout, '\n')
	if err := checkDiffOutput(stdout); err != nil {
		return execproto.GitRunResult{}, err
	}
	return result(stdout, out.truncated), nil
}

// commit is A14 §6 commit: only with HEAD on the session branch; add with
// the DENY excludes; refused when anything deny-listed is staged (an
// approved command may have staged it by hand); no empty commit; no hooks
// (--no-verify and the empty hooks directory), no signing; identity
// warden <ses_…> from the environment (A06 l.780).
//
// HEAD is checked before add and commit, not atomically with them: a
// process of another live handle could move HEAD in between. Such a
// process can already run git itself, so exec.git.run widens nothing.
func (g *Git) commit(ctx context.Context, deadline time.Time, r gitRequest, rels []string) (execproto.GitRunResult, error) {
	ref, err := g.headRef(ctx, deadline)
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	if ref != "refs/heads/"+g.cfg.SessionBranch {
		return execproto.GitRunResult{}, execproto.NewError(execproto.ErrBranchMismatch, "HEAD is not the session branch")
	}
	out, err := g.git(ctx, deadline, g.args.add(rels), "")
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	if out.exit != 0 {
		return execproto.GitRunResult{}, gitFailed("add")
	}
	out, err = g.git(ctx, deadline, g.args.staged(), "")
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	if out.exit != 0 || out.truncated {
		return execproto.GitRunResult{}, uncheckable()
	}
	if err := checkNameList(out.stdout); err != nil {
		return execproto.GitRunResult{}, err
	}
	if len(out.stdout) == 0 {
		res := result([]byte("nothing to commit\n"), false)
		res.ExitCode = 1
		return res, nil
	}
	out, err = g.git(ctx, deadline, g.args.commit(r.message), g.cfg.sessionEmail())
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	if out.exit != 0 {
		return execproto.GitRunResult{}, gitFailed("commit")
	}
	commit, branch, err := g.head(ctx, deadline)
	if err != nil {
		return execproto.GitRunResult{}, err
	}
	res := result(nil, false)
	res.Head = &execproto.GitHeadInfo{Commit: commit, Branch: branch}
	return res, nil
}
