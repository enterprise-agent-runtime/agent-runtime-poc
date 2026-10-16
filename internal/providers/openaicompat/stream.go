package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/wire"
)

type chunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int     `json:"index"`
		Delta        delta   `json:"delta"`
		Message      *delta  `json:"message"` // non-streaming responses
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage       `json:"usage"`
	Error *json.RawMessage `json:"error"`
}

type delta struct {
	Content          *string         `json:"content"`
	ReasoningContent string          `json:"reasoning_content"`
	Reasoning        string          `json:"reasoning"`
	ToolCalls        []toolCallDelta `json:"tool_calls"`
}

type toolCallDelta struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"` // a string, or an object from lenient servers
	} `json:"function"`
}

type chatUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type toolAcc struct {
	canon    int
	id, name string
	buf      []string // deltas held until id and name are known
	started  bool
}

// chunkStream turns chat.completion.chunk events into canonical events
// (design A11 §4.7, §6).
type chunkStream struct {
	ctx      context.Context
	resp     *http.Response
	sse      *wire.SSEReader
	wd       *wire.Watchdog
	provider string
	reqID    string
	estInput int

	pending  []model.StreamEvent
	started  bool
	done     bool
	closed   bool
	invalid  int
	textIdx  int
	reasIdx  int
	nextIdx  int
	calls    map[int]*toolAcc
	order    []int // provider indices in order of first appearance
	synth    int
	usage    *model.Usage
	stop     model.StopReason
	finished bool
	sawTools bool
	outBytes int
	note     string
}

func newChunkStream(ctx context.Context, resp *http.Response, wd *wire.Watchdog, provider string, estInput int) *chunkStream {
	s := &chunkStream{ctx: ctx, resp: resp, wd: wd, provider: provider, reqID: resp.Header.Get("x-request-id"), estInput: estInput, textIdx: -1, reasIdx: -1, calls: map[int]*toolAcc{}}
	s.sse = wire.NewSSEReader(resp.Body, wd.Touch)
	return s
}

