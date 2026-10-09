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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/goldentest"
	"warden.dev/warden/internal/model/providertest"
)

const secretRef = "secret://providers/anthropic/api_key"

func catalog(url string) model.ProviderConfig {
	return model.ProviderConfig{
		ID: "anthropic", Protocol: model.ProtocolAnthropic, BaseURL: url, Tier: model.T3,
		Auth: model.AuthConfig{Mode: model.AuthAPIKey, Secret: secretRef},
		Models: []model.ModelConfig{{ID: "anthropic/claude-sonnet", Model: "claude-sonnet-x",
			Capabilities: model.DeclaredCapabilities{ToolCalling: "native", StructuredOutput: true, Streaming: true, MaxContext: 200000, MaxOutput: 64000, PromptCaching: true}}},
	}
}

func creds() *providertest.FakeCredentials {
	return &providertest.FakeCredentials{Header: "x-api-key", Values: map[string]string{secretRef: providertest.FakeKey}}
}

// fakeServer records requests and answers /v1/messages with a fixed body.
type fakeServer struct {
	mu      sync.Mutex
	status  int
	ctype   string
	body    string
	headers map[string]string
	reqs    []recorded
	srv     *httptest.Server
}

type recorded struct {
	path   string
	header http.Header
	body   []byte
}

func newFake(t *testing.T, status int, ctype, body string) *fakeServer {
	f := &fakeServer{status: status, ctype: ctype, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.URL.Path, r.Header.Clone(), b})
		f.mu.Unlock()
		for k, v := range f.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("content-type", f.ctype)
		w.Header().Set("request-id", "req_test")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func sseFake(t *testing.T, name string) *fakeServer {
	b, err := os.ReadFile(filepath.Join("testdata", "golden", "stream", name+".sse"))
	if err != nil {
		t.Fatal(err)
	}
	return newFake(t, 200, "text/event-stream", string(b))
}

func adapter(t *testing.T, url string) *Adapter {
	t.Helper()
	p, err := New(catalog(url), creds(), &providertest.FakeProbeCache{})
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Adapter)
}

func simpleRequest() model.ModelRequest {
	return model.ModelRequest{ModelID: "anthropic/claude-sonnet", Messages: []model.Message{model.UserText("hi")}}
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }
func bptr(v bool) *bool      { return &v }

