//go:build linux || darwin

package exec

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
)

// maxSymlinks is the symlink budget of design A06 §5.1 step 4, the Linux
// MAXSYMLINKS value: a path may follow 40 symlinks, and the 41st fails
// path_escape (ELOOP).
const maxSymlinks = 40

// createMode is the permission of a file OpenCanonical creates (O_CREAT).
// Writes create a temporary file and rename it over the target (A06 §5.2),
// so this is the mode of that temporary file.
const createMode = 0o600

// Tree is the executor's view of its roots, with each root's directory
// opened once at start (A06 §5.1: rootfd). It resolves model-proposed paths
// against the real file system (A06 §5.1 step 4) and opens the result
// without following anything (§5.2).
//
// A Tree is not safe for concurrent use while a test hook is being set; its
// methods are otherwise safe to call from several goroutines.
type Tree struct {
	roots []treeRoot

	// openat2 opens rel beneath dirfd with RESOLVE_BENEATH,
	// RESOLVE_NO_SYMLINKS, RESOLVE_NO_MAGICLINKS and RESOLVE_NO_XDEV
	// (openat2_linux.go). nil where the kernel has no openat2 (probed at
	// OpenTree; always nil on macOS), and a call that fails ENOSYS also
	// falls back to the component walk. Tests replace it to force the walk.
	openat2 func(dirfd int, rel string, flags int) (int, error)

	// beforeOpen, when set by a test, runs just before each open system
	// call of OpenCanonical: with step 0 before openat2, and with the
	// component index before each step of the walk. It lets a test change
	// the file system exactly inside the window the open must survive.
	beforeOpen func(step int)
}

type treeRoot struct {
	Root
	fd  int
	dev uint64
}

// OpenTree opens the directory of every root. Root paths are trusted: they
// come from the launch spec (A06 §3.1 --roots), not from the model.
func OpenTree(roots []Root) (*Tree, error) {
	t := &Tree{}
	for _, r := range roots {
		fd, err := unix.Open(r.Path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			_ = t.Close()
			return nil, execproto.NewError(execproto.ErrIOError, "cannot open root "+string(r.Name))
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			_ = unix.Close(fd)
			_ = t.Close()
			return nil, execproto.NewError(execproto.ErrIOError, "cannot stat root "+string(r.Name))
		}
		t.roots = append(t.roots, treeRoot{Root: r, fd: fd, dev: uint64(st.Dev)})
	}
	if len(t.roots) > 0 {
		t.openat2 = probeOpenat2(t.roots[0].fd)
	}
	return t, nil
}

// Close closes the root directories.
func (t *Tree) Close() error {
	var first error
	for _, r := range t.roots {
		if err := unix.Close(r.fd); err != nil && first == nil {
			first = err
		}
	}
	t.roots = nil
	return first
}

// Openat2 reports whether opens use openat2 (exec.hello features.openat2).
func (t *Tree) Openat2() bool { return t.openat2 != nil }

func (t *Tree) plainRoots() []Root {
	out := make([]Root, len(t.roots))
	for i, r := range t.roots {
		out[i] = r.Root
	}
	return out
}

func (t *Tree) root(name execproto.RootName) (int, bool) {
	for i, r := range t.roots {
		if r.Name == name {
			return i, true
		}
	}
	return 0, false
}

// Resolve is design A06 §5.1 in full: the lexical steps of the package-level
// Resolve, then the symlink walk of step 4 inside the root, then the
// deny-list and .git checks again on the canonical path (step 6: both the
// requested and the canonical path are matched). The result contains no
// symlink, so OpenCanonical can refuse to follow any.
//
// A symlink is followed while it stays inside the root. An absolute target
// under another root is followed only if the method may address that root
// (a read may reach scratch, nothing else may); any other target outside
// the root, or a 41st symlink, is path_escape. A component that does not
// exist ends the walk: the rest is appended lexically, which is what a write
// creates (read, list and stat then report not_found at open).
func (t *Tree) Resolve(method string, root execproto.RootName, input string) (execproto.Resolved, error) {
	lex, err := Resolve(t.plainRoots(), method, root, input)
	if err != nil {
		return execproto.Resolved{}, err
	}
	idx, ok := t.root(lex.Root)
	if !ok {
		return execproto.Resolved{}, execproto.NewError(execproto.ErrOutsideRoots, "root not open")
	}
	acc := access(method)
	idx, done, err := t.walk(acc, idx, lex.RelPath)
	if err != nil {
		return execproto.Resolved{}, err
	}
	rel := strings.Join(done, "/")
	r := t.roots[idx]
	res := execproto.Resolved{Root: r.Name, RelPath: rel, Canonical: joinRoot(r.Path, rel), RequestedRelPath: lex.RequestedRelPath, DenyMatch: lex.DenyMatch}
	if err := checkResult(acc, &res, rel); err != nil {
		return execproto.Resolved{}, err
	}
	return res, nil
}

