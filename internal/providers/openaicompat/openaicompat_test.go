package openaicompat

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
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

func ollamaCfg(url string) model.ProviderConfig {
	return model.ProviderConfig{ID: "ollama", Protocol: model.ProtocolOpenAICompat, BaseURL: url, Tier: model.T0,
		Auth: model.AuthConfig{Mode: model.AuthNone},
		Models: []model.ModelConfig{
			{ID: "local/qwen-coder-32b", Model: "qwen2.5-coder:32b", Capabilities: model.DeclaredCapabilities{ToolCalling: "native", StructuredOutput: true, Streaming: true, MaxContext: 32768, MaxOutput: 8192}},
			{ID: "local/vision", Model: "llava", Capabilities: model.DeclaredCapabilities{ToolCalling: "native", Streaming: true, MaxContext: 8192, Vision: true}},
		}}
}

type recorded struct {
	path   string
	header http.Header
	body   []byte
}

// server answers chat/completions from a queue of (status, ctype, body).
type server struct {
	mu   sync.Mutex
	srv  *httptest.Server
	reqs []recorded
	resp []reply
	hdr  map[string]string
}

type reply struct {
	status int
	ctype  string
	body   string
}

func newServer(t *testing.T, replies ...reply) *server {
	s := &server{resp: replies}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{r.URL.Path, r.Header.Clone(), b})
		rp := reply{500, "application/json", `{"error":{"message":"script exhausted"}}`}
		if len(s.resp) > 0 {
			rp, s.resp = s.resp[0], s.resp[1:]
		}
		for k, v := range s.hdr {
			w.Header().Set(k, v)
		}
		s.mu.Unlock()
		w.Header().Set("content-type", rp.ctype)
		w.WriteHeader(rp.status)
		_, _ = io.WriteString(w, rp.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func fixture(t *testing.T, name string) reply {
	b, err := os.ReadFile(filepath.Join("testdata", "golden", "stream", name+".sse"))
	if err != nil {
		t.Fatal(err)
	}
	return reply{200, "text/event-stream", string(b)}
}

func client(t *testing.T, cfg model.ProviderConfig, creds model.CredentialSource, cache model.ProbeCache) *Client {
	t.Helper()
	p, err := New(cfg, creds, cache)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Client)
}

func req32(msgs ...model.Message) model.ModelRequest {
	return model.ModelRequest{ModelID: "local/qwen-coder-32b", Messages: msgs}
}

