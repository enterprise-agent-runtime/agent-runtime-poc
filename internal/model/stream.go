package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// EventType is the type of a StreamEvent (WRD-05 §4).
type EventType string

const (
	EvMessageStart   EventType = "message_start"
	EvTextDelta      EventType = "text_delta"
	EvReasoningDelta EventType = "reasoning_delta"
	EvToolUseStart   EventType = "tool_use_start"
	EvToolUseDelta   EventType = "tool_use_delta"
	EvToolUseEnd     EventType = "tool_use_end"
	EvUsage          EventType = "usage"
	EvMessageEnd     EventType = "message_end"
	EvError          EventType = "error"
)

// StopReason is why generation stopped.
type StopReason string

const (
	StopEndTurn       StopReason = "end_turn"
	StopToolUse       StopReason = "tool_use"
	StopMaxTokens     StopReason = "max_tokens"
	StopStopSequence  StopReason = "stop_sequence"
	StopContentFilter StopReason = "content_filter"
)

// StreamEvent is one streamed event (WRD-05 §4; Go names from design A02 §4.1).
// Only the fields of its Type are set.
type StreamEvent struct {
	Type          EventType  `json:"type"`
	Index         int        `json:"index"`
	Model         string     `json:"model,omitempty"`        // message_start
	Provider      string     `json:"provider,omitempty"`     // message_start, reasoning_delta
	ProviderReqID string     `json:"request_id,omitempty"`   // message_start
	Text          string     `json:"text,omitempty"`         // text_delta
	Opaque        string     `json:"opaque,omitempty"`       // reasoning_delta
	ToolUseID     string     `json:"id,omitempty"`           // tool_use_start
	ToolName      string     `json:"name,omitempty"`         // tool_use_start
	PartialJSON   string     `json:"partial_json,omitempty"` // tool_use_delta
	Usage         *Usage     `json:"usage,omitempty"`        // usage
	StopReason    StopReason `json:"stop_reason,omitempty"`  // message_end
	Note          string     `json:"note,omitempty"`         // diagnostics ("synthesized tool call id", ...)
	Err           *Error     `json:"error,omitempty"`        // error
}

// Usage is token accounting for one call. Adapters fill the counts and
// Estimated; the host fills BillingMode, Quota and EstimatedCost
// (design A11 §2). InputTokens includes cached tokens (WRD-09 §7).
type Usage struct {
	InputTokens       int    `json:"input_tokens"`
	OutputTokens      int    `json:"output_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens,omitempty"`
	ReasoningTokens   int    `json:"reasoning_tokens,omitempty"`
	Estimated         bool   `json:"estimated,omitempty"`
	BillingMode       string `json:"billing_mode,omitempty"` // api_key | none | gateway | harness_subscription
	Quota             *Quota `json:"quota,omitempty"`
	EstimatedCost     *Cost  `json:"estimated_cost,omitempty"`
}

// Quota counts subscription units (premium requests) for harnesses.
type Quota struct {
	Kind  string  `json:"kind"`
	Units float64 `json:"units"`
}

// Cost is an estimated price; nil when no price is known.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Basis    string  `json:"basis"`
}

// Stream yields events; Recv returns io.EOF after message_end or error.
// Close is idempotent and always releases the underlying connection.
type Stream interface {
	Recv() (StreamEvent, error)
	Close() error
}

// SliceStream replays a fixed event sequence; used for buffered responses
// and in tests.
type SliceStream struct {
	Events []StreamEvent
	pos    int
}

// Recv returns the next event or io.EOF.
func (s *SliceStream) Recv() (StreamEvent, error) {
	if s.pos >= len(s.Events) {
		return StreamEvent{}, io.EOF
	}
	ev := s.Events[s.pos]
	s.pos++
	return ev, nil
}

// Close implements Stream.
func (s *SliceStream) Close() error { return nil }

