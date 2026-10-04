package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"warden.dev/warden/internal/model"
)

// jsonOutputTool is the adapter-internal tool used to emulate
// response_format by tool forcing (design A11 §3.6).
const jsonOutputTool = "warden_json_output"

type wireRequest struct {
	Model         string          `json:"model"`
	System        []wireBlock     `json:"system,omitempty"`
	Messages      []wireMessage   `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Tools         []wireTool      `json:"tools,omitempty"`
	ToolChoice    *wireToolChoice `json:"tool_choice,omitempty"`
	Thinking      *wireThinking   `json:"thinking,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

// wireBlock is any Anthropic content block; raw carries a replayed
// reasoning block byte for byte.
type wireBlock struct {
	Type         string          `json:"type,omitempty"`
	Text         string          `json:"text,omitempty"`
	Source       *wireSource     `json:"source,omitempty"`
	Title        string          `json:"title,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      []wireBlock     `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
	raw          json.RawMessage
}

func (b wireBlock) MarshalJSON() ([]byte, error) {
	if b.raw != nil {
		return b.raw, nil
	}
	type plain wireBlock
	return json.Marshal(plain(b))
}

type wireSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type wireTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type wireToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type wireThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

var imageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

var thinkingBudget = map[string]int{"low": 2048, "medium": 8192, "high": 24576}

// buildRequest maps a canonical request to the Messages API body
// (design A11 §3.2–§3.6). It reports whether response_format is emulated by
// forcing the jsonOutputTool.
func buildRequest(req model.ModelRequest, mc model.ModelConfig, providerID string, stream bool) ([]byte, bool, error) {
	w := wireRequest{Model: mc.Model, Stream: stream}
	caching := req.Cache != nil && req.Cache.Hint == "system_prefix" && mc.Capabilities.PromptCaching

	// max_tokens is required by Anthropic: default min(max_output, 8192).
	w.MaxTokens = 8192
	if mc.Capabilities.MaxOutput > 0 && mc.Capabilities.MaxOutput < w.MaxTokens {
		w.MaxTokens = mc.Capabilities.MaxOutput
	}
	if g := req.Generation; g != nil {
		if g.MaxOutputTokens > 0 {
			w.MaxTokens = g.MaxOutputTokens
		}
		w.Temperature, w.TopP, w.StopSequences = g.Temperature, g.TopP, g.Stop
		// Seed is not supported by Anthropic and is dropped (A11 §3.2).
		if b, ok := thinkingBudget[g.ReasoningEffort]; ok && mc.Capabilities.Reasoning {
			w.Thinking = &wireThinking{Type: "enabled", BudgetTokens: b}
		}
	}

	for _, t := range req.Tools {
		w.Tools = append(w.Tools, wireTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}

	// Tool choice (A11 §3.5).
	if c := req.ToolChoice; c != nil || (req.ParallelToolCalls != nil && !*req.ParallelToolCalls && len(req.Tools) > 0) {
		tc := &wireToolChoice{Type: "auto"}
		if c != nil {
			switch c.Type {
			case model.ToolChoiceAuto, "":
			case model.ToolChoiceNone:
				tc.Type = "none"
			case model.ToolChoiceRequired:
				tc.Type = "any"
			case model.ToolChoiceTool:
				tc.Type, tc.Name = "tool", c.Name
			default:
				return nil, false, fmt.Errorf("unknown tool_choice %q", c.Type)
			}
		}
		if req.ParallelToolCalls != nil && !*req.ParallelToolCalls && tc.Type != "none" {
			tc.DisableParallelToolUse = true
		}
		// Extended thinking does not allow forced tool use.
		if w.Thinking != nil && (tc.Type == "tool" || tc.Type == "any") {
			tc.Type, tc.Name = "auto", ""
		}
		if len(req.Tools) > 0 || tc.Type == "none" {
			w.ToolChoice = tc
		}
	}

	// response_format → tool forcing (A11 §3.6).
	forcing := false
	if rf := req.ResponseFormat; rf != nil && rf.Type == "json_schema" {
		if len(req.Tools) > 0 {
			return nil, false, errors.New("response_format cannot be combined with tools on anthropic-messages")
		}
		w.Tools = []wireTool{{Name: jsonOutputTool, Description: "Return the final answer as a JSON object matching the schema.", InputSchema: rf.Schema}}
		w.ToolChoice = &wireToolChoice{Type: "tool", Name: jsonOutputTool}
		forcing = true
	}

	sawSystem := false
	for i, m := range req.Messages {
		if m.Role == model.RoleSystem {
			if sawSystem || i != 0 {
				return nil, false, errors.New("only one system message, first, is allowed")
			}
			sawSystem = true
			for _, b := range m.Content {
				if b.Type == model.BlockText && b.Text != "" {
					w.System = append(w.System, wireBlock{Type: "text", Text: b.Text})
				}
			}
			continue
		}
		blocks, err := mapBlocks(m.Content, providerID)
		if err != nil {
			return nil, false, err
		}
		role := "user"
		if m.Role == model.RoleAssistant {
			role = "assistant"
		} else if m.Role != model.RoleUser && m.Role != model.RoleTool {
			return nil, false, fmt.Errorf("unknown role %q", m.Role)
		}
		if len(blocks) == 0 {
			continue
		}
		// Consecutive same-role messages merge; a tool message becomes the
		// leading tool_result blocks of the next user turn (A11 §3.3).
		if n := len(w.Messages); n > 0 && w.Messages[n-1].Role == role {
			w.Messages[n-1].Content = append(w.Messages[n-1].Content, blocks...)
			continue
		}
		w.Messages = append(w.Messages, wireMessage{Role: role, Content: blocks})
	}
	if caching {
		if n := len(w.System); n > 0 {
			w.System[n-1].CacheControl = &cacheControl{Type: "ephemeral"}
		}
		if n := len(w.Tools); n > 0 {
			w.Tools[n-1].CacheControl = &cacheControl{Type: "ephemeral"}
		}
	}
	if w.Messages == nil {
		w.Messages = []wireMessage{}
	}
	body, err := json.Marshal(w)
	if err != nil {
		return nil, false, err
	}
	body, err = mergeOptions(body, req.ProviderOptions[model.ProtocolAnthropic])
	return body, forcing, err
}

func mapBlocks(in []model.ContentBlock, providerID string) ([]wireBlock, error) {
	var out []wireBlock
	for _, b := range in {
		switch b.Type {
		case model.BlockText:
			if b.Text != "" { // Anthropic rejects empty text blocks
				out = append(out, wireBlock{Type: "text", Text: b.Text})
			}
		case model.BlockImage:
			out = append(out, imageBlock(b.Media))
		case model.BlockDocument:
			if b.Media == nil {
				continue
			}
			src := &wireSource{Type: "base64", MediaType: "application/pdf", Data: b.Media.Data}
			if b.Media.MediaType == "text/plain" {
				src = &wireSource{Type: "text", MediaType: "text/plain", Data: b.Media.Data}
			}
			out = append(out, wireBlock{Type: "document", Source: src, Title: b.Media.Title})
		case model.BlockToolUse:
			u := b.ToolUse
			input := u.Input
			if input == nil {
				input = json.RawMessage(`{}`) // RawInput stays in Warden only
			}
			out = append(out, wireBlock{Type: "tool_use", ID: u.ID, Name: u.Name, Input: input})
		case model.BlockToolResult:
			r := b.ToolResult
			var content []wireBlock
			for _, c := range r.Content {
				switch c.Type {
				case model.BlockText:
					if c.Text != "" {
						content = append(content, wireBlock{Type: "text", Text: c.Text})
					}
				case model.BlockImage:
					content = append(content, imageBlock(c.Media))
				}
			}
			out = append(out, wireBlock{Type: "tool_result", ToolUseID: r.ToolUseID, Content: content, IsError: r.IsError})
		case model.BlockReasoning:
			// Replayed byte for byte only to the provider that produced it.
			if b.Reasoning == nil || b.Reasoning.Provider != providerID {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(b.Reasoning.Opaque)
			if err != nil || !json.Valid(raw) {
				continue
			}
			out = append(out, wireBlock{raw: raw})
		default:
			return nil, fmt.Errorf("unknown content block %q", b.Type)
		}
	}
	// Reasoning blocks must come first in an assistant turn; keep the
	// canonical order otherwise.
	return out, nil
}

func imageBlock(m *model.Media) wireBlock {
	if m == nil {
		return wireBlock{Type: "text", Text: "[image omitted: unsupported type]"}
	}
	if m.URI != "" && len(m.URI) > 8 && m.URI[:8] == "https://" {
		return wireBlock{Type: "image", Source: &wireSource{Type: "url", URL: m.URI}}
	}
	if !imageTypes[m.MediaType] || m.Data == "" {
		return wireBlock{Type: "text", Text: "[image omitted: unsupported type]"}
	}
	return wireBlock{Type: "image", Source: &wireSource{Type: "base64", MediaType: m.MediaType, Data: m.Data}}
}

// mergeOptions shallow-merges provider_options into the body last.
func mergeOptions(body []byte, opts json.RawMessage) ([]byte, error) {
	if len(opts) == 0 {
		return body, nil
	}
	var base, extra map[string]json.RawMessage
	if err := json.Unmarshal(body, &base); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(opts, &extra); err != nil {
		return nil, fmt.Errorf("provider_options must be a JSON object: %w", err)
	}
	for k, v := range extra {
		base[k] = v
	}
	return json.Marshal(base)
}
