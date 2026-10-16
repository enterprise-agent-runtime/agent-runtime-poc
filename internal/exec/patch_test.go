package exec

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"warden.dev/warden/internal/execproto"
)

// The patch parser reads text the model wrote and decides which files are
// touched and how. Every file it names is then resolved with the write
// rules (deny-list, .git ban, roots), so a parser that read a different set
// of files than the text says, or that silently skipped a line it did not
// understand, would apply something nobody reviewed. It is therefore
// strict: every line must belong to the grammar (git or plain unified
// diff), hunk line counts must match their headers exactly, and anything it
// does not support (binary patches, copies, symlinks and submodules,
// C-quoted names) is refused instead of approximated. Hunks apply at their
// stated line only: no offset search, no fuzz.

type patchCase struct {
	name  string
	patch string
	strip int
	want  []filePatch // nil: only the error code is checked
	code  execproto.Code
}

func hunkOf(oldStart, oldCount, newStart, newCount int, lines ...string) hunk {
	h := hunk{oldStart: oldStart, oldCount: oldCount, newStart: newStart, newCount: newCount}
	for _, l := range lines {
		noEOL := strings.HasSuffix(l, "$")
		l = strings.TrimSuffix(l, "$")
		h.lines = append(h.lines, hunkLine{kind: l[0], text: l[1:], noEOL: noEOL})
	}
	return h
}

