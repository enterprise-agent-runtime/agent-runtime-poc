//go:build linux

package exec

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
)

// These tests are design A06 §5.1 step 4 (the symlink walk inside the root)
// and §5.2 (opening the canonical path so that a change in between is
// refused), on a real file system. Each case runs twice: once with openat2
// (Linux >= 5.6, RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS | ...) and once
// through the component walk that older kernels and macOS use
// (TestOpenCanonical_Fallback), because the two paths share no code below
// Tree.OpenCanonical and either one alone could be the hole.

// fixture is a sandbox stand-in under a temp dir: base/work (worktree),
// base/scratch, and base/outside, which is under no root.
type fixture struct {
	base, work, scratch, outside string
	tree                         *Tree
}

// openMode selects how OpenCanonical opens: openat2, or the component walk
// forced by an openat2 that reports ENOSYS, as a pre-5.6 kernel does.
type openMode int

const (
	modeOpenat2 openMode = iota
	modeFallback
)

func newFixture(t *testing.T, mode openMode) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{base: base, work: base + "/work", scratch: base + "/scratch", outside: base + "/outside"}
	for _, d := range []string{f.work, f.scratch, f.outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.file("outside/secret", "outside the roots")
	f.file("scratch/vitest.json", "{}")
	f.file("work/sub/f.txt", "inside")
	f.file("work/a/b/c.txt", "deep")
	f.file("work/.env", "TOKEN=x")
	tree, err := OpenTree([]Root{
		{Name: execproto.RootWorktree, Path: f.work, Mode: execproto.RootRW},
		{Name: execproto.RootScratch, Path: f.scratch, Mode: execproto.RootRW},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tree.Close() })
	switch mode {
	case modeOpenat2:
		if !tree.Openat2() {
			t.Skip("openat2 unavailable on this kernel; the component walk is covered by TestOpenCanonical_Fallback")
		}
	case modeFallback:
		tree.openat2 = func(int, string, int) (int, error) {
			return -1, unix.ENOSYS
		}
	}
	f.tree = tree
	return f
}

