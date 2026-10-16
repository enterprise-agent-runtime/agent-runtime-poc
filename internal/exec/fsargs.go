// Argument checks of the exec.fs methods (design A06 §4.2, §4.3 fs.json,
// §4.4; the package doc is in server.go). Decode refuses unknown,
// case-variant and duplicate keys but checks no ranges, so every bound of
// the schema is enforced here, before any path is resolved. The limits are
// execproto's constants (pinned to the schema by execproto.TestLimits).
//
// Codes: a value the schema forbids is invalid_argument, except a size over
// a cap A06 §4.4 names (read 2 MiB, write 4 MiB, the patch text), which is
// too_large. Zero means "absent, use the default" wherever the wire type
// is omitempty, because the two cannot be told apart after decoding; the
// schema's minimum of 1 makes an explicit 0 invalid anyway.

package exec

import (
	"encoding/base64"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"warden.dev/warden/internal/execproto"
)

// redactionMarker is the prefix of the A15 §6 placeholder. Content written
// by exec.fs.write or added by exec.fs.patch must not contain it (A15
// §6.4, A06 §4.4 redaction_marker): the model saw the placeholder in place
// of a secret, and writing it back would destroy the secret it stands for.
const redactionMarker = "[REDACTED:"

func invalid(detail string) error { return execproto.NewError(execproto.ErrInvalidArgument, detail) }

func tooLarge(detail string) error { return execproto.NewError(execproto.ErrTooLarge, detail) }

func redacted() error {
	return execproto.NewError(execproto.ErrRedactionMarker, "content contains a [REDACTED: placeholder; edit around that line with a patch instead")
}

type readSpec struct{ offset, length int64 }

// readArgs checks exec.fs.read's range. A length over 2 MiB is too_large
// (A06 §4.4 "read 2 MiB"); a larger file is read in several calls.
func readArgs(p execproto.ReadParams) (readSpec, error) {
	switch {
	case p.Offset < 0:
		return readSpec{}, invalid("offset must not be negative")
	case p.Length < 0:
		return readSpec{}, invalid("length must be at least 1")
	case p.Length > execproto.MaxReadBytes:
		return readSpec{}, tooLarge("length over the 2 MiB read cap")
	}
	n := p.Length
	if n == 0 {
		n = execproto.MaxReadBytes
	}
	return readSpec{offset: p.Offset, length: n}, nil
}

type listSpec struct{ depth, max int }

func listArgs(p execproto.ListParams) (listSpec, error) {
	s := listSpec{depth: p.Depth, max: p.MaxEntries}
	if s.depth == 0 {
		s.depth = execproto.DefaultListDepth
	}
	if s.max == 0 {
		s.max = execproto.DefaultListEntries
	}
	if s.depth < 1 || s.depth > execproto.MaxListDepth {
		return listSpec{}, invalid("depth must be 1 to 4")
	}
	if s.max < 1 || s.max > execproto.MaxListEntries {
		return listSpec{}, invalid("max_entries must be 1 to 2000")
	}
	return s, nil
}

type searchSpec struct {
	re               *regexp.Regexp
	include, exclude []string
	maxMatches       int
	context          int
	maxFileBytes     int64
}

// searchArgs checks exec.fs.search's bounds and compiles the query. A
// literal query is quoted, so it is matched literally by the same RE2
// engine. RE2 runs in time linear in the input, which is what keeps a
// model-written pattern from stalling the executor: there is no
// backtracking, and a Perl-only construct such as a backreference does not
// compile.
func searchArgs(p execproto.SearchParams) (searchSpec, error) {
	if p.Query == "" || len(p.Query) > execproto.MaxSearchQueryBytes {
		return searchSpec{}, invalid("query must be 1 to 1024 bytes")
	}
	s := searchSpec{maxMatches: p.MaxMatches, context: p.ContextLines, maxFileBytes: p.MaxFileBytes}
	if s.maxMatches == 0 {
		s.maxMatches = execproto.MaxSearchMatches
	}
	if s.maxFileBytes == 0 {
		s.maxFileBytes = execproto.MaxSearchFileBytes
	}
	switch {
	case s.maxMatches < 1 || s.maxMatches > execproto.MaxSearchMatches:
		return searchSpec{}, invalid("max_matches must be 1 to 200")
	case s.context < 0 || s.context > execproto.MaxSearchContextLines:
		return searchSpec{}, invalid("context_lines must be 0 to 3")
	case s.maxFileBytes < 1 || s.maxFileBytes > execproto.MaxSearchFileBytes:
		return searchSpec{}, invalid("max_file_bytes must be 1 to 2 MiB")
	}
	var err error
	if s.include, err = globArgs(p.Include); err != nil {
		return searchSpec{}, err
	}
	if s.exclude, err = globArgs(p.Exclude); err != nil {
		return searchSpec{}, err
	}
	q := p.Query
	if !p.Regex {
		q = regexp.QuoteMeta(q)
	}
	if p.CaseSensitive != nil && !*p.CaseSensitive {
		q = "(?i)" + q
	}
	if s.re, err = regexp.Compile(q); err != nil {
		return searchSpec{}, invalid("query is not valid RE2")
	}
	return s, nil
}

func globArgs(gs []string) ([]string, error) {
	if len(gs) > execproto.MaxSearchGlobs {
		return nil, invalid("at most 32 include or exclude globs")
	}
	for _, g := range gs {
		if g == "" || len(g) > execproto.MaxSearchGlobBytes {
			return nil, invalid("a glob must be 1 to 256 bytes")
		}
		for _, seg := range strings.Split(g, "/") {
			if _, err := path.Match(seg, ""); err != nil {
				return nil, invalid("malformed glob")
			}
		}
	}
	return gs, nil
}

