//go:build linux || darwin

// The exec.fs methods (design A06 §4.2-§4.4, §5.2, §5.3; the package doc
// is in server.go) on the Tree of tree_unix.go. The rules every method
// keeps:
//
//   - Every path goes through Tree.Resolve with the method's own row of the
//     §5.3 table, and every open of file content goes through
//     OpenCanonical with that Resolved, never one built by hand: the open
//     re-checks the root, the deny-list and the .git ban itself (D-054).
//     Directories are opened with openBeneath, the same race-free open
//     without the content checks, only to list them, stat an entry or
//     create a file in them.
//   - A file read for content is opened O_NONBLOCK first, so a FIFO cannot
//     block the executor, and must then be a regular file (not_regular)
//     with a single link (A06 §5.2). A hard link is the same file under a
//     second name: a link to .env, or to a file outside the roots on the
//     same file system, would read it under a name no check refuses
//     (CWE-59). The executor cannot find a file's other names, so any file
//     with st_nlink > 1 is refused for content (read and patch: deny_list,
//     a violation; search: skipped as denied). Writes are not affected:
//     they rename over the name, never write through it.
//   - Writes go to a temporary file that is fsynced and renamed into place
//     in a directory fd held since the walk, so neither a directory swapped
//     for a symlink nor a hard link can redirect them (CWE-367). Temporary
//     names start with ".warden-tmp-", which matches no deny-list glob and
//     is not an 8.3 short name, because OpenCanonical applies the write
//     checks to every O_CREAT open. Parent directories are created with
//     mkdirat, which no open check sees, so every new segment is held to
//     the write rules first: a write to ".env/x" must not create a
//     directory named .env.
//   - Errors are A06 §4.4 codes with fixed details; no OS error text, path
//     or content reaches the wire.

package exec

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/execproto/denylist"
)

// tempPrefix starts the name of every temporary file and staging
// directory the fs methods create.
const tempPrefix = ".warden-tmp-"

// Executor-side bounds that are not on the wire.
const (
	// maxDirNames is the most names read from one directory by list or
	// search; a larger directory is reported truncated.
	maxDirNames = 1 << 16
	// searchMaxTotalBytes and searchMaxEntries bound one search: the bytes
	// read from all files together and the directory entries visited.
	// Reaching either ends the search with truncated: true.
	searchMaxTotalBytes = 64 << 20
	searchMaxEntries    = 100_000
	// searchBinaryProbe is how much of a file is checked for a NUL byte.
	searchBinaryProbe = 8 << 10
	// searchTimeout is the method deadline of exec.fs.search (A06 §4.4
	// timeout; A06 gives no value). Tree.now makes the clock injectable.
	searchTimeout = 30 * time.Second
	// searchMaxFileCost bounds a file's size times the query's cost
	// (searchSpec.cost): a larger file is skipped as too_large. It bounds
	// every single match call, which no check can interrupt, to about one
	// second for the worst program (RE2 measured about 14 ns per
	// instruction and byte in review): 2 MiB at cost 32, 64 KiB at the
	// 1000-instruction cap. A case-sensitive literal costs 1.
	searchMaxFileCost = 1 << 26
	// searchCheckLines is how often a file's lines check cancellation and
	// the deadline.
	searchCheckLines = 1024
	// patchMaxStagedBytes bounds the files one patch produces together.
	patchMaxStagedBytes = 128 << 20
	// patchFrameReserve is what the result budget of a patch leaves of the
	// 8 MiB frame (A06 §4.1) for the JSON-RPC envelope around the result:
	// {"jsonrpc":"2.0","id":...,"result":...}. 64 KiB covers any id the
	// daemon sends (it uses integers) with a wide margin.
	patchFrameReserve = 64 << 10

	newFileMode = 0o644
	execMode    = 0o755
	newDirMode  = 0o755
	stageMode   = 0o700
)

func notRegular() error { return execproto.NewError(execproto.ErrNotRegular, "not a regular file") }

func hardLinked() error {
	return execproto.NewError(execproto.ErrDenyList, "file has more than one hard link: its content may be a protected file's")
}

func ioError(detail string) error { return execproto.NewError(execproto.ErrIOError, detail) }

// expectCanonical is A06 §5.2: when the daemon passes the canonical path
// the policy engine decided on, the fresh resolution must give the same
// path, or the call fails conflict and the daemon asks again.
func expectCanonical(r execproto.Resolved, want string) error {
	if want != "" && want != r.Canonical {
		return execproto.NewError(execproto.ErrConflict, "the path resolves differently than when the policy decided")
	}
	return nil
}

// openFile opens a resolved file for its content: O_NONBLOCK first, then
// fstat, refusing anything but a regular file with one link.
func (t *Tree) openFile(r execproto.Resolved) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	f, err := t.OpenCanonical(r, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return nil, st, err
	}
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		_ = f.Close()
		return nil, st, ioError("stat failed")
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		_ = f.Close()
		return nil, st, notRegular()
	case uint64(st.Nlink) > 1:
		_ = f.Close()
		return nil, st, hardLinked()
	}
	return f, st, nil
}

// lstat stats a resolved path without following it, from its parent
// directory opened race-free. A missing path or parent is ok == false.
func (t *Tree) lstat(r execproto.Resolved) (st unix.Stat_t, ok bool, err error) {
	idx, found := t.root(r.Root)
	if !found {
		return st, false, execproto.NewError(execproto.ErrOutsideRoots, "root not open")
	}
	comps := splitPath(r.RelPath)
	if len(comps) == 0 {
		if err := unix.Fstat(t.roots[idx].fd, &st); err != nil {
			return st, false, ioError("stat failed")
		}
		return st, true, nil
	}
	dir, err := t.openBeneath(idx, comps[:len(comps)-1], unix.O_RDONLY|unix.O_DIRECTORY)
	if errors.Is(err, execproto.ErrNotFound) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	defer unix.Close(dir)
	err = unix.Fstatat(dir, comps[len(comps)-1], &st, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case err == nil:
		return st, true, nil
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
		return st, false, nil
	}
	return st, false, ioError("stat failed")
}

