package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRenderDoctor_SanitizesDetailAndHint guards the wiring of CWE-150
// (M2-3): termsafe.SanitizeDetail is tested on its own, but the bug was the
// renderer printing Detail and FixHint raw, and only a test of the renderer
// catches a renderer that forgets to call it. The detail imitates a process
// on the Docker endpoint that clears the screen, retitles the terminal and
// forges a second "ok" line.
func TestRenderDoctor_SanitizesDetailAndHint(t *testing.T) {
	hint := "start \x1b]0;pwned\x07docker\u009b2J"
	r := doctorResult{Status: "warn", Checks: []doctorCheck{{
		ID: "sandbox.l2", Status: "warn", Title: "L2",
		Detail:  "engine \x1b[2J\x1b[Hdown\nok    sandbox.l2   L2: fine",
		FixHint: &hint,
	}}}
	var b bytes.Buffer
	renderDoctor(&b, r)
	out := b.String()
	for _, r := range strings.TrimSuffix(out, "\n") {
		if r == '\n' {
			continue // the renderer's own line breaks
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			t.Fatalf("output contains control character %U:\n%q", r, out)
		}
	}
	if got := strings.Count(out, "\n"); got != 4 { // check, fix, blank, overall
		t.Errorf("%d lines, want 4 (a forged line got through?):\n%s", got, out)
	}
	for _, want := range []string{"engine down ok", "fix: start docker"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
