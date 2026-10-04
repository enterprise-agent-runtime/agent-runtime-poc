package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestErrorCodes_MatchWRD05 pins the exact set of normalized codes: a new or
// renamed code silently breaks router fallback rules and event schemas.
func TestErrorCodes_MatchWRD05(t *testing.T) {
	want := []string{"rate_limited", "auth_failed", "context_too_long", "provider_unavailable", "content_filtered",
		"invalid_request", "tool_format_unsupported", "model_not_found", "timeout", "cancelled"}
	var got []string
	for _, c := range ErrorCodes {
		got = append(got, string(c))
		if !c.Valid() {
			t.Errorf("%s not valid", c)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %v, want %v", got, want)
	}
	if ErrorCode("overloaded").Valid() {
		t.Error("unknown code reported valid")
	}
}

func TestErrorCode_RetryAndFallbackClasses(t *testing.T) {
	retry := map[ErrorCode]bool{ErrRateLimited: true, ErrProviderUnavailable: true, ErrTimeout: true}
	fallback := map[ErrorCode]bool{ErrRateLimited: true, ErrProviderUnavailable: true, ErrTimeout: true, ErrModelNotFound: true}
	for _, c := range ErrorCodes {
		if c.DefaultRetryable() != retry[c] {
			t.Errorf("%s retryable = %v", c, c.DefaultRetryable())
		}
		if c.Fallback() != fallback[c] {
			t.Errorf("%s fallback = %v", c, c.Fallback())
		}
	}
}

func TestError_IsMatchesCodeThroughWrapping(t *testing.T) {
	err := fmt.Errorf("call anthropic: %w", NewError(ErrRateLimited, "slow down"))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatal("errors.Is(err, ErrRateLimited) = false")
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatal("errors.Is(err, ErrTimeout) = true")
	}
	var me *Error
	if !errors.As(err, &me) || !me.Retryable {
		t.Fatalf("errors.As = %v, retryable %v", me, me != nil && me.Retryable)
	}
}

func TestError_JSONCarriesRetryAfterMS(t *testing.T) {
	e := &Error{Code: ErrRateLimited, Message: "m", Retryable: true, RetryAfter: 1500 * time.Millisecond}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"retry_after_ms":1500`) {
		t.Fatalf("json = %s", b)
	}
	var back Error
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.RetryAfter != e.RetryAfter || back.Code != e.Code {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestModelRequest_JSONRoundTrip(t *testing.T) {
	temp := 0.2
	req := ModelRequest{
		ModelID:    "anthropic/claude-sonnet",
		Messages:   []Message{SystemText("sys"), UserText("hi"), {Role: RoleAssistant, Content: []ContentBlock{ToolUseBlock("t1", "fs__read", json.RawMessage(`{"path":"a"}`))}}, {Role: RoleTool, Content: []ContentBlock{ToolResultBlock("t1", "data", false)}}},
		Tools:      []ToolDefinition{{Name: "fs__read", Description: "Read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &ToolChoice{Type: ToolChoiceAuto}, Generation: &Generation{MaxOutputTokens: 100, Temperature: &temp},
		Trace: Trace{SessionID: "ses_1", Step: 3},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back ModelRequest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(back)
	if string(b) != string(b2) {
		t.Fatalf("round trip differs:\n%s\n%s", b, b2)
	}
}

func usage() *Usage { return &Usage{InputTokens: 1, OutputTokens: 1} }

func TestValidateSequence(t *testing.T) {
	start := StreamEvent{Type: EvMessageStart, Model: "m", Provider: "p"}
	ok := []StreamEvent{start,
		{Type: EvTextDelta, Text: "a"},
		{Type: EvToolUseStart, Index: 1, ToolUseID: "x", ToolName: "n"},
		{Type: EvToolUseDelta, Index: 1, PartialJSON: "{}"},
		{Type: EvToolUseEnd, Index: 1},
		{Type: EvUsage, Usage: usage()},
		{Type: EvMessageEnd, StopReason: StopToolUse}}
	cases := []struct {
		name string
		evs  []StreamEvent
		bad  bool
	}{
		{"complete", ok, false},
		{"lone error", []StreamEvent{{Type: EvError, Err: NewError(ErrTimeout, "")}}, false},
		{"error after start", []StreamEvent{start, {Type: EvError, Err: NewError(ErrTimeout, "")}}, false},
		{"empty", nil, true},
		{"no start", ok[1:], true},
		{"no terminal", ok[:5], true},
		{"usage not before end", []StreamEvent{start, {Type: EvUsage, Usage: usage()}, {Type: EvTextDelta}, {Type: EvMessageEnd, StopReason: StopEndTurn}}, true},
		{"delta before start", []StreamEvent{start, {Type: EvToolUseDelta, Index: 0}, {Type: EvUsage, Usage: usage()}, {Type: EvMessageEnd, StopReason: StopEndTurn}}, true},
		{"open tool at end", []StreamEvent{start, {Type: EvToolUseStart, ToolUseID: "x", ToolName: "n"}, {Type: EvUsage, Usage: usage()}, {Type: EvMessageEnd, StopReason: StopToolUse}}, true},
		{"two terminals", []StreamEvent{start, {Type: EvUsage, Usage: usage()}, {Type: EvMessageEnd, StopReason: StopEndTurn}, {Type: EvError, Err: NewError(ErrTimeout, "")}}, true},
		{"error without code", []StreamEvent{start, {Type: EvError, Err: &Error{Code: "nope"}}}, true},
		{"end without stop reason", []StreamEvent{start, {Type: EvUsage, Usage: usage()}, {Type: EvMessageEnd}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateSequence(c.evs)
			if (err != nil) != c.bad {
				t.Fatalf("err = %v, want bad=%v", err, c.bad)
			}
		})
	}
}

func TestAssemble_TextAndToolUses(t *testing.T) {
	evs := []StreamEvent{
		{Type: EvMessageStart, Model: "m", Provider: "p", ProviderReqID: "r"},
		{Type: EvTextDelta, Index: 0, Text: "Let me "},
		{Type: EvTextDelta, Index: 0, Text: "look."},
		{Type: EvToolUseStart, Index: 1, ToolUseID: "c1", ToolName: "fs__read"},
		{Type: EvToolUseDelta, Index: 1, PartialJSON: `{"pa`},
		{Type: EvToolUseDelta, Index: 1, PartialJSON: `th": "a.ts"}`},
		{Type: EvToolUseEnd, Index: 1},
		{Type: EvToolUseStart, Index: 2, ToolUseID: "c2", ToolName: "fs__list"},
		{Type: EvToolUseDelta, Index: 2, PartialJSON: `{"bad`},
		{Type: EvToolUseEnd, Index: 2},
		{Type: EvUsage, Usage: &Usage{InputTokens: 10, OutputTokens: 5}},
		{Type: EvMessageEnd, StopReason: StopToolUse},
	}
	r, err := Assemble(evs)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text() != "Let me look." || r.StopReason != StopToolUse || r.Usage.InputTokens != 10 || r.RequestID != "r" {
		t.Fatalf("response = %+v", r)
	}
	uses := r.ToolUses()
	if len(uses) != 2 {
		t.Fatalf("tool uses = %+v", uses)
	}
	if string(uses[0].Input) != `{"path":"a.ts"}` || uses[0].RawInput != "" {
		t.Errorf("first = %+v", uses[0])
	}
	if uses[1].Input != nil || uses[1].RawInput != `{"bad` {
		t.Errorf("second = %+v, want RawInput and nil Input", uses[1])
	}
}