func kindOf(st *unix.Stat_t) execproto.Kind {
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return execproto.KindFile
	case unix.S_IFDIR:
		return execproto.KindDir
	case unix.S_IFLNK:
		return execproto.KindSymlink
	}
	return execproto.KindOther
}

// meta is the size, mode and mtime of an entry, as list and stat report
// them for entries that are not deny-listed.
func meta(st *unix.Stat_t) (*int64, string, *time.Time) {
	size := st.Size
	sec, nsec := st.Mtim.Unix()
	mtime := time.Unix(sec, nsec).UTC()
	return &size, fmt.Sprintf("0%03o", uint32(st.Mode)&0o7777), &mtime
}

// Read is exec.fs.read: at most 2 MiB of a regular file from offset.
func (t *Tree) Read(_ context.Context, p execproto.ReadParams) (execproto.ReadResult, error) {
	spec, err := readArgs(p)
	if err != nil {
		return execproto.ReadResult{}, err
	}
	res, err := t.Resolve(execproto.MethodFSRead, p.Root, p.Path)
	if err != nil {
		return execproto.ReadResult{}, err
	}
	if err := expectCanonical(res, p.ExpectCanonical); err != nil {
		return execproto.ReadResult{}, err
	}
	f, st, err := t.openFile(res)
	if err != nil {
		return execproto.ReadResult{}, err
	}
	defer f.Close()
	var n int64
	if spec.offset < st.Size {
		n = min(spec.length, st.Size-spec.offset)
	}
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, spec.offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return execproto.ReadResult{}, ioError("read failed")
	}
	buf = buf[:got]
	content, enc := encodeContent(buf)
	sum := sha256.Sum256(buf)
	return execproto.ReadResult{
		Resolved: res, Content: content, Encoding: enc, Size: st.Size, Offset: spec.offset,
		Returned: int64(got), EOF: spec.offset+int64(got) >= st.Size, Sha256: "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

// Stat is exec.fs.stat, the canonicalization step before every policy
// decision (A06 §5.4). A deny-listed path is reported with deny_match,
// not refused (§5.1 step 6), and like a denied list entry without size,
// mode or mtime.
func (t *Tree) Stat(_ context.Context, p execproto.StatParams) (execproto.StatResult, error) {
	res, err := t.Resolve(execproto.MethodFSStat, p.Root, p.Path)
	if err != nil {
		return execproto.StatResult{}, err
	}
	st, ok, err := t.lstat(res)
	if err != nil {
		return execproto.StatResult{}, err
	}
	out := execproto.StatResult{Resolved: res, Exists: ok}
	if ok {
		out.Kind = kindOf(&st)
		if res.DenyMatch == nil {
			out.Size, out.Mode, out.MTime = meta(&st)
		}
	}
	return out, nil
}

// readNames returns up to maxDirNames names of the directory fd, sorted,
// and whether there were more. fd stays open.
func readNames(fd int) ([]string, bool, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, false, ioError("cannot read a directory")
	}
	f := os.NewFile(uintptr(dup), "dir")
	defer f.Close()
	names, err := f.Readdirnames(maxDirNames + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, ioError("cannot read a directory")
	}
	more := len(names) > maxDirNames
	if more {
		names = names[:maxDirNames]
	}
	sort.Strings(names)
	return names, more, nil
}

func join(comps []string, name string) string {
	if len(comps) == 0 {
		return name
	}
	return strings.Join(comps, "/") + "/" + name
}

func with(comps []string, name string) []string {
	return append(append(make([]string, 0, len(comps)+1), comps...), name)
}

// List is exec.fs.list: breadth first, each directory sorted by name, at
// most depth levels and max_entries entries. Entries whose root-relative
// path matches the deny-list are listed by name with denied: true and no
// size, mode, mtime or link target (A06 fs.json "size and mtime omitted";
// the golden omits the mode too), and denied directories are not entered,
// so the names inside them are not shown. Symlinks are reported with their
// raw target and never followed. Listing a directory that is itself
// deny-listed fails deny_list: everything in it would be content of the
// denied tree.
func (t *Tree) List(ctx context.Context, p execproto.ListParams) (execproto.ListResult, error) {
	spec, err := listArgs(p)
	if err != nil {
		return execproto.ListResult{}, err
	}
	in := p.Path
	if in == "" {
		in = "."
	}
	res, err := t.Resolve(execproto.MethodFSList, p.Root, in)
	if err != nil {
		return execproto.ListResult{}, err
	}
	if res.DenyMatch != nil {
		return execproto.ListResult{}, execproto.NewError(execproto.ErrDenyList, "the directory matches the platform deny-list")
	}
	idx, _ := t.root(res.Root)
	type dir struct {
		comps []string
		level int
	}
	out := execproto.ListResult{Resolved: res}
	queue := []dir{{splitPath(res.RelPath), 1}}
	for first := true; len(queue) > 0; first = false {
		if err := ctx.Err(); err != nil {
			return execproto.ListResult{}, err
		}
		d := queue[0]
		queue = queue[1:]
		fd, err := t.openBeneath(idx, d.comps, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			if first {
				return execproto.ListResult{}, err
			}
			continue // changed since it was listed; not entered
		}
		names, more, err := readNames(fd)
		if err != nil {
			_ = unix.Close(fd)
			if first {
				return execproto.ListResult{}, err
			}
			continue
		}
		out.Truncated = out.Truncated || more
		for _, name := range names {
			var st unix.Stat_t
			if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				continue // removed since the directory was read
			}
			if len(out.Entries) == spec.max {
				_ = unix.Close(fd)
				out.Truncated = true
				return out, nil
			}
			rel := join(d.comps, name)
			e := execproto.ListEntry{RelPath: rel, Kind: kindOf(&st)}
			if _, denied := denylist.Match(rel); denied {
				e.Denied = true
			} else {
				e.Size, e.Mode, e.MTime = meta(&st)
				if e.Kind == execproto.KindSymlink {
					e.LinkTarget, _ = readlinkat(fd, name)
				}
				if e.Kind == execproto.KindDir && d.level < spec.depth {
					queue = append(queue, dir{with(d.comps, name), d.level + 1})
				}
			}
			out.Entries = append(out.Entries, e)
		}
		_ = unix.Close(fd)
	}
	return out, nil
}

