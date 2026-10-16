package termsafe

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestEscapeJSON guards the --json half of CWE-150 (review of M2-3, confirmed
// by the reviewer): encoding/json escapes C0 controls but writes DEL, the C1
// controls U+0080–U+009F and the bidi characters raw, so a doctor Detail
// carrying an engine's "500 x<OSC>0;pwn<ST><CSI>2J" status line reached the
// terminal intact. EscapeJSON must rewrite them as \uXXXX and leave the JSON
// meaning unchanged.
func TestEscapeJSON(t *testing.T) {
	esc := func(hex string) string { return string(rune(0x5c)) + "u" + hex }
	cases := []struct{ name, in, want string }{
		{"plain JSON untouched", "{\n  \"a\": \"caf\xc3\xa9 \xe2\x82\xac\"\n}", "{\n  \"a\": \"caf\xc3\xa9 \xe2\x82\xac\"\n}"},
		{"C1 CSI", "\"x" + string(rune(0x9b)) + "2J\"", "\"x" + esc("009b") + "2J\""},
		{"C1 OSC and ST", "\"" + string(rune(0x9d)) + "0;t" + string(rune(0x9c)) + "\"", "\"" + esc("009d") + "0;t" + esc("009c") + "\""},
		{"NEL", "\"" + string(rune(0x85)) + "\"", "\"" + esc("0085") + "\""},
		{"DEL", "\"a\x7fb\"", "\"a" + esc("007f") + "b\""},
		{"raw ESC", "\"a\x1b[2Jb\"", "\"a" + esc("001b") + "[2Jb\""},
		{"bidi override and isolate", "\"" + string(rune(0x202e)) + string(rune(0x2066)) + "\"", "\"" + esc("202e") + esc("2066") + "\""},
		{"line separator", "\"" + string(rune(0x2028)) + "\"", "\"" + esc("2028") + "\""},
		{"invalid UTF-8 byte", "\"a\x9bb\"", "\"a" + esc("fffd") + "b\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EscapeJSON([]byte(c.in))
			if string(got) != c.want {
				t.Errorf("EscapeJSON(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	t.Run("meaning unchanged", func(t *testing.T) {
		s := "x" + string(rune(0x9b)) + "2J" + string(rune(0x202e)) + "\x7f caf\xc3\xa9"
		in, _ := json.Marshal(map[string]string{"detail": s})
		out := EscapeJSON(in)
		if bytes.Contains(out, []byte{0xc2, 0x9b}) || bytes.Contains(out, []byte{0xe2, 0x80, 0xae}) || bytes.Contains(out, []byte{0x7f}) {
			t.Fatalf("raw control left in %q", out)
		}
		var back map[string]string
		if err := json.Unmarshal(out, &back); err != nil || back["detail"] != s {
			t.Errorf("round trip: %q, %v", back["detail"], err)
		}
	})
}
