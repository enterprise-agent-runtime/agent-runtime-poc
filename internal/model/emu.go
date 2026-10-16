package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Emulated tool calling (design A10 §6.2–§6.3, protocol "warden-emu@1").
// It lives here, not in internal/agentloop, because the provider probe
// (A11 §9.2 step P2e) needs the same renderer and parser and providers may
// import only internal/model (CLAUDE.md §6; docs/DECISIONS-poc.md D-007).

// EmulatedProtocol is the protocol identifier.
const EmulatedProtocol = "warden-emu@1"

const emuHeader = `## Tools

You can call tools. To call a tool, end your message with one block in exactly this format:

<warden_tool_call>
{"name": "TOOL_NAME", "arguments": {ARGUMENTS}}
</warden_tool_call>

Rules for tool calls:
- One block per message. Write the block last; write nothing after it.
- The block contains one JSON object with exactly two keys: "name" (a tool name from the list below) and "arguments" (an object that matches that tool's parameters).
- Use valid JSON: double quotes, no comments, no trailing commas, newlines inside strings written as \n.
- After your message the runtime runs the tool and replies with a message starting with <warden_tool_result>. Never write <warden_tool_result> yourself and never guess a tool's output.
- When the task is complete, call result__submit with your result as its arguments.

Available tools, one JSON object per line (name, description, parameters as JSON Schema):
`

// RenderEmulatedTools renders the tool protocol text appended to the system
// prompt for models without native tool calling.
func RenderEmulatedTools(tools []ToolDefinition) string {
	var sb strings.Builder
	sb.WriteString(emuHeader)
	for _, t := range tools {
		params := t.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object"}`)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, params); err != nil {
			compact.Reset()
			compact.WriteString(`{"type":"object"}`)
		}
		line, _ := json.Marshal(struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}{t.Name, t.Description, compact.Bytes()})
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

var (
	emuOpen  = regexp.MustCompile(`<(?:warden_)?tool_call\s*>`)
	emuClose = regexp.MustCompile(`</(?:warden_)?tool_call\s*>`)
	emuFence = regexp.MustCompile("^```[a-zA-Z]*\n?|\n?```$")
)

// ErrNoEmulatedCall means the text contains no tool-call block.
var ErrNoEmulatedCall = errors.New("no tool call block")

// ErrMalformedEmulatedCall means a block exists but cannot be decoded.
var ErrMalformedEmulatedCall = errors.New("malformed tool call block")

// ParseEmulatedCall extracts the first tool call from a model reply
// (design A10 §6.3). It returns the call (with a nil ID; the caller assigns
// one) and the text before the block. A block without a closing sentinel is
// malformed when generation stopped at max_tokens and taken to the end
// otherwise.
func ParseEmulatedCall(text string, stop StopReason) (*ToolUse, string, error) {
	t := strings.ReplaceAll(text, "\r\n", "\n")
	loc := emuOpen.FindStringIndex(t)
	if loc == nil {
		return nil, t, ErrNoEmulatedCall
	}
	prefix := t[:loc[0]]
	rest := t[loc[1]:]
	if c := emuClose.FindStringIndex(rest); c != nil {
		rest = rest[:c[0]]
	} else if stop == StopMaxTokens {
		return nil, prefix, ErrMalformedEmulatedCall
	}
	body := strings.TrimSpace(rest)
	body = strings.TrimSpace(emuFence.ReplaceAllString(body, ""))
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	var v map[string]json.RawMessage
	if err := dec.Decode(&v); err != nil || v == nil {
		return nil, prefix, ErrMalformedEmulatedCall
	}
	if dec.More() {
		return nil, prefix, ErrMalformedEmulatedCall
	}
	var name string
	if err := json.Unmarshal(v["name"], &name); err != nil || name == "" {
		return nil, prefix, ErrMalformedEmulatedCall
	}
	args, ok := v["arguments"]
	if !ok {
		args, ok = v["parameters"]
	}
	if !ok {
		return nil, prefix, ErrMalformedEmulatedCall
	}
	// A string that itself decodes to an object is accepted.
	var s string
	if json.Unmarshal(args, &s) == nil {
		args = json.RawMessage(s)
	}
	in, raw := ParseToolInput(string(args))
	if in == nil {
		return nil, prefix, ErrMalformedEmulatedCall
	}
	_ = raw
	return &ToolUse{Name: name, Input: in}, prefix, nil
}