func TestParsePatch(t *testing.T) {
	cases := []patchCase{
		{name: "git modify", strip: 1, patch: "diff --git a/src/x.go b/src/x.go\nindex 1111111..2222222 100644\n--- a/src/x.go\n+++ b/src/x.go\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n",
			want: []filePatch{{op: execproto.PatchModify, from: "src/x.go", to: "src/x.go", hunks: []hunk{hunkOf(1, 2, 1, 2, " a", "-b", "+c")}}}},
		{name: "plain modify with timestamps", strip: 1, patch: "--- a/x.txt\t2026-10-16 10:00:00\n+++ b/x.txt\t2026-10-16 10:01:00\n@@ -1 +1 @@\n-old\n+new\n",
			want: []filePatch{{op: execproto.PatchModify, from: "x.txt", to: "x.txt", hunks: []hunk{hunkOf(1, 1, 1, 1, "-old", "+new")}}}},
		{name: "strip 0", strip: 0, patch: "--- x.txt\n+++ x.txt\n@@ -1 +1 @@\n-old\n+new\n",
			want: []filePatch{{op: execproto.PatchModify, from: "x.txt", to: "x.txt", hunks: []hunk{hunkOf(1, 1, 1, 1, "-old", "+new")}}}},
		{name: "strip 2", strip: 2, patch: "--- a/b/x.txt\n+++ a/b/x.txt\n@@ -1 +1 @@\n-old\n+new\n",
			want: []filePatch{{op: execproto.PatchModify, from: "x.txt", to: "x.txt", hunks: []hunk{hunkOf(1, 1, 1, 1, "-old", "+new")}}}},
		{name: "git create", strip: 1, patch: "diff --git a/new.txt b/new.txt\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,2 @@\n+one\n+two\n",
			want: []filePatch{{op: execproto.PatchCreate, to: "new.txt", mode: 0o644, hunks: []hunk{hunkOf(0, 0, 1, 2, "+one", "+two")}}}},
		{name: "plain create", strip: 1, patch: "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+one\n",
			want: []filePatch{{op: execproto.PatchCreate, to: "new.txt", hunks: []hunk{hunkOf(0, 0, 1, 1, "+one")}}}},
		{name: "git create executable", strip: 1, patch: "diff --git a/run.sh b/run.sh\nnew file mode 100755\n--- /dev/null\n+++ b/run.sh\n@@ -0,0 +1 @@\n+echo\n",
			want: []filePatch{{op: execproto.PatchCreate, to: "run.sh", mode: 0o755, hunks: []hunk{hunkOf(0, 0, 1, 1, "+echo")}}}},
		{name: "git create empty", strip: 1, patch: "diff --git a/empty b/empty\nnew file mode 100644\nindex 0000000..e69de29\n",
			want: []filePatch{{op: execproto.PatchCreate, to: "empty", mode: 0o644}}},
		{name: "git delete", strip: 1, patch: "diff --git a/old.txt b/old.txt\ndeleted file mode 100644\nindex 1111111..0000000\n--- a/old.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n",
			want: []filePatch{{op: execproto.PatchDelete, from: "old.txt", hunks: []hunk{hunkOf(1, 1, 0, 0, "-gone")}}}},
		{name: "git delete empty", strip: 1, patch: "diff --git a/e b/e\ndeleted file mode 100644\n",
			want: []filePatch{{op: execproto.PatchDelete, from: "e"}}},
		{name: "pure rename", strip: 1, patch: "diff --git a/a.txt b/b.txt\nsimilarity index 100%\nrename from a.txt\nrename to b.txt\n",
			want: []filePatch{{op: execproto.PatchRename, from: "a.txt", to: "b.txt"}}},
		{name: "rename with a hunk", strip: 1, patch: "diff --git a/a.txt b/d/b.txt\nsimilarity index 80%\nrename from a.txt\nrename to d/b.txt\nindex 1..2 100644\n--- a/a.txt\n+++ b/d/b.txt\n@@ -1 +1 @@\n-x\n+y\n",
			want: []filePatch{{op: execproto.PatchRename, from: "a.txt", to: "d/b.txt", hunks: []hunk{hunkOf(1, 1, 1, 1, "-x", "+y")}}}},
		{name: "mode change only", strip: 1, patch: "diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n",
			want: []filePatch{{op: execproto.PatchModify, from: "run.sh", to: "run.sh", mode: 0o755}}},
		{name: "mode-only name with a space", strip: 1, patch: "diff --git a/my file b/my file\nold mode 100644\nnew mode 100755\n",
			want: []filePatch{{op: execproto.PatchModify, from: "my file", to: "my file", mode: 0o755}}},
		{name: "no newline at end, both sides", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n\\ No newline at end of file\n",
			want: []filePatch{{op: execproto.PatchModify, from: "x", to: "x", hunks: []hunk{hunkOf(1, 1, 1, 1, "-a$", "+b$")}}}},
		{name: "two hunks, empty context line, CRLF headers", strip: 1, patch: "--- a/x\r\n+++ b/x\r\n@@ -1,3 +1,3 @@\r\n a\n\n-c\n+C\n@@ -10 +10 @@\n-j\n+J\n",
			want: []filePatch{{op: execproto.PatchModify, from: "x", to: "x", hunks: []hunk{
				hunkOf(1, 3, 1, 3, " a", " ", "-c", "+C"), hunkOf(10, 1, 10, 1, "-j", "+J")}}}},
		{name: "two files", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-a\n+b\n",
			want: []filePatch{
				{op: execproto.PatchModify, from: "x", to: "x", hunks: []hunk{hunkOf(1, 1, 1, 1, "-a", "+b")}},
				{op: execproto.PatchModify, from: "y", to: "y", hunks: []hunk{hunkOf(1, 1, 1, 1, "-a", "+b")}}}},
		{name: "a hunk line that looks like a header", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n--- a/y\n+++ b/y\n",
			want: []filePatch{{op: execproto.PatchModify, from: "x", to: "x", hunks: []hunk{hunkOf(1, 1, 1, 1, "--- a/y", "+++ b/y")}}}},
		{name: "added marker in context is fine", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n k=[REDACTED:t]\n-a\n+b\n",
			want: []filePatch{{op: execproto.PatchModify, from: "x", to: "x", hunks: []hunk{hunkOf(1, 2, 1, 2, " k=[REDACTED:t]", "-a", "+b")}}}},

		// Refused: anything outside the grammar.
		{name: "leading prose", strip: 1, patch: "Here is the fix:\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "trailing prose", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\nthanks\n", code: execproto.ErrInvalidArgument},
		{name: "too few hunk lines", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "too many hunk lines", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n-b\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "bad hunk header", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1,x +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "huge hunk number", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -99999999999999999999 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "no hunk after headers", strip: 1, patch: "--- a/x\n+++ b/x\n", code: execproto.ErrInvalidArgument},
		{name: "missing +++", strip: 1, patch: "--- a/x\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "plain names differ", strip: 1, patch: "--- a/x.orig\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "both /dev/null", strip: 1, patch: "--- /dev/null\n+++ /dev/null\n@@ -0,0 +1 @@\n+a\n", code: execproto.ErrInvalidArgument},
		{name: "quoted name", strip: 1, patch: "--- \"a/x\\ty\"\n+++ \"b/x\\ty\"\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "binary patch", strip: 1, patch: "diff --git a/b.bin b/b.bin\nindex 1..2 100644\nGIT binary patch\nliteral 1\nIcmZ?d00001\n", code: execproto.ErrInvalidArgument},
		{name: "binary files differ", strip: 1, patch: "diff --git a/b.bin b/b.bin\nindex 1..2 100644\nBinary files a/b.bin and b/b.bin differ\n", code: execproto.ErrInvalidArgument},
		{name: "copy", strip: 1, patch: "diff --git a/a b/b\nsimilarity index 100%\ncopy from a\ncopy to b\n", code: execproto.ErrInvalidArgument},
		{name: "symlink create", strip: 1, patch: "diff --git a/l b/l\nnew file mode 120000\n--- /dev/null\n+++ b/l\n@@ -0,0 +1 @@\n+/etc/passwd\n\\ No newline at end of file\n", code: execproto.ErrInvalidArgument},
		{name: "symlink in index line", strip: 1, patch: "diff --git a/l b/l\nindex 1..2 120000\n--- a/l\n+++ b/l\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "submodule", strip: 1, patch: "diff --git a/m b/m\nnew file mode 160000\nindex 0..1\n--- /dev/null\n+++ b/m\n@@ -0,0 +1 @@\n+Subproject commit 1\n", code: execproto.ErrInvalidArgument},
		{name: "mode change to symlink", strip: 1, patch: "diff --git a/x b/x\nold mode 100644\nnew mode 120000\n", code: execproto.ErrInvalidArgument},
		{name: "git header with nothing", strip: 1, patch: "diff --git a/x b/x\nindex 1..2 100644\n", code: execproto.ErrInvalidArgument},
		{name: "git header names disagree with ---", strip: 1, patch: "diff --git a/x b/x\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "rename disagrees with ---/+++", strip: 1, patch: "diff --git a/a b/b\nrename from a\nrename to b\n--- a/a\n+++ b/c\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "rename without to", strip: 1, patch: "diff --git a/a b/b\nrename from a\n", code: execproto.ErrInvalidArgument},
		{name: "new file with old side", strip: 1, patch: "diff --git a/x b/x\nnew file mode 100644\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "strip removes everything", strip: 2, patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "marker with nothing before", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n\\ No newline at end of file\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "same file twice", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n--- a/x\n+++ b/x\n@@ -2 +2 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "rename onto a modified file", strip: 1, patch: "diff --git a/a b/x\nrename from a\nrename to x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "empty", strip: 1, patch: "\n", code: execproto.ErrInvalidArgument},
		{name: "create with an old side", strip: 1, patch: "--- /dev/null\n+++ b/x\n@@ -0,0 +1,2 @@\n a\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "create with two hunks", strip: 1, patch: "--- /dev/null\n+++ b/x\n@@ -0,0 +1 @@\n+a\n@@ -0,0 +1 @@\n+b\n", code: execproto.ErrInvalidArgument},
		{name: "delete keeping a line", strip: 1, patch: "--- a/x\n+++ /dev/null\n@@ -1,2 +1 @@\n a\n-b\n", code: execproto.ErrInvalidArgument},
		{name: "NUL in a name", strip: 1, patch: "--- a/x\x00\n+++ b/x\x00\n@@ -1 +1 @@\n-a\n+b\n", code: execproto.ErrInvalidArgument},
		// A15 §6.4: content the patch adds must not hold a redaction marker.
		{name: "added marker", strip: 1, patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+KEY=[REDACTED:openai_key]\n", code: execproto.ErrRedactionMarker},
	}
	for _, c := range cases {
		got, err := parsePatch(c.patch, c.strip)
		if codeOf(err) != c.code {
			t.Errorf("%s: err %v, want code %d", c.name, err, c.code)
			continue
		}
		if c.want == nil {
			continue
		}
		if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

// TestParsePatch_FileCap is D-048: a patch touching more than 4096 files is
// refused by the parser, before any path is resolved or any file read, so
// nothing can be written; 4096 is accepted. The count is of file sections,
// stopped at the first one over the cap, so a 4 MiB patch of tiny sections
// costs no more than its own size.
func TestParsePatch_FileCap(t *testing.T) {
	gen := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "diff --git a/f%d b/f%d\nnew file mode 100644\n", i, i)
		}
		return b.String()
	}
	files, err := parsePatch(gen(execproto.MaxPatchFiles), 1)
	if err != nil || len(files) != execproto.MaxPatchFiles {
		t.Fatalf("%d files: %d parsed, %v", execproto.MaxPatchFiles, len(files), err)
	}
	if _, err := parsePatch(gen(execproto.MaxPatchFiles+1), 1); codeOf(err) != execproto.ErrTooLarge {
		t.Fatalf("%d files: %v, want too_large", execproto.MaxPatchFiles+1, err)
	}
}

func TestApplyHunks(t *testing.T) {
	cases := []struct {
		name  string
		orig  string
		hunks []hunk
		want  string
		code  execproto.Code
	}{
		{"modify", "a\nb\nc\n", []hunk{hunkOf(2, 1, 2, 1, "-b", "+B")}, "a\nB\nc\n", 0},
		{"insert at start", "a\n", []hunk{hunkOf(0, 0, 1, 1, "+z")}, "z\na\n", 0},
		{"insert after line 1", "a\nb\n", []hunk{hunkOf(1, 0, 2, 1, "+z")}, "a\nz\nb\n", 0},
		{"create from empty", "", []hunk{hunkOf(0, 0, 1, 2, "+x", "+y")}, "x\ny\n", 0},
		{"delete all", "x\ny\n", []hunk{hunkOf(1, 2, 0, 0, "-x", "-y")}, "", 0},
		{"two hunks", "1\n2\n3\n4\n", []hunk{hunkOf(1, 1, 1, 1, "-1", "+one"), hunkOf(4, 1, 4, 1, "-4", "+four")}, "one\n2\n3\nfour\n", 0},
		{"add newline at EOF", "a", []hunk{hunkOf(1, 1, 1, 1, "-a$", "+a")}, "a\n", 0},
		{"remove newline at EOF", "a\n", []hunk{hunkOf(1, 1, 1, 1, "-a", "+a$")}, "a", 0},
		{"CRLF kept", "a\r\nb\r\n", []hunk{hunkOf(2, 1, 2, 1, "-b\r", "+c\r")}, "a\r\nc\r\n", 0},
		{"context mismatch", "a\nb\n", []hunk{hunkOf(1, 1, 1, 1, "-x", "+y")}, "", execproto.ErrPatchFailed},
		{"wrong line, no offset search", "a\nb\nc\n", []hunk{hunkOf(1, 1, 1, 1, "-b", "+B")}, "", execproto.ErrPatchFailed},
		{"past the end", "a\n", []hunk{hunkOf(2, 1, 2, 1, "-a", "+b")}, "", execproto.ErrPatchFailed},
		{"insert past the end", "a\n", []hunk{hunkOf(5, 0, 6, 1, "+b")}, "", execproto.ErrPatchFailed},
		{"overlapping hunks", "a\nb\n", []hunk{hunkOf(1, 2, 1, 2, " a", " b"), hunkOf(2, 1, 2, 1, "-b", "+c")}, "", execproto.ErrPatchFailed},
		{"newline mismatch", "a", []hunk{hunkOf(1, 1, 1, 1, "-a", "+b")}, "", execproto.ErrPatchFailed},
		{"no-newline line not last", "a\nb\n", []hunk{hunkOf(1, 1, 1, 1, "-a", "+x$")}, "", execproto.ErrPatchFailed},
	}
	for _, c := range cases {
		got, err := applyHunks([]byte(c.orig), c.hunks)
		if codeOf(err) != c.code {
			t.Errorf("%s: err %v, want code %d", c.name, err, c.code)
			continue
		}
		if err == nil && string(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// FuzzPatch feeds arbitrary text to the parser. It must never panic, must
// refuse only with the codes the method documents (invalid_argument for
// malformed text, too_large for D-048, redaction_marker for A15 §6.4), and
// everything it accepts must be internally consistent: every file has the
// paths its operation needs, no path appears twice, hunk line counts match
// their headers, and a created file's hunks apply to an empty file.
func FuzzPatch(f *testing.F) {
	for _, c := range []string{
		"diff --git a/src/x.go b/src/x.go\nindex 1..2 100644\n--- a/src/x.go\n+++ b/src/x.go\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n",
		"--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+one\n\\ No newline at end of file\n",
		"diff --git a/a.txt b/b.txt\nsimilarity index 100%\nrename from a.txt\nrename to b.txt\n",
		"diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n",
		"diff --git a/e b/e\ndeleted file mode 100644\n",
		"--- a/x\n+++ b/x\n@@ -1,3 +1,3 @@\n a\n\n-c\n+C\n@@ -10 +10 @@\n-j\n+J\n",
		// The quadratic diff --git line of review round 1 (CWE-407).
		"diff --git " + strings.Repeat(" ", 60000) + "\nnew mode 100755\n",
		"diff --git a/x b/y b/x b/y\nold mode 100644\nnew mode 100755\n",
	} {
		f.Add(c, 1)
	}
	f.Fuzz(func(t *testing.T, text string, strip int) {
		if strip < 0 || strip > execproto.MaxPatchStrip || len(text) > 1<<16 {
			return
		}
		files, err := parsePatch(text, strip)
		if err != nil {
			switch codeOf(err) {
			case execproto.ErrInvalidArgument, execproto.ErrTooLarge, execproto.ErrRedactionMarker:
				return
			}
			t.Fatalf("unexpected error %v", err)
		}
		if len(files) == 0 || len(files) > execproto.MaxPatchFiles {
			t.Fatalf("%d files accepted", len(files))
		}
		seen := map[string]bool{}
		for _, fp := range files {
			names := []string{fp.from, fp.to}
			if fp.op == execproto.PatchModify {
				names = names[:1]
			}
			for _, p := range names {
				if p == "" {
					continue
				}
				if seen[p] {
					t.Fatalf("path %q twice", p)
				}
				seen[p] = true
			}
			switch fp.op {
			case execproto.PatchModify:
				if fp.from == "" || fp.from != fp.to {
					t.Fatalf("modify %+v", fp)
				}
			case execproto.PatchCreate:
				if fp.from != "" || fp.to == "" {
					t.Fatalf("create %+v", fp)
				}
				if _, err := applyHunks(nil, fp.hunks); err != nil {
					t.Fatalf("create apply: %v", err)
				}
			case execproto.PatchDelete:
				if fp.from == "" || fp.to != "" {
					t.Fatalf("delete %+v", fp)
				}
			case execproto.PatchRename:
				if fp.from == "" || fp.to == "" || fp.from == fp.to {
					t.Fatalf("rename %+v", fp)
				}
			default:
				t.Fatalf("op %q", fp.op)
			}
			for _, h := range fp.hunks {
				var o, n int
				for _, l := range h.lines {
					switch l.kind {
					case ' ':
						o, n = o+1, n+1
					case '-':
						o++
					case '+':
						n++
					default:
						t.Fatalf("line kind %q", l.kind)
					}
					if l.kind == '+' && strings.Contains(l.text, "[REDACTED:") {
						t.Fatal("added redaction marker accepted")
					}
				}
				if o != h.oldCount || n != h.newCount {
					t.Fatalf("hunk counts %d/%d, header %d/%d", o, n, h.oldCount, h.newCount)
				}
			}
		}
	})
}

func TestParseHunkHeader(t *testing.T) {
	for _, c := range []struct {
		line string
		ok   bool
	}{
		{"@@ -1,2 +3,4 @@", true},
		{"@@ -1 +1 @@ func main() {", true},
		{"@@ -0,0 +1 @@", true},
		{"@@ -1 +0,0 @@", true},
		{"@@ -1 +1", false},             // no closing @@
		{"@@ -1,1 @@", false},           // no new range
		{"@@ +1 -1 @@", false},          // order
		{"@@ -0,1 +1 @@", false},        // lines from line 0
		{"@@ -1 +0,1 @@", false},        // lines to line 0
		{"@@ -1,-1 +1 @@", false},       // sign
		{"@@ -1, +1 @@", false},         // empty count
		{"@@ -01x +1 @@", false},        // not a number
		{"@@ -1073741825 +1 @@", false}, // over maxHunkNumber
		{"@@@ -1 +1 @@@", false},
	} {
		if _, ok := parseHunkHeader(c.line); ok != c.ok {
			t.Errorf("parseHunkHeader(%q) ok = %v, want %v", c.line, ok, c.ok)
		}
	}
}

// TestParsePatch_GitLineNames: the diff --git line is split into its two
// names without trying every space (see TestParsePatch_GitLineLinear);
// these are the splits the old every-space search found.
func TestParsePatch_GitLineNames(t *testing.T) {
	mode := "\nold mode 100644\nnew mode 100755\n"
	cases := []struct {
		name, patch string
		strip       int
		want        string // "" = invalid_argument
	}{
		{"names with ' b/' inside", "diff --git a/x b/y b/x b/y" + mode, 1, "x b/y"},
		{"strip 0", "diff --git x x" + mode, 0, "x"},
		{"strip 2", "diff --git a/b/x c/d/x" + mode, 2, "x"},
		{"spaces", "diff --git a/my file.txt b/my file.txt" + mode, 1, "my file.txt"},
		{"different names", "diff --git a/x b/y" + mode, 1, ""},
		{"one name", "diff --git a/x" + mode, 1, ""},
		{"no prefix to strip", "diff --git x x" + mode, 1, ""},
		{"only spaces", "diff --git      " + mode, 1, ""},
	}
	for _, c := range cases {
		files, err := parsePatch(c.patch, c.strip)
		switch {
		case c.want == "" && codeOf(err) != execproto.ErrInvalidArgument:
			t.Errorf("%s: %+v, %v; want invalid_argument", c.name, files, err)
		case c.want != "" && (err != nil || len(files) != 1 || files[0].from != c.want || files[0].to != c.want):
			t.Errorf("%s: %+v, %v; want %q", c.name, files, err, c.want)
		}
	}
	files, err := parsePatch("diff --git a/my a.txt b/my b.txt\nrename from my a.txt\nrename to my b.txt\n", 1)
	if err != nil || files[0].from != "my a.txt" || files[0].to != "my b.txt" {
		t.Errorf("rename with spaces: %+v, %v", files, err)
	}
	if _, err := parsePatch("diff --git a/my a.txt b/my c.txt\nrename from my a.txt\nrename to my b.txt\n", 1); codeOf(err) != execproto.ErrInvalidArgument {
		t.Errorf("rename disagreeing with the git line: %v", err)
	}
}

// TestParsePatch_GitLineLinear is CWE-407 from review round 1: the old
// parser tried a split at every space of the diff --git line and stripped
// both halves for each, quadratic in the line: a 1 MiB line of spaces took
// 21 s. A 4 MiB line (the whole patch cap) must be refused at once, and a
// long line of a legal shape must parse in linear time.
func TestParsePatch_GitLineLinear(t *testing.T) {
	done := make(chan error, 2)
	go func() {
		_, err := parsePatch("diff --git "+strings.Repeat(" ", execproto.MaxPatchBytes-64)+"\nnew mode 100755\n", 1)
		done <- err
	}()
	go func() {
		// Every section's git line at the longest legal length: 4095-byte
		// names made of spaces and slashes. All the names are equal, so
		// the patch is refused as duplicates, after every line was split.
		name := strings.Repeat("a /", 1365)
		var b strings.Builder
		for b.Len() < execproto.MaxPatchBytes-3*len(name) {
			fmt.Fprintf(&b, "diff --git a/%s b/%s\nold mode 100644\nnew mode 100755\n", name, name)
		}
		_, err := parsePatch(b.String(), 1)
		done <- err
	}()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if codeOf(err) != execproto.ErrInvalidArgument {
				t.Errorf("long git line: %v, want invalid_argument", err)
			}
		case <-time.After(hangGuardPure):
			t.Fatal("parsing a long diff --git line did not return: quadratic")
		}
	}
}

// hangGuardPure bounds waits for pure calls that must return; a deadlock
// guard, not a performance budget (CLAUDE.md §8.6).
const hangGuardPure = 20 * time.Second
