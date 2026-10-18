package exec

// exec.git.run, the pure part (WRD-04 §5, WRD-16 §9; design A06 §3.1
// --git-dir, --session-branch, --base-commit, --hooks-dir; §4.2, §4.3
// git.json, §4.4, §8; design A14 §2, §4, §6; INV-I; CLAUDE.md §9 rule 6).
// The process side is git_unix.go. The package doc is in server.go.
//
// The model never supplies git arguments (A14 §6): params choose one of
// four operations (status, diff, head, commit) and fill a few checked
// slots (a commit id or base/HEAD, root-relative paths that went through
// Tree.Resolve, a context count, a bounded message). Everything else is
// fixed here, element by element:
//
//   - The repository is --git-dir and the worktree root, both launch flags.
//   - Command-line -c overrides, which take precedence over the
//     repository configuration the sandbox can write, switch off hooks
//     (core.hooksPath = the empty --hooks-dir), fsmonitor, pagers, signing,
//     auto-gc and every protocol; diff runs with --no-ext-diff and
//     --no-textconv. The configuration itself is checked against Warden's
//     template before every operation (checkConfigKeys), because a filter
//     driver's clean command runs on add and status under whatever name
//     .gitattributes gives it, and no override can name that in advance.
//   - The environment is built from nothing (gitEnv), not filtered.
//   - Every pathspec is followed by the deny-list excludes (A14 §6 DENY),
//     and the output is checked again for a deny-listed path (fail closed:
//     refused, never filtered), which also catches what no pathspec
//     expresses, such as an NTFS 8.3 alias (D-055).

import (
	"bytes"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/execproto/denylist"
)

// Bounds of git.json (A06 §4.3) and A14 §6.
const (
	gitDefaultTimeoutMS = 60000
	gitMinTimeoutMS     = 1000
	gitMaxTimeoutMS     = 120000
	gitMaxPaths         = 256
	gitMaxContext       = 10
	gitDefaultContext   = 3
	gitMaxMessage       = 4096
)

// methodGitPaths is the access row (A06 §5.3) under which exec.git.run
// resolves its paths: the worktree only, nothing written through the path
// itself, a deny-list match refused. It is not a wire method name; the
// method exec.git.run itself addresses no root.
const methodGitPaths = "exec.git.run/paths"

// GitConfig is the git environment of exec.git.run: the launch flags
// --git-dir, --session-branch, --base-commit and --hooks-dir (A06 §3.1).
// Nothing in it comes from params.
type GitConfig struct {
	GitDir        string // the private gitdir, /git
	SessionBranch string // warden/<ulid>; commit refuses any other HEAD
	BaseCommit    string // the session base id; diff against "base" uses it, never refs/warden/base
	HooksDir      string // the empty read-only hooks directory, /warden/empty
}

func badConfig(what string) error {
	return execproto.NewError(execproto.ErrInvalidArgument, "git launch flag "+what)
}

// absClean reports an absolute, clean slash path other than "/".
func absClean(p string) bool {
	return strings.HasPrefix(p, "/") && p != "/" && path.Clean(p) == p && !strings.ContainsRune(p, 0)
}

// crockford reports a 26-character ULID in Crockford base32 (either case).
func crockford(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, c := range strings.ToUpper(s) {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z' && c != 'I' && c != 'L' && c != 'O' && c != 'U':
		default:
			return false
		}
	}
	return true
}

// objectID reports a full lower-case object id (sha1 or sha256).
func objectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

func (c GitConfig) check() error {
	switch {
	case !absClean(c.GitDir):
		return badConfig("--git-dir must be an absolute clean path")
	case !absClean(c.HooksDir):
		return badConfig("--hooks-dir must be an absolute clean path")
	case !strings.HasPrefix(c.SessionBranch, "warden/") || !crockford(strings.TrimPrefix(c.SessionBranch, "warden/")):
		return badConfig("--session-branch must be warden/<ulid>")
	case !objectID(c.BaseCommit):
		return badConfig("--base-commit must be a full object id")
	}
	return nil
}

