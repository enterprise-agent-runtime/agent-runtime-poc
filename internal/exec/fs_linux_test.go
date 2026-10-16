//go:build linux

package exec

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
)

// These are the exec.fs methods (design A06 §4.2-§4.4, §5.2) on a real
// file system, each run through both open paths (openat2 and the component
// walk, see tree_linux_test.go). They hold the rules that earlier reviews
// found missing and that only the file system can show: a FIFO must not
// block the executor (§5.2 "opened O_NONBLOCK first"); a hard link must not
// read a protected file's content under another name (CWE-59); a write is
// a rename in a directory fd, so a swapped directory or a hard link can
// never redirect it (CWE-367); a write must not create a deny-listed or
// .git directory on its way to the file; and a patch either applies
// completely or leaves every byte as it was.

// hangGuard bounds waits for calls that must return. It is a deadlock
// guard, not a performance budget (CLAUDE.md §8.6).
const hangGuard = 20 * time.Second

var bg = context.Background()

func bothModes(t *testing.T, fn func(t *testing.T, f *fixture)) {
	t.Run("openat2", func(t *testing.T) { fn(t, newFixture(t, modeOpenat2)) })
	t.Run("fallback", func(t *testing.T) { fn(t, newFixture(t, modeFallback)) })
}

func sha(b string) string {
	s := sha256.Sum256([]byte(b))
	return "sha256:" + hex.EncodeToString(s[:])
}

func (f *fixture) content(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.base, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func (f *fixture) missing(t *testing.T, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(f.base, rel)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists (%v), want nothing there", rel, err)
	}
}

// snapshot is every entry under base (worktree, scratch and outside) with
// its kind, mode and content or link target: what "nothing changed" means.
func (f *fixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(f.base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(f.base, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "link " + target
		case d.IsDir():
			out[rel] = fmt.Sprintf("dir %o", info.Mode().Perm())
		default:
			b, _ := os.ReadFile(p)
			out[rel] = fmt.Sprintf("file %o %q", info.Mode().Perm(), b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshot(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s: %s was %s, is %q", what, k, v, after[k])
		}
	}
	for k, v := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("%s: %s appeared (%s)", what, k, v)
		}
	}
}

// noTemps: no .warden-tmp- entry is left anywhere, whatever happened.
func (f *fixture) noTemps(t *testing.T) {
	t.Helper()
	_ = filepath.WalkDir(f.base, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), tempPrefix) {
			t.Errorf("temporary file left behind: %s", p)
		}
		return nil
	})
}

// returns runs fn and fails the test if it does not return in hangGuard.
func returns[R any](t *testing.T, what string, fn func() (R, error)) (R, error) {
	t.Helper()
	type out struct {
		r   R
		err error
	}
	c := make(chan out, 1)
	go func() { r, err := fn(); c <- out{r, err} }()
	select {
	case o := <-c:
		return o.r, o.err
	case <-time.After(hangGuard):
		t.Fatalf("%s did not return: the executor is blocked", what)
		var zero R
		return zero, nil
	}
}

func TestFSRead(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		r, err := f.tree.Read(bg, execproto.ReadParams{Path: "sub/f.txt"})
		if err != nil {
			t.Fatal(err)
		}
		if r.Content != "inside" || r.Encoding != execproto.EncodingUTF8 || r.Size != 6 || r.Offset != 0 || r.Returned != 6 || !r.EOF || r.Sha256 != sha("inside") {
			t.Errorf("whole read: %+v", r)
		}
		if r.Resolved.RelPath != "sub/f.txt" || r.Resolved.Canonical != f.work+"/sub/f.txt" {
			t.Errorf("resolved: %+v", r.Resolved)
		}

		r, err = f.tree.Read(bg, execproto.ReadParams{Path: "sub/f.txt", Offset: 2, Length: 2})
		if err != nil || r.Content != "si" || r.Returned != 2 || r.EOF || r.Size != 6 || r.Offset != 2 || r.Sha256 != sha("si") {
			t.Errorf("range: %+v, %v", r, err)
		}
		r, err = f.tree.Read(bg, execproto.ReadParams{Path: "sub/f.txt", Offset: 4, Length: 10})
		if err != nil || r.Content != "de" || !r.EOF {
			t.Errorf("range to the end: %+v, %v", r, err)
		}
		r, err = f.tree.Read(bg, execproto.ReadParams{Path: "sub/f.txt", Offset: 100})
		if err != nil || r.Content != "" || r.Returned != 0 || !r.EOF || r.Sha256 != sha("") {
			t.Errorf("past the end: %+v, %v", r, err)
		}

		f.file("work/bin.dat", "a\x00b")
		r, err = f.tree.Read(bg, execproto.ReadParams{Path: "bin.dat"})
		if err != nil || r.Encoding != execproto.EncodingBase64 || r.Content != base64.StdEncoding.EncodeToString([]byte("a\x00b")) {
			t.Errorf("binary: %+v, %v", r, err)
		}

		// scratch is readable (daemon-internal reads, A06 §5.3).
		r, err = f.tree.Read(bg, execproto.ReadParams{Path: "vitest.json", Root: execproto.RootScratch})
		if err != nil || r.Content != "{}" || r.Resolved.Root != execproto.RootScratch {
			t.Errorf("scratch: %+v, %v", r, err)
		}

		// expect_canonical (A06 §5.2): the path the PDP decided on.
		if _, err := f.tree.Read(bg, execproto.ReadParams{Path: "sub/f.txt", ExpectCanonical: f.work + "/sub/f.txt"}); err != nil {
			t.Errorf("matching expect_canonical: %v", err)
		}
		f.link(t, "sub/f.txt", "work/alias.txt")
		_, err = f.tree.Read(bg, execproto.ReadParams{Path: "alias.txt", ExpectCanonical: f.work + "/alias.txt"})
		wantCode(t, "expect_canonical differs from the fresh resolution", err, execproto.ErrConflict)

		for _, c := range []struct {
			path string
			code execproto.Code
		}{
			{".env", execproto.ErrDenyList},
			{"../outside/secret", execproto.ErrPathEscape},
			{"nope.txt", execproto.ErrNotFound},
			{"sub", execproto.ErrNotRegular},
			{"", execproto.ErrInvalidArgument},
		} {
			_, err := f.tree.Read(bg, execproto.ReadParams{Path: c.path})
			wantCode(t, "read "+c.path, err, c.code)
		}
		_, err = f.tree.Read(bg, execproto.ReadParams{Path: "sub/f.txt", Length: execproto.MaxReadBytes + 1})
		wantCode(t, "length over the cap", err, execproto.ErrTooLarge)
	})
}

