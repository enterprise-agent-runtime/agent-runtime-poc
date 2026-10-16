//go:build linux

package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
)

// Regression tests for the findings of the first M2-7 review. Each names
// the finding it guards; the reviewer's probes are the red tests.

// probePatch is finding 1 (CWE-223/754): 4096 creates whose names are
// mostly "<", which JSON escapes as six bytes. The request fits the 8 MiB
// frame, but the PatchResult listing every file does not.
func probePatch() execproto.PatchParams {
	var b strings.Builder
	for i := 0; i < execproto.MaxPatchFiles; i++ {
		fmt.Fprintf(&b, "--- /dev/null\n+++ %s/p%04d%s\n@@ -0,0 +1 @@\n+x\n", strings.Repeat("<", 200), i, strings.Repeat("<", 127))
	}
	zero := 0
	return execproto.PatchParams{Patch: b.String(), Strip: &zero}
}

// TestFSPatch_ResultMustFitTheFrame: the result is sized before anything
// is staged. Before the fix the files were committed and the response then
// replaced by too_large at the frame: the daemon recorded a failure for
// 4096 writes that had happened. Now the patch is too_large and nothing is
// written; a dry run gives the same answer.
func TestFSPatch_ResultMustFitTheFrame(t *testing.T) {
	f := newFixture(t, modeOpenat2)
	before := f.snapshot(t)
	p := probePatch()
	// Refused from the parse alone: nothing is even opened, so nothing can
	// have been staged (a crash cannot leave a staging directory behind).
	opened := false
	f.tree.beforeOpen = func(int) { opened = true }
	for _, dry := range []bool{true, false} {
		p.DryRun = dry
		_, err := f.tree.Patch(bg, p)
		wantCode(t, fmt.Sprintf("dry run %v", dry), err, execproto.ErrTooLarge)
		sameSnapshot(t, "oversized result", before, f.snapshot(t))
	}
	if opened {
		t.Error("an oversized result was found only after files were opened")
	}
	f.noTemps(t)
}

// TestFSSearch_RegexCostBudget is finding 2 (CWE-400/1333). A file is
// searched only when its size times the query's program size fits
// searchMaxFileCost, so no single match call can run for long; a file over
// the budget is skipped as too_large and reported, never half-searched. A
// case-sensitive literal costs one per byte and searches a 2 MiB file.
func TestFSSearch_RegexCostBudget(t *testing.T) {
	f := newFixture(t, modeFallback)
	f.file("work/big/one-line.txt", strings.Repeat("a", 2<<20-2)+"q\n")
	r, err := returns(t, "costly regex", func() (execproto.SearchResult, error) {
		return f.tree.Search(bg, execproto.SearchParams{Query: `[a-z]{500}q`, Regex: true, Path: "big"})
	})
	if err != nil || r.Skipped.TooLarge != 1 || r.FilesScanned != 0 || len(r.Matches) != 0 {
		t.Errorf("over the cost budget: %+v, %v", r, err)
	}
	r, err = f.tree.Search(bg, execproto.SearchParams{Query: strings.Repeat("a", 1000) + "q", Path: "big"})
	if err != nil || r.FilesScanned != 1 || len(r.Matches) != 1 || r.Matches[0].Column != 2<<20-2-1000+1 {
		t.Errorf("literal: %+v %+v, %v", r.Skipped, r.Matches, err)
	}
}

// fakeClock advances by step each time the search reads it.
type fakeClock struct {
	now  time.Time
	step time.Duration
}

func (c *fakeClock) read() time.Time { c.now = c.now.Add(c.step); return c.now }

// TestFSSearch_Deadline is finding 2: A06 §4.4 lists timeout for search.
// The method deadline (searchTimeout) is checked per file and inside each
// file; past it the search fails timeout. The clock is injected, so
// nothing here depends on how fast the machine is.
func TestFSSearch_Deadline(t *testing.T) {
	f := newFixture(t, modeFallback)
	f.file("work/lines.txt", strings.Repeat("x\n", 100_000))
	f.tree.now = (&fakeClock{now: time.Unix(0, 0), step: searchTimeout / 4}).read
	_, err := f.tree.Search(bg, execproto.SearchParams{Query: "y", Path: "lines.txt"})
	wantCode(t, "past the deadline", err, execproto.ErrTimeout)

	f.tree.now = (&fakeClock{now: time.Unix(0, 0)}).read // frozen
	r, err := f.tree.Search(bg, execproto.SearchParams{Query: "x", Path: "lines.txt", MaxMatches: 1})
	if err != nil || len(r.Matches) != 1 {
		t.Errorf("before the deadline: %+v, %v", r.Matches, err)
	}
}