// sessionEmail is the commit identity's address, <ses_…> (A06 l.780,
// A14 §6), derived from the session branch: the identity comes from a
// launch flag, never from params or the repository configuration.
func (c GitConfig) sessionEmail() string {
	return "ses_" + strings.ToUpper(strings.TrimPrefix(c.SessionBranch, "warden/"))
}

// gitRequest is a checked exec.git.run request, before its paths are
// resolved.
type gitRequest struct {
	op           execproto.GitOp
	against      string // base, HEAD or an object id
	paths        []string
	statOnly     bool
	contextLines int
	message      string
	timeoutMS    int64
}

func invalidGit(detail string) error {
	return execproto.NewError(execproto.ErrInvalidArgument, detail)
}

// parseGitParams checks params against git.json (A06 §4.3) and A14 §6.
// Only the four operations exist: show, cat-file, log, grep, blame,
// archive, config and every remote operation are not refused case by
// case, they have no entry.
func parseGitParams(p execproto.GitRunParams) (gitRequest, error) {
	r := gitRequest{op: p.Op, timeoutMS: p.TimeoutMS}
	if r.timeoutMS == 0 {
		r.timeoutMS = gitDefaultTimeoutMS
	}
	if r.timeoutMS < gitMinTimeoutMS || r.timeoutMS > gitMaxTimeoutMS {
		return gitRequest{}, invalidGit("timeout_ms must be 1000 to 120000")
	}
	switch p.Op {
	case execproto.GitStatus, execproto.GitHead:
		if p.Diff != nil || p.Commit != nil {
			return gitRequest{}, invalidGit("this operation takes no diff or commit options")
		}
	case execproto.GitDiff:
		if p.Commit != nil {
			return gitRequest{}, invalidGit("diff takes no commit options")
		}
		d := execproto.GitDiffOptions{}
		if p.Diff != nil {
			d = *p.Diff
		}
		r.against = d.Against
		if r.against == "" {
			r.against = "base"
		}
		if r.against != "base" && r.against != "HEAD" && !objectID(r.against) {
			return gitRequest{}, invalidGit("against must be base, HEAD or a full commit id")
		}
		r.contextLines = gitDefaultContext
		if d.ContextLines != nil {
			r.contextLines = *d.ContextLines
		}
		if r.contextLines < 0 || r.contextLines > gitMaxContext {
			return gitRequest{}, invalidGit("context_lines must be 0 to 10")
		}
		if len(d.Paths) > gitMaxPaths {
			return gitRequest{}, invalidGit("at most 256 paths")
		}
		r.paths, r.statOnly = d.Paths, d.StatOnly
	case execproto.GitCommit:
		if p.Diff != nil || p.Commit == nil {
			return gitRequest{}, invalidGit("commit takes commit options and a message")
		}
		m := p.Commit.Message
		if m == "" || len(m) > gitMaxMessage || !utf8.ValidString(m) || strings.IndexByte(m, 0) >= 0 {
			return gitRequest{}, invalidGit("message must be 1 to 4096 bytes of UTF-8 without NUL")
		}
		if len(p.Commit.Paths) > gitMaxPaths {
			return gitRequest{}, invalidGit("at most 256 paths")
		}
		r.message, r.paths = m, p.Commit.Paths
	default:
		return gitRequest{}, invalidGit("unknown git operation (status, diff, head, commit)")
	}
	return r, nil
}

// gitArgs builds the argv of every command exec.git.run runs.
type gitArgs struct {
	gitDir, work, hooks string
}