// TestFSRead_ReadsAtMostTheCap: a file larger than 2 MiB is returned in
// parts, each at most MaxReadBytes, never all at once.
func TestFSRead_ReadsAtMostTheCap(t *testing.T) {
	f := newFixture(t, modeFallback)
	f.file("work/big", strings.Repeat("x", execproto.MaxReadBytes+10))
	r, err := f.tree.Read(bg, execproto.ReadParams{Path: "big"})
	if err != nil || r.Returned != execproto.MaxReadBytes || r.EOF || r.Size != execproto.MaxReadBytes+10 {
		t.Fatalf("first part: returned %d eof %v size %d, %v", r.Returned, r.EOF, r.Size, err)
	}
	r, err = f.tree.Read(bg, execproto.ReadParams{Path: "big", Offset: execproto.MaxReadBytes})
	if err != nil || r.Returned != 10 || !r.EOF {
		t.Fatalf("second part: returned %d eof %v, %v", r.Returned, r.EOF, err)
	}
}

// TestFSRead_SpecialFilesNeverBlock is A06 §5.2: "files are opened
// O_NONBLOCK first so a FIFO cannot block the executor". A FIFO without a
// writer blocks a plain open forever; a model that can create one (mkfifo
// in a spawned process) could otherwise hang every later file call. Read,
// write and search must all refuse it, and a socket and a directory, with
// not_regular, and return.
func TestFSRead_SpecialFilesNeverBlock(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		if err := unix.Mkfifo(f.work+"/pipe", 0o600); err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("unix", f.work+"/sock")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		for _, p := range []string{"pipe", "sock", "sub"} {
			_, err := returns(t, "read "+p, func() (execproto.ReadResult, error) {
				return f.tree.Read(bg, execproto.ReadParams{Path: p})
			})
			wantCode(t, "read "+p, err, execproto.ErrNotRegular)
		}
		for _, p := range []string{"pipe", "sock", "sub"} {
			_, err := returns(t, "write "+p, func() (execproto.WriteResult, error) {
				return f.tree.Write(bg, execproto.WriteParams{Path: p, Content: "x"})
			})
			wantCode(t, "write "+p, err, execproto.ErrNotRegular)
		}
		res, err := returns(t, "search", func() (execproto.SearchResult, error) {
			return f.tree.Search(bg, execproto.SearchParams{Query: "inside"})
		})
		if err != nil || len(res.Matches) != 1 {
			t.Errorf("search beside a FIFO: %+v, %v", res, err)
		}
		_, err = returns(t, "patch pipe", func() (execproto.PatchResult, error) {
			return f.tree.Patch(bg, execproto.PatchParams{Patch: "--- a/pipe\n+++ b/pipe\n@@ -1 +1 @@\n-a\n+b\n"})
		})
		wantCode(t, "patch a FIFO", err, execproto.ErrNotRegular)
	})
}

// TestFSRead_HardLinkRefused is CWE-59. A hard link is the same file under
// a second name: a link work/notes.txt to work/.env reads the secret under
// a name the deny-list does not match, and a link to a file outside the
// roots (on the same file system) reads outside them, with no symlink for
// Resolve to see. The executor cannot find a file's other names, so any
// regular file with more than one link is refused for content access:
// read and patch fail deny_list (a sandbox.violation of kind deny_list),
// search skips it as denied. Stat and list show only metadata and still
// work, and a write replaces the name by rename, never writing through.
func TestFSRead_HardLinkRefused(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		if err := os.Link(f.work+"/.env", f.work+"/notes.txt"); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(f.outside+"/secret", f.work+"/s.txt"); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"notes.txt", "s.txt"} {
			_, err := f.tree.Read(bg, execproto.ReadParams{Path: p})
			wantCode(t, "read hard link "+p, err, execproto.ErrDenyList)
			var e *execproto.Error
			if errors.As(err, &e) && (!e.Data.Violation || e.Data.Kind != execproto.ViolationDenyList) {
				t.Errorf("hard link refusal is not a deny_list violation: %+v", e.Data)
			}
		}
		res, err := f.tree.Search(bg, execproto.SearchParams{Query: "TOKEN"})
		if err != nil || len(res.Matches) != 0 || res.Skipped.Denied != 3 {
			t.Errorf("search: %+v, %v; want no match and .env, notes.txt and s.txt denied", res, err)
		}
		res, err = f.tree.Search(bg, execproto.SearchParams{Query: "outside"})
		if err != nil || len(res.Matches) != 0 {
			t.Errorf("search found content outside the roots through a hard link: %+v, %v", res, err)
		}
		_, err = f.tree.Patch(bg, execproto.PatchParams{Patch: "--- a/notes.txt\n+++ b/notes.txt\n@@ -1 +1 @@\n-TOKEN=x\n+TOKEN=y\n"})
		wantCode(t, "patch a hard link", err, execproto.ErrDenyList)
		if f.content(t, "work/.env") != "TOKEN=x" {
			t.Error(".env changed through a hard link")
		}
		// A rename-only patch would copy the content to a fresh,
		// single-link name that read then accepts: refused too.
		_, err = f.tree.Patch(bg, execproto.PatchParams{Patch: "diff --git a/s.txt b/t.txt\nrename from s.txt\nrename to t.txt\n"})
		wantCode(t, "rename a hard link", err, execproto.ErrDenyList)
		f.missing(t, "work/t.txt")

		if st, err := f.tree.Stat(bg, execproto.StatParams{Path: "notes.txt"}); err != nil || !st.Exists || st.Kind != execproto.KindFile {
			t.Errorf("stat of a hard link: %+v, %v", st, err)
		}
		if _, err := f.tree.Write(bg, execproto.WriteParams{Path: "s.txt", Content: "replaced"}); err != nil {
			t.Fatal(err)
		}
		if f.content(t, "outside/secret") != "outside the roots" || f.content(t, "work/s.txt") != "replaced" {
			t.Error("a write went through a hard link instead of replacing the name")
		}
	})
}