// searcher is one exec.fs.search walk.
type searcher struct {
	t        *Tree
	ctx      context.Context
	idx      int
	root     execproto.RootName
	spec     searchSpec
	out      execproto.SearchResult
	bytes    int64
	entries  int
	now      func() time.Time
	deadline time.Time
}

// check is the cancellation and deadline check (A06 §4.4 timeout): first
// the caller's context (the server cancels every handler when the
// connection ends, and Serve waits for them), then the method deadline.
func (s *searcher) check() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.now().After(s.deadline) {
		return execproto.NewError(execproto.ErrTimeout, "search exceeded its time limit")
	}
	return nil
}

// Search is exec.fs.search: RE2 (or literal) matching, line by line, over
// the regular files under path, depth first in name order. It never opens
// what it does not search: a deny-listed name (counted skipped.denied, and
// a denied directory is not entered), a symlink (never followed), a FIFO,
// socket or device, and a .git directory. Each file it does search is
// resolved with the search row (worktree only) and opened like a read
// (OpenCanonical, O_NONBLOCK, regular, one link), so a file reached under
// another name or in another root than the walk saw, or with a second hard
// link, is skipped rather than searched.
//
// Bounds (CWE-400, CWE-1333): files over max_file_bytes are skipped whole
// (too_large), and so are files whose size times the query's cost (its
// program size, see searchArgs) exceeds searchMaxFileCost, which bounds
// every single match call; files with a NUL in their first 8 KiB are
// binary. One search reads at most 64 MiB and visits at most 100 000
// entries; reaching a bound, or max_matches, returns what was found with
// truncated: true. Cancellation and the method deadline (searchTimeout,
// A06 §4.4 timeout) are checked per directory, per file and every
// searchCheckLines lines; past the deadline the search fails timeout and
// returns no partial result.
func (t *Tree) Search(ctx context.Context, p execproto.SearchParams) (execproto.SearchResult, error) {
	spec, err := searchArgs(p)
	if err != nil {
		return execproto.SearchResult{}, err
	}
	in := p.Path
	if in == "" {
		in = "."
	}
	res, err := t.Resolve(execproto.MethodFSSearch, p.Root, in)
	if err != nil {
		return execproto.SearchResult{}, err
	}
	idx, _ := t.root(res.Root)
	now := t.now
	if now == nil {
		now = time.Now
	}
	s := &searcher{t: t, ctx: ctx, idx: idx, root: res.Root, spec: spec, now: now, deadline: now().Add(searchTimeout)}
	if err := s.check(); err != nil {
		return execproto.SearchResult{}, err
	}
	st, ok, err := t.lstat(res)
	switch {
	case err != nil:
		return execproto.SearchResult{}, err
	case !ok:
		return execproto.SearchResult{}, execproto.NewError(execproto.ErrNotFound, "path does not exist")
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		err = s.dir(splitPath(res.RelPath))
	case unix.S_IFREG:
		_, err = s.file(res.RelPath, &st)
	default:
		return execproto.SearchResult{}, notRegular()
	}
	if err != nil {
		return execproto.SearchResult{}, err
	}
	return s.out, nil
}

func (s *searcher) dir(start []string) error {
	stack := [][]string{start}
	for len(stack) > 0 {
		if err := s.check(); err != nil {
			return err
		}
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		fd, err := s.t.openBeneath(s.idx, d, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			s.out.Skipped.Unreadable++
			continue
		}
		names, more, err := readNames(fd)
		if err != nil {
			_ = unix.Close(fd)
			s.out.Skipped.Unreadable++
			continue
		}
		s.out.Truncated = s.out.Truncated || more
		var subdirs [][]string
		for _, name := range names {
			if s.entries++; s.entries > searchMaxEntries {
				_ = unix.Close(fd)
				s.out.Truncated = true
				return nil
			}
			var st unix.Stat_t
			if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				continue
			}
			rel := join(d, name)
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				switch _, denied := denylist.Match(rel); {
				case denied:
					s.out.Skipped.Denied++
				case strings.EqualFold(name, ".git"), s.excluded(rel):
				default:
					subdirs = append(subdirs, with(d, name))
				}
			case unix.S_IFREG:
				stop, err := s.file(rel, &st)
				if err != nil || stop {
					_ = unix.Close(fd)
					return err
				}
			}
		}
		_ = unix.Close(fd)
		for i := len(subdirs) - 1; i >= 0; i-- {
			stack = append(stack, subdirs[i])
		}
	}
	return nil
}

func (s *searcher) excluded(rel string) bool {
	for _, g := range s.spec.exclude {
		if globMatch(g, rel) {
			return true
		}
	}
	return false
}

func (s *searcher) included(rel string) bool {
	if s.excluded(rel) {
		return false
	}
	if len(s.spec.include) == 0 {
		return true
	}
	for _, g := range s.spec.include {
		if globMatch(g, rel) {
			return true
		}
	}
	return false
}