// walk is A06 §5.1 step 4. done holds the resolved components, each an
// open directory in fds except, at the very end, a final non-directory or
// the lexical remainder after a missing component; todo holds what is left,
// and a symlink's target is put in front of todo so that every component of
// the target is checked the same way (a chain of links, or a link to a
// directory holding a link, resolves; nothing in done is ever a link).
func (t *Tree) walk(acc methodAccess, idx int, rel string) (int, []string, error) {
	escape := func() error { return execproto.NewError(execproto.ErrPathEscape, "symlink leads outside the root") }
	var done []string
	var fds []int
	closeAll := func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		fds = fds[:0]
	}
	defer closeAll()
	cur := func() int {
		if len(fds) == 0 {
			return t.roots[idx].fd
		}
		return fds[len(fds)-1]
	}
	budget := maxSymlinks
	todo := splitPath(rel)
	for len(todo) > 0 {
		comp := todo[0]
		todo = todo[1:]
		switch comp {
		case ".":
			continue
		case "..":
			if len(done) == 0 {
				return 0, nil, escape()
			}
			done = done[:len(done)-1]
			_ = unix.Close(fds[len(fds)-1])
			fds = fds[:len(fds)-1]
			continue
		}
		var st unix.Stat_t
		err := unix.Fstatat(cur(), comp, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			rest, ok := lexicalAppend(done, append([]string{comp}, todo...))
			if !ok {
				return 0, nil, escape()
			}
			return idx, rest, nil
		}
		if err != nil {
			return 0, nil, execproto.NewError(execproto.ErrIOError, "cannot stat a path component")
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			if budget == 0 {
				return 0, nil, execproto.NewError(execproto.ErrPathEscape, "too many symlinks")
			}
			budget--
			target, err := readlinkat(cur(), comp)
			if err != nil {
				return 0, nil, execproto.NewError(execproto.ErrIOError, "cannot read a symlink")
			}
			if strings.HasPrefix(target, "/") {
				r, tail, ok := rootOf(t.plainRoots(), target)
				if !ok {
					return 0, nil, escape()
				}
				next, _ := t.root(r.Name)
				if next != idx && !acc.permits(r) {
					return 0, nil, escape()
				}
				idx = next
				closeAll()
				done = done[:0]
				todo = append(splitPath(tail), todo...)
			} else {
				todo = append(splitPath(target), todo...)
			}
		case unix.S_IFDIR:
			fd, err := openDir(cur(), comp)
			if err != nil {
				return 0, nil, err
			}
			fds = append(fds, fd)
			done = append(done, comp)
		default:
			// A file (or FIFO, socket, device: the open rejects those
			// for reads and writes). Anything after it cannot exist; it
			// is appended lexically and the open reports not_found.
			rest, ok := lexicalAppend(done, append([]string{comp}, todo...))
			if !ok {
				return 0, nil, escape()
			}
			return idx, rest, nil
		}
	}
	return idx, done, nil
}

// lexicalAppend appends comps to done without touching the file system,
// with ".." removing a component; ok is false when ".." would climb above
// the root.
func lexicalAppend(done, comps []string) ([]string, bool) {
	out := append([]string(nil), done...)
	for _, c := range comps {
		switch c {
		case "", ".":
		case "..":
			if len(out) == 0 {
				return nil, false
			}
			out = out[:len(out)-1]
		default:
			out = append(out, c)
		}
	}
	return out, true
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func readlinkat(dirfd int, name string) (string, error) {
	for size := 256; size <= 1<<16; size *= 2 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(dirfd, name, buf)
		if err != nil {
			return "", err
		}
		if n < size {
			if n == 0 {
				return "", unix.ENOENT
			}
			return string(buf[:n]), nil
		}
	}
	return "", unix.ENAMETOOLONG
}