func TestFSWrite(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		w, err := f.tree.Write(bg, execproto.WriteParams{Path: "new/dir/a.txt", Content: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		if !w.Created || w.BytesWritten != 5 || w.Sha256 != sha("hello") || w.Resolved.RelPath != "new/dir/a.txt" {
			t.Errorf("create nested: %+v", w)
		}
		if f.content(t, "work/new/dir/a.txt") != "hello" {
			t.Error("content not on disk")
		}
		if st, _ := os.Stat(f.work + "/new/dir/a.txt"); st.Mode().Perm() != 0o644 {
			t.Errorf("new file mode %o, want 644", st.Mode().Perm())
		}

		// Overwrite keeps the file's permission bits.
		must(t, os.Chmod(f.work+"/sub/f.txt", 0o640))
		w, err = f.tree.Write(bg, execproto.WriteParams{Path: "sub/f.txt", Content: "changed"})
		if err != nil || w.Created || f.content(t, "work/sub/f.txt") != "changed" {
			t.Errorf("overwrite: %+v, %v", w, err)
		}
		if st, _ := os.Stat(f.work + "/sub/f.txt"); st.Mode().Perm() != 0o640 {
			t.Errorf("overwrite changed the mode to %o", st.Mode().Perm())
		}
		if _, err := f.tree.Write(bg, execproto.WriteParams{Path: "run.sh", Content: "#!/bin/sh\n", Executable: true}); err != nil {
			t.Fatal(err)
		}
		if st, _ := os.Stat(f.work + "/run.sh"); st.Mode().Perm() != 0o755 {
			t.Errorf("executable mode %o, want 755", st.Mode().Perm())
		}

		// mode create refuses an existing file and changes nothing.
		_, err = f.tree.Write(bg, execproto.WriteParams{Path: "sub/f.txt", Content: "no", Mode: execproto.WriteCreate})
		wantCode(t, "create over an existing file", err, execproto.ErrInvalidArgument)
		if f.content(t, "work/sub/f.txt") != "changed" {
			t.Error("create mode overwrote a file")
		}
		if _, err := f.tree.Write(bg, execproto.WriteParams{Path: "fresh.txt", Content: "1", Mode: execproto.WriteCreate}); err != nil {
			t.Errorf("create a new file: %v", err)
		}

		// A symlink inside the root resolves to its target (the canonical
		// path the PDP decided on); the link itself stays a link.
		f.link(t, "sub/f.txt", "work/l.txt")
		w, err = f.tree.Write(bg, execproto.WriteParams{Path: "l.txt", Content: "via link"})
		if err != nil || w.Resolved.RelPath != "sub/f.txt" || f.content(t, "work/sub/f.txt") != "via link" {
			t.Errorf("write through an in-root link: %+v, %v", w, err)
		}
		if fi, _ := os.Lstat(f.work + "/l.txt"); fi.Mode()&fs.ModeSymlink == 0 {
			t.Error("the link was replaced")
		}

		_, err = f.tree.Write(bg, execproto.WriteParams{Path: "sub/f.txt", Content: "x", ExpectCanonical: f.work + "/other"})
		wantCode(t, "expect_canonical mismatch", err, execproto.ErrConflict)
		if f.content(t, "work/sub/f.txt") != "via link" {
			t.Error("a conflicting write changed the file")
		}

		for _, c := range []struct {
			path string
			code execproto.Code
		}{
			{"", execproto.ErrInvalidArgument},
			{".", execproto.ErrInvalidArgument},
			{"../outside/x", execproto.ErrPathEscape},
			{"sub/f.txt/x", execproto.ErrInvalidArgument},
		} {
			_, err := f.tree.Write(bg, execproto.WriteParams{Path: c.path, Content: "x"})
			wantCode(t, "write "+c.path, err, c.code)
		}
		_, err = f.tree.Write(bg, execproto.WriteParams{Path: "k.txt", Content: "[REDACTED:openai_key]"})
		wantCode(t, "redaction marker", err, execproto.ErrRedactionMarker)
		f.missing(t, "work/k.txt")
		f.noTemps(t)
	})
}

// TestFSWrite_DenyListedParentNotCreated: parent directories are created
// with mkdirat, which OpenCanonical's checks never see. Resolve matches the
// whole path, and "**/.env" does not match ".env/x", so without a check of
// every new segment a write to ".env/x" would create a directory named
// .env (and "a/.git/x" a nested .git). Every prefix is held to the write
// rules before anything is created.
func TestFSWrite_DenyListedParentNotCreated(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		cases := []struct {
			path, first string
			code        execproto.Code
		}{
			{"nd/.env/x", "nd", execproto.ErrDenyList},
			{"nd/id_rsa/x", "nd", execproto.ErrDenyList},
			{"nd/credentials.json/x", "nd", execproto.ErrDenyList},
			{"nd/x.pem/y", "nd", execproto.ErrDenyList},
			{"nd/.ssh/x", "nd", execproto.ErrDenyList},
			{"nd/.warden/x", "nd", execproto.ErrDenyList},
			{"nd/ENV~1/x", "nd", execproto.ErrOutsideRoots},
			{"nd/.git/hooks/pre-commit", "nd", execproto.ErrOutsideRoots},
			{"nd/.GIT/x", "nd", execproto.ErrOutsideRoots},
		}
		before := f.snapshot(t)
		for _, c := range cases {
			_, err := f.tree.Write(bg, execproto.WriteParams{Path: c.path, Content: "x"})
			wantCode(t, "write "+c.path, err, c.code)
			f.missing(t, "work/"+c.first)
			_, err = f.tree.Patch(bg, execproto.PatchParams{Patch: "--- /dev/null\n+++ b/" + c.path + "\n@@ -0,0 +1 @@\n+x\n"})
			wantCode(t, "patch "+c.path, err, c.code)
			f.missing(t, "work/"+c.first)
		}
		sameSnapshot(t, "refused writes", before, f.snapshot(t))
	})
}