// global is the fixed prefix of every git command (A14 §2 profile P, §4).
// -c is command-line scope, above the repository configuration.
func (g gitArgs) global() []string {
	return []string{
		"git", "--git-dir=" + g.gitDir, "--work-tree=" + g.work, "--no-pager", "--no-replace-objects",
		"-c", "core.hooksPath=" + g.hooks,
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "core.attributesFile=/dev/null",
		"-c", "core.quotePath=true", // the output check reads C-quoted names
		"-c", "core.pager=cat",
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "protocol.allow=never",
		"-c", "submodule.recurse=false",
		"-c", "credential.helper=",
		"-c", "safe.directory=" + g.work,
		"-c", "safe.directory=" + g.gitDir,
	}
}

// denyPathspecs is A14 §6 DENY: one exclude per deny-list glob.
func denyPathspecs() []string {
	out := make([]string, 0, len(denylist.Patterns))
	for _, p := range denylist.Patterns {
		out = append(out, ":(exclude,glob,icase)"+p)
	}
	return out
}

// literalPathspecs turns resolved root-relative paths into pathspecs with
// no magic (no glob, no exclude, no :(top)); the worktree root is ".".
// None given means the whole worktree.
func literalPathspecs(rels []string) []string {
	if len(rels) == 0 {
		return []string{"."}
	}
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		if r == "" {
			out = append(out, ".")
		} else {
			out = append(out, ":(literal)"+r)
		}
	}
	return out
}

func (g gitArgs) with(args ...[]string) []string {
	out := g.global()
	for _, a := range args {
		out = append(out, a...)
	}
	return out
}

func (g gitArgs) status() []string {
	return g.with([]string{"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--ignore-submodules=all", "--no-renames", "--", "."}, denyPathspecs())
}

// verifyCommit resolves rev to a commit id, failing (exit 1) for a blob,
// a tree or an unknown id.
func (g gitArgs) verifyCommit(rev string) []string {
	return g.with([]string{"rev-parse", "--verify", "--quiet", "--end-of-options", rev + "^{commit}"})
}

func (g gitArgs) diff(commit string, r gitRequest, rels []string) []string {
	a := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--ignore-submodules=all", "--src-prefix=a/", "--dst-prefix=b/", "-U" + strconv.Itoa(r.contextLines)}
	if r.statOnly {
		a = append(a, "--stat")
	}
	a = append(a, commit, "--")
	return g.with(a, literalPathspecs(rels), denyPathspecs())
}

func (g gitArgs) symbolicRef() []string {
	return g.with([]string{"symbolic-ref", "-q", "HEAD"})
}

func (g gitArgs) revParseHead() []string {
	return g.with([]string{"rev-parse", "--verify", "--quiet", "HEAD"})
}

func (g gitArgs) add(rels []string) []string {
	return g.with([]string{"add", "-A", "--"}, literalPathspecs(rels), denyPathspecs())
}

// staged lists every path whose staged content differs from HEAD, NUL
// separated: what a commit would record.
func (g gitArgs) staged() []string {
	return g.with([]string{"diff", "--cached", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "HEAD", "--"})
}

func (g gitArgs) commit(message string) []string {
	return g.with([]string{"commit", "--no-verify", "--no-gpg-sign", "--quiet", "-m", message})
}

// configList lists the key names of the gitdir's own configuration file,
// without following includes (an include.path key is itself refused).
func (g gitArgs) configList() []string {
	return g.with([]string{"config", "--file", g.gitDir + "/config", "--no-includes", "--list", "--name-only", "-z"})
}

// gitEnv is git's whole environment (A06 §8, A14 §2: the environment is
// cleared first). pathValue is the PATH git was resolved in; home is the
// empty read-only hooks directory, so no ~/.gitconfig or ~/.config/git
// file can exist. email, when set, adds the commit identity of A06 l.780.
func gitEnv(pathValue, home, email string) []string {
	env := []string{
		"PATH=" + pathValue, "HOME=" + home, "LC_ALL=C", "LANG=C", "TERM=dumb",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_PAGER=cat",
	}
	if email != "" {
		env = append(env, "GIT_AUTHOR_NAME=warden", "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_NAME=warden", "GIT_COMMITTER_EMAIL="+email)
	}
	return env
}

