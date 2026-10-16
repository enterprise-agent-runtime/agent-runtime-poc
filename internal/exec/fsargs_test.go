package exec

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"warden.dev/warden/internal/execproto"
)

// These are the limit tables of the exec.fs methods (design A06 §4.2 and
// the fs.json schema of §4.3): one row per bound, on both sides of it. The
// executor is the last place a bound is enforced (Decode checks no
// ranges, and the daemon's check is a convenience, not a boundary), so an
// off-by-one here is an over-long read, an unbounded listing or a search
// that never ends. The values are execproto's constants, which TestLimits
// pins to the schema.

func intp(n int) *int    { return &n }
func boolp(b bool) *bool { return &b }

// codeOf is the executor code of err, 0 for nil, -1 for an error that is not
// an executor error (which would reach the wire as io_error).
func codeOf(err error) execproto.Code {
	if err == nil {
		return 0
	}
	var e *execproto.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return -1
}

func TestReadArgs_Limits(t *testing.T) {
	cases := []struct {
		name         string
		p            execproto.ReadParams
		code         execproto.Code
		offset, size int64
	}{
		{"defaults", execproto.ReadParams{}, 0, 0, execproto.MaxReadBytes},
		{"length 1", execproto.ReadParams{Length: 1}, 0, 0, 1},
		{"length at the cap", execproto.ReadParams{Length: execproto.MaxReadBytes}, 0, 0, execproto.MaxReadBytes},
		{"length over the cap is too_large", execproto.ReadParams{Length: execproto.MaxReadBytes + 1}, execproto.ErrTooLarge, 0, 0},
		{"negative length", execproto.ReadParams{Length: -1}, execproto.ErrInvalidArgument, 0, 0},
		{"offset kept", execproto.ReadParams{Offset: 7}, 0, 7, execproto.MaxReadBytes},
		{"negative offset", execproto.ReadParams{Offset: -1}, execproto.ErrInvalidArgument, 0, 0},
	}
	for _, c := range cases {
		got, err := readArgs(c.p)
		if codeOf(err) != c.code {
			t.Errorf("%s: err %v, want code %d", c.name, err, c.code)
			continue
		}
		if err == nil && (got.offset != c.offset || got.length != c.size) {
			t.Errorf("%s: got %+v, want offset %d length %d", c.name, got, c.offset, c.size)
		}
	}
}

func TestListArgs_Limits(t *testing.T) {
	cases := []struct {
		name       string
		p          execproto.ListParams
		code       execproto.Code
		depth, max int
	}{
		{"defaults", execproto.ListParams{}, 0, execproto.DefaultListDepth, execproto.DefaultListEntries},
		{"depth at the cap", execproto.ListParams{Depth: execproto.MaxListDepth}, 0, execproto.MaxListDepth, execproto.DefaultListEntries},
		{"depth over the cap", execproto.ListParams{Depth: execproto.MaxListDepth + 1}, execproto.ErrInvalidArgument, 0, 0},
		{"negative depth", execproto.ListParams{Depth: -1}, execproto.ErrInvalidArgument, 0, 0},
		{"entries at the cap", execproto.ListParams{MaxEntries: execproto.MaxListEntries}, 0, 1, execproto.MaxListEntries},
		{"entries over the cap", execproto.ListParams{MaxEntries: execproto.MaxListEntries + 1}, execproto.ErrInvalidArgument, 0, 0},
		{"negative entries", execproto.ListParams{MaxEntries: -1}, execproto.ErrInvalidArgument, 0, 0},
	}
	for _, c := range cases {
		got, err := listArgs(c.p)
		if codeOf(err) != c.code {
			t.Errorf("%s: err %v, want code %d", c.name, err, c.code)
			continue
		}
		if err == nil && (got.depth != c.depth || got.max != c.max) {
			t.Errorf("%s: got %+v, want depth %d max %d", c.name, got, c.depth, c.max)
		}
	}
}