// OpenCanonical opens a resolved path (A06 §5.2) with flags plus O_NOFOLLOW
// and O_CLOEXEC, refusing to follow any symlink, to climb with "..", or to
// cross into another mount. The path Resolve produced contains none of
// these, so a refusal means the tree changed since (or r was forged) and is
// reported as conflict, after which the daemon re-resolves and asks the
// policy engine again (A06 §5.2, §4.4).
//
// With openat2 the kernel enforces this in one call (RESOLVE_BENEATH,
// RESOLVE_NO_SYMLINKS, RESOLVE_NO_MAGICLINKS, RESOLVE_NO_XDEV). Without it
// (Linux < 5.6, macOS) the path is walked one directory at a time from the
// root fd with O_DIRECTORY | O_NOFOLLOW, and each fd's device is compared
// with the root's in place of RESOLVE_NO_XDEV.
//
// It does not assume r came from Resolve. Besides the conflict checks, it
// re-applies Resolve's own checks to the path it opens, judging by flags
// alone: any write flag (O_WRONLY, O_RDWR, O_CREAT, O_TRUNC, O_APPEND) is a
// write, held to the exec.fs.write row of A06 §5.3 (worktree only, a
// read-write root, no .git or 8.3 segment); anything else is a read, held to
// the exec.fs.read row (worktree or scratch). A deny-list match is refused
// for every flag set: opening is content access, while list and stat
// report a match from Resolve without opening the entry.
//
// It only opens: the regular-file check and the O_NONBLOCK first open for
// reads belong to the fs methods (A06 §5.2, M2-7).
func (t *Tree) OpenCanonical(r execproto.Resolved, flags int) (*os.File, error) {
	idx, ok := t.root(r.Root)
	if !ok {
		return nil, execproto.NewError(execproto.ErrOutsideRoots, "root not open")
	}
	root := t.roots[idx]
	comps := splitPath(r.RelPath)
	if strings.HasPrefix(r.RelPath, "/") {
		return nil, conflict()
	}
	for _, c := range comps {
		if c == "." || c == ".." {
			return nil, conflict()
		}
	}
	acc := access(execproto.MethodFSRead)
	if flags&(unix.O_WRONLY|unix.O_RDWR|unix.O_CREAT|unix.O_TRUNC|unix.O_APPEND) != 0 {
		acc = access(execproto.MethodFSWrite)
	}
	if !acc.permits(root.Root) {
		return nil, execproto.NewError(execproto.ErrOutsideRoots, "root not allowed for this access")
	}
	var discard execproto.Resolved
	if err := checkResult(acc, &discard, strings.Join(comps, "/")); err != nil {
		return nil, err
	}
	flags |= unix.O_NOFOLLOW | unix.O_CLOEXEC
	name := root.Path
	if len(comps) > 0 {
		name += "/" + strings.Join(comps, "/")
	}

	if t.openat2 != nil {
		t.hook(0)
		rel := "."
		if len(comps) > 0 {
			rel = strings.Join(comps, "/")
		}
		fd, err := t.openat2(root.fd, rel, flags)
		if err == nil {
			return os.NewFile(uintptr(fd), name), nil
		}
		if !errors.Is(err, unix.ENOSYS) {
			return nil, openError(err)
		}
	}

	if len(comps) == 0 {
		t.hook(0)
		fd, err := unix.Openat(root.fd, ".", flags, createMode)
		if err != nil {
			return nil, openError(err)
		}
		return os.NewFile(uintptr(fd), name), nil
	}
	dir := root.fd
	closeDir := func() {
		if dir != root.fd {
			_ = unix.Close(dir)
		}
	}
	defer closeDir()
	for i, c := range comps[:len(comps)-1] {
		t.hook(i)
		fd, err := openDir(dir, c)
		if err != nil {
			return nil, err
		}
		closeDir()
		dir = fd
		if !sameDev(fd, root.dev) {
			return nil, conflict()
		}
	}
	t.hook(len(comps) - 1)
	fd, err := unix.Openat(dir, comps[len(comps)-1], flags, createMode)
	if err != nil {
		return nil, openError(err)
	}
	if !sameDev(fd, root.dev) {
		_ = unix.Close(fd)
		return nil, conflict()
	}
	return os.NewFile(uintptr(fd), name), nil
}

// openDir opens one directory component without following it. Linux
// reports O_DIRECTORY | O_NOFOLLOW on a symlink as ENOTDIR, not ELOOP, which
// is also what a regular file gives; the entry is looked at again so that a
// symlink is a conflict (the tree changed) and a file stays not_found.
func openDir(dirfd int, name string) (int, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil {
		return fd, nil
	}
	if errors.Is(err, unix.ENOTDIR) {
		var st unix.Stat_t
		if unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return -1, conflict()
		}
	}
	return -1, openError(err)
}

func (t *Tree) hook(step int) {
	if t.beforeOpen != nil {
		t.beforeOpen(step)
	}
}

func sameDev(fd int, dev uint64) bool {
	var st unix.Stat_t
	return unix.Fstat(fd, &st) == nil && uint64(st.Dev) == dev
}

func conflict() error {
	return execproto.NewError(execproto.ErrConflict, "a path component changed during the operation")
}

// openError maps an open failure to an A06 §4.4 code. A symlink where none
// was (ELOOP; EMLINK on some BSDs), a mount crossing or a ".." trick (EXDEV)
// and a rename race openat2 detected (EAGAIN) mean the tree changed:
// conflict. The OS error text is not passed on (io_error: "message
// sanitized").
func openError(err error) error {
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EMLINK), errors.Is(err, unix.EXDEV), errors.Is(err, unix.EAGAIN):
		return conflict()
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
		return execproto.NewError(execproto.ErrNotFound, "path does not exist")
	}
	return execproto.NewError(execproto.ErrIOError, "open failed")
}
