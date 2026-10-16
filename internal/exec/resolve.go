// Path resolution for the executor (WRD-04 §5, WRD-16 §9, design A06; the
// package doc is in server.go). This file is the pure, OS-independent part: design A06 §5.1 steps 1-3 and 6
// (input validation, root choice, lexical clean, deny-list) and the
// per-method root table of §5.3. The symlink walk of step 4 and the
// race-free open of §5.2 need the file system and live in tree_unix.go
// (Linux and macOS); the executor never runs natively on Windows (it runs
// in L2 there), so tree_other.go only reports the open as unsupported, while
// Resolve compiles and is tested on every OS.
//
// Every path an executor method receives was proposed by the model. The
// rules are CLAUDE.md §9 rule 2 (paths are canonicalized inside the sandbox
// before matching) and rule 6 (no writes to .git), and INV-I's second layer:
// the platform deny-list is enforced here again, independently of the policy
// engine, with the one matcher all three layers share
// (internal/execproto/denylist, docs/DECISIONS-poc.md D-014). An attempted
// escape is an error with an A06 §4.4 code, never clamped to the root, so the
// attempt reaches the audit trail as a sandbox.violation (A06 §5.4).

package exec

import (
	"path"
	"strings"
	"unicode/utf8"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/execproto/denylist"
)

// Input limits of A06 §5.1 step 1.
const (
	maxPathBytes    = 4096
	maxSegmentBytes = 255
)

// Root is one sandbox root (A06 §2, §3.1 --roots): its logical name, its
// absolute slash-separated path inside the sandbox, and its mount mode.
type Root struct {
	Name execproto.RootName
	Path string
	Mode execproto.RootMode
}

// methodAccess is one row of the A06 §5.3 table: the roots a method may
// address, whether it writes, and whether a deny-list match is reported
// (Resolved.DenyMatch) rather than refused.
type methodAccess struct {
	roots      []execproto.RootName
	write      bool
	reportDeny bool
}

// access returns the §5.3 row for method. A method that takes no path
// (exec.git.run, exec.proc.wait, ...) may address no root. exec.fs.list and
// exec.fs.stat report a deny-list match instead of failing (§5.1 step 6).
func access(method string) methodAccess {
	switch method {
	case execproto.MethodFSRead:
		return methodAccess{roots: []execproto.RootName{execproto.RootWorktree, execproto.RootScratch}}
	case execproto.MethodFSStat:
		return methodAccess{roots: []execproto.RootName{execproto.RootWorktree, execproto.RootScratch}, reportDeny: true}
	case execproto.MethodFSList:
		return methodAccess{roots: []execproto.RootName{execproto.RootWorktree}, reportDeny: true}
	case execproto.MethodFSSearch, execproto.MethodSpawn:
		return methodAccess{roots: []execproto.RootName{execproto.RootWorktree}}
	case execproto.MethodFSWrite, execproto.MethodFSPatch:
		return methodAccess{roots: []execproto.RootName{execproto.RootWorktree}, write: true}
	}
	return methodAccess{}
}

// permits reports whether the method may address the root, given its mode:
// a write method never addresses a read-only root.
func (a methodAccess) permits(r Root) bool {
	for _, n := range a.roots {
		if n == r.Name {
			return !a.write || r.Mode == execproto.RootRW
		}
	}
	return false
}