func (s *chunkStream) Recv() (model.StreamEvent, error) {
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

func (s *chunkStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.wd.Stop()
	return s.resp.Body.Close()
}

func (s *chunkStream) emit(ev model.StreamEvent) { s.pending = append(s.pending, ev) }

func (s *chunkStream) fail(e *model.Error) {
	s.emit(model.StreamEvent{Type: model.EvError, Err: e})
	s.done = true
}

func (s *chunkStream) step() {
	ev, err := s.sse.Next()
	if err != nil {
		switch {
		case errors.Is(err, io.EOF):
			if s.finished {
				s.end()
				return
			}
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
	if strings.TrimSpace(ev.Data) == "[DONE]" {
		if !s.finished {
			slog.Warn("openaicompat: stream ended without finish_reason", "provider", s.provider)
		}
		s.end()
		return
	}
	var c chunk
	if err := json.Unmarshal([]byte(ev.Data), &c); err != nil {
		s.invalid++
		slog.Warn("openaicompat: invalid SSE data skipped", "provider", s.provider)
		if s.invalid >= 2 {
			s.fail(model.NewError(model.ErrProviderUnavailable, "provider sent invalid stream data"))
		}
		return
	}
	if c.Error != nil {
		s.fail(normalizeBody(0, *c.Error))
		return
	}
	if len(c.Choices) > 0 && !s.started {
		s.started = true
		id := s.reqID
		if id == "" {
			id = c.ID
		}
		s.emit(model.StreamEvent{Type: model.EvMessageStart, Model: c.Model, Provider: s.provider, ProviderReqID: id})
	}
	for _, ch := range c.Choices {
		if ch.Index != 0 {
			continue
		}
		s.applyDelta(ch.Delta)
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			s.finish(*ch.FinishReason)
		}
	}
	if c.Usage != nil {
		s.usage = mapUsage(c.Usage)
	}
}

func (s *chunkStream) applyDelta(d delta) {
	if d.Content != nil && *d.Content != "" {
		if s.textIdx < 0 {
			s.textIdx = s.nextIdx
			s.nextIdx++
		}
		s.outBytes += len(*d.Content)
		s.emit(model.StreamEvent{Type: model.EvTextDelta, Index: s.textIdx, Text: *d.Content})
	}
	if r := d.ReasoningContent + d.Reasoning; r != "" {
		if s.reasIdx < 0 {
			s.reasIdx = s.nextIdx
			s.nextIdx++
		}
		s.outBytes += len(r)
		s.emit(model.StreamEvent{Type: model.EvReasoningDelta, Index: s.reasIdx, Provider: s.provider, Opaque: r})
	}
	for pos, tc := range d.ToolCalls {
		s.sawTools = true
		key := pos
		if tc.Index != nil {
			key = *tc.Index
		}
		acc, ok := s.calls[key]
		// A chunk carrying a new id for an index already in use starts a new call
		// (servers that omit the index).
		if ok && tc.Index == nil && tc.ID != "" && acc.id != "" && tc.ID != acc.id {
			key = 1000 + len(s.order)
			ok = false
		}
		if !ok {
			acc = &toolAcc{canon: s.nextIdx}
			s.nextIdx++
			s.calls[key] = acc
			s.order = append(s.order, key)
		}
		if acc.id == "" && tc.ID != "" {
			acc.id = tc.ID
		}
		if acc.name == "" && tc.Function.Name != "" {
			acc.name = tc.Function.Name
		}
		if args := argString(tc.Function.Arguments); args != "" {
			s.outBytes += len(args)
			if acc.started {
				s.emit(model.StreamEvent{Type: model.EvToolUseDelta, Index: acc.canon, PartialJSON: args})
			} else {
				acc.buf = append(acc.buf, args)
			}
		}
		s.maybeStart(acc, false)
	}
}

// maybeStart emits tool_use_start once id and name are known; with force,
// a missing id is synthesized (A11 §4.7 tolerances).
func (s *chunkStream) maybeStart(acc *toolAcc, force bool) {
	if acc.started || acc.name == "" {
		return
	}
	if acc.id == "" {
		if !force {
			return
		}
		s.synth++
		acc.id = fmt.Sprintf("call_%d", s.synth)
		s.note = "synthesized tool call id"
	}
	acc.started = true
	s.emit(model.StreamEvent{Type: model.EvToolUseStart, Index: acc.canon, ToolUseID: acc.id, ToolName: acc.name})
	for _, b := range acc.buf {
		s.emit(model.StreamEvent{Type: model.EvToolUseDelta, Index: acc.canon, PartialJSON: b})
	}
	acc.buf = nil
}

func (s *chunkStream) closeTools() {
	for _, key := range s.order {
		acc := s.calls[key]
		s.maybeStart(acc, true)
		if acc.started {
			s.emit(model.StreamEvent{Type: model.EvToolUseEnd, Index: acc.canon})
		}
	}
	s.calls, s.order = map[int]*toolAcc{}, nil
}

func (s *chunkStream) finish(reason string) {
	if s.finished {
		return
	}
	s.finished = true
	s.closeTools()
	s.stop = mapFinish(reason, s.sawTools, s.provider)
}

func (s *chunkStream) end() {
	if !s.finished {
		s.closeTools()
		s.stop = model.StopEndTurn
		if s.sawTools {
			s.stop = model.StopToolUse
		}
	}
	if !s.started {
		s.emit(model.StreamEvent{Type: model.EvMessageStart, Provider: s.provider, ProviderReqID: s.reqID})
	}
	u := s.usage
	if u == nil {
		u = &model.Usage{InputTokens: s.estInput, OutputTokens: estimate(s.outBytes), Estimated: true}
	}
	s.emit(model.StreamEvent{Type: model.EvUsage, Usage: u})
	s.emit(model.StreamEvent{Type: model.EvMessageEnd, StopReason: s.stop, Note: s.note})
	s.done = true
}

func mapUsage(u *chatUsage) *model.Usage {
	out := &model.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	if u.PromptTokensDetails != nil {
		out.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

// mapFinish maps finish_reason (A11 §4.7).
func mapFinish(r string, sawTools bool, provider string) model.StopReason {
	switch r {
	case "stop":
		if sawTools {
			return model.StopToolUse
		}
		return model.StopEndTurn
	case "tool_calls", "function_call":
		return model.StopToolUse
	case "length":
		return model.StopMaxTokens
	case "content_filter":
		return model.StopContentFilter
	}
	slog.Warn("openaicompat: unmapped finish_reason", "provider", provider, "finish_reason", r)
	return model.StopEndTurn
}

// argString returns tool arguments as a string; an object (lenient
// servers) is re-marshaled compactly.
func argString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) == nil {
		return b.String()
	}
	return string(raw)
}

// estimate is the 3.2-bytes-per-token rule of A11 §6.5.
func estimate(n int) int { return (n*10 + 31) / 32 }

// buffered synthesizes the canonical sequence from a non-streaming
// response (quirk stream_tools: false, A11 §4.7).
func buffered(body []byte, provider, reqID string, estInput int) []model.StreamEvent {
	var c chunk
	if err := json.Unmarshal(body, &c); err != nil || len(c.Choices) == 0 {
		return []model.StreamEvent{{Type: model.EvError, Err: model.NewError(model.ErrProviderUnavailable, "provider sent an invalid response")}}
	}
	if reqID == "" {
		reqID = c.ID
	}
	s := &chunkStream{provider: provider, reqID: reqID, estInput: estInput, textIdx: -1, reasIdx: -1, calls: map[int]*toolAcc{}, started: true}
	s.emit(model.StreamEvent{Type: model.EvMessageStart, Model: c.Model, Provider: provider, ProviderReqID: reqID})
	ch := c.Choices[0]
	if ch.Message != nil {
		d := *ch.Message
		// Non-streaming tool calls carry no index; their position is the index.
		for i := range d.ToolCalls {
			idx := i
			d.ToolCalls[i].Index = &idx
		}
		s.applyDelta(d)
	}
	reason := "stop"
	if ch.FinishReason != nil {
		reason = *ch.FinishReason
	}
	s.finish(reason)
	if c.Usage != nil {
		s.usage = mapUsage(c.Usage)
	}
	s.end()
	return s.pending
}
