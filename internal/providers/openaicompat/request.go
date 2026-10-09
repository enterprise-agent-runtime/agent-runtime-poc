package openaicompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"warden.dev/warden/internal/model"
)

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                []string        `json:"stop,omitempty"`
	Seed                *int64          `json:"seed,omitempty"`
	Stream              bool            `json:"stream"`
	StreamOptions       *streamOptions  `json:"stream_options,omitempty"`
	Tools               []chatTool      `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      json.RawMessage `json:"response_format,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatMessage.Content is a string, an array of parts, or null.
type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string      `json:"type"`
	Function chatToolDef `json:"function"`
}

type chatToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// buildChatRequest maps a canonical request to chat/completions
// (design A11 §4.2–§4.6) under the model's quirks; it also reports which
// optional fields were sent, for the shape retry.
func buildChatRequest(req model.ModelRequest, mc model.ModelConfig, q Quirks, stream bool) ([]byte, sentFields, error) {
	var sent sentFields
	w := chatRequest{Model: mc.Model, Stream: stream}
	if stream && q.StreamUsage {
		w.StreamOptions = &streamOptions{IncludeUsage: true}
		sent.streamOptions = true
	}
	if g := req.Generation; g != nil {
		if g.MaxOutputTokens > 0 {
			sent.maxTokens = true
			if q.MaxTokensField == "max_completion_tokens" {
				w.MaxCompletionTokens = g.MaxOutputTokens
			} else {
				w.MaxTokens = g.MaxOutputTokens
			}
		}
		if !q.OmitTemperature {
			w.Temperature, w.TopP = g.Temperature, g.TopP
			sent.temperature = g.Temperature != nil || g.TopP != nil
		}
		w.Stop, w.Seed = g.Stop, g.Seed
	}

	tools := req.Tools
	if !q.Tools {
		tools = nil // emulated models get their tools as text from the agent loop
	}
	choice := req.ToolChoice
	if choice != nil && choice.Type == model.ToolChoiceNone && !q.ToolChoiceNone {
		tools, choice = nil, nil // omit tools entirely
	}
	for _, t := range tools {
		w.Tools = append(w.Tools, chatTool{Type: "function", Function: chatToolDef{Name: t.Name, Description: t.Description, Parameters: t.InputSchema}})
	}
	if len(w.Tools) > 0 && choice != nil {
		switch choice.Type {
		case model.ToolChoiceAuto, "":
			w.ToolChoice = jsonString("auto")
		case model.ToolChoiceNone:
			w.ToolChoice, sent.toolChoice = jsonString("none"), "none"
		case model.ToolChoiceRequired:
			if q.ToolChoiceRequired {
				w.ToolChoice, sent.toolChoice = jsonString("required"), "required"
			} else {
				w.ToolChoice = jsonString("auto")
			}
		case model.ToolChoiceTool:
			switch {
			case q.ToolChoiceNamed:
				b, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": choice.Name}})
				w.ToolChoice, sent.toolChoice = b, "named"
			case q.ToolChoiceRequired:
				w.ToolChoice, sent.toolChoice = jsonString("required"), "required"
			default:
				w.ToolChoice = jsonString("auto")
			}
		default:
			return nil, sent, fmt.Errorf("unknown tool_choice %q", choice.Type)
		}
	}
	if len(w.Tools) > 0 && req.ParallelToolCalls != nil && !*req.ParallelToolCalls && q.ParallelToolCallsParam {
		f := false
		w.ParallelToolCalls = &f
		sent.parallel = true
	}

	// response_format (A11 §4.6) with its fallback chain.
	schemaHint := ""
	if rf := req.ResponseFormat; rf != nil && rf.Type == "json_schema" {
		switch q.ResponseFormat {
		case "json_schema":
			js := map[string]any{"name": "warden_output", "schema": rf.Schema}
			if rf.Strict != nil {
				js["strict"] = *rf.Strict
			}
			w.ResponseFormat, _ = json.Marshal(map[string]any{"type": "json_schema", "json_schema": js})
			sent.responseFormat = true
		case "json_object":
			w.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
			sent.responseFormat = true
			schemaHint = "Respond with one JSON object matching this JSON Schema: " + compact(rf.Schema)
		default:
			schemaHint = "Respond with one JSON object matching this JSON Schema: " + compact(rf.Schema)
		}
	}

	vision := mc.Capabilities.Vision
	sawSystem := false
	for i, m := range req.Messages {
		switch m.Role {
		case model.RoleSystem:
			if sawSystem || i != 0 {
				return nil, sent, errors.New("only one system message, first, is allowed")
			}
			sawSystem = true
			text := joinText(m.Content)
			if schemaHint != "" {
				text = strings.TrimSpace(text + "\n\n" + schemaHint)
				schemaHint = ""
			}
			w.Messages = append(w.Messages, chatMessage{Role: q.SystemRole, Content: jsonString(text)})
		case model.RoleUser:
			w.Messages = append(w.Messages, chatMessage{Role: "user", Content: userContent(m.Content, vision)})
		case model.RoleAssistant:
			cm := chatMessage{Role: "assistant", Content: json.RawMessage("null")}
			if t := joinText(m.Content); t != "" {
				cm.Content = jsonString(t)
			}
			for _, b := range m.Content {
				if b.Type != model.BlockToolUse {
					continue
				}
				args := b.ToolUse.RawInput
				if b.ToolUse.Input != nil {
					args = compact(b.ToolUse.Input)
				}
				cm.ToolCalls = append(cm.ToolCalls, chatToolCall{ID: b.ToolUse.ID, Type: "function", Function: chatFunction{Name: b.ToolUse.Name, Arguments: args}})
			}
			w.Messages = append(w.Messages, cm)
		case model.RoleTool:
			var extra []model.ContentBlock
			for _, b := range m.Content {
				if b.Type != model.BlockToolResult {
					extra = append(extra, b)
					continue
				}
				w.Messages = append(w.Messages, chatMessage{Role: "tool", ToolCallID: b.ToolResult.ToolUseID, Content: jsonString(joinText(b.ToolResult.Content))})
			}
			if len(extra) > 0 {
				w.Messages = append(w.Messages, chatMessage{Role: "user", Content: userContent(extra, vision)})
			}
		default:
			return nil, sent, fmt.Errorf("unknown role %q", m.Role)
		}
	}
	if schemaHint != "" { // no system message: add one carrying the hint
		w.Messages = append([]chatMessage{{Role: q.SystemRole, Content: jsonString(schemaHint)}}, w.Messages...)
	}
	if w.Messages == nil {
		w.Messages = []chatMessage{}
	}
	body, err := json.Marshal(w)
	if err != nil {
		return nil, sent, err
	}
	body, err = mergeOptions(body, req.ProviderOptions[model.ProtocolOpenAICompat])
	return body, sent, err
}

