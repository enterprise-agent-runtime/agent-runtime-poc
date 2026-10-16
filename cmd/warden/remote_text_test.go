package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// The tests in this file guard CWE-150 in the CLI renderers that print text
// a remote party chose (M2-3 review, sweep of cmd/warden): a provider probe
// error is the model endpoint's message, model ids of local servers are
// discovered from the endpoint, and API error messages can carry either. A
// hostile endpoint could recolour or retitle the terminal, clear it, or
// forge a further line ("probe ok", "admitted").

// c1CSI is the 8-bit CSI, U+009B, which encoding/json passes through raw.
var c1CSI = string(rune(0x9b))

// assertPrintable fails when out holds a control character other than the
// renderer's own line breaks, or more than lines lines.
func assertPrintable(t *testing.T, out string, lines int) {
	t.Helper()
	for _, r := range out {
		if r != '\n' && (r < 0x20 || (r >= 0x7f && r <= 0x9f)) {
			t.Fatalf("output contains control character %U:\n%q", r, out)
		}
	}
	if got := strings.Count(out, "\n"); got != lines {
		t.Errorf("%d lines, want %d (a forged line got through?):\n%q", got, lines, out)
	}
}

func TestPrintTest_SanitizesProbeError(t *testing.T) {
	r := map[string]any{"ok": false,
		"error":  map[string]any{"code": "provider_unavailable", "message": "upstream said \x1b]0;pwn\x07" + c1CSI + "2J\nprobe ok (1 ms)"},
		"models": []any{map[string]any{"model_id": "llama\x1b[31m3\nfake", "tool_calling": "native"}},
	}
	var b bytes.Buffer
	printTest(&b, r)
	out := b.String()
	assertPrintable(t, out, 2)
	if !strings.Contains(out, "probe failed: provider_unavailable: upstream said") || !strings.Contains(out, "llama3 fake") {
		t.Errorf("content lost:\n%s", out)
	}
}

func TestRenderProviderList_SanitizesModels(t *testing.T) {
	providers := []map[string]any{{"provider_id": "ollama", "protocol": "openai-compatible", "tier": "T0", "auth_mode": "none", "status": "ok",
		"models": []any{"qwen\x1b[2J", "x\nollama2  fake"}}}
	var b bytes.Buffer
	renderProviderList(&b, providers, nil)
	out := b.String()
	assertPrintable(t, out, 1)
	if !strings.Contains(out, "qwen") {
		t.Errorf("content lost:\n%s", out)
	}
}

func TestRenderModels_SanitizesModelIDAndReason(t *testing.T) {
	models := []map[string]any{{"model_id": "m\x1b[2J\nfake  local T0 admitted", "kind": "local", "tier": "T0", "admissible": false,
		"reason": "tier T3 not admitted" + c1CSI + "1A"}}
	var b bytes.Buffer
	renderModels(&b, "", nil, models)
	out := b.String()
	assertPrintable(t, out, 1)
	if !strings.Contains(out, "NOT ADMITTED") || !strings.Contains(out, "tier T3 not admitted") {
		t.Errorf("content lost:\n%s", out)
	}
}

func TestDescribe_SanitizesErrorText(t *testing.T) {
	got := describe(errors.New("provider said \x1b]0;pwn\x07" + c1CSI + "2J\nwarden: ok"))
	assertPrintable(t, got, 0)
	if !strings.HasPrefix(got, "provider said") {
		t.Errorf("describe = %q", got)
	}
}

// TestRenderEvents_CutsOnRuneBoundary: the payload was cut at byte 160
// regardless of UTF-8, so a multi-byte character across the cut left a
// broken sequence on the terminal (M2-3 review).
func TestRenderEvents_CutsOnRuneBoundary(t *testing.T) {
	// {"m":" is 6 bytes; 153 A's put the 3-byte euro sign at bytes 159-161.
	ev, _ := json.Marshal(map[string]any{"seq": 1, "type": "x", "payload": map[string]string{"m": strings.Repeat("A", 153) + "\xe2\x82\xac" + strings.Repeat("B", 20)}})
	var b bytes.Buffer
	renderEvents(&b, []json.RawMessage{ev})
	out := b.String()
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8: %q", out[len(out)-12:])
	}
	if !strings.HasSuffix(out, strings.Repeat("A", 153)+"\xe2\x80\xa6\n") {
		t.Errorf("want cut before the euro sign, then an ellipsis: %q", out[len(out)-12:])
	}
}