// countdownCtx reports Canceled from its (left+1)-th Err call on.
type countdownCtx struct {
	context.Context
	left int
}

func (c *countdownCtx) Err() error {
	if c.left--; c.left < 0 {
		return context.Canceled
	}
	return nil
}

// TestFSSearch_CancelInsideAFile is finding 2: cancellation is seen inside
// a file, not only between directories (a search cancelled at 200 ms
// returned after 45 s, and Serve waits for its handlers, so shutdown hung
// too). The context allows three checks (the search, the directory, the
// file) and is cancelled from the fourth, which only a check inside the
// 200 000-line file can reach.
func TestFSSearch_CancelInsideAFile(t *testing.T) {
	f := newFixture(t, modeFallback)
	f.file("work/d/lines.txt", strings.Repeat("y\n", 200_000))
	_, err := f.tree.Search(&countdownCtx{Context: bg, left: 3}, execproto.SearchParams{Query: "x", Path: "d"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled inside a file: %v", err)
	}
	// Files that are skipped (here binary) never reach the check inside a
	// file, so each file checks before anything else: two checks allowed
	// (the search, the directory), the first file's is cancelled.
	for i := 0; i < 3; i++ {
		f.file(fmt.Sprintf("work/bin/f%d", i), "\x00")
	}
	_, err = f.tree.Search(&countdownCtx{Context: bg, left: 2}, execproto.SearchParams{Query: "x", Path: "bin"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled between skipped files: %v", err)
	}
	// And a search cancelled before it starts returns at once, whatever
	// the files would cost.
	for i := 0; i < 3; i++ {
		f.file(fmt.Sprintf("work/big/f%d.txt", i), strings.Repeat("a", 2<<20-1)+"\n")
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err = returns(t, "cancelled search", func() (execproto.SearchResult, error) {
		return f.tree.Search(ctx, execproto.SearchParams{Query: `[a-z]{500}q`, Regex: true, Path: "big"})
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled search: %v", err)
	}
}

// TestFSSearch_FileStaysInTheWorktree is finding 4. The walk saw a regular
// file; before it is opened, a process in the sandbox replaces it with an
// absolute symlink into /scratch. Resolving it with the read row (which
// may address scratch) followed the link and returned scratch content as a
// worktree match; search resolves with its own row (worktree only), so the
// link is an escape and the file unreadable.
func TestFSSearch_FileStaysInTheWorktree(t *testing.T) {
	f := newFixture(t, modeOpenat2)
	f.file("scratch/vitest.json", "SCRATCHDATA\n")
	must(t, os.Symlink(f.scratch+"/vitest.json", f.work+"/vitest.json"))
	idx, _ := f.tree.root(execproto.RootWorktree)
	spec, err := searchArgs(execproto.SearchParams{Query: "SCRATCH"})
	must(t, err)
	s := &searcher{t: f.tree, ctx: bg, idx: idx, root: execproto.RootWorktree, spec: spec, now: time.Now, deadline: time.Now().Add(time.Hour)}
	st := unix.Stat_t{Mode: unix.S_IFREG, Nlink: 1, Size: 12} // what the walk saw a moment earlier
	if _, err := s.file("vitest.json", &st); err != nil {
		t.Fatal(err)
	}
	if len(s.out.Matches) != 0 || s.out.Skipped.Unreadable != 1 {
		t.Errorf("search returned scratch content: %+v %+v", s.out.Matches, s.out.Skipped)
	}
}

// TestFSPatch_UndoFailureKeepsStaging is finding 5. When an undo step
// itself fails, the tree is half applied and the staging directory holds
// the only copy of the originals: it must be kept, and the io_error names
// it (by its fixed temporary name, in rel_path). The executor's start-up
// sweep (Tree.SweepTemps) removes it later.
func TestFSPatch_UndoFailureKeepsStaging(t *testing.T) {
	f := newFixture(t, modeFallback)
	f.file("work/sub/g.txt", "g\n")
	f.tree.beforeCommit = func(step int) error {
		if step == 1 {
			// The first step's directory disappears: its undo cannot
			// reach it.
			must(t, os.Rename(f.work+"/a/b", f.work+"/a/gone"))
			return errors.New("injected")
		}
		return nil
	}
	p := "--- a/a/b/c.txt\n+++ b/a/b/c.txt\n@@ -1 +1 @@\n-deep\n\\ No newline at end of file\n+DEEP\n" +
		"--- a/sub/g.txt\n+++ b/sub/g.txt\n@@ -1 +1 @@\n-g\n+G\n"
	_, err := f.tree.Patch(bg, execproto.PatchParams{Patch: p})
	wantCode(t, "undo failed", err, execproto.ErrIOError)
	var e *execproto.Error
	if !errors.As(err, &e) || !strings.HasPrefix(e.Data.RelPath, tempPrefix) || strings.Contains(e.Data.RelPath, "/") {
		t.Fatalf("io_error does not name the staging directory: %+v", err)
	}
	if f.content(t, "work/"+e.Data.RelPath+"/b0") != "deep" {
		t.Error("the original of the first step was not kept")
	}
	n, err := f.tree.SweepTemps()
	if err != nil || n != 1 {
		t.Errorf("sweep: %d, %v", n, err)
	}
	f.noTemps(t)
}

// TestTree_SweepTemps is finding 5: at start the executor removes what a
// crashed write or patch left at the worktree root, .warden-tmp-* files
// and staging directories, without following links and without touching
// anything else (a link named like a temporary is removed as a link; what
// it points to stays). Temporary files of writes in subdirectories are not
// swept (a full walk at every start is not worth it; they are inert).
func TestTree_SweepTemps(t *testing.T) {
	f := newFixture(t, modeFallback)
	stage, temp, link := tempPrefix+"0123456789abcdef", tempPrefix+"fedcba9876543210", tempPrefix+"00000000000000aa"
	f.file("work/"+stage+"/t0", "staged")
	f.file("work/"+stage+"/b0", "backup")
	f.file("work/"+temp, "temp")
	f.link(t, "../outside", "work/"+link)
	f.file("work/keep.txt", "keep")
	f.file("work/sub/"+tempPrefix+"4444444444444444", "not at the root")
	// Review round 2: names only like the executor's. A repository can
	// commit a file named .warden-tmp-anything; sweeping it would show the
	// agent a deletion it could commit. Only tempPrefix followed by
	// exactly 16 lowercase hex digits (randomName) is ever swept.
	survivors := []string{
		tempPrefix + "repo.txt",
		tempPrefix + "0123456789abcdeg",   // one non-hex digit
		tempPrefix + "0123456789ABCDEF",   // upper case: never generated
		tempPrefix + "0123456789abcde",    // 15 digits
		tempPrefix + "0123456789abcdef0",  // 17 digits
		tempPrefix + "0123456789abcdef.x", // a suffix
	}
	for _, s := range survivors {
		f.file("work/"+s, "committed by the repository")
	}
	n, err := f.tree.SweepTemps()
	if err != nil || n != 3 {
		t.Errorf("sweep: %d, %v", n, err)
	}
	for _, p := range []string{stage, temp, link} {
		f.missing(t, "work/"+p)
	}
	for _, s := range survivors {
		if b, err := os.ReadFile(f.work + "/" + s); err != nil || string(b) != "committed by the repository" {
			t.Errorf("the sweep removed or changed %s", s)
		}
	}
	if f.content(t, "outside/secret") != "outside the roots" || f.content(t, "work/keep.txt") != "keep" || f.content(t, "work/sub/"+tempPrefix+"4444444444444444") != "not at the root" {
		t.Error("the sweep removed something it must not")
	}
}

// TestIsTempName pins the exact shape randomName produces.
func TestIsTempName(t *testing.T) {
	for i := 0; i < 100; i++ {
		n, err := randomName()
		if err != nil || !isTempName(n) {
			t.Fatalf("randomName %q is not a temp name", n)
		}
	}
	for _, n := range []string{tempPrefix, tempPrefix + "repo.txt", tempPrefix + "0123456789abcdeg", tempPrefix + "0123456789ABCDEF", "x" + tempPrefix + "0123456789abcdef"} {
		if isTempName(n) {
			t.Errorf("isTempName(%q)", n)
		}
	}
}