// Resolve is the lexical half of A06 §5.1 for one executor call: method is
// the executor method name, root the request's optional root (default
// worktree), input the path as the model spelled it. It returns the
// Resolved of §5.1 step 5 with RelPath and Canonical equal to the lexically
// cleaned path. It does not touch the file system: Tree.Resolve adds the
// symlink walk of step 4 and the checks that apply to its result.
//
// Errors are *execproto.Error with these codes: invalid_argument (empty,
// NUL, invalid UTF-8, over 4096 bytes, a segment over 255 bytes, an unknown
// root name), outside_roots (an absolute path under no root this method may
// address, a Windows drive or UNC form, a root the method may not address,
// a write to a read-only root or into .git), path_escape (".." above the
// root) and deny_list.
//
// Backslashes are separators, as in the deny-list matcher (D-014): the
// policy engine and the mount layer read `a\..\.env` as `.env`, so the
// executor must too, or the layers would disagree about which file a path
// names.
func Resolve(roots []Root, method string, root execproto.RootName, input string) (execproto.Resolved, error) {
	if err := validateInput(input); err != nil {
		return execproto.Resolved{}, err
	}
	acc := access(method)
	p := strings.ReplaceAll(input, `\`, "/")

	// Step 2: choose the root.
	var r Root
	var rel string
	switch {
	case isDrive(p):
		return execproto.Resolved{}, execproto.NewError(execproto.ErrOutsideRoots, "absolute path not under any root")
	case strings.HasPrefix(p, "/"):
		var ok bool
		if r, rel, ok = rootOf(roots, p); !ok || !acc.permits(r) {
			return execproto.Resolved{}, execproto.NewError(execproto.ErrOutsideRoots, "absolute path not under any root this method may address")
		}
	default:
		if root == "" {
			root = execproto.RootWorktree
		}
		if !knownRoot(root) {
			return execproto.Resolved{}, execproto.NewError(execproto.ErrInvalidArgument, "unknown root")
		}
		var ok bool
		if r, ok = findRoot(roots, root); !ok || !acc.permits(r) {
			return execproto.Resolved{}, execproto.NewError(execproto.ErrOutsideRoots, "root not allowed for this method")
		}
		rel = p
	}

	// Step 3: lexical clean. An escape is an error, never the root.
	c := path.Clean(rel)
	if c == ".." || strings.HasPrefix(c, "../") {
		return execproto.Resolved{}, execproto.NewError(execproto.ErrPathEscape, "path escapes the root")
	}
	if c == "." {
		c = ""
	}
	res := execproto.Resolved{Root: r.Name, RelPath: c, Canonical: joinRoot(r.Path, c), RequestedRelPath: c}
	if err := checkResult(acc, &res, c); err != nil {
		return execproto.Resolved{}, err
	}
	return res, nil
}

// checkResult applies the checks of A06 §5.1 step 6 and §5.3 to one
// root-relative path: the deny-list (root-relative rule, CF-17) and the
// .git write ban. It is called for the requested path and again for the
// canonical path after the symlink walk, so a link named "notes.txt" that
// points at ".env" or ".git/config" is refused as what it reaches.
// res.DenyMatch keeps the first match.
func checkResult(acc methodAccess, res *execproto.Resolved, rel string) error {
	if pat, ok := denylist.Match(rel); ok && rel != "" {
		if !acc.reportDeny {
			return execproto.NewError(execproto.ErrDenyList, "path matches the platform deny-list")
		}
		if res.DenyMatch == nil {
			res.DenyMatch = &pat
		}
	}
	if acc.write && touchesGit(rel) {
		return execproto.NewError(execproto.ErrOutsideRoots, "writes to .git are not allowed")
	}
	return nil
}

// touchesGit reports whether a root-relative path has a ".git" segment. A06
// §5.3 says the path "must not start with .git", meaning the worktree's .git
// file; read literally that would also ban .gitignore and .github/, so the
// rule is applied per segment. Any segment counts, not only the first, so a
// nested repository's hooks cannot be planted either, and the comparison
// ignores case because macOS and Windows file systems do (CLAUDE.md §9 rule
// 6).
func touchesGit(rel string) bool {
	for _, s := range strings.Split(rel, "/") {
		if strings.EqualFold(s, ".git") {
			return true
		}
	}
	return false
}

// validateInput is A06 §5.1 step 1.
func validateInput(s string) error {
	bad := func() error { return execproto.NewError(execproto.ErrInvalidArgument, "invalid path") }
	if s == "" || len(s) > maxPathBytes || strings.IndexByte(s, 0) >= 0 || !utf8.ValidString(s) {
		return bad()
	}
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
		if len(seg) > maxSegmentBytes {
			return bad()
		}
	}
	return nil
}

// isDrive reports a Windows drive form, "C:x", "C:/x" (after backslashes
// became slashes). No sandbox root is ever on a drive.
func isDrive(p string) bool {
	return len(p) >= 2 && p[1] == ':' && (p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z')
}

// rootOf finds the root an absolute sandbox path lies under and returns the
// path relative to it, uncleaned (A06 §5.1 step 2: equal to R, or starting
// with R + "/"). The longest matching root wins, so a root nested in another
// is never shadowed. "//server/share" and "/./work" match no root: the input
// is not cleaned first, because cleaning "/work/../etc" would hide that it
// climbs out of /work.
func rootOf(roots []Root, p string) (Root, string, bool) {
	var best Root
	var rel string
	found := false
	for _, r := range roots {
		var tail string
		switch {
		case p == r.Path:
		case strings.HasPrefix(p, r.Path+"/"):
			tail = p[len(r.Path)+1:]
		default:
			continue
		}
		if !found || len(r.Path) > len(best.Path) {
			best, rel, found = r, tail, true
		}
	}
	if rel == "" {
		rel = "."
	}
	return best, rel, found
}

func findRoot(roots []Root, name execproto.RootName) (Root, bool) {
	for _, r := range roots {
		if r.Name == name {
			return r, true
		}
	}
	return Root{}, false
}

func knownRoot(n execproto.RootName) bool {
	switch n {
	case execproto.RootWorktree, execproto.RootScratch, execproto.RootCache, execproto.RootGit:
		return true
	}
	return false
}

// joinRoot is A06 §5.1 step 5: canonical = R + "/" + rel_path, R itself for
// the root.
func joinRoot(rootPath, rel string) string {
	if rel == "" {
		return rootPath
	}
	return rootPath + "/" + rel
}