func (f *fixture) file(rel, content string) {
	p := filepath.Join(f.base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		panic(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		panic(err)
	}
}

func (f *fixture) link(t *testing.T, target, rel string) {
	t.Helper()
	p := filepath.Join(f.base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// read resolves and opens input for method and returns the content.
func (f *fixture) read(method, input string) (execproto.Resolved, string, error) {
	r, err := f.tree.Resolve(method, "", input)
	if err != nil {
		return r, "", err
	}
	fh, err := f.tree.OpenCanonical(r, unix.O_RDONLY)
	if err != nil {
		return r, "", err
	}
	defer fh.Close()
	b, err := io.ReadAll(fh)
	return r, string(b), err
}

func wantCode(t *testing.T, what string, err error, code execproto.Code) {
	t.Helper()
	if !errors.Is(err, code) {
		t.Fatalf("%s: %v, want %s", what, err, code.Name())
	}
}

// symlinkOutsideRefused: a symlink in the worktree that leads out of every
// root is a path_escape, whether absolute, relative, or reached through a
// chain, and a forged Resolved that names such a link is refused at open.
// Symlinks that stay inside are followed, and the deny-list and the .git ban
// apply to where a link leads, not to its name.
func symlinkOutsideRefused(t *testing.T, mode openMode) {
	f := newFixture(t, mode)
	f.link(t, f.outside+"/secret", "work/abs")
	f.link(t, "../outside/secret", "work/rel")
	f.link(t, "../../..", "work/sub/up")
	f.link(t, "abs", "work/chain")
	f.link(t, f.outside, "work/outdir")
	f.link(t, f.work+"/../outside/secret", "work/absdotdot")
	f.link(t, "sub/f.txt", "work/in")
	f.link(t, f.work+"/sub/f.txt", "work/absin")
	f.link(t, "c2", "work/c1")
	f.link(t, "sub", "work/c2")
	f.link(t, ".env", "work/notes.txt")
	f.link(t, ".git", "work/g")
	f.link(t, f.scratch+"/vitest.json", "work/toscratch")
	f.link(t, "missing", "work/dangling")

	for _, in := range []string{"abs", "rel", "sub/up/outside/secret", "chain", "outdir/secret", "absdotdot"} {
		_, _, err := f.read(execproto.MethodFSRead, in)
		wantCode(t, "read "+in, err, execproto.ErrPathEscape)
	}
	// A symlink into another root is followed only where the method may
	// address that root (A06 §5.1 step 4): read may name scratch, write not.
	r, got, err := f.read(execproto.MethodFSRead, "toscratch")
	if err != nil || r.Root != execproto.RootScratch || r.RelPath != "vitest.json" || got != "{}" {
		t.Fatalf("read toscratch = %+v %q %v; want scratch/vitest.json", r, got, err)
	}
	_, err = f.tree.Resolve(execproto.MethodFSWrite, "", "toscratch")
	wantCode(t, "write toscratch", err, execproto.ErrPathEscape)

	for in, want := range map[string]string{"in": "sub/f.txt", "absin": "sub/f.txt", "c1/f.txt": "sub/f.txt", "sub/up/../f.txt": "sub/f.txt"} {
		r, got, err := f.read(execproto.MethodFSRead, in)
		if err != nil || r.RelPath != want || r.Canonical != f.work+"/"+want || got != "inside" {
			t.Errorf("read %s = %+v %q %v; want rel %s", in, r, got, err, want)
		}
		if r.RequestedRelPath != filepath.Clean(in) {
			t.Errorf("read %s: requested_rel_path %q", in, r.RequestedRelPath)
		}
	}

	_, _, err = f.read(execproto.MethodFSRead, "notes.txt")
	wantCode(t, "read a link to .env", err, execproto.ErrDenyList)
	r, err = f.tree.Resolve(execproto.MethodFSStat, "", "notes.txt")
	if err != nil || r.RelPath != ".env" || r.DenyMatch == nil {
		t.Errorf("stat a link to .env = %+v %v; want rel .env with deny_match", r, err)
	}
	_, err = f.tree.Resolve(execproto.MethodFSWrite, "", "g/config")
	wantCode(t, "write through a link to .git", err, execproto.ErrOutsideRoots)

	// A dangling link resolves to its target, lexically: valid for a write
	// (it creates the target, never writes through the link), not_found on
	// open for a read.
	r, err = f.tree.Resolve(execproto.MethodFSWrite, "", "dangling")
	if err != nil || r.RelPath != "missing" {
		t.Errorf("write dangling = %+v %v; want rel missing", r, err)
	}
	_, _, err = f.read(execproto.MethodFSRead, "dangling")
	wantCode(t, "read dangling", err, execproto.ErrNotFound)

	// OpenCanonical trusts nothing in a Resolved: one naming a symlink, a
	// ".." or an absolute path is refused, whatever produced it.
	for _, rel := range []string{"abs", "rel", "outdir/secret", "in", "../outside/secret", "sub/../../outside/secret", "/etc/passwd"} {
		fh, err := f.tree.OpenCanonical(execproto.Resolved{Root: execproto.RootWorktree, RelPath: rel}, unix.O_RDONLY)
		if err == nil {
			fh.Close()
		}
		wantCode(t, "OpenCanonical of forged "+rel, err, execproto.ErrConflict)
	}
}

// symlinkLoop: the walk follows at most 40 symlinks (A06 §5.1 step 4, the
// Linux MAXSYMLINKS value) and fails path_escape past that, so a loop
// terminates and a chain cannot make one call walk forever.
func symlinkLoop(t *testing.T, mode openMode) {
	f := newFixture(t, mode)
	f.link(t, "loop-b", "work/loop-a")
	f.link(t, "loop-a", "work/loop-b")
	f.link(t, ".", "work/self")
	_, err := f.tree.Resolve(execproto.MethodFSRead, "", "loop-a")
	wantCode(t, "two-link loop", err, execproto.ErrPathEscape)
	_, err = f.tree.Resolve(execproto.MethodFSRead, "", strings.Repeat("self/", 41)+"sub/f.txt")
	wantCode(t, "41 links in one path", err, execproto.ErrPathEscape)

	// A chain of exactly 40 links resolves; one more does not.
	for i := 1; i <= 41; i++ {
		f.link(t, "l"+strconv.Itoa(i-1), "work/l"+strconv.Itoa(i))
	}
	f.link(t, "sub/f.txt", "work/l0")
	r, got, err := f.read(execproto.MethodFSRead, "l39")
	if err != nil || r.RelPath != "sub/f.txt" || got != "inside" {
		t.Fatalf("chain of 40 links = %+v %q %v; want sub/f.txt", r, got, err)
	}
	_, err = f.tree.Resolve(execproto.MethodFSRead, "", "l40")
	wantCode(t, "chain of 41 links", err, execproto.ErrPathEscape)
	_, _, err = f.read(execproto.MethodFSRead, strings.Repeat("self/", 40)+"sub/f.txt")
	if err != nil {
		t.Fatalf("40 links in one path: %v", err)
	}
}

// symlinkSwapIsConflict: a directory component replaced by a symlink after
// the walk produced the canonical path and before the open (the TOCTOU
// window of A06 §5.2) makes the open fail conflict, so a process racing the
// executor cannot redirect an allowed path. The hook swaps at the last
// moment: just before the openat2 call, or in the component walk after "a"
// is open and before "b" is.
func symlinkSwapIsConflict(t *testing.T, mode openMode) {
	for name, target := range map[string]func(f *fixture) string{
		"to outside": func(f *fixture) string { return f.outside },
		"inside":     func(f *fixture) string { return "b.real" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, mode)
			f.file("outside/c.txt", "outside the roots")
			r, err := f.tree.Resolve(execproto.MethodFSRead, "", "a/b/c.txt")
			if err != nil || r.RelPath != "a/b/c.txt" {
				t.Fatalf("resolve before the swap = %+v %v", r, err)
			}
			swapped := false
			f.tree.beforeOpen = func(step int) {
				// openat2 opens everything in one call (step 0); the
				// walk is hit between its first and second component.
				if swapped || mode == modeFallback && step != 1 {
					return
				}
				swapped = true
				if err := os.Rename(f.work+"/a/b", f.work+"/a/b.real"); err != nil {
					t.Error(err)
				}
				if err := os.Symlink(target(f), f.work+"/a/b"); err != nil {
					t.Error(err)
				}
			}
			fh, err := f.tree.OpenCanonical(r, unix.O_RDONLY)
			if err == nil {
				fh.Close()
			}
			if !swapped {
				t.Fatal("the hook never ran")
			}
			wantCode(t, "open after the swap", err, execproto.ErrConflict)
		})
	}
	t.Run("final component", func(t *testing.T) {
		f := newFixture(t, mode)
		r, err := f.tree.Resolve(execproto.MethodFSRead, "", "sub/f.txt")
		if err != nil {
			t.Fatal(err)
		}
		f.tree.beforeOpen = func(int) {
			f.tree.beforeOpen = nil
			if err := os.Remove(f.work + "/sub/f.txt"); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(f.outside+"/secret", f.work+"/sub/f.txt"); err != nil {
				t.Error(err)
			}
		}
		fh, err := f.tree.OpenCanonical(r, unix.O_RDONLY)
		if err == nil {
			fh.Close()
		}
		wantCode(t, "open after swapping the file for a link", err, execproto.ErrConflict)
	})
	t.Run("no swap opens", func(t *testing.T) {
		f := newFixture(t, mode)
		_, got, err := f.read(execproto.MethodFSRead, "a/b/c.txt")
		if err != nil || got != "deep" {
			t.Fatalf("read a/b/c.txt = %q %v", got, err)
		}
		r, err := f.tree.Resolve(execproto.MethodFSList, "", ".")
		if err != nil {
			t.Fatal(err)
		}
		fh, err := f.tree.OpenCanonical(r, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			t.Fatalf("open the root itself: %v", err)
		}
		fh.Close()
		_, _, err = f.read(execproto.MethodFSRead, "sub/nope.txt")
		wantCode(t, "read a missing file", err, execproto.ErrNotFound)
	})
}

func TestOpenCanonical_SymlinkOutsideRefused(t *testing.T) {
	symlinkOutsideRefused(t, modeOpenat2)
}

func TestOpenCanonical_SymlinkLoop(t *testing.T) {
	symlinkLoop(t, modeOpenat2)
}

func TestOpenCanonical_SymlinkSwapIsConflict(t *testing.T) {
	symlinkSwapIsConflict(t, modeOpenat2)
}

// TestOpenCanonical_Fallback runs the three cases above through the
// component walk, by injecting an openat2 that fails ENOSYS as kernels
// before 5.6 do. It also checks that the walk really ran (the injected
// openat2 was asked first and the walk's per-component hook fired).
func TestOpenCanonical_Fallback(t *testing.T) {
	t.Run("SymlinkOutsideRefused", func(t *testing.T) { symlinkOutsideRefused(t, modeFallback) })
	t.Run("SymlinkLoop", func(t *testing.T) { symlinkLoop(t, modeFallback) })
	t.Run("SymlinkSwapIsConflict", func(t *testing.T) { symlinkSwapIsConflict(t, modeFallback) })
	t.Run("walk is taken", func(t *testing.T) {
		f := newFixture(t, modeFallback)
		asked := 0
		f.tree.openat2 = func(int, string, int) (int, error) {
			asked++
			return -1, unix.ENOSYS
		}
		var steps []int
		f.tree.beforeOpen = func(step int) { steps = append(steps, step) }
		r, err := f.tree.Resolve(execproto.MethodFSRead, "", "a/b/c.txt")
		if err != nil {
			t.Fatal(err)
		}
		fh, err := f.tree.OpenCanonical(r, unix.O_RDONLY)
		if err != nil {
			t.Fatal(err)
		}
		fh.Close()
		if asked != 1 || len(steps) != 4 || steps[3] != 2 {
			t.Fatalf("openat2 asked %d times, walk steps %v; want 1 and [0 0 1 2]", asked, steps)
		}
	})
}
