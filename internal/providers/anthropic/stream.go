package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/wire"
)

// eventStream maps Messages API server-sent events to canonical events
// (design A11 §3.7–§3.9).
type eventStream struct {
	ctx      context.Context // caller context
	resp     *http.Response
	sse      *wire.SSEReader
	wd       *wire.Watchdog
	provider string
	reqID    string
	forcing  bool // response_format via warden_json_output

	pending []model.StreamEvent
	done    bool
	closed  bool
	invalid int

	blocks map[int]*blockState
	usage  wireUsage
	stop   model.StopReason
	note   string
}

type blockState struct {
	kind      string // text | tool_use | thinking | redacted_thinking | other
	forced    bool   // the warden_json_output tool
	thinking  bytes.Buffer
	signature string
	data      string // redacted_thinking
}

type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type sseData struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID    string    `json:"id"`
		Model string    `json:"model"`
		Usage wireUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Data string `json:"data"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *wireUsage `json:"usage"`
	Error *wireError `json:"error"`
}

type wireError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (s *eventStream) Recv() (model.StreamEvent, error) {
	for len(s.pending) == 0 {
		if s.done {
			return model.StreamEvent{}, io.EOF
		}
		s.step()
	}
	ev := s.pending[0]
	s.pending = s.pending[1:]
	return ev, nil
}

func (s *eventStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.wd.Stop()
	return s.resp.Body.Close()
}

func (s *eventStream) fail(e *model.Error) {
	s.pending = append(s.pending, model.StreamEvent{Type: model.EvError, Err: e})
	s.done = true
}

// step reads one SSE event and appends the canonical events it produces.
func (s *eventStream) step() {
	ev, err := s.sse.Next()
	if err != nil {
		switch {
		case errors.Is(err, io.EOF):
			e := model.NewError(model.ErrProviderUnavailable, "stream ended early")
			e.Details = wire.DetailsJSON(wire.Details{Note: "stream ended early", RequestID: s.reqID})
			s.fail(e)
		case errors.Is(err, wire.ErrLineTooLong):
			e := &model.Error{Code: model.ErrProviderUnavailable, Message: "provider sent an oversized event"}
			e.Details = wire.DetailsJSON(wire.Details{Note: "sse line too long"})
			s.fail(e)
		default:
			s.fail(wire.TransportError(err, s.ctx, s.wd))
		}
		return
	}
	var d sseData
	if err := json.Unmarshal([]byte(ev.Data), &d); err != nil {
		s.invalid++
		slog.Warn("anthropic: invalid SSE data skipped", "provider", s.provider)
		if s.invalid >= 2 {
			s.fail(model.NewError(model.ErrProviderUnavailable, "provider sent invalid stream data"))
		}
		return
	}
	if d.Type == "" {
		d.Type = ev.Name
	}
	switch d.Type {
	case "message_start":
		if d.Message != nil {
			s.usage = d.Message.Usage
			if s.reqID == "" {
				s.reqID = d.Message.ID
			}
			s.emit(model.StreamEvent{Type: model.EvMessageStart, Model: d.Message.Model, Provider: s.provider, ProviderReqID: s.reqID})
		}
	case "content_block_start":
		if d.ContentBlock == nil {
			return
		}
		b := &blockState{kind: d.ContentBlock.Type}
		s.blocks[d.Index] = b
		switch b.kind {
		case "tool_use":
			if s.forcing && d.ContentBlock.Name == jsonOutputTool {
				b.forced = true
				return
			}
			s.emit(model.StreamEvent{Type: model.EvToolUseStart, Index: d.Index, ToolUseID: d.ContentBlock.ID, ToolName: d.ContentBlock.Name})
		case "redacted_thinking":
			b.data = d.ContentBlock.Data
		}
	case "content_block_delta":
		b := s.blocks[d.Index]
		if d.Delta == nil || b == nil {
			return
		}
		switch d.Delta.Type {
		case "text_delta":
			s.emit(model.StreamEvent{Type: model.EvTextDelta, Index: d.Index, Text: d.Delta.Text})
		case "input_json_delta":
			if b.forced {
				s.emit(model.StreamEvent{Type: model.EvTextDelta, Index: d.Index, Text: d.Delta.PartialJSON})
			} else if d.Delta.PartialJSON != "" {
				s.emit(model.StreamEvent{Type: model.EvToolUseDelta, Index: d.Index, PartialJSON: d.Delta.PartialJSON})
			}
		case "thinking_delta":
			b.thinking.WriteString(d.Delta.Thinking)
		case "signature_delta":
			b.signature += d.Delta.Signature
		}
	case "content_block_stop":
		b := s.blocks[d.Index]
		if b == nil {
			return
		}
		switch b.kind {
		case "tool_use":
			if !b.forced {
				s.emit(model.StreamEvent{Type: model.EvToolUseEnd, Index: d.Index})
			}
		case "thinking", "redacted_thinking":
			var raw []byte
			if b.kind == "thinking" {
				raw, _ = json.Marshal(map[string]string{"type": "thinking", "thinking": b.thinking.String(), "signature": b.signature})
			} else {
				raw, _ = json.Marshal(map[string]string{"type": "redacted_thinking", "data": b.data})
			}
			s.emit(model.StreamEvent{Type: model.EvReasoningDelta, Index: d.Index, Provider: s.provider, Opaque: base64.StdEncoding.EncodeToString(raw)})
		}
	case "message_delta":
		if d.Delta != nil && d.Delta.StopReason != "" {
			s.stop, s.note = mapStop(d.Delta.StopReason)
		}
		if d.Usage != nil && d.Usage.OutputTokens > 0 {
			s.usage.OutputTokens = d.Usage.OutputTokens
		}
	case "message_stop":
		stop := s.stop
		if stop == "" {
			stop = model.StopEndTurn
		}
		if s.forcing && stop == model.StopToolUse {
			stop = model.StopEndTurn
		}
		u := s.usage
		s.emit(model.StreamEvent{Type: model.EvUsage, Usage: &model.Usage{
			InputTokens:       u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
			OutputTokens:      u.OutputTokens,
			CachedInputTokens: u.CacheReadInputTokens,
		}})
		s.emit(model.StreamEvent{Type: model.EvMessageEnd, StopReason: stop, Note: s.note})
		s.done = true
	case "error":
		we := wireError{Type: "api_error"}
		if d.Error != nil {
			we = *d.Error
		}
		s.fail(normalizeType(we, 0))
	case "ping":
	}
}

func (s *eventStream) emit(ev model.StreamEvent) { s.pending = append(s.pending, ev) }

// mapStop maps stop_reason (A11 §3.9); the note asks the loop to compact.
func mapStop(r string) (model.StopReason, string) {
	switch r {
	case "end_turn":
		return model.StopEndTurn, ""
	case "tool_use":
		return model.StopToolUse, ""
	case "max_tokens":
		return model.StopMaxTokens, ""
	case "stop_sequence":
		return model.StopStopSequence, ""
	case "refusal":
		return model.StopContentFilter, ""
	case "model_context_window_exceeded":
		return model.StopMaxTokens, "context window exceeded"
	}
	slog.Warn("anthropic: unmapped stop_reason", "stop_reason", r)
	return model.StopEndTurn, ""
}