// TestFSWrite_SwapDuringWrite is CWE-367 for writes. The parent directory
// is replaced by a symlink out of the root at the two moments that matter:
// while the temporary file is opened (the open must refuse with conflict),
// and after it was written but before the rename (the rename happens in the
// directory fd held since the walk, so the file lands in the real
// directory, wherever it was moved, and never outside).
func TestFSWrite_SwapDuringWrite(t *testing.T) {
	swap := func(t *testing.T, f *fixture) {
		if err := os.Rename(f.work+"/sub", f.work+"/sub.moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(f.outside, f.work+"/sub"); err != nil {
			t.Fatal(err)
		}
	}
	bothModes(t, func(t *testing.T, f *fixture) {
		done := false
		f.tree.beforeOpen = func(int) {
			if !done {
				done = true
				swap(t, f)
			}
		}
		_, err := f.tree.Write(bg, execproto.WriteParams{Path: "sub/new.txt", Content: "x"})
		wantCode(t, "swap before the temporary file is opened", err, execproto.ErrConflict)
		f.missing(t, "outside/new.txt")
		if es, _ := os.ReadDir(f.outside); len(es) != 1 {
			t.Errorf("outside holds %d entries, want only secret", len(es))
		}
		f.noTemps(t)
	})
	bothModes(t, func(t *testing.T, f *fixture) {
		f.tree.beforeCommit = func(int) error { swap(t, f); return nil }
		if _, err := f.tree.Write(bg, execproto.WriteParams{Path: "sub/new.txt", Content: "x"}); err != nil {
			t.Fatal(err)
		}
		f.missing(t, "outside/new.txt")
		if f.content(t, "work/sub.moved/new.txt") != "x" {
			t.Error("the rename did not happen in the directory that was walked")
		}
		f.noTemps(t)
	})
}

func TestFSStat(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		st, err := f.tree.Stat(bg, execproto.StatParams{Path: "sub/f.txt"})
		if err != nil || !st.Exists || st.Kind != execproto.KindFile || st.Size == nil || *st.Size != 6 || st.Mode != "0600" || st.MTime == nil {
			t.Errorf("file: %+v, %v", st, err)
		}
		st, err = f.tree.Stat(bg, execproto.StatParams{Path: "."})
		if err != nil || !st.Exists || st.Kind != execproto.KindDir || st.Resolved.RelPath != "" {
			t.Errorf("root: %+v, %v", st, err)
		}
		for _, p := range []string{"nope", "no/such/dir/f"} {
			st, err = f.tree.Stat(bg, execproto.StatParams{Path: p})
			if err != nil || st.Exists || st.Kind != "" || st.Size != nil {
				t.Errorf("missing %s: %+v, %v", p, st, err)
			}
		}
		// A deny-listed path is reported, not refused (A06 §5.1 step 6), and
		// like a denied list entry shows no size, mode or mtime.
		st, err = f.tree.Stat(bg, execproto.StatParams{Path: ".env"})
		if err != nil || st.Resolved.DenyMatch == nil || *st.Resolved.DenyMatch != "**/.env" || !st.Exists || st.Kind != execproto.KindFile || st.Size != nil || st.Mode != "" || st.MTime != nil {
			t.Errorf("denied: %+v, %v", st, err)
		}
		f.link(t, "sub/f.txt", "work/l.txt")
		st, err = f.tree.Stat(bg, execproto.StatParams{Path: "l.txt"})
		if err != nil || st.Resolved.RelPath != "sub/f.txt" || st.Kind != execproto.KindFile {
			t.Errorf("through a link: %+v, %v", st, err)
		}
		st, err = f.tree.Stat(bg, execproto.StatParams{Path: "vitest.json", Root: execproto.RootScratch})
		if err != nil || !st.Exists {
			t.Errorf("scratch: %+v, %v", st, err)
		}
		_, err = f.tree.Stat(bg, execproto.StatParams{Path: "../outside/secret"})
		wantCode(t, "escape", err, execproto.ErrPathEscape)
	})
}

func entryNames(es []execproto.ListEntry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.RelPath)
	}
	return strings.Join(out, ",")
}