// configAllowed is Warden's configuration template (A14 §3 step 4), the
// only keys exec.git.run runs with. core.hooksPath and core.fsmonitor are
// allowed because husky-style package scripts write them and the -c
// overrides win; user.* because the commit identity comes from the
// environment, which wins too.
var configAllowed = map[string]bool{
	"core.repositoryformatversion": true, "core.filemode": true, "core.bare": true, "core.logallrefupdates": true,
	"core.symlinks": true, "core.ignorecase": true, "core.precomposeunicode": true, "core.hookspath": true,
	"core.fsmonitor": true, "core.untrackedcache": true, "core.autocrlf": true, "extensions.objectformat": true,
	"user.name": true, "user.email": true, "commit.gpgsign": true, "tag.gpgsign": true, "gc.auto": true,
	"maintenance.auto": true, "protocol.allow": true, "advice.detachedhead": true,
}

func configRefused() error {
	return execproto.NewError(execproto.ErrInvalidState, "the private repository's configuration has a key exec.git.run does not run with")
}

// checkConfigKeys checks a NUL-terminated key listing (configList) against
// the template. Anything else, an empty name, or a listing that does not
// end in NUL (cut by the output cap) is refused: unread means unchecked.
func checkConfigKeys(listing []byte) error {
	if len(listing) == 0 {
		return nil
	}
	if listing[len(listing)-1] != 0 {
		return configRefused()
	}
	for _, k := range strings.Split(string(listing[:len(listing)-1]), "\x00") {
		if !configAllowed[strings.ToLower(k)] {
			return configRefused()
		}
	}
	return nil
}

func deniedOutput() error {
	return execproto.NewError(execproto.ErrDenyList, "git output names a path that matches the platform deny-list")
}

func uncheckable() error {
	return execproto.NewError(execproto.ErrIOError, "git output could not be checked")
}

// deniedPath is the deny-list check for a path git printed (root-relative,
// slash-separated). The 8.3 rule applies, as for every path the executor
// serves (D-055): git reads an index entry ENV~1 through the file system,
// which on a volume with short names opens .env.
func deniedPath(p string) bool {
	_, ok := denylist.Match(p)
	return ok && p != ""
}

// checkStatusOutput checks porcelain v2 -z output (whole records only,
// completeLines). A record of a type it does not know, or with too few
// fields, is uncheckable and refused.
func checkStatusOutput(out []byte) error {
	if len(out) == 0 {
		return nil
	}
	recs := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		var paths []string
		switch {
		case strings.HasPrefix(rec, "# "):
			continue
		case strings.HasPrefix(rec, "? "), strings.HasPrefix(rec, "! "):
			paths = []string{rec[2:]}
		case strings.HasPrefix(rec, "1 "):
			f := strings.SplitN(rec, " ", 9)
			if len(f) != 9 {
				return uncheckable()
			}
			paths = []string{f[8]}
		case strings.HasPrefix(rec, "2 "):
			f := strings.SplitN(rec, " ", 10)
			if len(f) != 10 || i+1 >= len(recs) {
				return uncheckable()
			}
			i++
			paths = []string{f[9], recs[i]}
		case strings.HasPrefix(rec, "u "):
			f := strings.SplitN(rec, " ", 11)
			if len(f) != 11 {
				return uncheckable()
			}
			paths = []string{f[10]}
		default:
			return uncheckable()
		}
		for _, p := range paths {
			if deniedPath(p) {
				return deniedOutput()
			}
		}
	}
	return nil
}

// headerName reads one path as git prints it in a header: C-quoted when it
// starts with a double quote, else the rest of the line.
func headerName(s string) (string, bool) {
	if strings.HasPrefix(s, `"`) {
		v, rest, ok := unquoteC(s)
		return v, ok && rest == ""
	}
	return s, s != ""
}