func TestSearchArgs_Limits(t *testing.T) {
	globs := func(n, size int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strings.Repeat("a", size)
		}
		return out
	}
	q := execproto.SearchParams{Query: "x"}
	with := func(f func(*execproto.SearchParams)) execproto.SearchParams { p := q; f(&p); return p }
	cases := []struct {
		name string
		p    execproto.SearchParams
		code execproto.Code
	}{
		{"defaults", q, 0},
		{"empty query", execproto.SearchParams{}, execproto.ErrInvalidArgument},
		{"query at the cap", execproto.SearchParams{Query: strings.Repeat("q", execproto.MaxSearchQueryBytes)}, 0},
		{"query over the cap", execproto.SearchParams{Query: strings.Repeat("q", execproto.MaxSearchQueryBytes+1)}, execproto.ErrInvalidArgument},
		{"matches at the cap", with(func(p *execproto.SearchParams) { p.MaxMatches = execproto.MaxSearchMatches }), 0},
		{"matches over the cap", with(func(p *execproto.SearchParams) { p.MaxMatches = execproto.MaxSearchMatches + 1 }), execproto.ErrInvalidArgument},
		{"negative matches", with(func(p *execproto.SearchParams) { p.MaxMatches = -1 }), execproto.ErrInvalidArgument},
		{"context at the cap", with(func(p *execproto.SearchParams) { p.ContextLines = execproto.MaxSearchContextLines }), 0},
		{"context over the cap", with(func(p *execproto.SearchParams) { p.ContextLines = execproto.MaxSearchContextLines + 1 }), execproto.ErrInvalidArgument},
		{"negative context", with(func(p *execproto.SearchParams) { p.ContextLines = -1 }), execproto.ErrInvalidArgument},
		{"file bytes at the cap", with(func(p *execproto.SearchParams) { p.MaxFileBytes = execproto.MaxSearchFileBytes }), 0},
		{"file bytes over the cap", with(func(p *execproto.SearchParams) { p.MaxFileBytes = execproto.MaxSearchFileBytes + 1 }), execproto.ErrInvalidArgument},
		{"negative file bytes", with(func(p *execproto.SearchParams) { p.MaxFileBytes = -1 }), execproto.ErrInvalidArgument},
		{"include at the cap", with(func(p *execproto.SearchParams) {
			p.Include = globs(execproto.MaxSearchGlobs, execproto.MaxSearchGlobBytes)
		}), 0},
		{"include over the count cap", with(func(p *execproto.SearchParams) { p.Include = globs(execproto.MaxSearchGlobs+1, 1) }), execproto.ErrInvalidArgument},
		{"include over the length cap", with(func(p *execproto.SearchParams) { p.Include = globs(1, execproto.MaxSearchGlobBytes+1) }), execproto.ErrInvalidArgument},
		{"exclude over the count cap", with(func(p *execproto.SearchParams) { p.Exclude = globs(execproto.MaxSearchGlobs+1, 1) }), execproto.ErrInvalidArgument},
		{"exclude over the length cap", with(func(p *execproto.SearchParams) { p.Exclude = globs(1, execproto.MaxSearchGlobBytes+1) }), execproto.ErrInvalidArgument},
		{"malformed glob", with(func(p *execproto.SearchParams) { p.Include = []string{"["} }), execproto.ErrInvalidArgument},
		{"empty glob", with(func(p *execproto.SearchParams) { p.Exclude = []string{""} }), execproto.ErrInvalidArgument},
		{"malformed RE2", execproto.SearchParams{Query: "(", Regex: true}, execproto.ErrInvalidArgument},
		{"Perl backreference is not RE2", execproto.SearchParams{Query: `(a)\1`, Regex: true}, execproto.ErrInvalidArgument},
		{"literal metacharacters are fine", execproto.SearchParams{Query: "("}, 0},
	}
	for _, c := range cases {
		_, err := searchArgs(c.p)
		if codeOf(err) != c.code {
			t.Errorf("%s: err %v, want code %d", c.name, err, c.code)
		}
	}

	s, err := searchArgs(q)
	if err != nil || s.maxMatches != execproto.MaxSearchMatches || s.context != 0 || s.maxFileBytes != execproto.MaxSearchFileBytes {
		t.Errorf("defaults: %+v, %v", s, err)
	}
	// A literal query is literal, and case_sensitive defaults to true.
	s, _ = searchArgs(execproto.SearchParams{Query: "a.b"})
	if s.re.MatchString("axb") || !s.re.MatchString("a.b") || s.re.MatchString("A.B") {
		t.Error("literal query is not matched literally and case-sensitively")
	}
	s, _ = searchArgs(execproto.SearchParams{Query: "a.b", CaseSensitive: boolp(false)})
	if !s.re.MatchString("A.B") || s.re.MatchString("AxB") {
		t.Error("case-insensitive literal query")
	}
	s, _ = searchArgs(execproto.SearchParams{Query: "a.b", Regex: true})
	if !s.re.MatchString("axb") {
		t.Error("regex query is not a regex")
	}
}