func TestFSList(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		f.link(t, "sub", "work/ln")
		f.file("work/.aws/credentials", "[default]")
		r, err := f.tree.List(bg, execproto.ListParams{})
		if err != nil {
			t.Fatal(err)
		}
		if got := entryNames(r.Entries); got != ".aws,.env,a,ln,sub" || r.Truncated {
			t.Errorf("depth 1: %s truncated %v", got, r.Truncated)
		}
		for _, e := range r.Entries {
			switch e.RelPath {
			case ".env", ".aws":
				// A06: denied entries are listed by name, with size and
				// mtime omitted; the mode and a link target are left out
				// too (fs.list.result golden).
				if !e.Denied || e.Size != nil || e.MTime != nil || e.Mode != "" || e.LinkTarget != "" {
					t.Errorf("denied entry shows metadata: %+v", e)
				}
			case "ln":
				if e.Kind != execproto.KindSymlink || e.LinkTarget != "sub" || e.Denied {
					t.Errorf("symlink entry: %+v", e)
				}
			case "sub", "a":
				if e.Kind != execproto.KindDir || e.Mode != "0700" || e.MTime == nil || e.Denied {
					t.Errorf("dir entry: %+v", e)
				}
			}
		}
		// Breadth first, sorted; symlinks and denied directories are not
		// entered, so neither ln/f.txt nor .aws/credentials appears.
		r, err = f.tree.List(bg, execproto.ListParams{Depth: execproto.MaxListDepth})
		if got := entryNames(r.Entries); err != nil || got != ".aws,.env,a,ln,sub,a/b,sub/f.txt,a/b/c.txt" {
			t.Errorf("depth 4: %s, %v", got, err)
		}
		r, err = f.tree.List(bg, execproto.ListParams{Depth: 4, MaxEntries: 2})
		if got := entryNames(r.Entries); err != nil || got != ".aws,.env" || !r.Truncated {
			t.Errorf("max_entries 2: %s truncated %v, %v", got, r.Truncated, err)
		}
		r, err = f.tree.List(bg, execproto.ListParams{Path: "a", Depth: 2})
		if got := entryNames(r.Entries); err != nil || got != "a/b,a/b/c.txt" || r.Resolved.RelPath != "a" {
			t.Errorf("subdirectory: %s, %v", got, err)
		}
		if r, err := f.tree.List(bg, execproto.ListParams{Path: "a", Depth: 1, MaxEntries: 1}); err != nil || r.Truncated {
			t.Errorf("exactly max_entries entries is not truncated: %+v, %v", r, err)
		}
		r, err = f.tree.List(bg, execproto.ListParams{Path: "ln"})
		if got := entryNames(r.Entries); err != nil || got != "sub/f.txt" || r.Resolved.RelPath != "sub" {
			t.Errorf("listing through a link lists its target: %s, %v", got, err)
		}

		// Listing a denied directory would show what is inside it.
		_, err = f.tree.List(bg, execproto.ListParams{Path: ".aws"})
		wantCode(t, "list a denied directory", err, execproto.ErrDenyList)
		_, err = f.tree.List(bg, execproto.ListParams{Path: "sub/f.txt"})
		wantCode(t, "list a file", err, execproto.ErrNotFound)
		_, err = f.tree.List(bg, execproto.ListParams{Path: ".."})
		wantCode(t, "escape", err, execproto.ErrPathEscape)
		_, err = f.tree.List(bg, execproto.ListParams{Root: execproto.RootScratch})
		wantCode(t, "scratch", err, execproto.ErrOutsideRoots)
	})
}

func matchList(ms []execproto.SearchMatch) string {
	var out []string
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s:%d:%d", m.RelPath, m.Line, m.Column))
	}
	return strings.Join(out, ",")
}

func TestFSSearch(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		f.file("work/src/a.go", "package a\nfunc Hello() {}\n// hello\n")
		f.file("work/src/b.ts", "const hello = 1\n")
		f.file("work/bin.dat", "hello\x00")
		f.file("work/.git/config", "hello")
		f.file("work/.aws/config", "hello")
		f.file("work/.aws/credentials", "hello") // a denied directory counts once: it is not entered
		f.link(t, "../outside/secret", "work/out.txt")
		f.link(t, "src/b.ts", "work/dup.ts")

		r, err := f.tree.Search(bg, execproto.SearchParams{Query: "hello"})
		if got := matchList(r.Matches); err != nil || got != "src/a.go:3:4,src/b.ts:1:7" || r.Truncated {
			t.Errorf("literal: %s, %v", got, err)
		}
		// Not counted as scanned: denied (.aws, .env), binary, links, .git.
		if r.Skipped.Denied != 2 || r.Skipped.Binary != 1 || r.FilesScanned != 4 {
			t.Errorf("counts: scanned %d skipped %+v", r.FilesScanned, r.Skipped)
		}
		if len(r.Matches) == 0 || r.Matches[0].Text != "// hello" {
			t.Errorf("text %q", r.Matches[0].Text)
		}

		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: "hello", CaseSensitive: boolp(false)})
		if got := matchList(r.Matches); got != "src/a.go:2:6,src/a.go:3:4,src/b.ts:1:7" {
			t.Errorf("case-insensitive: %s", got)
		}
		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: `func \w+\(`, Regex: true})
		if got := matchList(r.Matches); got != "src/a.go:2:1" {
			t.Errorf("regex: %s", got)
		}
		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: "hello", Include: []string{"*.go"}})
		if got := matchList(r.Matches); got != "src/a.go:3:4" {
			t.Errorf("include: %s", got)
		}
		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: "hello", Exclude: []string{"src/**"}})
		if len(r.Matches) != 0 {
			t.Errorf("exclude: %s", matchList(r.Matches))
		}
		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: "Hello", ContextLines: 1})
		if len(r.Matches) != 1 || strings.Join(r.Matches[0].Before, "|") != "package a" || strings.Join(r.Matches[0].After, "|") != "// hello" {
			t.Errorf("context: %+v", r.Matches)
		}
		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: "hello", MaxMatches: 1})
		if len(r.Matches) != 1 || !r.Truncated {
			t.Errorf("max_matches 1: %d matches truncated %v", len(r.Matches), r.Truncated)
		}
		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: "hello", MaxFileBytes: 10})
		if r.Skipped.TooLarge != 2 || matchList(r.Matches) != "" {
			t.Errorf("max_file_bytes: %+v %s", r.Skipped, matchList(r.Matches))
		}
		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: "hello", Path: "src/a.go"})
		if got := matchList(r.Matches); got != "src/a.go:3:4" {
			t.Errorf("single file: %s", got)
		}
		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: "outside"})
		if len(r.Matches) != 0 {
			t.Errorf("search followed a symlink out of the root: %s", matchList(r.Matches))
		}
		r, _ = f.tree.Search(bg, execproto.SearchParams{Query: "TOKEN"})
		if len(r.Matches) != 0 {
			t.Errorf("search returned deny-listed content: %s", matchList(r.Matches))
		}
		_, err = f.tree.Search(bg, execproto.SearchParams{Query: "x", Path: ".aws"})
		wantCode(t, "search a denied directory", err, execproto.ErrDenyList)
		_, err = f.tree.Search(bg, execproto.SearchParams{Query: "x", Path: "../outside"})
		wantCode(t, "escape", err, execproto.ErrPathEscape)
		_, err = f.tree.Search(bg, execproto.SearchParams{Query: "x", Root: execproto.RootScratch})
		wantCode(t, "scratch", err, execproto.ErrOutsideRoots)
	})
}