// diffGitPaths parses the rest of a "diff --git " line. With --no-renames
// both names are the same path, so an unquoted line is "a/P b/P" and can
// be split by length even when P holds spaces.
func diffGitPaths(rest string) ([]string, bool) {
	if strings.HasPrefix(rest, `"`) {
		a, r, ok := unquoteC(rest)
		if !ok || !strings.HasPrefix(r, ` "`) {
			return nil, false
		}
		b, r, ok := unquoteC(r[1:])
		if !ok || r != "" || !strings.HasPrefix(a, "a/") || !strings.HasPrefix(b, "b/") {
			return nil, false
		}
		return []string{a[2:], b[2:]}, true
	}
	if len(rest) < 7 || (len(rest)-5)%2 != 0 {
		return nil, false
	}
	n := (len(rest) - 5) / 2
	if rest[:2] != "a/" || rest[2+n:5+n] != " b/" || rest[2:2+n] != rest[5+n:] {
		return nil, false
	}
	return []string{rest[2 : 2+n]}, true
}

// checkDiffOutput checks diff output (whole lines only). Every file section
// starts with a "diff " header; hunk lines carry a prefix (" ", "+", "-",
// "\"), so no content line can look like one. A header naming a
// deny-listed path is refused, as is a "diff " line it cannot read, and
// the extended rename/copy headers are checked too.
func checkDiffOutput(out []byte) error {
	for _, line := range strings.Split(string(out), "\n") {
		var names []string
		ok := true
		switch {
		case strings.HasPrefix(line, "diff --git "):
			names, ok = diffGitPaths(strings.TrimPrefix(line, "diff --git "))
		case strings.HasPrefix(line, "diff --cc "), strings.HasPrefix(line, "diff --combined "):
			_, rest, _ := strings.Cut(line[len("diff --"):], " ")
			var n string
			n, ok = headerName(rest)
			names = []string{n}
		case strings.HasPrefix(line, "diff "):
			ok = false
		default:
			for _, h := range []string{"rename from ", "rename to ", "copy from ", "copy to "} {
				if strings.HasPrefix(line, h) {
					var n string
					n, ok = headerName(strings.TrimPrefix(line, h))
					names = []string{n}
				}
			}
		}
		if !ok {
			return uncheckable()
		}
		for _, n := range names {
			if deniedPath(n) {
				return deniedOutput()
			}
		}
	}
	return nil
}

// checkNameList checks a NUL-terminated list of paths (staged).
func checkNameList(out []byte) error {
	if len(out) == 0 {
		return nil
	}
	if out[len(out)-1] != 0 {
		return uncheckable()
	}
	for _, p := range strings.Split(string(out[:len(out)-1]), "\x00") {
		if deniedPath(p) {
			return deniedOutput()
		}
	}
	return nil
}

// unquoteC reads a C-quoted string as git writes one (quote_c_style): it
// returns the value and what follows the closing quote. Only git's escapes
// are accepted: \a \b \t \n \v \f \r \" \\ and three octal digits.
func unquoteC(s string) (string, string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", "", false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			return b.String(), s[i+1:], true
		case '\\':
			if i+1 >= len(s) {
				return "", "", false
			}
			i++
			switch e := s[i]; e {
			case 'a':
				b.WriteByte('\a')
			case 'b':
				b.WriteByte('\b')
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'v':
				b.WriteByte('\v')
			case 'f':
				b.WriteByte('\f')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\':
				b.WriteByte(e)
			case '0', '1', '2', '3':
				if i+2 >= len(s) || !isOctal(s[i+1]) || !isOctal(s[i+2]) {
					return "", "", false
				}
				b.WriteByte((e-'0')<<6 | (s[i+1]-'0')<<3 | (s[i+2] - '0'))
				i += 2
			default:
				return "", "", false
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", false
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// completeLines cuts out after its last sep: output the cap cut is checked
// and returned only up to its last whole line (or -z record).
func completeLines(out []byte, sep byte) []byte {
	return out[:bytes.LastIndexByte(out, sep)+1]
}
