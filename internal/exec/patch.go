// The exec.fs.patch parser and hunk applier (design A06 §4.2 "apply a
// unified diff, all or nothing", §4.3 PatchParams; the package doc is in
// server.go). Both are pure: the file system half, which resolves every
// path with the write rules and stages and commits the results, is in
// fs_unix.go.
//
// The patch is model-written text that decides which files change, so the
// parser is strict. Every line must belong to the grammar of a git diff
// (diff --git, its extended headers, ---/+++, hunks) or a plain unified diff
// (---/+++, hunks); prose before, between or after the files is refused,
// because a lenient parser that skipped a line it did not understand would
// apply something different from what the text shows a reviewer. Hunk line
// counts must match their headers exactly. Unsupported features are
// refused rather than approximated: binary patches, copies, C-quoted names,
// and any mode other than a regular file (a symlink, 120000, would let a
// patch plant a link; a gitlink, 160000, is a submodule). Hunks apply at
// their stated line only, with no offset search and no fuzz, which keeps
// the cost linear in the file and the patch.
//
// Codes: malformed or unsupported text is invalid_argument, more than
// execproto.MaxPatchFiles files is too_large (D-048, counted while parsing,
// so nothing has been resolved or read yet), an added line holding the
// redaction marker is redaction_marker (A15 §6.4), and a hunk that does not
// match the file is patch_failed. Details carry line numbers, never text
// from the patch.

package exec

import (
	"bytes"
	"fmt"
	"path"
	"strconv"
	"strings"

	"warden.dev/warden/internal/execproto"
)

type hunkLine struct {
	kind  byte // ' ', '-' or '+'
	text  string
	noEOL bool // followed by "\ No newline at end of file"
}

type hunk struct {
	oldStart, oldCount, newStart, newCount int
	lines                                  []hunkLine
}

// filePatch is one file of a patch. A modify has from == to; a create has
// only to, a delete only from; a rename has both, different. mode is the
// permission the result gets (0o644 or 0o755), 0 to keep the file's own.
type filePatch struct {
	op       execproto.PatchOp
	from, to string
	hunks    []hunk
	mode     uint32
}

const devNull = "/dev/null"

// maxHunkNumber bounds a hunk header number: no file within the 4 MiB
// write cap has more lines, and the bound keeps index arithmetic far from
// overflow.
const maxHunkNumber = 1 << 30

type patchParser struct {
	lines []string
	i     int
	strip int
}

// parsePatch parses a unified diff and strips strip leading components
// from every ---/+++ and diff --git name (rename from/to lines carry no
// prefix and are not stripped, as in git).
func parsePatch(text string, strip int) ([]filePatch, error) {
	lines := strings.Split(text, "\n")
	// Blank lines are allowed only at the very end, after the last hunk.
	for len(lines) > 0 && strings.TrimSuffix(lines[len(lines)-1], "\r") == "" {
		lines = lines[:len(lines)-1]
	}
	p := &patchParser{lines: lines, strip: strip}
	var files []filePatch
	for p.i < len(p.lines) {
		var fp filePatch
		var err error
		switch l := p.header(); {
		case strings.HasPrefix(l, "diff --git "):
			fp, err = p.gitFile()
		case strings.HasPrefix(l, "--- "):
			fp, err = p.plainFile()
		default:
			return nil, p.malformed()
		}
		if err != nil {
			return nil, err
		}
		if len(files) == execproto.MaxPatchFiles {
			return nil, tooLarge("patch touches more than 4096 files")
		}
		files = append(files, fp)
	}
	if len(files) == 0 {
		return nil, invalid("patch touches no file")
	}
	// A path touched twice would make the staged results depend on order.
	// Names are compared cleaned and case-folded, because a case-insensitive
	// file system (macOS, NTFS under L2) treats A.txt and a.txt as one file.
	seen := map[string]bool{}
	for _, fp := range files {
		names := []string{fp.from, fp.to}
		if fp.op == execproto.PatchModify {
			names = names[:1] // from == to, counted once
		}
		for _, n := range names {
			if n == "" {
				continue
			}
			k := key(n)
			if seen[k] {
				return nil, invalid("a file appears more than once in the patch")
			}
			seen[k] = true
		}
	}
	return files, nil
}