// file searches one regular file; it reports whether the search is over,
// and fails only on cancellation or the deadline.
func (s *searcher) file(rel string, st *unix.Stat_t) (stop bool, err error) {
	if err := s.check(); err != nil {
		return true, err
	}
	if _, denied := denylist.Match(rel); denied {
		s.out.Skipped.Denied++
		return false, nil
	}
	if !s.included(rel) {
		return false, nil
	}
	switch {
	case uint64(st.Nlink) > 1:
		s.out.Skipped.Denied++
		return false, nil
	case st.Size > s.spec.maxFileBytes, st.Size*int64(s.spec.cost) > searchMaxFileCost:
		s.out.Skipped.TooLarge++
		return false, nil
	case s.bytes+st.Size > searchMaxTotalBytes:
		s.out.Truncated = true
		return true, nil
	}
	// The search row, not the read row: read may address scratch, and an
	// absolute link swapped in since the walk would be followed there.
	r, err := s.t.Resolve(execproto.MethodFSSearch, s.root, rel)
	if err == nil && (r.Root != s.root || r.RelPath != rel) {
		err = conflict() // a link appeared on the way since the walk
	}
	var f *os.File
	if err == nil {
		f, _, err = s.t.openFile(r)
	}
	if err != nil {
		if errors.Is(err, execproto.ErrDenyList) {
			s.out.Skipped.Denied++
		} else {
			s.out.Skipped.Unreadable++
		}
		return false, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, s.spec.maxFileBytes+1))
	_ = f.Close()
	switch {
	case err != nil:
		s.out.Skipped.Unreadable++
		return false, nil
	case int64(len(data)) > s.spec.maxFileBytes, int64(len(data))*int64(s.spec.cost) > searchMaxFileCost:
		s.out.Skipped.TooLarge++
		return false, nil
	}
	s.bytes += int64(len(data))
	if bytes.IndexByte(data[:min(len(data), searchBinaryProbe)], 0) >= 0 {
		s.out.Skipped.Binary++
		return false, nil
	}
	s.out.FilesScanned++
	return s.match(rel, string(data))
}

func (s *searcher) match(rel, data string) (stop bool, err error) {
	lines := strings.Split(data, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	line := func(i int) string { return cutText(strings.TrimSuffix(lines[i], "\r")) }
	for i, l := range lines {
		if i%searchCheckLines == 0 {
			if err := s.check(); err != nil {
				return true, err
			}
		}
		col := s.spec.find(strings.TrimSuffix(l, "\r"))
		if col < 0 {
			continue
		}
		if len(s.out.Matches) == s.spec.maxMatches {
			s.out.Truncated = true
			return true, nil
		}
		m := execproto.SearchMatch{RelPath: rel, Line: i + 1, Column: col + 1, Text: line(i)}
		for j := max(0, i-s.spec.context); j < i; j++ {
			m.Before = append(m.Before, line(j))
		}
		for j := i + 1; j < len(lines) && j <= i+s.spec.context; j++ {
			m.After = append(m.After, line(j))
		}
		s.out.Matches = append(s.out.Matches, m)
	}
	return false, nil
}

// checkPrefixes holds every directory a write may create on its way to
// the file to the write rules (deny-list, .git and 8.3 ban). Resolve
// matched the whole path only, and "**/.env" does not match ".env/x".
func checkPrefixes(comps []string) error {
	acc := access(execproto.MethodFSWrite)
	var discard execproto.Resolved
	for i := 1; i < len(comps); i++ {
		if err := checkResult(acc, &discard, strings.Join(comps[:i], "/")); err != nil {
			return err
		}
	}
	return nil
}

// mkdirs opens comps beneath root idx one directory at a time without
// following links, creating the missing ones with mkdirat, and returns the
// last directory's fd and the directories it created (removed again when
// it fails). The caller checked the segments with checkPrefixes.
func (t *Tree) mkdirs(idx int, comps []string) (int, [][]string, error) {
	root := t.roots[idx]
	dir, err := unix.Openat(root.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, nil, ioError("cannot open the root")
	}
	var made [][]string
	for i, c := range comps {
		fd, err := openDir(dir, c)
		if errors.Is(err, execproto.ErrNotFound) {
			merr := unix.Mkdirat(dir, c, newDirMode)
			if merr == nil {
				made = append(made, append([]string(nil), comps[:i+1]...))
			}
			if merr != nil && !errors.Is(merr, unix.EEXIST) {
				err = ioError("cannot create a directory")
			} else if fd, err = openDir(dir, c); errors.Is(err, execproto.ErrNotFound) {
				// EEXIST and still no directory: a file is in the way.
				err = invalid("a parent path component is not a directory")
			}
		}
		if err == nil && !sameDev(fd, root.dev) {
			_ = unix.Close(fd)
			err = conflict()
		}
		_ = unix.Close(dir)
		if err != nil {
			t.removeDirs(idx, made)
			return -1, nil, err
		}
		dir = fd
	}
	return dir, made, nil
}

// removeDirs removes directories mkdirs created, deepest first, if they
// are still empty. Failures are ignored: a directory that cannot be
// reached or is no longer empty is left.
func (t *Tree) removeDirs(idx int, made [][]string) {
	for i := len(made) - 1; i >= 0; i-- {
		d := made[i]
		fd, err := t.openBeneath(idx, d[:len(d)-1], unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			continue
		}
		_ = unix.Unlinkat(fd, d[len(d)-1], unix.AT_REMOVEDIR)
		_ = unix.Close(fd)
	}
}

func randomName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ioError("no randomness for a temporary name")
	}
	return tempPrefix + hex.EncodeToString(b[:]), nil
}

// tempHexLen is the length of randomName's random part: 8 bytes in hex.
const tempHexLen = 16