func TestWriteArgs_Limits(t *testing.T) {
	big := strings.Repeat("x", execproto.MaxWriteBytes)
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		name   string
		p      execproto.WriteParams
		code   execproto.Code
		create bool
		size   int
	}{
		{"defaults overwrite", execproto.WriteParams{Path: "f", Content: "hi"}, 0, false, 2},
		{"create", execproto.WriteParams{Path: "f", Content: "hi", Mode: execproto.WriteCreate}, 0, true, 2},
		{"unknown mode", execproto.WriteParams{Path: "f", Content: "hi", Mode: "append"}, execproto.ErrInvalidArgument, false, 0},
		{"unknown encoding", execproto.WriteParams{Path: "f", Content: "hi", Encoding: "latin1"}, execproto.ErrInvalidArgument, false, 0},
		{"content at the cap", execproto.WriteParams{Path: "f", Content: big}, 0, false, execproto.MaxWriteBytes},
		{"content over the cap", execproto.WriteParams{Path: "f", Content: big + "x"}, execproto.ErrTooLarge, false, 0},
		{"base64 decoded", execproto.WriteParams{Path: "f", Content: b64("\x00\x01"), Encoding: execproto.EncodingBase64}, 0, false, 2},
		{"base64 at the cap", execproto.WriteParams{Path: "f", Content: b64(big), Encoding: execproto.EncodingBase64}, 0, false, execproto.MaxWriteBytes},
		{"base64 over the cap", execproto.WriteParams{Path: "f", Content: b64(big + "x"), Encoding: execproto.EncodingBase64}, execproto.ErrTooLarge, false, 0},
		{"malformed base64", execproto.WriteParams{Path: "f", Content: "!!", Encoding: execproto.EncodingBase64}, execproto.ErrInvalidArgument, false, 0},
		// A15 §6.4: a placeholder the model saw must never be written back
		// in place of the secret it stands for.
		{"redaction marker", execproto.WriteParams{Path: "f", Content: "KEY=[REDACTED:openai_key]"}, execproto.ErrRedactionMarker, false, 0},
		{"redaction marker in base64", execproto.WriteParams{Path: "f", Content: b64("[REDACTED:x]"), Encoding: execproto.EncodingBase64}, execproto.ErrRedactionMarker, false, 0},
	}
	for _, c := range cases {
		got, err := writeArgs(c.p)
		if codeOf(err) != c.code {
			t.Errorf("%s: err %v, want code %d", c.name, err, c.code)
			continue
		}
		if err == nil && (got.create != c.create || len(got.content) != c.size) {
			t.Errorf("%s: create %v size %d, want %v %d", c.name, got.create, len(got.content), c.create, c.size)
		}
	}
}

func TestPatchArgs_Limits(t *testing.T) {
	cases := []struct {
		name  string
		p     execproto.PatchParams
		code  execproto.Code
		strip int
	}{
		{"default strip", execproto.PatchParams{Patch: "x"}, 0, execproto.DefaultPatchStrip},
		{"strip 0", execproto.PatchParams{Patch: "x", Strip: intp(0)}, 0, 0},
		{"strip at the cap", execproto.PatchParams{Patch: "x", Strip: intp(execproto.MaxPatchStrip)}, 0, execproto.MaxPatchStrip},
		{"strip over the cap", execproto.PatchParams{Patch: "x", Strip: intp(execproto.MaxPatchStrip + 1)}, execproto.ErrInvalidArgument, 0},
		{"negative strip", execproto.PatchParams{Patch: "x", Strip: intp(-1)}, execproto.ErrInvalidArgument, 0},
		{"empty patch", execproto.PatchParams{}, execproto.ErrInvalidArgument, 0},
		{"patch at the cap", execproto.PatchParams{Patch: strings.Repeat("x", execproto.MaxPatchBytes)}, 0, 1},
		{"patch over the cap", execproto.PatchParams{Patch: strings.Repeat("x", execproto.MaxPatchBytes+1)}, execproto.ErrTooLarge, 0},
	}
	for _, c := range cases {
		got, err := patchArgs(c.p)
		if codeOf(err) != c.code {
			t.Errorf("%s: err %v, want code %d", c.name, err, c.code)
			continue
		}
		if err == nil && got != c.strip {
			t.Errorf("%s: strip %d, want %d", c.name, got, c.strip)
		}
	}
}