var readTool = model.ToolDefinition{Name: "fs__read", Description: "Read a file.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}

func reasoningOpaque() string {
	return base64.StdEncoding.EncodeToString([]byte(`{"signature":"sig==","thinking":"hmm","type":"thinking"}`))
}

// TestGolden_Request pins the canonical → Messages API mapping (A11 §3.2–§3.6).
func TestGolden_Request(t *testing.T) {
	sonnet := catalog("").Models[0]
	cases := map[string]model.ModelRequest{
		"system_user": {ModelID: "anthropic/claude-sonnet", Messages: []model.Message{model.SystemText("You are the coder."), model.UserText("Add an endpoint.")}},
		"cache_hint": {ModelID: "anthropic/claude-sonnet", Cache: &model.CacheHint{Hint: "system_prefix"}, Tools: []model.ToolDefinition{readTool},
			Messages: []model.Message{model.SystemText("sys"), model.UserText("u")}},
		"generation": {ModelID: "anthropic/claude-sonnet", Messages: []model.Message{model.UserText("u")},
			Generation: &model.Generation{MaxOutputTokens: 100, Temperature: f64(0.2), TopP: f64(0.9), Stop: []string{"END"}, Seed: i64(7)}},
		"tool_turns": {ModelID: "anthropic/claude-sonnet", Tools: []model.ToolDefinition{readTool},
			Messages: []model.Message{
				model.SystemText("sys"),
				model.UserText("read it"),
				{Role: model.RoleAssistant, Content: []model.ContentBlock{
					{Type: model.BlockReasoning, Reasoning: &model.Reasoning{Provider: "anthropic", Opaque: reasoningOpaque()}},
					{Type: model.BlockReasoning, Reasoning: &model.Reasoning{Provider: "openai", Opaque: "eA=="}},
					model.TextBlock(""), model.TextBlock("Reading."),
					model.ToolUseBlock("toolu_1", "fs__read", json.RawMessage(`{"path":"a.ts"}`)),
					{Type: model.BlockToolUse, ToolUse: &model.ToolUse{ID: "toolu_2", Name: "fs__read", RawInput: `{"pa`}},
				}},
				{Role: model.RoleTool, Content: []model.ContentBlock{model.ToolResultBlock("toolu_1", "file body", false), model.ToolResultBlock("toolu_2", "invalid_arguments", true)}},
				model.UserText("note from the user"),
			}},
		"tool_choice_named_no_parallel": {ModelID: "anthropic/claude-sonnet", Tools: []model.ToolDefinition{readTool},
			ToolChoice: &model.ToolChoice{Type: model.ToolChoiceTool, Name: "fs__read"}, ParallelToolCalls: bptr(false), Messages: []model.Message{model.UserText("u")}},
		"tool_choice_required": {ModelID: "anthropic/claude-sonnet", Tools: []model.ToolDefinition{readTool},
			ToolChoice: &model.ToolChoice{Type: model.ToolChoiceRequired}, Messages: []model.Message{model.UserText("u")}},
		"tool_choice_none": {ModelID: "anthropic/claude-sonnet", Tools: []model.ToolDefinition{readTool},
			ToolChoice: &model.ToolChoice{Type: model.ToolChoiceNone}, ParallelToolCalls: bptr(false), Messages: []model.Message{model.UserText("u")}},
		"response_format": {ModelID: "anthropic/claude-sonnet", Messages: []model.Message{model.UserText("answer")},
			ResponseFormat: &model.ResponseFormat{Type: "json_schema", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`)}},
		"provider_options": {ModelID: "anthropic/claude-sonnet", Messages: []model.Message{model.UserText("u")},
			ProviderOptions: map[string]json.RawMessage{model.ProtocolAnthropic: json.RawMessage(`{"metadata":{"x":1}}`), model.ProtocolOpenAICompat: json.RawMessage(`{"ignored":true}`)}},
		"images": {ModelID: "anthropic/claude-sonnet", Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{
			{Type: model.BlockImage, Media: &model.Media{MediaType: "image/png", Data: "iVBORw0KGgo="}},
			{Type: model.BlockImage, Media: &model.Media{MediaType: "image/tiff", Data: "AAAA"}},
			{Type: model.BlockDocument, Media: &model.Media{MediaType: "text/plain", Data: "notes", Title: "README"}},
		}}}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			body, _, err := buildRequest(req, sonnet, "anthropic", true)
			if err != nil {
				t.Fatal(err)
			}
			goldentest.JSON(t, filepath.Join("testdata", "golden", "request", name+".json"), body)
		})
	}
}

func TestBuildRequest_Rejects(t *testing.T) {
	sonnet := catalog("").Models[0]
	cases := map[string]model.ModelRequest{
		"second system":           {Messages: []model.Message{model.SystemText("a"), model.UserText("u"), model.SystemText("b")}},
		"system not first":        {Messages: []model.Message{model.UserText("u"), model.SystemText("b")}},
		"response_format + tools": {Tools: []model.ToolDefinition{readTool}, ResponseFormat: &model.ResponseFormat{Type: "json_schema", Schema: json.RawMessage(`{}`)}, Messages: []model.Message{model.UserText("u")}},
		"unknown tool choice":     {Tools: []model.ToolDefinition{readTool}, ToolChoice: &model.ToolChoice{Type: "maybe"}, Messages: []model.Message{model.UserText("u")}},
		"unknown role":            {Messages: []model.Message{{Role: "robot", Content: []model.ContentBlock{model.TextBlock("x")}}}},
		"bad provider_options":    {Messages: []model.Message{model.UserText("u")}, ProviderOptions: map[string]json.RawMessage{model.ProtocolAnthropic: json.RawMessage(`[1]`)}},
	}
	for name, req := range cases {
		if _, _, err := buildRequest(req, sonnet, "anthropic", true); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBuildRequest_DefaultsAndThinking(t *testing.T) {
	mc := model.ModelConfig{Model: "m", Capabilities: model.DeclaredCapabilities{MaxOutput: 4096, Reasoning: true}}
	body, _, err := buildRequest(model.ModelRequest{Tools: []model.ToolDefinition{readTool}, ToolChoice: &model.ToolChoice{Type: model.ToolChoiceRequired},
		Generation: &model.Generation{ReasoningEffort: "medium"}, Messages: []model.Message{model.UserText("u")}}, mc, "anthropic", true)
	if err != nil {
		t.Fatal(err)
	}
	var w wireRequest
	_ = json.Unmarshal(body, &w)
	if w.MaxTokens != 4096 {
		t.Errorf("max_tokens = %d, want min(max_output, 8192)", w.MaxTokens)
	}
	if w.Thinking == nil || w.Thinking.BudgetTokens != 8192 || w.ToolChoice.Type != "auto" {
		t.Errorf("thinking = %+v, choice = %+v: forced choice must downgrade to auto", w.Thinking, w.ToolChoice)
	}
}

// TestGolden_Stream pins the SSE → canonical mapping (A11 §3.7–§3.9) and
// checks every stream against the ordering contract.
func TestGolden_Stream(t *testing.T) {
	names := []string{"text", "tool_use", "thinking", "midstream_error", "ended_early", "forced_json", "invalid_json", "context_exceeded"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			f := sseFake(t, name)
			a := adapter(t, f.srv.URL)
			req := simpleRequest()
			if name == "forced_json" {
				req.ResponseFormat = &model.ResponseFormat{Type: "json_schema", Schema: json.RawMessage(`{"type":"object"}`)}
			}
			s, err := a.Generate(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			evs, err := model.Collect(s)
			if err != nil {
				t.Fatal(err)
			}
			if err := model.ValidateSequence(evs); err != nil {
				t.Fatalf("sequence: %v\n%+v", err, evs)
			}
			goldentest.JSON(t, filepath.Join("testdata", "golden", "stream", name+".events.json"), evs)
		})
	}
}

func collect(t *testing.T, name string, req model.ModelRequest) (model.Response, error) {
	t.Helper()
	f := sseFake(t, name)
	s, err := adapter(t, f.srv.URL).Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := model.Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	return model.Assemble(evs)
}

func TestStream_ToolUseAssembly(t *testing.T) {
	r, err := collect(t, "tool_use", simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	uses := r.ToolUses()
	if len(uses) != 2 || string(uses[0].Input) != `{"path":"src/app.ts"}` || string(uses[1].Input) != `{}` {
		t.Fatalf("tool uses = %+v", uses)
	}
	// Canonical input tokens include cache creation and cache reads (WRD-09 §7).
	if r.Usage.InputTokens != 150 || r.Usage.CachedInputTokens != 30 || r.Usage.OutputTokens != 42 || r.StopReason != model.StopToolUse {
		t.Fatalf("usage/stop = %+v %s", r.Usage, r.StopReason)
	}
	if r.RequestID != "req_test" {
		t.Fatalf("request id = %q, want the request-id header", r.RequestID)
	}
}

func TestStream_ForcedJSONBecomesText(t *testing.T) {
	r, err := collect(t, "forced_json", model.ModelRequest{ModelID: "anthropic/claude-sonnet", Messages: []model.Message{model.UserText("q")},
		ResponseFormat: &model.ResponseFormat{Type: "json_schema", Schema: json.RawMessage(`{"type":"object"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ToolUses()) != 0 || r.StopReason != model.StopEndTurn {
		t.Fatalf("forced tool leaked as a tool call: %+v", r)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(r.Text()), &v); err != nil || v["unit"] != "apples" {
		t.Fatalf("text = %q", r.Text())
	}
}

func TestStream_ErrorsAreNormalized(t *testing.T) {
	cases := map[string]struct {
		code  model.ErrorCode
		retry bool
	}{
		"midstream_error": {model.ErrProviderUnavailable, true},
		"ended_early":     {model.ErrProviderUnavailable, true},
		"invalid_json":    {model.ErrProviderUnavailable, true},
	}
	for name, c := range cases {
		_, err := collect(t, name, simpleRequest())
		var me *model.Error
		if !errors.As(err, &me) || me.Code != c.code || me.Retryable != c.retry {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestStream_ReasoningRoundTrip(t *testing.T) {
	r, err := collect(t, "thinking", simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if r.StopReason != model.StopContentFilter {
		t.Fatalf("refusal → %s, want content_filter", r.StopReason)
	}
	var reasoning *model.Reasoning
	for _, b := range r.Message.Content {
		if b.Type == model.BlockReasoning {
			reasoning = b.Reasoning
		}
	}
	if reasoning == nil || reasoning.Provider != "anthropic" {
		t.Fatalf("no reasoning block: %+v", r.Message)
	}
	// Replaying the assembled turn sends the original thinking block back.
	body, _, err := buildRequest(model.ModelRequest{Messages: []model.Message{model.UserText("q"), r.Message, model.UserText("next")}}, catalog("").Models[0], "anthropic", true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`{"signature":"sig==","thinking":"Let me think","type":"thinking"}`)) {
		t.Fatalf("thinking block not replayed byte for byte: %s", body)
	}
}

// TestGenerate_HeadersAndCredential checks the wire headers and that the key
// is only on the wire, never in errors or logs (INV-C).
func TestGenerate_HeadersAndCredential(t *testing.T) {
	f := sseFake(t, "text")
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	s, err := adapter(t, f.srv.URL).Generate(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Collect(s); err != nil {
		t.Fatal(err)
	}
	h := f.reqs[0].header
	if h.Get("x-api-key") != providertest.FakeKey || h.Get("anthropic-version") != APIVersion || h.Get("anthropic-beta") != "" || h.Get("Authorization") != "" {
		t.Fatalf("headers = %v", h)
	}
	if f.reqs[0].path != "/v1/messages" || !bytes.Contains(f.reqs[0].body, []byte(`"stream":true`)) {
		t.Fatalf("request = %s %s", f.reqs[0].path, f.reqs[0].body)
	}
	if strings.Contains(logs.String(), providertest.FakeKey) {
		t.Fatal("key in logs")
	}
}

// TestGenerate_HTTPErrors covers A11 §8 rows 6–13.
func TestGenerate_HTTPErrors(t *testing.T) {
	cases := []struct {
		status  int
		body    string
		headers map[string]string
		code    model.ErrorCode
		retry   bool
		after   time.Duration
	}{
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, nil, model.ErrContextTooLong, false, 0},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: field required"}}`, nil, model.ErrInvalidRequest, false, 0},
		{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, nil, model.ErrAuthFailed, false, 0},
		{403, `{"type":"error","error":{"type":"permission_error","message":"no"}}`, nil, model.ErrAuthFailed, false, 0},
		{404, `{"type":"error","error":{"type":"not_found_error","message":"model"}}`, nil, model.ErrModelNotFound, false, 0},
		{413, `{"type":"error","error":{"type":"request_too_large","message":"big"}}`, nil, model.ErrContextTooLong, false, 0},
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`, map[string]string{"retry-after": "7"}, model.ErrRateLimited, true, 7 * time.Second},
		{500, `{"type":"error","error":{"type":"api_error","message":"oops"}}`, nil, model.ErrProviderUnavailable, true, 0},
		{503, `upstream down`, nil, model.ErrProviderUnavailable, true, 0},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, nil, model.ErrProviderUnavailable, true, 0},
		{200, `{"type":"message"}`, nil, model.ErrProviderUnavailable, true, 0}, // JSON instead of an event stream
	}
	for _, c := range cases {
		f := newFake(t, c.status, "application/json", c.body)
		f.headers = c.headers
		_, err := adapter(t, f.srv.URL).Generate(context.Background(), simpleRequest())
		var me *model.Error
		if !errors.As(err, &me) || me.Code != c.code || me.Retryable != c.retry || me.RetryAfter != c.after {
			t.Errorf("%d %s: err = %+v", c.status, c.body, me)
			continue
		}
		if strings.Contains(string(me.Details), providertest.FakeKey) || strings.Contains(me.Error(), providertest.FakeKey) {
			t.Errorf("%d: key in error", c.status)
		}
	}
}

func TestGenerate_UnknownModel(t *testing.T) {
	_, err := adapter(t, "http://127.0.0.1:1").Generate(context.Background(), model.ModelRequest{ModelID: "nope"})
	if !errors.Is(err, model.ErrModelNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// TestGenerate_CancelStopsStream: cancelling the caller context ends the
// stream with a cancelled error, then io.EOF.
func TestGenerate_CancelStopsStream(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"x\",\"usage\":{}}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	s, err := adapter(t, srv.URL).Generate(ctx, simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if ev, err := s.Recv(); err != nil || ev.Type != model.EvMessageStart {
		t.Fatalf("first = %+v %v", ev, err)
	}
	cancel()
	ev, err := s.Recv()
	if err != nil || ev.Type != model.EvError || ev.Err.Code != model.ErrCancelled {
		t.Fatalf("after cancel = %+v %v", ev, err)
	}
	if _, err := s.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestGenerate_IdleTimeout: a stalled stream ends with timeout (retryable).
func TestGenerate_IdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"x\",\"usage\":{}}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	cfg := catalog(srv.URL)
	cfg.Timeouts = model.TimeoutConfig{FirstByteMS: 2000, IdleMS: 100}
	p, _ := New(cfg, creds(), nil)
	s, err := p.Generate(context.Background(), simpleRequest())
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Assemble(mustCollect(t, s))
	if !errors.Is(err, model.ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
}

func mustCollect(t *testing.T, s model.Stream) []model.StreamEvent {
	evs, err := model.Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestNew_Rejects(t *testing.T) {
	cfg := catalog("https://api.anthropic.com")
	cfg.Auth.Mode = model.AuthNone
	if _, err := New(cfg, nil, nil); err == nil {
		t.Error("auth none accepted")
	}
	cfg = catalog("https://api.anthropic.com")
	cfg.Protocol = model.ProtocolOpenAICompat
	if _, err := New(cfg, nil, nil); err == nil {
		t.Error("wrong protocol accepted")
	}
	if _, err := New(catalog("http://api.anthropic.com"), nil, nil); err == nil {
		t.Error("plain http to a non-loopback host accepted")
	}
}

func TestCountTokens_ExactThenEstimate(t *testing.T) {
	f := newFake(t, 200, "application/json", `{"input_tokens":321}`)
	n, err := adapter(t, f.srv.URL).CountTokens(context.Background(), simpleRequest())
	if err != nil || n != 321 || f.reqs[0].path != "/v1/messages/count_tokens" || bytes.Contains(f.reqs[0].body, []byte("max_tokens")) {
		t.Fatalf("n=%d err=%v req=%s", n, err, f.reqs[0].body)
	}
	bad := newFake(t, 500, "application/json", `{}`)
	if n, err := adapter(t, bad.srv.URL).CountTokens(context.Background(), simpleRequest()); err != nil || n <= 0 {
		t.Fatalf("estimate = %d, %v", n, err)
	}
}

func TestCapabilities_FromCacheOnlyLowers(t *testing.T) {
	cache := &providertest.FakeProbeCache{}
	_ = cache.Store(model.ProbeResult{ProviderID: "anthropic", Models: []model.ModelProbe{{ModelID: "anthropic/claude-sonnet", ToolCalling: "none", Streaming: true}}})
	p, _ := New(catalog("https://api.anthropic.com"), creds(), cache)
	c, err := p.Capabilities("anthropic/claude-sonnet")
	if err != nil || c.ToolCalling != "none" || c.MaxContext != 200000 || c.StructuredMode != "tool_forcing" {
		t.Fatalf("caps = %+v %v", c, err)
	}
	if _, err := p.Capabilities("x"); !errors.Is(err, model.ErrModelNotFound) {
		t.Fatal("unknown model accepted")
	}
}