// Collect drains a stream and closes it.
func Collect(s Stream) ([]StreamEvent, error) {
	defer s.Close()
	var out []StreamEvent
	for {
		ev, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}

// ValidateSequence checks the ordering contract of design A11 §6.1:
// message_start first; per tool block, start before its deltas and end
// after them; usage immediately before message_end; exactly one terminal
// event (message_end or error), last. A stream may consist of a single
// error event (failure before message_start).
func ValidateSequence(evs []StreamEvent) error {
	if len(evs) == 0 {
		return errors.New("empty stream")
	}
	last := evs[len(evs)-1]
	if last.Type != EvMessageEnd && last.Type != EvError {
		return fmt.Errorf("last event is %s, want message_end or error", last.Type)
	}
	if evs[0].Type != EvMessageStart && !(len(evs) == 1 && evs[0].Type == EvError) {
		return fmt.Errorf("first event is %s, want message_start", evs[0].Type)
	}
	open := map[int]bool{}
	ended := map[int]bool{}
	for i, ev := range evs {
		if i > 0 && ev.Type == EvMessageStart {
			return fmt.Errorf("event %d: second message_start", i)
		}
		if i < len(evs)-1 && (ev.Type == EvMessageEnd || ev.Type == EvError) {
			return fmt.Errorf("event %d: terminal %s before the end", i, ev.Type)
		}
		switch ev.Type {
		case EvToolUseStart:
			if open[ev.Index] || ended[ev.Index] {
				return fmt.Errorf("event %d: tool block %d started twice", i, ev.Index)
			}
			if ev.ToolUseID == "" || ev.ToolName == "" {
				return fmt.Errorf("event %d: tool_use_start without id or name", i)
			}
			open[ev.Index] = true
		case EvToolUseDelta:
			if !open[ev.Index] {
				return fmt.Errorf("event %d: tool_use_delta for block %d that is not open", i, ev.Index)
			}
		case EvToolUseEnd:
			if !open[ev.Index] {
				return fmt.Errorf("event %d: tool_use_end for block %d that is not open", i, ev.Index)
			}
			delete(open, ev.Index)
			ended[ev.Index] = true
		case EvMessageEnd:
			if i == 0 || evs[i-1].Type != EvUsage {
				return fmt.Errorf("event %d: message_end not immediately preceded by usage", i)
			}
			if len(open) > 0 {
				return fmt.Errorf("event %d: message_end with open tool blocks", i)
			}
			if ev.StopReason == "" {
				return fmt.Errorf("event %d: message_end without stop_reason", i)
			}
		case EvUsage:
			if ev.Usage == nil {
				return fmt.Errorf("event %d: usage event without usage", i)
			}
		case EvError:
			if ev.Err == nil || !ev.Err.Code.Valid() {
				return fmt.Errorf("event %d: error event without a valid code", i)
			}
		}
	}
	return nil
}

// Response is a complete assistant turn assembled from a stream.
type Response struct {
	Model      string
	Provider   string
	RequestID  string
	Message    Message // role assistant: reasoning, text, tool_use blocks in index order
	StopReason StopReason
	Usage      Usage
	Notes      []string
}

// ToolUses returns the tool calls in the response.
func (r Response) ToolUses() []ToolUse {
	var out []ToolUse
	for _, b := range r.Message.Content {
		if b.Type == BlockToolUse {
			out = append(out, *b.ToolUse)
		}
	}
	return out
}

// Text returns the concatenated text blocks.
func (r Response) Text() string {
	var sb strings.Builder
	for _, b := range r.Message.Content {
		if b.Type == BlockText {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// Assemble builds the assistant message from an event sequence. A terminal
// error event is returned as the *Error.
func Assemble(evs []StreamEvent) (Response, error) {
	r := Response{Message: Message{Role: RoleAssistant}}
	type block struct {
		kind  BlockType
		text  strings.Builder
		id    string
		name  string
		opaq  string
		prov  string
		order int
	}
	blocks := map[int]*block{}
	get := func(idx int, kind BlockType) *block {
		b, ok := blocks[idx]
		if !ok {
			b = &block{kind: kind, order: idx}
			blocks[idx] = b
		}
		return b
	}
	for _, ev := range evs {
		switch ev.Type {
		case EvMessageStart:
			r.Model, r.Provider, r.RequestID = ev.Model, ev.Provider, ev.ProviderReqID
		case EvTextDelta:
			get(ev.Index, BlockText).text.WriteString(ev.Text)
		case EvReasoningDelta:
			b := get(ev.Index, BlockReasoning)
			b.opaq += ev.Opaque
			b.prov = ev.Provider
		case EvToolUseStart:
			b := get(ev.Index, BlockToolUse)
			b.id, b.name = ev.ToolUseID, ev.ToolName
		case EvToolUseDelta:
			get(ev.Index, BlockToolUse).text.WriteString(ev.PartialJSON)
		case EvUsage:
			r.Usage = *ev.Usage
		case EvMessageEnd:
			r.StopReason = ev.StopReason
		case EvError:
			return r, ev.Err
		}
		if ev.Note != "" {
			r.Notes = append(r.Notes, ev.Note)
		}
	}
	idx := make([]int, 0, len(blocks))
	for i := range blocks {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		b := blocks[i]
		switch b.kind {
		case BlockText:
			r.Message.Content = append(r.Message.Content, TextBlock(b.text.String()))
		case BlockReasoning:
			r.Message.Content = append(r.Message.Content, ContentBlock{Type: BlockReasoning, Reasoning: &Reasoning{Provider: b.prov, Opaque: b.opaq}})
		case BlockToolUse:
			in, raw := ParseToolInput(b.text.String())
			r.Message.Content = append(r.Message.Content, ContentBlock{Type: BlockToolUse, ToolUse: &ToolUse{ID: b.id, Name: b.name, Input: in, RawInput: raw}})
		}
	}
	return r, nil
}

// ParseToolInput applies the argument rule of design A11 §6.3: empty means
// {}; a valid JSON object is returned compacted; anything else is returned
// as raw text with a nil Input. JSON is never repaired.
func ParseToolInput(s string) (json.RawMessage, string) {
	t := strings.TrimSpace(s)
	if t == "" {
		return json.RawMessage(`{}`), ""
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(t), &probe); err != nil || probe == nil {
		return nil, s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(t)); err != nil {
		return nil, s
	}
	return json.RawMessage(buf.Bytes()), ""
}