type writeSpec struct {
	content    []byte
	create     bool
	executable bool
}

// writeArgs checks exec.fs.write's mode and encoding and decodes the
// content, refusing more than 4 MiB (too_large) and the redaction marker.
func writeArgs(p execproto.WriteParams) (writeSpec, error) {
	s := writeSpec{executable: p.Executable}
	switch p.Mode {
	case "", execproto.WriteOverwrite:
	case execproto.WriteCreate:
		s.create = true
	default:
		return writeSpec{}, invalid("mode must be create or overwrite")
	}
	switch p.Encoding {
	case "", execproto.EncodingUTF8:
		s.content = []byte(p.Content)
	case execproto.EncodingBase64:
		// Refused before decoding: no allocation for content over the cap.
		if len(p.Content) > base64.StdEncoding.EncodedLen(execproto.MaxWriteBytes) {
			return writeSpec{}, tooLarge("content over the 4 MiB write cap")
		}
		b, err := base64.StdEncoding.Strict().DecodeString(p.Content)
		if err != nil {
			return writeSpec{}, invalid("content is not valid base64")
		}
		s.content = b
	default:
		return writeSpec{}, invalid("encoding must be utf-8 or base64")
	}
	if len(s.content) > execproto.MaxWriteBytes {
		return writeSpec{}, tooLarge("content over the 4 MiB write cap")
	}
	if strings.Contains(string(s.content), redactionMarker) {
		return writeSpec{}, redacted()
	}
	return s, nil
}

// patchArgs checks exec.fs.patch's size and strip.
func patchArgs(p execproto.PatchParams) (int, error) {
	switch {
	case p.Patch == "":
		return 0, invalid("patch must not be empty")
	case len(p.Patch) > execproto.MaxPatchBytes:
		return 0, tooLarge("patch over the 4 MiB cap")
	}
	if p.Strip == nil {
		return execproto.DefaultPatchStrip, nil
	}
	if *p.Strip < 0 || *p.Strip > execproto.MaxPatchStrip {
		return 0, invalid("strip must be 0 to 2")
	}
	return *p.Strip, nil
}

// maxTextJSON bounds the JSON-escaped size of read content sent as UTF-8
// text, leaving room in the 8 MiB frame (A06 §4.1) for the rest of the
// result. Base64 of the 2 MiB read cap is under 2.7 MiB, always below it.
const maxTextJSON = 6 << 20

// encodeContent picks the ReadResult encoding: UTF-8 text when the bytes
// are valid UTF-8 without NUL and their JSON escaping stays under
// maxTextJSON, base64 otherwise. json.Marshal escapes control bytes and
// <, > and & as \u00XX (six bytes for one), so 2 MiB of "<" would become a
// 12 MiB string that no frame can carry; base64 cannot grow that way.
func encodeContent(b []byte) (string, execproto.Encoding) {
	if utf8.Valid(b) && !strings.Contains(string(b), "\x00") && jsonLen(b) <= maxTextJSON {
		return string(b), execproto.EncodingUTF8
	}
	return base64.StdEncoding.EncodeToString(b), execproto.EncodingBase64
}

// U+2028 and U+2029, which json.Marshal escapes as six-byte sequences
// (built from numbers: the editing tools would turn an escape in the
// source into the invisible character itself).
const (
	lineSeparator      = rune(0x2028)
	paragraphSeparator = rune(0x2029)
)

// jsonLen is the length json.Marshal gives valid UTF-8 b as a string,
// without the quotes.
func jsonLen(b []byte) int {
	n := 0
	for i := 0; i < len(b); {
		c := b[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t':
				n += 2
			case c < 0x20 || c == '<' || c == '>' || c == '&':
				n += 6
			default:
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == lineSeparator || r == paragraphSeparator {
			n += 6
		} else {
			n += size
		}
		i += size
	}
	return n
}

// globMatch matches an include or exclude glob against a root-relative
// path. A glob without "/" matches the base name ("*.go" finds Go files at
// any depth); otherwise it matches the whole path segment by segment, where
// "**" is any number of segments (zero included) and every other segment
// is a path.Match pattern. The segments are matched with the classic
// single-backtrack wildcard algorithm, so the cost is at most the product
// of the two lengths, whatever the number of "**".
func globMatch(pat, rel string) bool {
	if !strings.Contains(pat, "/") {
		return segMatch(pat, path.Base(rel))
	}
	ps, ss := strings.Split(pat, "/"), strings.Split(rel, "/")
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(ss) {
		switch {
		case pi < len(ps) && ps[pi] == "**":
			star, mark = pi, si
			pi++
		case pi < len(ps) && segMatch(ps[pi], ss[si]):
			pi++
			si++
		case star >= 0:
			mark++
			pi, si = star+1, mark
		default:
			return false
		}
	}
	for pi < len(ps) && ps[pi] == "**" {
		pi++
	}
	return pi == len(ps)
}

func segMatch(pat, seg string) bool {
	ok, _ := path.Match(pat, seg)
	return ok
}

// cutText cuts a search line to MaxSearchTextBytes on a UTF-8 boundary
// (fs.json text and context maxLength).
func cutText(s string) string {
	if len(s) <= execproto.MaxSearchTextBytes {
		return s
	}
	cut := execproto.MaxSearchTextBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