// TestFSSearch_LongLinesCut: text and context are cut to 512 bytes
// (fs.json maxLength), so one minified file cannot fill the frame.
func TestFSSearch_LongLinesCut(t *testing.T) {
	f := newFixture(t, modeFallback)
	long := strings.Repeat("x", 4000)
	f.file("work/min.js", long+"\nneedle"+long+"\n"+long+"\n")
	r, err := f.tree.Search(bg, execproto.SearchParams{Query: "needle", ContextLines: 1})
	if err != nil || len(r.Matches) != 1 {
		t.Fatalf("%+v, %v", r, err)
	}
	m := r.Matches[0]
	if len(m.Before) != 1 || len(m.After) != 1 {
		t.Fatalf("context: %+v", m)
	}
	if len(m.Text) != execproto.MaxSearchTextBytes || len(m.Before[0]) != execproto.MaxSearchTextBytes || len(m.After[0]) != execproto.MaxSearchTextBytes {
		t.Errorf("lengths %d %d %d", len(m.Text), len(m.Before[0]), len(m.After[0]))
	}
}

// TestFSSearch_Cancelled: search honours its context (the server cancels
// every handler when the connection ends).
func TestFSSearch_Cancelled(t *testing.T) {
	f := newFixture(t, modeFallback)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err := f.tree.Search(ctx, execproto.SearchParams{Query: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled search: %v", err)
	}
}

const fullPatch = `diff --git a/sub/f.txt b/sub/f.txt
--- a/sub/f.txt
+++ b/sub/f.txt
@@ -1 +1 @@
-inside
\ No newline at end of file
+patched
diff --git a/new/dir/n.txt b/new/dir/n.txt
new file mode 100755
--- /dev/null
+++ b/new/dir/n.txt
@@ -0,0 +1 @@
+fresh
diff --git a/a/b/c.txt b/a/b/c.txt
deleted file mode 100644
--- a/a/b/c.txt
+++ /dev/null
@@ -1 +0,0 @@
-deep
\ No newline at end of file
diff --git a/scratch.txt b/moved/s.txt
similarity index 100%
rename from scratch.txt
rename to moved/s.txt
`

func TestFSPatch(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		f.file("work/scratch.txt", "keep me")
		before := f.snapshot(t)

		dry, err := f.tree.Patch(bg, execproto.PatchParams{Patch: fullPatch, DryRun: true})
		if err != nil || dry.Applied || len(dry.Files) != 4 {
			t.Fatalf("dry run: %+v, %v", dry, err)
		}
		sameSnapshot(t, "dry run", before, f.snapshot(t))

		r, err := f.tree.Patch(bg, execproto.PatchParams{Patch: fullPatch})
		if err != nil {
			t.Fatal(err)
		}
		want := []execproto.PatchFile{
			{RelPath: "sub/f.txt", Op: execproto.PatchModify, Hunks: 1, Sha256After: sha("patched\n")},
			{RelPath: "new/dir/n.txt", Op: execproto.PatchCreate, Hunks: 1, Sha256After: sha("fresh\n")},
			{RelPath: "a/b/c.txt", Op: execproto.PatchDelete, Hunks: 1},
			{RelPath: "moved/s.txt", FromRelPath: "scratch.txt", Op: execproto.PatchRename, Sha256After: sha("keep me")},
		}
		if !r.Applied || fmt.Sprint(r.Files) != fmt.Sprint(want) || fmt.Sprint(dry.Files) != fmt.Sprint(want) {
			t.Errorf("result:\n got %+v\nwant %+v", r, want)
		}
		if f.content(t, "work/sub/f.txt") != "patched\n" || f.content(t, "work/new/dir/n.txt") != "fresh\n" || f.content(t, "work/moved/s.txt") != "keep me" {
			t.Error("contents after the patch")
		}
		f.missing(t, "work/a/b/c.txt")
		f.missing(t, "work/scratch.txt")
		if st, _ := os.Stat(f.work + "/new/dir/n.txt"); st.Mode().Perm() != 0o755 {
			t.Errorf("created mode %o", st.Mode().Perm())
		}
		if st, _ := os.Stat(f.work + "/sub/f.txt"); st.Mode().Perm() != 0o600 {
			t.Errorf("modified file mode %o, want the original 600", st.Mode().Perm())
		}
		f.noTemps(t)

		// A mode change alone.
		if _, err := f.tree.Patch(bg, execproto.PatchParams{Patch: "diff --git a/sub/f.txt b/sub/f.txt\nold mode 100600\nnew mode 100755\n"}); codeOf(err) != execproto.ErrInvalidArgument {
			t.Errorf("mode 100600 is not a git mode: %v", err)
		}
		if _, err := f.tree.Patch(bg, execproto.PatchParams{Patch: "diff --git a/sub/f.txt b/sub/f.txt\nold mode 100644\nnew mode 100755\n"}); err != nil {
			t.Fatal(err)
		}
		if st, _ := os.Stat(f.work + "/sub/f.txt"); st.Mode().Perm() != 0o755 || f.content(t, "work/sub/f.txt") != "patched\n" {
			t.Errorf("mode change: %o", st.Mode().Perm())
		}
	})
}