func TestAssemble_ReturnsTerminalError(t *testing.T) {
	_, err := Assemble([]StreamEvent{{Type: EvMessageStart}, {Type: EvError, Err: NewError(ErrProviderUnavailable, "x")}})
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseToolInput(t *testing.T) {
	cases := []struct{ in, wantIn, wantRaw string }{
		{"", "{}", ""},
		{"  ", "{}", ""},
		{`{ "a" : 1 }`, `{"a":1}`, ""},
		{`[1,2]`, "", `[1,2]`},
		{`"str"`, "", `"str"`},
		{`{"a":`, "", `{"a":`},
		{`null`, "", `null`},
		{`{"b":1,"a":{"d":2,"c":1}}`, `{"a":{"c":1,"d":2},"b":1}`, ""},                         // keys sorted at every level
		{`{"n":1.50,"big":12345678901234567890}`, `{"big":12345678901234567890,"n":1.50}`, ""}, // numbers verbatim
		{`{"html":"<a>&</a>"}`, `{"html":"<a>&</a>"}`, ""},                                     // no HTML escaping
		{`{"a":1} {"b":2}`, "", `{"a":1} {"b":2}`},                                             // trailing value is not an object
	}
	for _, c := range cases {
		in, raw := ParseToolInput(c.in)
		if string(in) != c.wantIn || raw != c.wantRaw {
			t.Errorf("ParseToolInput(%q) = %q, %q; want %q, %q", c.in, in, raw, c.wantIn, c.wantRaw)
		}
	}
}

func intp(i int) *int { return &i }

// TestEffective_ProbeOnlyLowers guards A11 §9.3: a probe can never raise a
// declared capability, so a misbehaving endpoint cannot talk its way into
// native tool calling or a larger context than the catalog allows.
func TestEffective_ProbeOnlyLowers(t *testing.T) {
	d := DeclaredCapabilities{ToolCalling: ToolCallingEmulated, StructuredOutput: false, Streaming: true, MaxContext: 32768}
	p := &ModelProbe{ToolCalling: ToolCallingNative, StructuredOutput: true, Streaming: true, MaxContextMeta: intp(131072), MaxContextEffective: intp(8192)}
	c := Effective(d, p)
	if c.ToolCalling != ToolCallingEmulated || c.StructuredOutput || c.MaxContext != 8192 {
		t.Fatalf("effective = %+v", c)
	}
	if c2 := Effective(DeclaredCapabilities{ToolCalling: ToolCallingNative, MaxContext: 100}, &ModelProbe{ToolCalling: ToolCallingNone}); c2.ToolCalling != ToolCallingNone {
		t.Fatalf("probe none did not lower native: %+v", c2)
	}
	if c3 := Effective(DeclaredCapabilities{}, &ModelProbe{ToolCalling: ToolCallingNative, Quirks: map[string]any{"tool_choice_named": false}}); c3.ToolCalling != ToolCallingNative || c3.ToolChoiceNamed {
		t.Fatalf("undeclared field must take probed value: %+v", c3)
	}
	if c4 := Effective(DeclaredCapabilities{ToolCalling: ToolCallingNative}, nil); c4.ToolCalling != ToolCallingNative || !c4.ToolChoiceNamed {
		t.Fatalf("nil probe: %+v", c4)
	}
}

func TestCredential_WipeAndNeverPrints(t *testing.T) {
	c := &Credential{Header: "x-api-key", Value: []byte("sk-ant-secret")}
	if s := fmt.Sprintf("%v %+v %#v %s", c, c, c, c); strings.Contains(s, "sk-ant") {
		t.Fatalf("credential printed: %s", s)
	}
	v := c.Value
	c.Wipe()
	if c.Value != nil || strings.Contains(string(v), "sk") {
		t.Fatalf("not wiped: %q", v)
	}
	(*Credential)(nil).Wipe()
}

func TestParseEmulatedCall(t *testing.T) {
	cases := []struct {
		name, text string
		stop       StopReason
		wantName   string
		wantIn     string
		wantErr    error
		wantPrefix string
	}{
		{"canonical", "Adding.\n<warden_tool_call>\n{\"name\": \"probe_add\", \"arguments\": {\"a\": 2, \"b\": 3}}\n</warden_tool_call>", StopEndTurn, "probe_add", `{"a":2,"b":3}`, nil, "Adding.\n"},
		{"alias and fence", "<tool_call>\n```json\n{\"name\":\"x\",\"parameters\":{\"k\":1}}\n```\n</tool_call>", StopEndTurn, "x", `{"k":1}`, nil, ""},
		{"arguments as string", `<warden_tool_call>{"name":"x","arguments":"{\"k\":1}"}</warden_tool_call>`, StopEndTurn, "x", `{"k":1}`, nil, ""},
		{"no close, end_turn", `<warden_tool_call>{"name":"x","arguments":{}}`, StopEndTurn, "x", `{}`, nil, ""},
		{"no close, max_tokens", `<warden_tool_call>{"name":"x","arguments":{}`, StopMaxTokens, "", "", ErrMalformedEmulatedCall, ""},
		{"no block", "just text", StopEndTurn, "", "", ErrNoEmulatedCall, "just text"},
		{"two values", `<warden_tool_call>{"name":"x","arguments":{}} {}</warden_tool_call>`, StopEndTurn, "", "", ErrMalformedEmulatedCall, ""},
		{"missing name", `<warden_tool_call>{"arguments":{}}</warden_tool_call>`, StopEndTurn, "", "", ErrMalformedEmulatedCall, ""},
		{"array arguments", `<warden_tool_call>{"name":"x","arguments":[1]}</warden_tool_call>`, StopEndTurn, "", "", ErrMalformedEmulatedCall, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, prefix, err := ParseEmulatedCall(c.text, c.stop)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if c.wantPrefix != "" && prefix != c.wantPrefix {
				t.Errorf("prefix = %q", prefix)
			}
			if c.wantErr != nil {
				return
			}
			if u.Name != c.wantName || string(u.Input) != c.wantIn {
				t.Fatalf("call = %+v", u)
			}
		})
	}
}

