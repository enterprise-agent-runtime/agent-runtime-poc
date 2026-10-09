// Package model implements the canonical model API of WRD-05 §3–§5 and the
// Go shapes of design A02 §4.1 and A11 §2, §11.1: requests, content blocks,
// streamed events, normalized errors and the Provider contract every adapter
// implements. It is a stdlib-only leaf: internal/providers/* and
// internal/harness/* import it and nothing else from the daemon
// (CLAUDE.md §6).
package model

import (
	"encoding/json"
)

// CanonicalVersion is the version of the canonical types (WRD-05 §12).
const CanonicalVersion = "model.v1"

// Tier is a trust tier (WRD-06 §3): T0 local loopback, T1 company-hosted,
// T2 company cloud tenant, T3 vendor API, T4 subscription harness.
type Tier string

const (
	T0 Tier = "T0"
	T1 Tier = "T1"
	T2 Tier = "T2"
	T3 Tier = "T3"
	T4 Tier = "T4"
)

// Classification is a workspace data classification (WRD-06 §2; the PoC
// accepts three values, CF-02).
type Classification string

const (
	Public       Classification = "public"
	Internal     Classification = "internal"
	Confidential Classification = "confidential"
)

// Role is a message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Trust marks where a content block came from (design A02 §4.1, BI-4/INV-D):
// the manifest system prompt and the user request are trusted, every
// observation is untrusted and must carry Provenance.
type Trust string

const (
	Trusted   Trust = "trusted"
	Untrusted Trust = "untrusted"
)

// ModelRequest is the canonical request (WRD-05 §3). Tool names are already
// provider-safe (fs__read) when they reach an adapter (design core §8).
type ModelRequest struct {
	ModelID           string                     `json:"model_id"`
	Messages          []Message                  `json:"messages"`
	Tools             []ToolDefinition           `json:"tools,omitempty"`
	ToolChoice        *ToolChoice                `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                      `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    *ResponseFormat            `json:"response_format,omitempty"`
	Generation        *Generation                `json:"generation,omitempty"`
	Cache             *CacheHint                 `json:"cache,omitempty"`
	Budget            *Budget                    `json:"budget,omitempty"`
	Trace             Trace                      `json:"trace"`
	ProviderOptions   map[string]json.RawMessage `json:"provider_options,omitempty"`
}

// Message is one turn.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// BlockType is the type of a content block.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockImage      BlockType = "image"
	BlockDocument   BlockType = "document"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	BlockReasoning  BlockType = "reasoning"
)

// ContentBlock is one block of a message (WRD-05 §3). Exactly the field
// matching Type is set.
type ContentBlock struct {
	Type       BlockType   `json:"type"`
	Text       string      `json:"text,omitempty"`
	Media      *Media      `json:"media,omitempty"`
	ToolUse    *ToolUse    `json:"tool_use,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
	Reasoning  *Reasoning  `json:"reasoning,omitempty"`
	Trust      Trust       `json:"trust,omitempty"`
	Provenance *Provenance `json:"provenance,omitempty"`
}

// Media is the payload of an image or document block.
type Media struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data,omitempty"` // base64, or raw text for text/plain documents
	URI       string `json:"uri,omitempty"`  // images only, https only
	Title     string `json:"title,omitempty"`
}

// ToolUse is a tool call proposed by the model. Input is a JSON object;
// when the model's arguments did not parse as an object, Input is nil and
// RawInput holds the text (design A11 §6.3, never repaired).
type ToolUse struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input,omitempty"`
	RawInput string          `json:"raw_input,omitempty"`
}

// ToolResult answers a ToolUse.
type ToolResult struct {
	ToolUseID string         `json:"tool_use_id"`
	CallID    string         `json:"call_id,omitempty"`
	OK        bool           `json:"ok"`
	Content   []ContentBlock `json:"content"`
	Truncated bool           `json:"truncated,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

// Reasoning is an opaque provider reasoning block, replayed only to the
// provider that produced it (WRD-05 §3).
type Reasoning struct {
	Provider string `json:"provider"`
	Opaque   string `json:"opaque"`
}

// Provenance says where an untrusted block came from (BI-4).
type Provenance struct {
	Source     string `json:"source"` // tool | file | artifact | user
	Ref        string `json:"ref,omitempty"`
	SandboxID  string `json:"sandbox_id,omitempty"`
	CallID     string `json:"call_id,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
}

// ToolDefinition is a tool offered to the model.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolChoice constrains tool use: auto | none | required | tool (+Name).
type ToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// Tool choice types.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceTool     = "tool"
)

// ResponseFormat requests structured output: json_schema | text.
type ResponseFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict *bool           `json:"strict,omitempty"`
}

// Generation holds sampling parameters; zero values mean "unset".
type Generation struct {
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	Stop            []string `json:"stop,omitempty"`
	Seed            *int64   `json:"seed,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"` // low | medium | high
}

// CacheHint asks the adapter to use provider prompt caching: system_prefix | none.
type CacheHint struct {
	Hint string `json:"hint"`
}

// Budget is enforced by the agent loop before sending; adapters ignore it.
type Budget struct {
	MaxInputTokens int `json:"max_input_tokens"`
}

// Trace identifies the call in Warden events; it is never sent to providers.
type Trace struct {
	SessionID   string `json:"session_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
	Step        int    `json:"step,omitempty"`
}

// TextBlock returns a text content block.
func TextBlock(s string) ContentBlock { return ContentBlock{Type: BlockText, Text: s} }

// ToolUseBlock returns a tool_use content block.
func ToolUseBlock(id, name string, input json.RawMessage) ContentBlock {
	return ContentBlock{Type: BlockToolUse, ToolUse: &ToolUse{ID: id, Name: name, Input: input}}
}

// ToolResultBlock returns a tool_result block whose content is one text block.
func ToolResultBlock(toolUseID, text string, isError bool) ContentBlock {
	return ContentBlock{Type: BlockToolResult, ToolResult: &ToolResult{
		ToolUseID: toolUseID, OK: !isError, IsError: isError, Content: []ContentBlock{TextBlock(text)},
	}}
}

// UserText returns a user message with one text block.
func UserText(s string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{TextBlock(s)}}
}

// SystemText returns a system message with one text block.
func SystemText(s string) Message {
	return Message{Role: RoleSystem, Content: []ContentBlock{TextBlock(s)}}
}
