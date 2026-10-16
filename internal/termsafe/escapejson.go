package termsafe

import (
	"fmt"
	"unicode/utf8"
)

// EscapeJSON returns the JSON text b with every rune a terminal acts on
// written as a \uXXXX escape: C0 controls other than tab, LF and CR, DEL,
// the C1 controls U+0080–U+009F, U+2028/U+2029 and the bidi formatting
// characters. encoding/json escapes C0 controls itself but writes the rest
// raw. In valid JSON these runes can only occur inside strings, where the
// escape means the same rune, so the result decodes to the same value.
// Invalid UTF-8 bytes become the escape of U+FFFD, which is what encoding/json decodes them
// to anyway. Tab, LF and CR stay: in valid JSON they are only whitespace
// between tokens (the indentation of MarshalIndent).
func EscapeJSON(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			out = fmt.Appendf(out, `\u%04x`, utf8.RuneError)
		case r == '\t' || r == '\n' || r == '\r':
			out = append(out, b[i])
		case r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028 || r == 0x2029 || isBidi(r):
			out = fmt.Appendf(out, `\u%04x`, r)
		default:
			out = append(out, b[i:i+n]...)
		}
		i += n
	}
	return out
}