var readTool = model.ToolDefinition{Name: "fs__read", Description: "Read a file.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}

func f64(v float64) *float64 { return &v }
func bptr(v bool) *bool      { return &v }

// TestGolden_Request pins the canonical → chat/completions mapping under
// default and non-default quirks (A11 §4.2–§4.6).
func TestGolden_Request(t *testing.T) {
	mc := ollamaCfg("").Models[0]
	vision := ollamaCfg("").Models[1]
	d := defaultQuirks()
	quirky := defaultQuirks()
	quirky.SystemRole, quirky.MaxTokensField, quirky.OmitTemperature, quirky.StreamUsage = "developer", "max_completion_tokens", true, false
	quirky.ToolChoiceNamed, quirky.ParallelToolCallsParam, quirky.ResponseFormat = false, false, "json_object"
	noTools := defaultQuirks()
	noTools.Tools = false
	noneOmits := defaultQuirks()
	noneOmits.ToolChoiceNone = false
	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}}}`)
	turns := []model.Message{
		model.SystemText("You are the coder."),
		model.UserText("read it"),
		{Role: model.RoleAssistant, Content: []model.ContentBlock{
			{Type: model.BlockReasoning, Reasoning: &model.Reasoning{Provider: "ollama", Opaque: "x"}},
			model.ToolUseBlock("call_1", "fs__read", json.RawMessage(`{ "path": "a.ts" }`)),
			{Type: model.BlockToolUse, ToolUse: &model.ToolUse{ID: "call_2", Name: "fs__read", RawInput: `{"pa`}},
		}},
		{Role: model.RoleTool, Content: []model.ContentBlock{model.ToolResultBlock("call_1", "body", false), model.ToolResultBlock("call_2", "invalid_arguments", true)}},
		model.UserText("continue"),
	}
	cases := []struct {
		name string
		req  model.ModelRequest
		mc   model.ModelConfig
		q    Quirks
	}{
		{"turns_default", model.ModelRequest{Tools: []model.ToolDefinition{readTool}, ToolChoice: &model.ToolChoice{Type: model.ToolChoiceAuto}, Messages: turns,
			Generation: &model.Generation{MaxOutputTokens: 512, Temperature: f64(0.1)}}, mc, d},
		{"named_choice_quirky", model.ModelRequest{Tools: []model.ToolDefinition{readTool}, ToolChoice: &model.ToolChoice{Type: model.ToolChoiceTool, Name: "fs__read"}, ParallelToolCalls: bptr(false),
			Messages: []model.Message{model.SystemText("sys"), model.UserText("u")}, Generation: &model.Generation{MaxOutputTokens: 100, Temperature: f64(0.5)}}, mc, quirky},
		{"named_choice_default", model.ModelRequest{Tools: []model.ToolDefinition{readTool}, ToolChoice: &model.ToolChoice{Type: model.ToolChoiceTool, Name: "fs__read"}, ParallelToolCalls: bptr(false),
			Messages: []model.Message{model.UserText("u")}}, mc, d},
		{"response_format_schema", model.ModelRequest{ResponseFormat: &model.ResponseFormat{Type: "json_schema", Schema: schema, Strict: bptr(true)}, Messages: []model.Message{model.UserText("u")}}, mc, d},
		{"response_format_json_object_no_system", model.ModelRequest{ResponseFormat: &model.ResponseFormat{Type: "json_schema", Schema: schema}, Messages: []model.Message{model.UserText("u")}}, mc, quirky},
		{"emulated_model_no_tools", model.ModelRequest{Tools: []model.ToolDefinition{readTool}, ToolChoice: &model.ToolChoice{Type: model.ToolChoiceRequired}, Messages: []model.Message{model.UserText("u")}}, mc, noTools},
		{"tool_choice_none_omits_tools", model.ModelRequest{Tools: []model.ToolDefinition{readTool}, ToolChoice: &model.ToolChoice{Type: model.ToolChoiceNone}, Messages: []model.Message{model.UserText("u")}}, mc, noneOmits},
		{"images_with_vision", model.ModelRequest{Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{model.TextBlock("look"),
			{Type: model.BlockImage, Media: &model.Media{MediaType: "image/png", Data: "iVBO"}},
			{Type: model.BlockDocument, Media: &model.Media{MediaType: "text/plain", Data: "notes", Title: "README"}}}}}}, vision, d},
		{"images_without_vision", model.ModelRequest{Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{model.TextBlock("look"),
			{Type: model.BlockImage, Media: &model.Media{MediaType: "image/png", Data: "iVBO"}},
			{Type: model.BlockDocument, Media: &model.Media{MediaType: "application/pdf", Data: "JVBE"}}}}}}, mc, d},
		{"provider_options", model.ModelRequest{Messages: []model.Message{model.UserText("u")},
			ProviderOptions: map[string]json.RawMessage{model.ProtocolOpenAICompat: json.RawMessage(`{"keep_alive":"30m"}`)}}, mc, d},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.req.ModelID = "local/qwen-coder-32b"
			body, _, err := buildChatRequest(c.req, c.mc, c.q, true)
			if err != nil {
				t.Fatal(err)
			}
			goldentest.JSON(t, filepath.Join("testdata", "golden", "request", c.name+".json"), body)
		})
	}
}