func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// joinText joins text blocks (and text/plain documents) with blank lines.
func joinText(blocks []model.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case model.BlockText:
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case model.BlockDocument:
			parts = append(parts, documentText(b.Media))
		}
	}
	return strings.Join(parts, "\n\n")
}

func documentText(m *model.Media) string {
	if m == nil || m.MediaType != "text/plain" {
		return "[document omitted]"
	}
	return "[document: " + m.Title + "]\n" + m.Data
}

func userContent(blocks []model.ContentBlock, vision bool) json.RawMessage {
	hasImage := false
	for _, b := range blocks {
		if b.Type == model.BlockImage {
			hasImage = true
		}
	}
	if !hasImage || !vision {
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case model.BlockText:
				if b.Text != "" {
					parts = append(parts, b.Text)
				}
			case model.BlockDocument:
				parts = append(parts, documentText(b.Media))
			case model.BlockImage:
				parts = append(parts, "[image omitted]")
			}
		}
		return jsonString(strings.Join(parts, "\n\n"))
	}
	var parts []contentPart
	for _, b := range blocks {
		switch b.Type {
		case model.BlockText:
			if b.Text != "" {
				parts = append(parts, contentPart{Type: "text", Text: b.Text})
			}
		case model.BlockDocument:
			parts = append(parts, contentPart{Type: "text", Text: documentText(b.Media)})
		case model.BlockImage:
			if b.Media != nil && b.Media.Data != "" {
				parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: "data:" + b.Media.MediaType + ";base64," + b.Media.Data}})
			} else if b.Media != nil && strings.HasPrefix(b.Media.URI, "https://") {
				parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: b.Media.URI}})
			}
		}
	}
	out, _ := json.Marshal(parts)
	return out
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
