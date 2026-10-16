// Package termsafe makes text from outside the CLI process safe to print on
// a terminal (CWE-150, improper neutralization of escape sequences). The
// daemon's doctor checks carry strings it did not write itself: Docker
// engine fields and error text from whatever answers on the Docker endpoint
// (design A05 §8.1). JSON output is already safe, because encoding/json
// escapes control characters; the human renderer of cmd/warden is not.
//
// It is its own package because cmd/* may contain only wiring (CLAUDE.md
// §6), the function is not OS-specific (so not internal/platform) and not
// doctor-specific (any renderer of daemon-supplied text needs it), and
// putting it in internal/sandbox would link the Docker probes into every
// CLI command that prints a string.
package termsafe

import (
	"strings"
	"unicode/utf8"
)

// MaxDetail is the longest sanitized text, in bytes, before the marker.
const MaxDetail = 1024

// TruncatedMarker ends a text that was cut at MaxDetail.
const TruncatedMarker = " [truncated]"

// SanitizeDetail returns s as one printable line of at most MaxDetail bytes
// (plus TruncatedMarker when cut):
//
//   - escape sequences (ECMA-48: CSI, OSC/DCS/SOS/PM/APC strings, two-byte
//     ESC sequences, and their 8-bit C1 forms) are removed whole, so no
//     parameter residue such as "[31m" is left behind;
//   - tab, CR, LF and U+2028/U+2029 become a space, so a detail cannot forge
//     a further line of output;
//   - every other C0 and C1 control, DEL and the bidirectional formatting
//     characters (Trojan Source, CVE-2021-42574) are dropped;
//   - invalid UTF-8 bytes become U+FFFD, because a raw 0x9b is CSI to a
//     terminal that is not in UTF-8 mode.
//
// Printable text, including non-ASCII letters, is unchanged.
func SanitizeDetail(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() <= MaxDetail; {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			b.WriteRune(utf8.RuneError)
			i++
		case r == 0x1b:
			i = skipEscape(s, i+1)
		case r == 0x9b: // C1 CSI
			i = skipCSI(s, i+n)
		case r == 0x90 || r == 0x98 || r == 0x9d || r == 0x9e || r == 0x9f: // C1 DCS, SOS, OSC, PM, APC
			i = skipString(s, i+n)
		case r == '\t' || r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029:
			b.WriteByte(' ')
			i += n
		case r < 0x20 || (r >= 0x7f && r <= 0x9f) || isBidi(r):
			i += n
		default:
			b.WriteString(s[i : i+n])
			i += n
		}
	}
	out := b.String()
	if len(out) <= MaxDetail {
		return out
	}
	cut := MaxDetail
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + TruncatedMarker
}

// skipEscape returns the index after the escape sequence whose ESC ended
// just before i.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return skipCSI(s, i+1)
	case ']', 'P', 'X', '^', '_':
		return skipString(s, i+1)
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f { // intermediate bytes
		i++
	}
	if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e { // final byte
		i++
	}
	return i
}

// skipCSI returns the index after the parameter, intermediate and final
// bytes of a control sequence starting at i. A byte outside the CSI ranges
// ends it unconsumed, and the main loop then judges that byte on its own.
func skipCSI(s string, i int) int {
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
		i++
	}
	if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipString returns the index after a control string starting at i: up to
// and including BEL, ESC \ or ST (U+009C), or to the end of s.
func skipString(s string, i int) int {
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == 0x07 || r == 0x9c:
			return i + n
		case r == 0x1b && i+1 < len(s) && s[i+1] == '\\':
			return i + 2
		}
		i += n
	}
	return i
}

// isBidi reports the Unicode bidirectional formatting characters.
func isBidi(r rune) bool {
	return r == 0x200e || r == 0x200f || r == 0x061c || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}
