package termsafe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSanitizeDetail guards CWE-150 in warden doctor (security review of
// FX-2, task M2-3): check details carry text from whatever answers on the
// Docker endpoint, and the human renderer printed them raw. A process
// squatting on the socket could recolour the output, rewrite the terminal
// title, fake an "ok" line with a newline, or flood the screen.
func TestSanitizeDetail(t *testing.T) {
	big := strings.Repeat("A", 64<<10)
	cases := []struct {
		name, in, want string
	}{
		{"normal text untouched", "Docker engine 27.0.1 reachable (linux containers)", "Docker engine 27.0.1 reachable (linux containers)"},
		{"non-ASCII letters untouched", "Fehler: Größe überschritten; 名前; café", "Fehler: Größe überschritten; 名前; café"},
		{"CSI colour sequence removed whole", "\x1b[31mred\x1b[0m text", "red text"},
		{"OSC title sequence (BEL) removed whole", "27\x1b]0;x\x07.0", "27.0"},
		{"OSC hyperlink (ST) removed whole", "a\x1b]8;;http://evil\x1b\\b", "ab"},
		{"two-byte ESC sequence removed", "a\x1bcb", "ab"},
		{"lone ESC at end removed", "a\x1b", "a"},
		{"C1 CSI U+009B removed with its parameters", "a\u009b2Jb", "ab"},
		{"C1 OSC U+009D removed to ST", "a\u009d0;x\u009cb", "ab"},
		{"other C1 removed", "a\u0085b", "ab"},
		{"raw byte 0x9b (invalid UTF-8) not passed through", "a\x9b2Jb", "a" + string(utf8.RuneError) + "2Jb"},
		{"NUL removed", "a\x00b", "ab"},
		{"DEL and backspace removed", "ab\x08\x7fc", "abc"},
		{"newline, CR and tab become spaces", "a\nok  fake\rb\tc", "a ok  fake b c"},
		{"bidi override removed", "abc" + string(rune(0x202e)) + "dcb", "abcdcb"},
		// Review of M2-3: sequence shapes and terminators the first table did
		// not pin. Control strings end the way a terminal ends them (vt100.net
		// DEC parser): CAN and SUB abort, ESC and any C1 start the next
		// control, so text after them is judged again rather than swallowed
		// or let through.
		{"CSI with intermediate byte removed whole", "a\x1b[1 qb", "ab"},
		{"ESC ( B charset designation removed whole", "a\x1b(Bb", "ab"},
		{"raw 0x9c byte does not end a string", "a\x1b]0;x\x9cy\x07b", "ab"},
		{"escape after 1024 bytes removed, no marker", strings.Repeat("A", 1024) + "\x1b[0m", strings.Repeat("A", 1024)},
		{"CAN aborts an OSC string", "a\x1b]0;x\x18b", "ab"},
		{"SUB aborts an OSC string", "a\x1b]0;x\x1ab", "ab"},
		{"ESC ends an OSC string and starts the next sequence", "a\x1b]0;x\x1b[31mb", "ab"},
		{"C1 CSI ends an OSC string and starts the next sequence", "a\x1b]0;x" + string(rune(0x9b)) + "2Jb", "ab"},
		{"CAN aborts a CSI", "a\x1b[31\x18b", "ab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SanitizeDetail(c.in); got != c.want {
				t.Errorf("SanitizeDetail(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	t.Run("64 KiB string truncated with a marker", func(t *testing.T) {
		got := SanitizeDetail(big)
		if len(got) > MaxDetail+len(TruncatedMarker) {
			t.Fatalf("len = %d, want <= %d", len(got), MaxDetail+len(TruncatedMarker))
		}
		if !strings.HasSuffix(got, TruncatedMarker) || !strings.HasPrefix(got, "AAAA") {
			t.Errorf("got %q…%q", got[:8], got[len(got)-20:])
		}
	})
	t.Run("truncation keeps UTF-8 valid", func(t *testing.T) {
		got := SanitizeDetail(strings.Repeat("€", MaxDetail)) // 3 bytes: MaxDetail is not a rune boundary
		if !utf8.ValidString(got) || !strings.HasSuffix(got, TruncatedMarker) {
			t.Errorf("invalid or unmarked: %q", got[len(got)-20:])
		}
	})
	t.Run("exactly at the limit is not truncated", func(t *testing.T) {
		s := strings.Repeat("A", MaxDetail)
		if got := SanitizeDetail(s); got != s {
			t.Errorf("len %d, marker added at the limit", len(got))
		}
	})
}
