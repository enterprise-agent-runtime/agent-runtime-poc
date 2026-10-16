package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestRenderAuditVerify_SanitizesDetail guards CWE-150 in warden audit
// verify (noticed in M2-3, review item 4): a violation Detail quotes fields
// of the chain under test, and --file verifies an export from another
// machine, so a crafted export could put escape sequences or a forged
// "VERIFIED" line into the output through, say, its event type.
func TestRenderAuditVerify_SanitizesDetail(t *testing.T) {
	detail := "unknown event type x\x1b]0;pwn\x07\x1b[2J" + string(rune(0x9b)) + "1A\nVERIFIED"
	vs, _ := json.Marshal([]map[string]any{{"seq": 3, "kind": "unknown_event_type", "severity": "error", "detail": detail}})
	var b bytes.Buffer
	renderAuditVerify(&b, auditVerifyResult{OK: false, Events: 3, Violations: vs})
	out := b.String()
	for _, r := range out {
		if r != '\n' && (r < 0x20 || (r >= 0x7f && r <= 0x9f)) {
			t.Fatalf("output contains control character %U:\n%q", r, out)
		}
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 || lines[2] != "NOT VERIFIED" {
		t.Errorf("want summary, one violation, NOT VERIFIED; got %q", lines)
	}
	if !strings.Contains(out, "unknown event type x") {
		t.Errorf("violation text lost: %q", out)
	}
}