// TestFSPatch_AllOrNothing: A06 §4.2 "all or nothing" and §4.4
// patch_failed "nothing written". The second file's hunk does not match, so
// the first file (already computed, its new directory already made and its
// result staged) must not change, and the staged files and directory must
// be gone.
func TestFSPatch_AllOrNothing(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		before := f.snapshot(t)
		bad := "--- /dev/null\n+++ b/brand/new/x.txt\n@@ -0,0 +1 @@\n+x\n" +
			"--- a/sub/f.txt\n+++ b/sub/f.txt\n@@ -1 +1 @@\n-something else\n+y\n"
		_, err := f.tree.Patch(bg, execproto.PatchParams{Patch: bad})
		wantCode(t, "second hunk fails", err, execproto.ErrPatchFailed)
		var e *execproto.Error
		if errors.As(err, &e) && e.Data.RelPath != "sub/f.txt" {
			t.Errorf("patch_failed names %q, want the failing file", e.Data.RelPath)
		}
		sameSnapshot(t, "failed patch", before, f.snapshot(t))

		for _, c := range []struct {
			name, patch string
			code        execproto.Code
		}{
			{"create over an existing file", "--- /dev/null\n+++ b/sub/f.txt\n@@ -0,0 +1 @@\n+x\n", execproto.ErrPatchFailed},
			{"modify a missing file", "--- a/nope\n+++ b/nope\n@@ -1 +1 @@\n-a\n+b\n", execproto.ErrPatchFailed},
			{"rename onto an existing file", "diff --git a/sub/f.txt b/a/b/c.txt\nrename from sub/f.txt\nrename to a/b/c.txt\n", execproto.ErrPatchFailed},
			{"delete with content left", "diff --git a/sub/f.txt b/sub/f.txt\ndeleted file mode 100644\n", execproto.ErrPatchFailed},
			{"the same file through two spellings", "--- a/sub/f.txt\n+++ b/sub/f.txt\n@@ -1 +1 @@\n-inside\n\\ No newline at end of file\n+y\n--- a/sub/../sub/f.txt\n+++ b/sub/../sub/f.txt\n@@ -1 +1 @@\n-inside\n\\ No newline at end of file\n+z\n", execproto.ErrInvalidArgument},
			{"the root", "--- /dev/null\n+++ b/.\n@@ -0,0 +1 @@\n+x\n", execproto.ErrInvalidArgument},
			{"escape", "--- /dev/null\n+++ b/../outside/x\n@@ -0,0 +1 @@\n+x\n", execproto.ErrPathEscape},
			{"deny-listed create", "--- /dev/null\n+++ b/.env.local\n@@ -0,0 +1 @@\n+x\n", execproto.ErrDenyList},
			{"deny-listed delete", "diff --git a/.env b/.env\ndeleted file mode 100644\n--- a/.env\n+++ /dev/null\n@@ -1 +0,0 @@\n-TOKEN=x\n\\ No newline at end of file\n", execproto.ErrDenyList},
			{"rename onto a deny-listed name", "diff --git a/sub/f.txt b/.npmrc\nrename from sub/f.txt\nrename to .npmrc\n", execproto.ErrDenyList},
			{"rename from a deny-listed name", "diff --git a/.env b/env.txt\nrename from .env\nrename to env.txt\n", execproto.ErrDenyList},
			{".git", "--- /dev/null\n+++ b/.git/hooks/post-checkout\n@@ -0,0 +1 @@\n+x\n", execproto.ErrOutsideRoots},
			{"result over the write cap", "--- /dev/null\n+++ b/big\n@@ -0,0 +1 @@\n+" + strings.Repeat("x", execproto.MaxWriteBytes) + "\n", execproto.ErrTooLarge},
		} {
			_, err := f.tree.Patch(bg, execproto.PatchParams{Patch: c.patch})
			wantCode(t, c.name, err, c.code)
			sameSnapshot(t, c.name, before, f.snapshot(t))
		}
		f.noTemps(t)
	})
}

// TestFSPatch_RollbackOnCommitFailure: the commit renames one file at a
// time, so a failure at step k (here injected before each step in turn) has
// already changed k files. Each of them must be restored, created files and
// directories removed and deleted files brought back, so that the patch
// still had no effect at all.
func TestFSPatch_RollbackOnCommitFailure(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		f.file("work/scratch.txt", "keep me")
		before := f.snapshot(t)
		for k := 0; ; k++ {
			calls := 0
			f.tree.beforeCommit = func(step int) error {
				calls++
				if step == k {
					return errors.New("injected")
				}
				return nil
			}
			_, err := f.tree.Patch(bg, execproto.PatchParams{Patch: fullPatch})
			if err == nil {
				if k < 4 {
					t.Fatalf("step %d: patch applied although the step failed", k)
				}
				break
			}
			wantCode(t, fmt.Sprintf("failure at step %d", k), err, execproto.ErrIOError)
			sameSnapshot(t, fmt.Sprintf("rollback after step %d", k), before, f.snapshot(t))
			f.noTemps(t)
		}
	})
}

// TestFSPatch_SymlinkRefused: no patch writes through a symlink. A patch
// names files, and a file reached through a link is a different file from
// the one the text shows, so any path whose resolution follows a link (a
// linked directory on the way, or the file itself) is refused, even when
// the link stays inside the root; a link that leaves the root is
// path_escape already.
func TestFSPatch_SymlinkRefused(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		f.link(t, "sub", "work/lnk")
		f.link(t, "sub/f.txt", "work/l.txt")
		f.link(t, "../outside", "work/out")
		before := f.snapshot(t)
		for _, c := range []struct {
			patch string
			code  execproto.Code
		}{
			{"--- a/lnk/f.txt\n+++ b/lnk/f.txt\n@@ -1 +1 @@\n-inside\n\\ No newline at end of file\n+x\n", execproto.ErrPatchFailed},
			{"--- a/l.txt\n+++ b/l.txt\n@@ -1 +1 @@\n-inside\n\\ No newline at end of file\n+x\n", execproto.ErrPatchFailed},
			{"--- /dev/null\n+++ b/lnk/new.txt\n@@ -0,0 +1 @@\n+x\n", execproto.ErrPatchFailed},
			{"diff --git a/l.txt b/l.txt\ndeleted file mode 100644\n", execproto.ErrPatchFailed},
			{"diff --git a/sub/f.txt b/lnk/g.txt\nrename from sub/f.txt\nrename to lnk/g.txt\n", execproto.ErrPatchFailed},
			{"--- /dev/null\n+++ b/out/x\n@@ -0,0 +1 @@\n+x\n", execproto.ErrPathEscape},
		} {
			_, err := f.tree.Patch(bg, execproto.PatchParams{Patch: c.patch})
			wantCode(t, c.patch, err, c.code)
		}
		sameSnapshot(t, "symlink patches", before, f.snapshot(t))
	})
}