func TestRenderEmulatedTools_OneLinePerTool(t *testing.T) {
	s := RenderEmulatedTools([]ToolDefinition{
		{Name: "probe_add", Description: "Add.", InputSchema: json.RawMessage("{\n \"type\": \"object\"\n}")},
		{Name: "empty", Description: "No schema."},
	})
	for _, want := range []string{
		"<warden_tool_call>",
		`{"name":"probe_add","description":"Add.","parameters":{"type":"object"}}` + "\n",
		`{"name":"empty","description":"No schema.","parameters":{"type":"object"}}` + "\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestMinToolCalling(t *testing.T) {
	cases := [][3]string{{"native", "emulated", "emulated"}, {"emulated", "native", "emulated"}, {"none", "native", "none"}, {"", "native", "none"}, {"native", "native", "native"}}
	for _, c := range cases {
		if got := MinToolCalling(c[0], c[1]); got != c[2] {
			t.Errorf("Min(%q,%q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestSliceStreamAndCollect(t *testing.T) {
	s := &SliceStream{Events: []StreamEvent{{Type: EvMessageStart}, {Type: EvUsage, Usage: usage()}, {Type: EvMessageEnd, StopReason: StopEndTurn}}}
	evs, err := Collect(s)
	if err != nil || len(evs) != 3 || ValidateSequence(evs) != nil {
		t.Fatalf("collect = %v, %v", evs, err)
	}
}