// isTempName reports whether name has exactly the shape randomName
// produces (temporary files and staging directories alike): tempPrefix
// and 16 lowercase hex digits, nothing more. SweepTemps removes only
// these: a repository may commit a file named .warden-tmp-anything, and
// sweeping it would leave a deletion the agent could commit (review
// round 2).
func isTempName(name string) bool {
	rest, ok := strings.CutPrefix(name, tempPrefix)
	if !ok || len(rest) != tempHexLen {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if c := rest[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// createIn creates the file name in the directory dirComps of root through
// Resolve and OpenCanonical (O_CREAT | O_EXCL), writes data, sets perm and
// fsyncs. dirfd is the same directory, opened by the caller's walk: the
// new file must be in it, or a directory was swapped between the walk and
// the open, and the call fails conflict.
func (t *Tree) createIn(root execproto.RootName, method string, dirComps []string, dirfd int, name string, data []byte, perm uint32) error {
	rel := join(dirComps, name)
	r, err := t.Resolve(method, root, rel)
	if err != nil {
		return err
	}
	if r.RelPath != rel {
		return conflict()
	}
	f, err := t.OpenCanonical(r, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return err
	}
	var fst, dst unix.Stat_t
	ok := unix.Fstat(int(f.Fd()), &fst) == nil && unix.Fstatat(dirfd, name, &dst, unix.AT_SYMLINK_NOFOLLOW) == nil &&
		fst.Dev == dst.Dev && fst.Ino == dst.Ino
	if !ok {
		_ = f.Close()
		return conflict()
	}
	err = writeAll(f, data, perm)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = ioError("write failed")
	}
	if err != nil {
		_ = unix.Unlinkat(dirfd, name, 0)
	}
	return err
}

func writeAll(f *os.File, data []byte, perm uint32) error {
	if _, err := f.Write(data); err != nil {
		return ioError("write failed")
	}
	if err := unix.Fchmod(int(f.Fd()), perm); err != nil {
		return ioError("write failed")
	}
	if err := f.Sync(); err != nil {
		return ioError("write failed")
	}
	return nil
}

func (t *Tree) commitHook(step int) error {
	if t.beforeCommit != nil && t.beforeCommit(step) != nil {
		return ioError("commit failed")
	}
	return nil
}

// Write is exec.fs.write: one file, atomically (A06 §5.2). The content
// goes to a temporary file in the target's directory, is fsynced and
// renamed over the target in that directory's fd; mode create links the
// temporary file instead, which fails if the target appeared meanwhile. A
// new file gets 0644 (0755 with executable); an overwrite keeps the
// file's permission bits (adding execute with executable). A target that
// exists and is not a regular file is not_regular; a symlink there is a
// conflict, since Resolve has already followed every link in the path.
func (t *Tree) Write(_ context.Context, p execproto.WriteParams) (execproto.WriteResult, error) {
	spec, err := writeArgs(p)
	if err != nil {
		return execproto.WriteResult{}, err
	}
	res, err := t.Resolve(execproto.MethodFSWrite, "", p.Path)
	if err != nil {
		return execproto.WriteResult{}, err
	}
	if err := expectCanonical(res, p.ExpectCanonical); err != nil {
		return execproto.WriteResult{}, err
	}
	comps := splitPath(res.RelPath)
	if len(comps) == 0 {
		return execproto.WriteResult{}, invalid("the root cannot be written")
	}
	if err := checkPrefixes(comps); err != nil {
		return execproto.WriteResult{}, err
	}
	idx, _ := t.root(res.Root)
	parent, base := comps[:len(comps)-1], comps[len(comps)-1]
	dir, made, err := t.mkdirs(idx, parent)
	if err != nil {
		return execproto.WriteResult{}, err
	}
	defer unix.Close(dir)
	done := false
	defer func() {
		if !done {
			t.removeDirs(idx, made)
		}
	}()

	var st unix.Stat_t
	exists := false
	switch err := unix.Fstatat(dir, base, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == nil:
		exists = true
	case !errors.Is(err, unix.ENOENT):
		return execproto.WriteResult{}, ioError("stat failed")
	}
	perm := uint32(newFileMode)
	if exists {
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFREG:
		case unix.S_IFLNK:
			return execproto.WriteResult{}, conflict()
		default:
			return execproto.WriteResult{}, notRegular()
		}
		if spec.create {
			return execproto.WriteResult{}, invalid("the file exists and mode is create")
		}
		perm = uint32(st.Mode) & 0o777
	}
	if spec.executable {
		if exists {
			perm |= 0o111
		} else {
			perm = execMode
		}
	}

	tmp, err := randomName()
	if err != nil {
		return execproto.WriteResult{}, err
	}
	if err := t.createIn(res.Root, execproto.MethodFSWrite, parent, dir, tmp, spec.content, perm); err != nil {
		return execproto.WriteResult{}, err
	}
	err = t.commitHook(0)
	switch {
	case err != nil:
	case spec.create:
		if err = unix.Linkat(dir, tmp, dir, base, 0); errors.Is(err, unix.EEXIST) {
			err = invalid("the file exists and mode is create")
		} else if err != nil {
			err = ioError("write failed")
		}
	default:
		if unix.Renameat(dir, tmp, dir, base) != nil {
			err = ioError("write failed")
		}
	}
	if err != nil || spec.create {
		_ = unix.Unlinkat(dir, tmp, 0)
	}
	if err != nil {
		return execproto.WriteResult{}, err
	}
	done = true
	_ = unix.Fsync(dir)
	sum := sha256.Sum256(spec.content)
	return execproto.WriteResult{Resolved: res, BytesWritten: int64(len(spec.content)), Created: !exists, Sha256: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

// patchItem is one file of a patch, planned: resolved, read, applied.
type patchItem struct {
	fp       filePatch
	from, to execproto.Resolved
	orig     unix.Stat_t // of from, when there is one
	result   []byte
	perm     uint32
	file     execproto.PatchFile
}

func patchFailedAt(rel, detail string) error {
	e := execproto.NewError(execproto.ErrPatchFailed, detail)
	e.Data.RelPath = rel
	return e
}

// Patch is exec.fs.patch: a unified diff applied all or nothing.
//
// Plan: the patch is parsed completely (more than 4096 files is too_large
// before anything else, D-048); then for each file, in order, every path
// is resolved with the patch row of A06 §5.3 (worktree, no .git, no
// deny-listed name, and no deny-listed directory on the way), and must
// resolve to itself: a path that reaches its file through a symlink, even
// one inside the root, is refused, because the file changed would not be
// the file the patch names. The original is read like exec.fs.read (one
// link, regular, at most 4 MiB), the hunks are applied in memory, and
// unless dry_run the result is staged: written and fsynced in a staging
// directory at the worktree root, with any new parent directory created.
//
// Commit: one file at a time, in the directory fds reopened race-free,
// after checking that the original is still the file that was read; a
// modified or deleted original is kept as a link in the staging directory
// until the end. Any failure, in planning or in commit, undoes the steps
// already taken in reverse, removes the directories made and the staging
// directory, and returns the error: nothing changes.
func (t *Tree) Patch(ctx context.Context, p execproto.PatchParams) (execproto.PatchResult, error) {
	strip, err := patchArgs(p)
	if err != nil {
		return execproto.PatchResult{}, err
	}
	files, err := parsePatch(p.Patch, strip)
	if err != nil {
		return execproto.PatchResult{}, err
	}
	idx, ok := t.root(execproto.RootWorktree)
	if !ok || !access(execproto.MethodFSPatch).permits(t.roots[idx].Root) {
		return execproto.PatchResult{}, execproto.NewError(execproto.ErrOutsideRoots, "root not allowed for this method")
	}
	var sg *stager
	if !p.DryRun {
		if sg, err = t.newStager(idx); err != nil {
			return execproto.PatchResult{}, err
		}
		defer sg.cleanup()
	}
	seen := map[string]bool{}
	items := make([]patchItem, 0, len(files))
	var staged int
	// The result must fit the frame, or the server replaces it by
	// too_large after the files were written, and the daemon records a
	// failure for writes that happened (CWE-223/754, review round 1). It
	// is estimated from the parse before anything is resolved or staged,
	// dry run included, and then sized exactly as json.Marshal
	// (MarshalWire) will write it, entry by entry, before each is staged.
	if patchResultEstimate(files) > execproto.MaxFrameBytes-patchFrameReserve {
		return execproto.PatchResult{}, tooLarge("the patch result would exceed the 8 MiB frame")
	}
	resultSize := patchResultBase()
	for i, fp := range files {
		if err := ctx.Err(); err != nil {
			return execproto.PatchResult{}, err
		}
		it, err := t.planFile(fp, seen)
		if err != nil {
			return execproto.PatchResult{}, err
		}
		if staged += len(it.result); staged > patchMaxStagedBytes {
			return execproto.PatchResult{}, tooLarge("the patched files exceed the staging cap")
		}
		entry, err := json.Marshal(it.file)
		if err != nil {
			return execproto.PatchResult{}, ioError("result could not be encoded")
		}
		if resultSize += len(entry) + 1; resultSize > execproto.MaxFrameBytes-patchFrameReserve { // +1: the comma
			return execproto.PatchResult{}, tooLarge("the patch result would exceed the 8 MiB frame")
		}
		if sg != nil && fp.op != execproto.PatchDelete {
			if err := sg.stage(i, it); err != nil {
				return execproto.PatchResult{}, err
			}
		}
		it.result = nil // staged on disk (or a dry run): not held until the commit
		items = append(items, it)
	}
	out := execproto.PatchResult{Applied: !p.DryRun}
	for _, it := range items {
		out.Files = append(out.Files, it.file)
	}
	if sg != nil {
		if err := sg.commit(items); err != nil {
			return execproto.PatchResult{}, err
		}
	}
	return out, nil
}

// planFile resolves, reads and applies one file of a patch.
func (t *Tree) planFile(fp filePatch, seen map[string]bool) (patchItem, error) {
	it := patchItem{fp: fp}
	resolve := func(name string) (execproto.Resolved, error) {
		r, err := t.Resolve(execproto.MethodFSPatch, "", name)
		switch {
		case err != nil:
			return r, err
		case r.RelPath == "":
			return r, invalid("the root cannot be patched")
		case r.RelPath != r.RequestedRelPath:
			return r, patchFailedAt(r.RequestedRelPath, "the path goes through a symlink")
		}
		if err := checkPrefixes(splitPath(r.RelPath)); err != nil {
			return r, err
		}
		k := strings.ToLower(r.RelPath)
		if seen[k] {
			return r, invalid("a file appears more than once in the patch")
		}
		seen[k] = true
		return r, nil
	}
	var err error
	if fp.from != "" {
		if it.from, err = resolve(fp.from); err != nil {
			return it, err
		}
	}
	switch fp.op {
	case execproto.PatchModify:
		it.to = it.from
	case execproto.PatchCreate, execproto.PatchRename:
		if it.to, err = resolve(fp.to); err != nil {
			return it, err
		}
		if _, exists, err := t.lstat(it.to); err != nil {
			return it, err
		} else if exists {
			return it, patchFailedAt(it.to.RelPath, "the file already exists")
		}
	}

	var orig []byte
	if fp.from != "" {
		f, st, err := t.openFile(it.from)
		if errors.Is(err, execproto.ErrNotFound) {
			return it, patchFailedAt(it.from.RelPath, "the file does not exist")
		}
		if err != nil {
			return it, err
		}
		if st.Size > execproto.MaxWriteBytes {
			_ = f.Close()
			return it, tooLarge("a patched file exceeds the 4 MiB write cap")
		}
		orig, err = io.ReadAll(io.LimitReader(f, execproto.MaxWriteBytes+1))
		_ = f.Close()
		if err != nil {
			return it, ioError("read failed")
		}
		if len(orig) > execproto.MaxWriteBytes || int64(len(orig)) != st.Size {
			return it, conflict() // it changed while it was read
		}
		it.orig = st
		it.perm = uint32(st.Mode) & 0o777
	}
	if it.result, err = applyHunks(orig, fp.hunks); err != nil {
		var e *execproto.Error
		if errors.As(err, &e) {
			e.Data.RelPath = fp.relPathOf(it)
		}
		return it, err
	}
	switch {
	case fp.op == execproto.PatchDelete && len(it.result) != 0:
		return it, patchFailedAt(it.from.RelPath, "the hunks do not remove the whole file")
	case len(it.result) > execproto.MaxWriteBytes:
		return it, tooLarge("a patched file exceeds the 4 MiB write cap")
	}
	if fp.op == execproto.PatchCreate {
		it.perm = newFileMode
	}
	if fp.mode != 0 {
		it.perm = fp.mode
	}
	it.file = execproto.PatchFile{RelPath: fp.relPathOf(it), Op: fp.op, Hunks: len(fp.hunks)}
	if fp.op == execproto.PatchRename {
		it.file.FromRelPath = it.from.RelPath
	}
	if fp.op != execproto.PatchDelete {
		sum := sha256.Sum256(it.result)
		it.file.Sha256After = "sha256:" + hex.EncodeToString(sum[:])
	}
	return it, nil
}

// relPathOf is the resolved rel_path of the file's result entry.
func (fp filePatch) relPathOf(it patchItem) string {
	if fp.op == execproto.PatchDelete {
		return it.from.RelPath
	}
	return it.to.RelPath
}

// patchResultEstimate is the encoded size of the PatchResult of files,
// computed from the parse alone, before any path is resolved or anything
// staged. It never underestimates: a relative name cleaned as Resolve
// cleans it (backslashes as separators, path.Clean) is exactly the
// rel_path the patch reports, since a path that resolves to anything else
// is refused, and an absolute name only gets shorter once its root prefix
// is removed. Sha256After always has the same length.
func patchResultEstimate(files []filePatch) int {
	clean := func(n string) string {
		c := path.Clean(strings.ReplaceAll(n, `\`, "/"))
		if c == "." {
			return ""
		}
		return c
	}
	size := patchResultBase()
	for _, fp := range files {
		e := execproto.PatchFile{Op: fp.op, Hunks: len(fp.hunks)}
		switch fp.op {
		case execproto.PatchDelete:
			e.RelPath = clean(fp.from)
		case execproto.PatchRename:
			e.RelPath, e.FromRelPath = clean(fp.to), clean(fp.from)
		default:
			e.RelPath = clean(fp.to)
		}
		if fp.op != execproto.PatchDelete {
			e.Sha256After = "sha256:" + strings.Repeat("0", 64)
		}
		b, _ := json.Marshal(e)
		size += len(b) + 1
	}
	return size
}

// patchResultBase is the encoded size of a PatchResult with no files,
// with the longer of the two applied values: what every entry is added to.
func patchResultBase() int {
	b, _ := json.Marshal(execproto.PatchResult{Applied: false, Files: []execproto.PatchFile{}})
	return len(b)
}

// stager holds one patch's staging directory: a fresh directory at the
// worktree root, open for the whole call, holding the staged results
// ("t<i>") and the originals replaced or removed by the commit ("b<i>").
// It is the one fd the patch keeps, whatever the number of files.
//
// Its name starts with tempPrefix. Normally cleanup removes it; it stays
// when an undo step failed (keep), because it then holds the only copy of
// the originals, and when the executor dies mid-patch. Either way the
// executor removes it at its next start (Tree.SweepTemps).
type stager struct {
	t         *Tree
	idx       int
	name      string
	fd        int
	made      [][]string // parent directories created for the results
	committed bool
	keep      bool
}

func (t *Tree) newStager(idx int) (*stager, error) {
	name, err := randomName()
	if err != nil {
		return nil, err
	}
	root := t.roots[idx]
	if unix.Mkdirat(root.fd, name, stageMode) != nil {
		return nil, ioError("cannot create the staging directory")
	}
	fd, err := openDir(root.fd, name)
	if err != nil {
		_ = unix.Unlinkat(root.fd, name, unix.AT_REMOVEDIR)
		return nil, err
	}
	return &stager{t: t, idx: idx, name: name, fd: fd}, nil
}

// stage creates the result's parent directories and writes the result.
func (s *stager) stage(i int, it patchItem) error {
	comps := splitPath(it.to.RelPath)
	dir, made, err := s.t.mkdirs(s.idx, comps[:len(comps)-1])
	if err != nil {
		return err
	}
	_ = unix.Close(dir)
	s.made = append(s.made, made...)
	return s.t.createIn(execproto.RootWorktree, execproto.MethodFSPatch, []string{s.name}, s.fd, "t"+strconv.Itoa(i), it.result, it.perm)
}

// cleanup empties and removes the staging directory and, unless the patch
// was committed, the directories made for it. A kept staging directory
// (an undo failed) and the tree around it are left as they are.
func (s *stager) cleanup() {
	if s.keep {
		_ = unix.Close(s.fd)
		return
	}
	if names, _, err := readNames(s.fd); err == nil {
		for _, n := range names {
			_ = unix.Unlinkat(s.fd, n, 0)
		}
	}
	_ = unix.Close(s.fd)
	_ = unix.Unlinkat(s.t.roots[s.idx].fd, s.name, unix.AT_REMOVEDIR)
	if !s.committed {
		s.t.removeDirs(s.idx, s.made)
	}
}

// parent opens the directory of a resolved path race-free.
func (s *stager) parent(r execproto.Resolved) (int, string, error) {
	comps := splitPath(r.RelPath)
	fd, err := s.t.openBeneath(s.idx, comps[:len(comps)-1], unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return -1, "", err
	}
	return fd, comps[len(comps)-1], nil
}

// unchanged checks that the original is still the file that was read.
func unchanged(dir int, base string, st unix.Stat_t) error {
	var now unix.Stat_t
	if unix.Fstatat(dir, base, &now, unix.AT_SYMLINK_NOFOLLOW) != nil || now.Mode&unix.S_IFMT != unix.S_IFREG ||
		now.Dev != st.Dev || now.Ino != st.Ino || now.Size != st.Size || now.Mtim != st.Mtim {
		return conflict()
	}
	return nil
}

// commit applies the staged results in order, undoing every step taken if
// one fails.
func (s *stager) commit(items []patchItem) error {
	var undo []func() error
	for i, it := range items {
		err := s.t.commitHook(i)
		var u func() error
		if err == nil {
			u, err = s.commitOne(i, it)
		}
		if err != nil {
			for j := len(undo) - 1; j >= 0; j-- {
				if undo[j]() != nil {
					s.keep = true
				}
			}
			if s.keep {
				return s.keptError()
			}
			return err
		}
		undo = append(undo, u)
	}
	s.committed = true
	return nil
}

// keptError is the io_error of a patch whose rollback failed: the tree is
// partly patched, and the staging directory, which holds the originals
// ("b<i>"), is kept and named in rel_path by its own temporary name, which
// carries nothing from the patch or the tree.
func (s *stager) keptError() error {
	e := ioError("the patch failed and could not be rolled back completely; the originals are kept in the staging directory")
	e.(*execproto.Error).Data.RelPath = s.name
	return e
}

// commitOne applies one staged result and returns how to undo it.
func (s *stager) commitOne(i int, it patchItem) (func() error, error) {
	tmp, bak := "t"+strconv.Itoa(i), "b"+strconv.Itoa(i)
	withParent := func(r execproto.Resolved, fn func(dir int, base string) error) error {
		dir, base, err := s.parent(r)
		if err != nil {
			return err
		}
		defer unix.Close(dir)
		if err := fn(dir, base); err != nil {
			return err
		}
		_ = unix.Fsync(dir)
		return nil
	}
	// link puts the staged result at r; it fails if anything is there.
	link := func(r execproto.Resolved) error {
		return withParent(r, func(dir int, base string) error {
			if err := unix.Linkat(s.fd, tmp, dir, base, 0); errors.Is(err, unix.EEXIST) {
				return conflict()
			} else if err != nil {
				return ioError("commit failed")
			}
			return nil
		})
	}
	unlink := func(r execproto.Resolved) func() error {
		return func() error {
			return withParent(r, func(dir int, base string) error { return unix.Unlinkat(dir, base, 0) })
		}
	}
	restore := func(r execproto.Resolved) func() error {
		return func() error {
			return withParent(r, func(dir int, base string) error { return unix.Renameat(s.fd, bak, dir, base) })
		}
	}
	// takeOriginal moves (or, for a modify, links) the original into the
	// staging directory after checking it is the file that was read.
	takeOriginal := func(dir int, base string, keep bool) error {
		if err := unchanged(dir, base, it.orig); err != nil {
			return err
		}
		var err error
		if keep {
			err = unix.Linkat(dir, base, s.fd, bak, 0)
		} else {
			err = unix.Renameat(dir, base, s.fd, bak)
		}
		if err != nil {
			return ioError("commit failed")
		}
		return nil
	}

	switch it.fp.op {
	case execproto.PatchModify:
		err := withParent(it.to, func(dir int, base string) error {
			if err := takeOriginal(dir, base, true); err != nil {
				return err
			}
			if unix.Renameat(s.fd, tmp, dir, base) != nil {
				_ = unix.Unlinkat(s.fd, bak, 0)
				return ioError("commit failed")
			}
			return nil
		})
		return restore(it.to), err
	case execproto.PatchCreate:
		return unlink(it.to), link(it.to)
	case execproto.PatchDelete:
		err := withParent(it.from, func(dir int, base string) error { return takeOriginal(dir, base, false) })
		return restore(it.from), err
	default: // rename
		if err := link(it.to); err != nil {
			return nil, err
		}
		if err := withParent(it.from, func(dir int, base string) error { return takeOriginal(dir, base, false) }); err != nil {
			if unlink(it.to)() != nil {
				s.keep = true // half a rename left: commit reports it like a failed undo
			}
			return nil, err
		}
		return func() error {
			err := unlink(it.to)()
			if rerr := restore(it.from)(); err == nil {
				err = rerr
			}
			return err
		}, nil
	}
}

// SweepTemps removes what an interrupted exec.fs.patch or exec.fs.write
// left at the worktree root: entries named exactly as randomName names them
// (isTempName: .warden-tmp- and 16 lowercase hex digits) that the
// executor's user owns. A staging directory is emptied of its files and
// removed; anything else (a file, or a link, which is removed as a link
// and never followed) is unlinked. Nothing is followed and nothing outside
// the root's own entries is touched; a directory with a subdirectory in it
// is left. It returns how many entries it removed.
//
// cmd/warden-exec calls it at start, before serving (M2-10): a staging
// directory kept after a failed rollback, or left by a crash, would
// otherwise stay in the worktree, where git add -A would pick it up.
// Temporary files of writes in subdirectories are not swept: a walk of the
// whole tree at every start is not worth it for empty or inert files.
func (t *Tree) SweepTemps() (int, error) {
	idx, ok := t.root(execproto.RootWorktree)
	if !ok || t.roots[idx].Mode != execproto.RootRW {
		return 0, nil
	}
	root := t.roots[idx].fd
	names, _, err := readNames(root)
	if err != nil {
		return 0, err
	}
	uid := os.Geteuid()
	n := 0
	for _, name := range names {
		if !isTempName(name) {
			continue
		}
		var st unix.Stat_t
		if unix.Fstatat(root, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || int(st.Uid) != uid {
			continue
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			dir, err := openDir(root, name)
			if err != nil {
				continue
			}
			if inner, _, err := readNames(dir); err == nil {
				for _, e := range inner {
					var est unix.Stat_t
					if unix.Fstatat(dir, e, &est, unix.AT_SYMLINK_NOFOLLOW) == nil && est.Mode&unix.S_IFMT != unix.S_IFDIR {
						_ = unix.Unlinkat(dir, e, 0)
					}
				}
			}
			_ = unix.Close(dir)
			if unix.Unlinkat(root, name, unix.AT_REMOVEDIR) == nil {
				n++
			}
			continue
		}
		if unix.Unlinkat(root, name, 0) == nil {
			n++
		}
	}
	return n, nil
}