// TestEncodeContent: read content goes out as UTF-8 text when it is text,
// and as base64 when it is not valid UTF-8, holds a NUL, or would escape to
// more JSON than fits the 8 MiB frame. A 2 MiB read of "<" escapes to
// 12 MiB (six bytes each) and would be replaced by too_large at the frame
// (A06 §4.1) although the read itself was within its cap.
func TestEncodeContent(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want execproto.Encoding
	}{
		{"text", []byte("hello\n\tworld\r\n"), execproto.EncodingUTF8},
		{"empty", nil, execproto.EncodingUTF8},
		{"invalid UTF-8", []byte{0xff, 'a'}, execproto.EncodingBase64},
		{"NUL", []byte("a\x00b"), execproto.EncodingBase64},
		{"2 MiB of letters", []byte(strings.Repeat("a", execproto.MaxReadBytes)), execproto.EncodingUTF8},
		{"2 MiB of <", []byte(strings.Repeat("<", execproto.MaxReadBytes)), execproto.EncodingBase64},
		{"2 MiB of control bytes", []byte(strings.Repeat("\x01", execproto.MaxReadBytes)), execproto.EncodingBase64},
	}
	for _, c := range cases {
		s, enc := encodeContent(c.in)
		if enc != c.want {
			t.Errorf("%s: encoding %s, want %s", c.name, enc, c.want)
			continue
		}
		var back []byte
		if enc == execproto.EncodingBase64 {
			var err error
			if back, err = base64.StdEncoding.DecodeString(s); err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
		} else {
			back = []byte(s)
		}
		if string(back) != string(c.in) {
			t.Errorf("%s: content does not round-trip", c.name)
		}
		j, _ := json.Marshal(s)
		if len(j) > execproto.MaxFrameBytes-(1<<20) {
			t.Errorf("%s: %d bytes of JSON leave no room in the frame", c.name, len(j))
		}
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, rel string
		want     bool
	}{
		{"*.go", "a/b/c.go", true}, // no slash: the base name
		{"*.go", "c.go", true},
		{"*.go", "c.ts", false},
		{"src/**/*.ts", "src/a/b.ts", true},
		{"src/**/*.ts", "src/b.ts", true},
		{"src/**/*.ts", "lib/b.ts", false},
		{"**", "x/y/z", true},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/b/d/c", false},
		{"a/**/b/**/c", "a/x/b/y/z/c", true},
		{"a/**/b/**/c", "a/x/y/z/c", false},
		{"vendor/**", "vendor/x/y.go", true},
		{"vendor/**", "src/vendor.go", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.rel); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pat, c.rel, got, c.want)
		}
	}
	// Many "**" against a deep path stay polynomial: this must return.
	if globMatch(strings.Repeat("**/", 60)+"x", strings.Repeat("a/", 200)+"y") {
		t.Error("no x in the path")
	}
}

func TestCutText(t *testing.T) {
	long := strings.Repeat("é", 600) // 1200 bytes
	got := cutText(long)
	if len(got) > execproto.MaxSearchTextBytes || !utf8.ValidString(got) || !strings.HasPrefix(long, got) {
		t.Errorf("cutText: %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if cutText("short") != "short" {
		t.Error("short text changed")
	}
}

// TestJSONLen: encodeContent's frame budget is only as good as jsonLen's
// prediction, so it must equal what json.Marshal produces, for every
// escape class (quotes, backslash, short escapes, \u00XX control and HTML
// bytes, the two line separators, multi-byte runes).
func TestJSONLen(t *testing.T) {
	ls, ps := string(lineSeparator), string(paragraphSeparator)
	for _, s := range []string{"", "plain", `"q"\`, "\n\r\t", "\x01\x1f<>&", ls + ps, "é漢🙂", "mixed\"" + ls + "<x>\x00"} {
		j, _ := json.Marshal(s)
		if got := jsonLen([]byte(s)); got != len(j)-2 {
			t.Errorf("jsonLen(%q) = %d, json.Marshal gives %d", s, got, len(j)-2)
		}
	}
}