// key is the duplicate-detection form of a patch name.
func key(n string) string {
	return strings.ToLower(path.Clean(strings.ReplaceAll(n, `\`, "/")))
}

// header is the current line with a trailing CR removed: header lines of a
// CRLF patch are read like LF ones. Hunk lines keep their CR, which is
// content.
func (p *patchParser) header() string { return strings.TrimSuffix(p.lines[p.i], "\r") }

func (p *patchParser) malformed() error {
	return invalid(fmt.Sprintf("malformed patch at line %d", p.i+1))
}

// name parses a ---/+++ name: the text up to a tab (plain diff appends a
// timestamp), /dev/null as itself, prefix components stripped.
func (p *patchParser) name(raw string) (string, error) {
	if i := strings.IndexByte(raw, '\t'); i >= 0 {
		raw = raw[:i]
	}
	if raw == devNull {
		return devNull, nil
	}
	n, ok := stripName(raw, p.strip)
	if !ok {
		return "", invalid(fmt.Sprintf("unusable file name at line %d", p.i+1))
	}
	return n, nil
}

// stripName removes strip leading components. A C-quoted name (git writes
// one for names with control characters or quotes) is refused, as is a
// name with nothing left after stripping or one Resolve would refuse.
func stripName(raw string, strip int) (string, bool) {
	if strings.HasPrefix(raw, `"`) {
		return "", false
	}
	segs := strings.Split(raw, "/")
	if len(segs) <= strip {
		return "", false
	}
	n := strings.Join(segs[strip:], "/")
	if validateInput(n) != nil {
		return "", false
	}
	return n, true
}

// plainFile parses ---, +++ and the hunks of one file.
func (p *patchParser) plainFile() (filePatch, error) {
	oldName, newName, hunks, err := p.content()
	if err != nil {
		return filePatch{}, err
	}
	fp := filePatch{hunks: hunks}
	switch {
	case oldName == devNull && newName == devNull:
		return filePatch{}, invalid("both sides of a file are /dev/null")
	case oldName == devNull:
		fp.op, fp.to = execproto.PatchCreate, newName
	case newName == devNull:
		fp.op, fp.from = execproto.PatchDelete, oldName
	case oldName != newName:
		return filePatch{}, invalid("old and new file names differ")
	default:
		fp.op, fp.from, fp.to = execproto.PatchModify, oldName, newName
	}
	return fp, checkShape(fp)
}

// content parses a ---/+++ pair and at least one hunk.
func (p *patchParser) content() (oldName, newName string, hunks []hunk, err error) {
	if oldName, err = p.name(strings.TrimPrefix(p.header(), "--- ")); err != nil {
		return "", "", nil, err
	}
	p.i++
	if p.i >= len(p.lines) || !strings.HasPrefix(p.header(), "+++ ") {
		return "", "", nil, p.malformed()
	}
	if newName, err = p.name(strings.TrimPrefix(p.header(), "+++ ")); err != nil {
		return "", "", nil, err
	}
	p.i++
	for p.i < len(p.lines) && strings.HasPrefix(p.header(), "@@ ") {
		h, err := p.hunk()
		if err != nil {
			return "", "", nil, err
		}
		hunks = append(hunks, h)
	}
	if len(hunks) == 0 {
		return "", "", nil, p.malformed()
	}
	return oldName, newName, hunks, nil
}

// gitFile parses one diff --git section.
func (p *patchParser) gitFile() (filePatch, error) {
	gitLine := strings.TrimPrefix(p.header(), "diff --git ")
	p.i++
	var oldMode, newMode, createMode, deleteMode, renameFrom, renameTo string
	var create, del, hasFrom, hasTo bool
headers:
	for p.i < len(p.lines) {
		l := p.header()
		switch {
		case strings.HasPrefix(l, "old mode "):
			oldMode = strings.TrimPrefix(l, "old mode ")
		case strings.HasPrefix(l, "new mode "):
			newMode = strings.TrimPrefix(l, "new mode ")
		case strings.HasPrefix(l, "new file mode "):
			create, createMode = true, strings.TrimPrefix(l, "new file mode ")
		case strings.HasPrefix(l, "deleted file mode "):
			del, deleteMode = true, strings.TrimPrefix(l, "deleted file mode ")
		case strings.HasPrefix(l, "rename from "):
			n, ok := stripName(strings.TrimPrefix(l, "rename from "), 0)
			if !ok || hasFrom {
				return filePatch{}, p.malformed()
			}
			renameFrom, hasFrom = n, true
		case strings.HasPrefix(l, "rename to "):
			n, ok := stripName(strings.TrimPrefix(l, "rename to "), 0)
			if !ok || hasTo {
				return filePatch{}, p.malformed()
			}
			renameTo, hasTo = n, true
		case strings.HasPrefix(l, "similarity index "), strings.HasPrefix(l, "dissimilarity index "):
		case strings.HasPrefix(l, "index "):
			// "index <old>..<new>[ <mode>]": the mode, when given, is the
			// file's unchanged mode and must be a regular file's too.
			if f := strings.Fields(l); len(f) == 3 && !regularMode(f[2]) {
				return filePatch{}, notRegularMode()
			} else if len(f) != 2 && len(f) != 3 {
				return filePatch{}, p.malformed()
			}
		case strings.HasPrefix(l, "copy from "), strings.HasPrefix(l, "copy to "):
			return filePatch{}, invalid("copies are not supported")
		case strings.HasPrefix(l, "Binary files "), l == "GIT binary patch":
			return filePatch{}, invalid("binary patches are not supported")
		default:
			break headers
		}
		p.i++
	}
	for _, m := range []string{oldMode, newMode, createMode, deleteMode} {
		if m != "" && !regularMode(m) {
			return filePatch{}, notRegularMode()
		}
	}
	var oldName, newName string
	var hunks []hunk
	hasContent := p.i < len(p.lines) && strings.HasPrefix(p.header(), "--- ")
	if hasContent {
		var err error
		if oldName, newName, hunks, err = p.content(); err != nil {
			return filePatch{}, err
		}
	}
	if hasFrom != hasTo || (create && del) || (hasFrom && (create || del)) {
		return filePatch{}, invalid("contradictory file headers")
	}

	fp := filePatch{hunks: hunks}
	switch {
	case hasFrom:
		fp.op, fp.from, fp.to = execproto.PatchRename, renameFrom, renameTo
		if fp.from == fp.to || !p.gitNames(gitLine, fp.from, fp.to) || (hasContent && (oldName != fp.from || newName != fp.to)) {
			return filePatch{}, invalid("rename headers disagree")
		}
		fp.mode = modeBits(newMode)
	default:
		// One name: the one both halves of the diff --git line give, which
		// ---/+++ must repeat when present, with /dev/null on the side a
		// create or delete does not have.
		n, ok := p.gitSame(gitLine)
		wantOld, wantNew := n, n
		switch {
		case create:
			fp.op, fp.to, fp.mode, wantOld = execproto.PatchCreate, n, modeBits(createMode), devNull
		case del:
			fp.op, fp.from, wantNew = execproto.PatchDelete, n, devNull
		default:
			if !hasContent && newMode == "" {
				return filePatch{}, invalid(fmt.Sprintf("file section without changes before line %d", p.i+1))
			}
			fp.op, fp.from, fp.to, fp.mode = execproto.PatchModify, n, n, modeBits(newMode)
		}
		if !ok || (hasContent && (oldName != wantOld || newName != wantNew)) {
			return filePatch{}, invalid("file headers disagree")
		}
	}
	return fp, checkShape(fp)
}

// maxGitLine bounds the diff --git line: two names of at most maxPathBytes
// after stripping, each behind up to MaxPatchStrip prefix components of at
// most maxSegmentBytes, and the space between them. A longer line cannot
// name a file Resolve would accept and is refused before it is split.
const maxGitLine = 2*(maxPathBytes+execproto.MaxPatchStrip*(maxSegmentBytes+1)) + 1

// nameStarts returns, for every index k of line, where a name that starts
// its prefix at k begins after strip components (just after the strip-th
// "/" at or after k), or -1 when there are not that many slashes. One pass
// from the end: next is the first slash at or after k.
func nameStarts(line string, strip int) []int {
	next := make([]int, len(line)+1)
	next[len(line)] = -1
	for k := len(line) - 1; k >= 0; k-- {
		if line[k] == '/' {
			next[k] = k
		} else {
			next[k] = next[k+1]
		}
	}
	out := make([]int, len(line)+1)
	for k := range out {
		at := k
		for j := 0; j < strip && at >= 0; j++ {
			if at > len(line) || next[at] < 0 {
				at = -1
				break
			}
			at = next[at] + 1
		}
		out[k] = at
	}
	return out
}

// gitSame finds the split of "a/<n> b/<n>" whose halves strip to the same
// name. Names may contain spaces, so the split is not simply the first or
// the middle space. Trying every space and stripping both halves each time
// was quadratic in the line (CWE-407: 21 s for a 1 MiB line of spaces), so
// the candidates are narrowed first: the left name starts at a fixed index
// (after the prefix of the whole line), the right one after the prefix
// following the space, and the two can only be equal if they are equally
// long. As the space moves right the left name grows and the right one
// does not, so at most one space qualifies, and only that split is
// compared.
func (p *patchParser) gitSame(gitLine string) (string, bool) {
	if len(gitLine) > maxGitLine {
		return "", false
	}
	starts := nameStarts(gitLine, p.strip)
	pre := starts[0]
	if pre < 0 {
		return "", false
	}
	for i := pre; i < len(gitLine); i++ {
		if gitLine[i] != ' ' {
			continue
		}
		q := starts[i+1]
		if q < 0 {
			break // fewer slashes after every later space too
		}
		if i-pre == len(gitLine)-q {
			a, okA := stripName(gitLine[:i], p.strip)
			b, okB := stripName(gitLine[i+1:], p.strip)
			if okA && okB && a == b {
				return a, true
			}
			return "", false
		}
	}
	return "", false
}

// gitNames reports whether the diff --git line names from and to: the left
// name starts after the line's prefix, so the split is right after from.
func (p *patchParser) gitNames(gitLine, from, to string) bool {
	if len(gitLine) > maxGitLine {
		return false
	}
	pre := nameStarts(gitLine, p.strip)[0]
	i := pre + len(from)
	if pre < 0 || i >= len(gitLine) || gitLine[i] != ' ' {
		return false
	}
	a, okA := stripName(gitLine[:i], p.strip)
	b, okB := stripName(gitLine[i+1:], p.strip)
	return okA && okB && a == from && b == to
}

func regularMode(m string) bool { return m == "100644" || m == "100755" }

func modeBits(m string) uint32 {
	switch m {
	case "100644":
		return 0o644
	case "100755":
		return 0o755
	}
	return 0
}

func notRegularMode() error {
	return invalid("only regular files can be patched: symlinks and submodules are not supported")
}

// checkShape holds creates and deletes to one whole-file hunk: a create
// adds lines to nothing (-0,0), a delete removes every line (+0,0). Without
// this a "create" could carry context that applies to nothing.
func checkShape(fp filePatch) error {
	if len(fp.hunks) > 1 && (fp.op == execproto.PatchCreate || fp.op == execproto.PatchDelete) {
		return invalid("a created or deleted file has at most one hunk")
	}
	for _, h := range fp.hunks {
		if fp.op == execproto.PatchCreate && (h.oldStart != 0 || h.oldCount != 0) {
			return invalid("a created file's hunk must start at -0,0")
		}
		if fp.op == execproto.PatchDelete && (h.newStart != 0 || h.newCount != 0) {
			return invalid("a deleted file's hunk must end at +0,0")
		}
	}
	return nil
}

// hunk parses one hunk: the @@ header, exactly the counted lines, and the
// optional "\ No newline at end of file" markers.
func (p *patchParser) hunk() (hunk, error) {
	h, ok := parseHunkHeader(p.header())
	if !ok {
		return hunk{}, p.malformed()
	}
	p.i++
	o, n := 0, 0
	for {
		if p.i < len(p.lines) && strings.HasPrefix(p.lines[p.i], `\`) {
			if len(h.lines) == 0 || h.lines[len(h.lines)-1].noEOL {
				return hunk{}, p.malformed()
			}
			h.lines[len(h.lines)-1].noEOL = true
			p.i++
			continue
		}
		if o == h.oldCount && n == h.newCount {
			return h, nil
		}
		if p.i >= len(p.lines) {
			return hunk{}, p.malformed()
		}
		l := p.lines[p.i]
		kind, text := byte(' '), ""
		if l != "" {
			kind, text = l[0], l[1:]
		}
		switch kind {
		case ' ':
			o, n = o+1, n+1
		case '-':
			o++
		case '+':
			n++
			if strings.Contains(text, redactionMarker) {
				return hunk{}, redacted()
			}
		default:
			return hunk{}, p.malformed()
		}
		if o > h.oldCount || n > h.newCount {
			return hunk{}, p.malformed()
		}
		h.lines = append(h.lines, hunkLine{kind: kind, text: text})
		p.i++
	}
}

// parseHunkHeader reads "@@ -a[,b] +c[,d] @@[ text]".
func parseHunkHeader(l string) (hunk, bool) {
	s, ok := strings.CutPrefix(l, "@@ -")
	if !ok {
		return hunk{}, false
	}
	end := strings.Index(s, " @@")
	if end < 0 {
		return hunk{}, false
	}
	oldR, newR, ok := strings.Cut(s[:end], " +")
	if !ok {
		return hunk{}, false
	}
	var h hunk
	var ok1, ok2 bool
	h.oldStart, h.oldCount, ok1 = parseRange(oldR)
	h.newStart, h.newCount, ok2 = parseRange(newR)
	if !ok1 || !ok2 || (h.oldCount > 0 && h.oldStart == 0) || (h.newCount > 0 && h.newStart == 0) {
		return hunk{}, false
	}
	return h, true
}

func parseRange(s string) (start, count int, ok bool) {
	a, b, hasCount := strings.Cut(s, ",")
	if start, ok = hunkNumber(a); !ok {
		return 0, 0, false
	}
	count = 1
	if hasCount {
		if count, ok = hunkNumber(b); !ok {
			return 0, 0, false
		}
	}
	return start, count, true
}

func hunkNumber(s string) (int, bool) {
	if s == "" || len(s) > 10 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n <= maxHunkNumber
}

func patchFailed(detail string) error { return execproto.NewError(execproto.ErrPatchFailed, detail) }

// applyHunks applies hunks to orig. Each hunk must match exactly at its
// stated line (the line before it for a pure insertion, oldCount 0), after
// the previous hunk; context and removed lines are compared with their
// line endings, so a CR or a missing final newline must be in the patch
// too. A line written without a newline must be the last of the result.
func applyHunks(orig []byte, hunks []hunk) ([]byte, error) {
	lines := splitLines(orig)
	var out bytes.Buffer
	open := true // false once a line without newline has been written
	write := func(s string) error {
		if s == "" {
			return nil
		}
		if !open {
			return patchFailed("a line without a final newline is not the last line")
		}
		out.WriteString(s)
		open = strings.HasSuffix(s, "\n")
		return nil
	}
	cursor := 0
	for hi, h := range hunks {
		idx := h.oldStart - 1
		if h.oldCount == 0 {
			idx = h.oldStart
		}
		if idx < cursor || idx+h.oldCount > len(lines) {
			return nil, patchFailed(fmt.Sprintf("hunk %d does not apply at its line", hi+1))
		}
		for _, l := range lines[cursor:idx] {
			if err := write(l); err != nil {
				return nil, err
			}
		}
		j := idx
		for _, hl := range h.lines {
			full := hl.text
			if !hl.noEOL {
				full += "\n"
			}
			if hl.kind != '+' {
				if lines[j] != full {
					return nil, patchFailed(fmt.Sprintf("hunk %d does not match the file", hi+1))
				}
				j++
			}
			if hl.kind != '-' {
				if err := write(full); err != nil {
					return nil, err
				}
			}
		}
		cursor = idx + h.oldCount
	}
	for _, l := range lines[cursor:] {
		if err := write(l); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

// splitLines splits b after each newline; the last line may lack one.
func splitLines(b []byte) []string {
	var out []string
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out = append(out, string(b))
			break
		}
		out = append(out, string(b[:i+1]))
		b = b[i+1:]
	}
	return out
}