func TestBuildChatRequest_Rejects(t *testing.T) {
	mc := ollamaCfg("").Models[0]
	for name, req := range map[string]model.ModelRequest{
		"second system": {Messages: []model.Message{model.SystemText("a"), model.SystemText("b")}},
		"unknown role":  {Messages: []model.Message{{Role: "robot"}}},
		"bad choice":    {Tools: []model.ToolDefinition{readTool}, ToolChoice: &model.ToolChoice{Type: "maybe"}},
		"bad options":   {ProviderOptions: map[string]json.RawMessage{model.ProtocolOpenAICompat: json.RawMessage(`"x"`)}},
	} {
		if _, _, err := buildChatRequest(req, mc, defaultQuirks(), true); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// TestGolden_Stream pins chunk → canonical mapping and every tolerance of
// A11 §4.7, and checks each sequence against the ordering contract.
func TestGolden_Stream(t *testing.T) {
	for _, name := range []string{"vllm_tool_deltas", "ollama_whole_args", "tolerances", "azure_crlf_no_usage", "reasoning",
		"no_finish_reason", "midstream_error", "ended_early", "invalid_twice", "content_filter", "finished_then_eof"} {
		t.Run(name, func(t *testing.T) {
			s := newServer(t, fixture(t, name))
			st, err := client(t, ollamaCfg(s.srv.URL), nil, nil).Generate(context.Background(), req32(model.UserText("hi")))
			if err != nil {
				t.Fatal(err)
			}
			evs, err := model.Collect(st)
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

func assemble(t *testing.T, name string) (model.Response, error) {
	s := newServer(t, fixture(t, name))
	st, err := client(t, ollamaCfg(s.srv.URL), nil, nil).Generate(context.Background(), req32(model.UserText("hi")))
	if err != nil {
		t.Fatal(err)
	}
	evs, err := model.Collect(st)
	if err != nil {
		t.Fatal(err)
	}
	return model.Assemble(evs)
}

func TestStream_Semantics(t *testing.T) {
	r, err := assemble(t, "vllm_tool_deltas")
	if err != nil {
		t.Fatal(err)
	}
	if u := r.ToolUses(); len(u) != 1 || u[0].ID != "chatcmpl-tool-a" || string(u[0].Input) != `{"path":"src/app.ts"}` {
		t.Fatalf("tool uses = %+v", u)
	}
	if r.Usage.InputTokens != 120 || r.Usage.CachedInputTokens != 64 || r.StopReason != model.StopToolUse || r.Text() != "Reading." {
		t.Fatalf("response = %+v", r)
	}

	r, _ = assemble(t, "ollama_whole_args")
	if r.StopReason != model.StopToolUse || len(r.ToolUses()) != 1 {
		t.Fatalf("finish_reason stop with tool calls must be tool_use: %+v", r)
	}

	r, _ = assemble(t, "tolerances")
	u := r.ToolUses()
	if len(u) != 2 || u[0].ID != "call_1" || string(u[0].Input) != `{"path":"a"}` || u[1].ID != "b" || r.StopReason != model.StopToolUse {
		t.Fatalf("tolerances = %+v", r)
	}
	if len(r.Notes) == 0 || r.Notes[0] != "synthesized tool call id" {
		t.Fatalf("notes = %v", r.Notes)
	}

	r, _ = assemble(t, "azure_crlf_no_usage")
	if r.Text() != "Hi there" || r.StopReason != model.StopMaxTokens || !r.Usage.Estimated || r.Usage.InputTokens == 0 {
		t.Fatalf("azure = %+v", r)
	}

	r, _ = assemble(t, "reasoning")
	if r.Usage.ReasoningTokens != 4 || r.Text() != "Answer" || r.Message.Content[0].Type != model.BlockReasoning {
		t.Fatalf("reasoning = %+v", r)
	}

	r, _ = assemble(t, "content_filter")
	if r.StopReason != model.StopContentFilter {
		t.Fatalf("content filter stop = %s", r.StopReason)
	}

	for _, name := range []string{"midstream_error", "ended_early", "invalid_twice"} {
		if _, err := assemble(t, name); !errors.Is(err, model.ErrProviderUnavailable) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// TestAuthModes_Headers covers the auth matrix of A11 §5.1 on the wire.
func TestAuthModes_Headers(t *testing.T) {
	cases := []struct {
		name       string
		auth       model.AuthConfig
		credHeader string
		wantHeader string
		wantValue  string
		absent     string
	}{
		{"none", model.AuthConfig{Mode: model.AuthNone}, "", "", "", "Authorization"},
		{"api_key bearer", model.AuthConfig{Mode: model.AuthAPIKey, Secret: "secret://providers/p/api_key"}, "Authorization", "Authorization", "Bearer " + providertest.FakeKey, "api-key"},
		{"azure api-key header", model.AuthConfig{Mode: model.AuthAPIKey, Header: "api-key", Secret: "secret://providers/p/api_key"}, "api-key", "api-key", providertest.FakeKey, "Authorization"},
		{"gateway bearer", model.AuthConfig{Mode: model.AuthGateway, Kind: model.KindBearer, Secret: "secret://providers/p/token"}, "Authorization", "Authorization", "Bearer " + providertest.FakeKey, "api-key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newServer(t, fixture(t, "no_finish_reason"))
			cfg := ollamaCfg(s.srv.URL)
			cfg.ID, cfg.Auth = "p", c.auth
			creds := &providertest.FakeCredentials{Header: c.credHeader, Values: map[string]string{c.auth.Secret: providertest.FakeKey}}
			if c.credHeader == "api-key" {
				creds.Header = "api-key"
			}
			// The secrets broker decides header and scheme; mirror it here.
			fc := &schemeCreds{inner: creds, scheme: map[string]string{"Authorization": "Bearer"}}
			st, err := client(t, cfg, fc, nil).Generate(context.Background(), req32(model.UserText("hi")))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = model.Collect(st)
			h := s.reqs[0].header
			if c.wantHeader != "" && h.Get(c.wantHeader) != c.wantValue {
				t.Fatalf("%s = %q", c.wantHeader, h.Get(c.wantHeader))
			}
			if h.Get(c.absent) != "" {
				t.Fatalf("%s must be absent, got %q", c.absent, h.Get(c.absent))
			}
			if c.auth.Mode == model.AuthNone && len(creds.Accesses) != 0 {
				t.Fatal("mode none read a credential")
			}
		})
	}
}

// schemeCreds sets the scheme per header like internal/secrets does.
type schemeCreds struct {
	inner  *providertest.FakeCredentials
	scheme map[string]string
}

func (s *schemeCreds) Credential(ctx context.Context, ref, consumer string) (*model.Credential, error) {
	c, err := s.inner.Credential(ctx, ref, consumer)
	if err != nil {
		return nil, err
	}
	c.Scheme = s.scheme[c.Header]
	return c, nil
}

// TestAuthModes_MTLS: the client certificate comes from the credential
// source and no auth header is sent (A11 §5.4).
func TestAuthModes_MTLS(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "warden"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	pool := x509.NewCertPool()
	pool.AddCert(ca)

	var gotAuth string
	var gotCN string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if len(r.TLS.PeerCertificates) > 0 {
			gotCN = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	srvCert := srv.Certificate()
	_ = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvCert.Raw}), 0o600)

	cfg := ollamaCfg(srv.URL)
	cfg.ID, cfg.Tier = "company-vllm", model.T1
	cfg.Auth = model.AuthConfig{Mode: model.AuthGateway, Kind: model.KindMTLS, Secret: "secret://providers/company-vllm/client_key", CAFile: caFile}
	creds := &providertest.FakeCredentials{Cert: &tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}
	st, err := client(t, cfg, creds, nil).Generate(context.Background(), req32(model.UserText("hi")))
	if err != nil {
		t.Fatal(err)
	}
	r, err := model.Assemble(mustCollect(t, st))
	if err != nil || r.Text() != "ok" {
		t.Fatalf("response = %+v %v", r, err)
	}
	if gotAuth != "" || gotCN != "warden" {
		t.Fatalf("auth header %q, client cert CN %q", gotAuth, gotCN)
	}
	if len(creds.Accesses) == 0 || creds.Accesses[0].Consumer != "adapter:company-vllm" {
		t.Fatalf("accesses = %+v", creds.Accesses)
	}
}

func mustCollect(t *testing.T, s model.Stream) []model.StreamEvent {
	evs, err := model.Collect(s)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// TestShapeRetry_LearnsQuirkAndResendsOnce (A11 §4.8).
func TestShapeRetry_LearnsQuirkAndResendsOnce(t *testing.T) {
	s := newServer(t,
		reply{400, "application/json", `{"error":{"message":"Unrecognized request argument supplied: stream_options","type":"invalid_request_error"}}`},
		fixture(t, "no_finish_reason"))
	cache := &providertest.FakeProbeCache{}
	c := client(t, ollamaCfg(s.srv.URL), nil, cache)
	st, err := c.Generate(context.Background(), req32(model.UserText("hi")))
	if err != nil {
		t.Fatal(err)
	}
	_ = mustCollect(t, st)
	if len(s.reqs) != 2 || bytes.Contains(s.reqs[1].body, []byte("stream_options")) || !bytes.Contains(s.reqs[0].body, []byte("stream_options")) {
		t.Fatalf("requests:\n%s\n%s", s.reqs[0].body, s.reqs[len(s.reqs)-1].body)
	}
	if len(cache.Learned) != 1 || cache.Learned[0] != "ollama/local/qwen-coder-32b stream_usage=false" {
		t.Fatalf("learned = %v", cache.Learned)
	}
	// The learned quirk sticks for the next call.
	s.resp = []reply{fixture(t, "no_finish_reason")}
	st, _ = c.Generate(context.Background(), req32(model.UserText("again")))
	_ = mustCollect(t, st)
	if bytes.Contains(s.reqs[2].body, []byte("stream_options")) {
		t.Fatal("quirk not remembered")
	}
}

func TestShapeRetry_OnlyOnceAndOnlyForSentFields(t *testing.T) {
	bad := reply{400, "application/json", `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`}
	s := newServer(t, bad, bad, bad)
	_, err := client(t, ollamaCfg(s.srv.URL), nil, nil).Generate(context.Background(), req32(model.UserText("hi")))
	if !errors.Is(err, model.ErrInvalidRequest) || len(s.reqs) != 2 {
		t.Fatalf("err = %v after %d requests", err, len(s.reqs))
	}
	unrelated := reply{400, "application/json", `{"error":{"message":"unknown field: logit_bias"}}`}
	s2 := newServer(t, unrelated)
	_, err = client(t, ollamaCfg(s2.srv.URL), nil, nil).Generate(context.Background(), req32(model.UserText("hi")))
	if !errors.Is(err, model.ErrInvalidRequest) || len(s2.reqs) != 1 {
		t.Fatalf("unrelated field retried: %v, %d requests", err, len(s2.reqs))
	}
}

func TestLearn_FieldToFlag(t *testing.T) {
	q := defaultQuirks()
	all := sentFields{streamOptions: true, parallel: true, maxTokens: true, temperature: true, responseFormat: true, toolChoice: "named"}
	cases := []struct {
		body  string
		sent  sentFields
		flag  string
		value any
	}{
		{"parallel_tool_calls is not supported", sentFields{parallel: true}, "parallel_tool_calls_param", false},
		{"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.", sentFields{maxTokens: true}, "max_tokens_field", "max_completion_tokens"},
		{"Unsupported value: 'temperature' does not support 0.2", sentFields{temperature: true}, "omit_temperature", true},
		{"tool_choice object is not supported", sentFields{toolChoice: "named"}, "tool_choice_named", false},
		{"tool_choice 'required' is not supported", sentFields{toolChoice: "required"}, "tool_choice_required", false},
		{"response_format json_schema unsupported", sentFields{responseFormat: true}, "response_format", "json_object"},
		{"stream_options unknown", all, "stream_usage", false},
	}
	for _, c := range cases {
		flag, value, ok := learn(c.body, c.sent, q)
		if !ok || flag != c.flag || value != c.value {
			t.Errorf("%q → %s=%v ok=%v", c.body, flag, value, ok)
		}
	}
	jo := defaultQuirks()
	jo.ResponseFormat = "json_object"
	if f, v, ok := learn("response_format not supported", sentFields{responseFormat: true}, jo); !ok || f != "response_format" || v != "none" {
		t.Errorf("json_object step-down = %s %v", f, v)
	}
	if _, _, ok := learn("everything is fine", all, q); ok {
		t.Error("learned from an unrelated body")
	}
}

// TestGenerate_HTTPErrors covers A11 §8 rows 15–25.
func TestGenerate_HTTPErrors(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   model.ErrorCode
		retry  bool
		note   string
	}{
		{400, `{"error":{"message":"This model's maximum context length is 32768 tokens","code":"context_length_exceeded"}}`, model.ErrContextTooLong, false, ""},
		{422, `{"error":{"message":"prompt is too long"}}`, model.ErrContextTooLong, false, ""},
		{400, `{"error":{"message":"registry.ollama.ai/library/codellama does not support tools"}}`, model.ErrToolFormatUnsupported, false, ""},
		{400, `{"error":{"message":"filtered","code":"content_filter"}}`, model.ErrContentFiltered, false, ""},
		{400, `{"error":{"message":"bad","innererror":{"code":"ResponsibleAIPolicyViolation"}}}`, model.ErrContentFiltered, false, ""},
		{400, `{"error":{"message":"bad request"}}`, model.ErrInvalidRequest, false, ""},
		{401, `{"error":{"message":"Incorrect API key"}}`, model.ErrAuthFailed, false, ""},
		{403, `forbidden`, model.ErrAuthFailed, false, ""},
		{404, `{"error":{"message":"The API deployment for this resource does not exist.","code":"DeploymentNotFound"}}`, model.ErrModelNotFound, false, ""},
		{404, `{"error":"model \"qwen9\" not found, try pulling it first"}`, model.ErrModelNotFound, false, ""},
		{404, `404 page not found`, model.ErrProviderUnavailable, false, "endpoint not found"},
		{408, `timeout`, model.ErrTimeout, true, ""},
		{429, `{"error":{"message":"quota","code":"insufficient_quota"}}`, model.ErrRateLimited, false, ""},
		{429, `{"error":{"message":"slow down"}}`, model.ErrRateLimited, true, ""},
		{503, `{"error":"server busy, please try again"}`, model.ErrProviderUnavailable, true, ""},
		{200, `{"not":"an event stream"}`, model.ErrProviderUnavailable, true, ""},
	}
	for _, c := range cases {
		s := newServer(t, reply{c.status, "application/json", c.body})
		_, err := client(t, ollamaCfg(s.srv.URL), nil, nil).Generate(context.Background(), req32(model.UserText("hi")))
		var me *model.Error
		if !errors.As(err, &me) || me.Code != c.code || me.Retryable != c.retry || !strings.Contains(string(me.Details), c.note) {
			t.Errorf("%d %s: %+v", c.status, c.body, me)
		}
	}
}

func TestGenerate_RetryAfterAzure(t *testing.T) {
	s := newServer(t, reply{429, "application/json", `{"error":{"message":"rate"}}`})
	s.hdr = map[string]string{"retry-after-ms": "2500"}
	_, err := client(t, ollamaCfg(s.srv.URL), nil, nil).Generate(context.Background(), req32(model.UserText("hi")))
	var me *model.Error
	if !errors.As(err, &me) || me.RetryAfter != 2500*time.Millisecond {
		t.Fatalf("err = %+v", me)
	}
}

// TestGenerate_NonStreamingFallback: stream_tools false sends stream:false
// and synthesizes the canonical sequence.
func TestGenerate_NonStreamingFallback(t *testing.T) {
	cache := &providertest.FakeProbeCache{}
	cfg := ollamaCfg("")
	full := `{"id":"r1","model":"qwen2.5-coder:7b","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"fs__read","arguments":"{\"path\":\"x\"}"}},{"id":"c2","type":"function","function":{"name":"fs__list","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`
	s := newServer(t, reply{200, "application/json", full})
	cfg.BaseURL = s.srv.URL
	_ = cache.Store(model.ProbeResult{ProviderID: "ollama", EndpointHash: EndpointHash(cfg), Models: []model.ModelProbe{{ModelID: "local/qwen-coder-32b", ToolCalling: "native", Quirks: map[string]any{"stream_tools": false}}}})
	st, err := client(t, cfg, nil, cache).Generate(context.Background(), model.ModelRequest{ModelID: "local/qwen-coder-32b", Tools: []model.ToolDefinition{readTool}, Messages: []model.Message{model.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := mustCollect(t, st)
	if err := model.ValidateSequence(evs); err != nil {
		t.Fatal(err)
	}
	r, _ := model.Assemble(evs)
	if len(r.ToolUses()) != 2 || r.StopReason != model.StopToolUse || r.Usage.InputTokens != 9 || !bytes.Contains(s.reqs[0].body, []byte(`"stream":false`)) {
		t.Fatalf("response = %+v body %s", r, s.reqs[0].body)
	}
}

func TestCapabilities_QuirksAndCache(t *testing.T) {
	cache := &providertest.FakeProbeCache{}
	cfg := ollamaCfg("http://127.0.0.1:11434/v1")
	_ = cache.Store(model.ProbeResult{ProviderID: "ollama", EndpointHash: EndpointHash(cfg), Models: []model.ModelProbe{{ModelID: "local/qwen-coder-32b", ToolCalling: "native", StructuredOutput: true, Streaming: true, Quirks: map[string]any{"tools": false, "tool_choice_named": false}}}})
	c := client(t, cfg, nil, cache)
	caps, err := c.Capabilities("local/qwen-coder-32b")
	if err != nil || caps.ToolCalling != model.ToolCallingEmulated || caps.ToolChoiceNamed {
		t.Fatalf("caps = %+v %v", caps, err)
	}
	// A cache for another endpoint is ignored.
	other := ollamaCfg("http://127.0.0.1:1234/v1")
	c2 := client(t, other, nil, cache)
	if caps, _ := c2.Capabilities("local/qwen-coder-32b"); caps.ToolCalling != model.ToolCallingNative {
		t.Fatalf("stale cache applied: %+v", caps)
	}
	if _, err := c.Capabilities("x"); !errors.Is(err, model.ErrModelNotFound) {
		t.Fatal("unknown model accepted")
	}
	if n, err := c.CountTokens(context.Background(), req32(model.UserText(strings.Repeat("x", 3200)))); err != nil || n < 1000 {
		t.Fatalf("count = %d %v", n, err)
	}
}

func TestNew_RejectsUnsupportedAuth(t *testing.T) {
	for _, a := range []model.AuthConfig{{Mode: "cloud_iam"}, {Mode: model.AuthGateway, Kind: "sso-oidc"}} {
		cfg := ollamaCfg("https://x.example/v1")
		cfg.Auth = a
		if _, err := New(cfg, nil, nil); err == nil {
			t.Errorf("%+v accepted", a)
		}
	}
	cfg := ollamaCfg("https://x.example/v1")
	cfg.Protocol = model.ProtocolAnthropic
	if _, err := New(cfg, nil, nil); err == nil {
		t.Error("wrong protocol accepted")
	}
}

func TestGenerate_CredentialNeverInErrors(t *testing.T) {
	s := newServer(t, reply{401, "application/json", `{"error":{"message":"Incorrect API key provided: ` + providertest.FakeKey[:10] + `***"}}`})
	cfg := ollamaCfg(s.srv.URL)
	cfg.ID, cfg.Auth = "p", model.AuthConfig{Mode: model.AuthAPIKey, Secret: "secret://providers/p/api_key"}
	creds := &providertest.FakeCredentials{Values: map[string]string{"secret://providers/p/api_key": providertest.FakeKey}}
	_, err := client(t, cfg, creds, nil).Generate(context.Background(), req32(model.UserText("hi")))
	var me *model.Error
	if !errors.As(err, &me) || strings.Contains(me.Error(), providertest.FakeKey) || strings.Contains(string(me.Details), providertest.FakeKey) {
		t.Fatalf("err = %+v", me)
	}
}
