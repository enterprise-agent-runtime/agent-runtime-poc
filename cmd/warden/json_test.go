package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

// hostile is a Detail as a process squatting on the Docker endpoint can make
// it: C1 CSI "2J" (clear screen), C1 OSC "0;pwn" ended by ST (retitle), and a
// bidi override; none of which encoding/json escapes.
var hostile = "engine answered 500 x" + string(rune(0x9d)) + "0;pwn" + string(rune(0x9c)) + string(rune(0x9b)) + "2J" + string(rune(0x202e)) + "end"

// rawTerminalControls are the UTF-8 forms of the runes above and DEL.
var rawTerminalControls = [][]byte{{0xc2, 0x9b}, {0xc2, 0x9c}, {0xc2, 0x9d}, {0xe2, 0x80, 0xae}, {0x7f}}

// TestDoJSON_EscapesC1 guards the --json path of CWE-150, confirmed by the
// review of M2-3: warden doctor --json printed a Detail carrying C1 and bidi
// controls raw, because encoding/json does not escape them. The output must
// hold none of them and still decode to the original string.
func TestDoJSON_EscapesC1(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"checks": []map[string]string{{"id": "sandbox.l2", "detail": hostile}}})
	var b bytes.Buffer
	printJSON(&b, raw)
	for _, c := range rawTerminalControls {
		if bytes.Contains(b.Bytes(), c) {
			t.Fatalf("output contains raw % x:\n%q", c, b.String())
		}
	}
	var back struct {
		Checks []struct {
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(b.Bytes(), &back); err != nil || len(back.Checks) != 1 || back.Checks[0].Detail != hostile {
		t.Errorf("does not decode to the original: %+v, %v", back, err)
	}
}

// TestRenderEvents_EscapesPayload: warden events prints each payload as JSON
// text, with the same C1/bidi gap as --json (M2-3 review).
func TestRenderEvents_EscapesPayload(t *testing.T) {
	ev, _ := json.Marshal(map[string]any{"seq": 7, "ts": "2026-10-16T00:00:00Z", "type": "tool.exec.end", "payload": map[string]string{"stderr": hostile}})
	var b bytes.Buffer
	renderEvents(&b, []json.RawMessage{ev})
	for _, c := range rawTerminalControls {
		if bytes.Contains(b.Bytes(), c) {
			t.Fatalf("output contains raw % x:\n%q", c, b.String())
		}
	}
	if !bytes.Contains(b.Bytes(), []byte("tool.exec.end")) || !bytes.Contains(b.Bytes(), []byte("0;pwn")) {
		t.Errorf("event line lost its content: %q", b.String())
	}
}