// TestFSPatch_SwapDuringCommit: a directory swapped for a symlink out of
// the root between staging and commit is refused at commit (the directory
// is reopened without following links) and rolled back.
func TestFSPatch_SwapDuringCommit(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		f.file("work/sub/g.txt", "g\n")
		f.tree.beforeCommit = func(step int) error {
			if step == 1 {
				must(t, os.Rename(f.work+"/sub", f.work+"/sub.moved"))
				must(t, os.Symlink(f.outside, f.work+"/sub"))
			}
			return nil
		}
		p := "--- a/a/b/c.txt\n+++ b/a/b/c.txt\n@@ -1 +1 @@\n-deep\n\\ No newline at end of file\n+DEEP\n" +
			"--- a/sub/g.txt\n+++ b/sub/g.txt\n@@ -1 +1 @@\n-g\n+G\n"
		_, err := f.tree.Patch(bg, execproto.PatchParams{Patch: p})
		wantCode(t, "swap before the second commit step", err, execproto.ErrConflict)
		if f.content(t, "work/a/b/c.txt") != "deep" {
			t.Error("the first file was not rolled back")
		}
		if f.content(t, "work/sub.moved/g.txt") != "g\n" {
			t.Error("the second file changed")
		}
		if es, _ := os.ReadDir(f.outside); len(es) != 1 {
			t.Errorf("outside holds %d entries", len(es))
		}
		f.noTemps(t)
	})
}

// TestFSPatch_FileCapBeforeApplying is D-048 end to end: a 4097-file patch
// whose first 4096 files would all apply is refused as too_large and
// creates nothing.
func TestFSPatch_FileCapBeforeApplying(t *testing.T) {
	f := newFixture(t, modeFallback)
	var b strings.Builder
	for i := 0; i <= execproto.MaxPatchFiles; i++ {
		fmt.Fprintf(&b, "--- /dev/null\n+++ b/many/f%d\n@@ -0,0 +1 @@\n+x\n", i)
	}
	_, err := f.tree.Patch(bg, execproto.PatchParams{Patch: b.String()})
	wantCode(t, "4097 files", err, execproto.ErrTooLarge)
	f.missing(t, "work/many")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestFSPatch_ChangedBeforeCommit: between planning and commit another
// process in the sandbox changes a file the patch read, or creates one it
// is about to create. The commit checks each original is still the file it
// read (device, inode, size, mtime) and creates without replacing, so the
// patch fails conflict and rolls back instead of overwriting that change
// (the daemon then re-reads and asks again, A06 §4.4).
func TestFSPatch_ChangedBeforeCommit(t *testing.T) {
	cases := []struct {
		name, patch, touch string
	}{
		{"modified original", "--- a/a/b/c.txt\n+++ b/a/b/c.txt\n@@ -1 +1 @@\n-deep\n\\ No newline at end of file\n+DEEP\n", "work/a/b/c.txt"},
		{"created target", "--- /dev/null\n+++ b/a/new.txt\n@@ -0,0 +1 @@\n+x\n", "work/a/new.txt"},
		{"rename target", "diff --git a/sub/f.txt b/a/g.txt\nrename from sub/f.txt\nrename to a/g.txt\n", "work/a/g.txt"},
		{"renamed original", "diff --git a/sub/f.txt b/a/h.txt\nrename from sub/f.txt\nrename to a/h.txt\n", "work/sub/f.txt"},
		{"deleted original", "diff --git a/sub/f.txt b/sub/f.txt\ndeleted file mode 100644\n--- a/sub/f.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-inside\n\\ No newline at end of file\n", "work/sub/f.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, modeFallback)
			// The first step of a two-file patch succeeds and must be
			// undone when the second, the case under test, fails.
			first := "--- a/.gitignore\n+++ b/.gitignore\n@@ -1 +1 @@\n-old\n+new\n"
			f.file("work/.gitignore", "old\n")
			f.tree.beforeCommit = func(step int) error {
				if step == 1 {
					f.file(c.touch, "changed by someone else")
				}
				return nil
			}
			_, err := f.tree.Patch(bg, execproto.PatchParams{Patch: first + c.patch})
			wantCode(t, c.name, err, execproto.ErrConflict)
			if f.content(t, c.touch) != "changed by someone else" || f.content(t, "work/.gitignore") != "old\n" {
				t.Error("the concurrent change was overwritten or the first step not undone")
			}
			f.noTemps(t)
		})
	}
}

// TestFSList_KindsAndUnreadable: a FIFO is listed as kind other without
// being opened, and a file search cannot open is counted unreadable.
func TestFSList_KindsAndUnreadable(t *testing.T) {
	f := newFixture(t, modeFallback)
	must(t, unix.Mkfifo(f.work+"/pipe", 0o600))
	r, err := f.tree.List(bg, execproto.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range r.Entries {
		if e.RelPath == "pipe" {
			found = e.Kind == execproto.KindOther
		}
	}
	if !found {
		t.Errorf("FIFO entry: %+v", r.Entries)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	f.file("work/locked.txt", "inside")
	must(t, os.Chmod(f.work+"/locked.txt", 0))
	s, err := f.tree.Search(bg, execproto.SearchParams{Query: "inside"})
	if err != nil || s.Skipped.Unreadable != 1 || len(s.Matches) != 1 {
		t.Errorf("unreadable: %+v, %v", s, err)
	}
}

// TestFSWrite_FailureRemovesNewDirectories: a write that fails after it
// created parent directories (here at the rename, injected) removes them
// again, and its temporary file: a failed write leaves the tree as it was.
func TestFSWrite_FailureRemovesNewDirectories(t *testing.T) {
	bothModes(t, func(t *testing.T, f *fixture) {
		before := f.snapshot(t)
		f.tree.beforeCommit = func(int) error { return errors.New("injected") }
		_, err := f.tree.Write(bg, execproto.WriteParams{Path: "made/deep/x.txt", Content: "x"})
		wantCode(t, "failed rename", err, execproto.ErrIOError)
		sameSnapshot(t, "failed write", before, f.snapshot(t))
	})
}
