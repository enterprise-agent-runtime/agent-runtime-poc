// Package openaicompat implements the openai-compatible adapter (WRD-05
// §5–§8, design A11 §4, §5, §8, §9, §10): local servers (Ollama, LM Studio),
// company-hosted vLLM/TGI/Ollama behind a gateway, internal gateways, Azure
// OpenAI and OpenAI, all through one code path. Deployments differ only in
// data (base URL, auth mode, probed quirks), never in branches on vendor
// names. Auth modes: none, api_key (Bearer or Azure api-key header) and
// gateway (bearer or mTLS); the credential is injected host-side.
package openaicompat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/probe"
	"warden.dev/warden/internal/model/wire"
)

// Client is the openai-compatible provider.
type Client struct {
	cfg    model.ProviderConfig
	hc     *http.Client
	cache  model.ProbeCache
	health *wire.Health
	now    func() time.Time

	mu     sync.Mutex
	quirks map[string]*Quirks // model id → quirks
}

// New builds the adapter; it matches model.ProviderFactory.
func New(cfg model.ProviderConfig, creds model.CredentialSource, cache model.ProbeCache) (model.Provider, error) {
	if cfg.Protocol != model.ProtocolOpenAICompat {
		return nil, fmt.Errorf("provider %s: protocol %q is not %s", cfg.ID, cfg.Protocol, model.ProtocolOpenAICompat)
	}
	switch {
	case cfg.Auth.Mode == model.AuthNone, cfg.Auth.Mode == model.AuthAPIKey:
	case cfg.Auth.Mode == model.AuthGateway && (cfg.Auth.Kind == model.KindBearer || cfg.Auth.Kind == model.KindMTLS):
	default:
		return nil, fmt.Errorf("provider %s: auth %s/%s is not supported in the PoC", cfg.ID, cfg.Auth.Mode, cfg.Auth.Kind)
	}
	hc, err := wire.NewHTTPClient(cfg, creds)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, hc: hc, cache: cache, health: wire.NewHealth(), now: time.Now, quirks: map[string]*Quirks{}}, nil
}

// ID returns the provider id.
func (c *Client) ID() string { return c.cfg.ID }

// Health reports rate-limit headers and the last error.
func (c *Client) Health() model.ProviderHealth { return c.health.Snapshot() }

func (c *Client) modelConfig(id string) (model.ModelConfig, error) {
	for _, m := range c.cfg.Models {
		if m.ID == id {
			return m, nil
		}
	}
	return model.ModelConfig{}, &model.Error{Code: model.ErrModelNotFound, Message: "model is not in the catalog for this provider"}
}

func (c *Client) cachedProbe(modelID string) *model.ModelProbe {
	if c.cache == nil {
		return nil
	}
	r, err := c.cache.Load(c.cfg.ID)
	if err != nil || r == nil || r.EndpointHash != EndpointHash(c.cfg) {
		return nil
	}
	for i := range r.Models {
		if r.Models[i].ModelID == modelID {
			return &r.Models[i]
		}
	}
	return nil
}

// quirksFor returns the model's quirks: defaults, overlaid with the probe
// cache, overlaid with anything learned in this process.
func (c *Client) quirksFor(modelID string) Quirks {
	c.mu.Lock()
	defer c.mu.Unlock()
	if q, ok := c.quirks[modelID]; ok {
		return *q
	}
	q := defaultQuirks()
	if mp := c.cachedProbe(modelID); mp != nil {
		q.apply(mp.Quirks)
	}
	c.quirks[modelID] = &q
	return q
}

func (c *Client) learnQuirk(modelID, flag string, value any) {
	c.mu.Lock()
	q, ok := c.quirks[modelID]
	if !ok {
		d := defaultQuirks()
		q = &d
		c.quirks[modelID] = q
	}
	q.set(flag, value)
	c.mu.Unlock()
	slog.Info("quirk learned", "provider", c.cfg.ID, "model", modelID, "flag", flag, "value", value)
	if c.cache != nil {
		_ = c.cache.LearnQuirk(c.cfg.ID, modelID, flag, value)
	}
}

// Capabilities returns the effective catalog entry (declared ∧ probed).
func (c *Client) Capabilities(modelID string) (model.ModelCapabilities, error) {
	mc, err := c.modelConfig(modelID)
	if err != nil {
		return model.ModelCapabilities{}, err
	}
	caps := model.Effective(mc.Capabilities, c.cachedProbe(modelID))
	q := c.quirksFor(modelID)
	caps.ToolChoiceNamed = q.ToolChoiceNamed
	if !q.Tools && caps.ToolCalling == model.ToolCallingNative {
		caps.ToolCalling = model.ToolCallingEmulated
	}
	if caps.StructuredMode == "" {
		caps.StructuredMode = q.ResponseFormat
	}
	return caps, nil
}

func (c *Client) endpoint(path string) string { return strings.TrimRight(c.cfg.BaseURL, "/") + path }

// post sends a chat request.
func (c *Client) post(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("content-type", "application/json")
	if stream {
		r.Header.Set("accept", "text/event-stream")
	}
	return c.hc.Do(r)
}

// Generate streams one chat/completions call. A 400/422 naming an optional
// field the server does not support flips that quirk and re-sends once; it
// is the only retry an adapter performs (A11 §4.8).
func (c *Client) Generate(ctx context.Context, req model.ModelRequest) (model.Stream, error) {
	mc, err := c.modelConfig(req.ModelID)
	if err != nil {
		return nil, err
	}
	_, first, idle := wire.Timeouts(string(c.cfg.Tier), 0, c.cfg.Timeouts.FirstByteMS, c.cfg.Timeouts.IdleMS)
	for attempt := 0; ; attempt++ {
		q := c.quirksFor(req.ModelID)
		stream := q.StreamTools || len(req.Tools) == 0 || !q.Tools
		body, sent, err := buildChatRequest(req, mc, q, stream)
		if err != nil {
			return nil, &model.Error{Code: model.ErrInvalidRequest, Message: "the request cannot be expressed for this provider", Details: wire.DetailsJSON(wire.Details{Note: err.Error()})}
		}
		estIn := estimate(len(body))
		wctx, wd := wire.StartWatchdog(ctx, first, idle)
		resp, err := c.post(wctx, body, stream)
		if err != nil {
			wd.Stop()
			e := wire.TransportError(err, ctx, wd)
			c.health.Fail(e.Code, c.now())
			return nil, e
		}
		c.health.Observe(resp.Header, c.now())
		ctype := resp.Header.Get("content-type")
		if resp.StatusCode/100 == 2 && stream && strings.HasPrefix(ctype, "text/event-stream") {
			return newChunkStream(ctx, resp, wd, c.cfg.ID, estIn), nil
		}
		raw := wire.ReadErrorBody(resp.Body)
		resp.Body.Close()
		wd.Stop()
		if resp.StatusCode/100 == 2 && !stream {
			return &model.SliceStream{Events: buffered(raw, c.cfg.ID, resp.Header.Get("x-request-id"), estIn)}, nil
		}
		if (resp.StatusCode == 400 || resp.StatusCode == 422) && attempt == 0 {
			if flag, value, ok := learn(string(raw), sent, q); ok {
				c.learnQuirk(req.ModelID, flag, value)
				continue
			}
		}
		e := normalizeHTTP(resp, raw, c.now())
		c.health.Fail(e.Code, c.now())
		return nil, e
	}
}

// CountTokens estimates with the 3.2-bytes-per-token rule (A11 §6.5).
func (c *Client) CountTokens(_ context.Context, req model.ModelRequest) (int, error) {
	mc, err := c.modelConfig(req.ModelID)
	if err != nil {
		return 0, err
	}
	body, _, err := buildChatRequest(req, mc, c.quirksFor(req.ModelID), false)
	if err != nil {
		return 0, model.NewError(model.ErrInvalidRequest, err.Error())
	}
	return estimate(len(body)), nil
}

var errorMessages = map[model.ErrorCode]string{
	model.ErrRateLimited:           "the provider is rate limiting requests",
	model.ErrAuthFailed:            "the provider rejected the credentials; run warden provider test",
	model.ErrContextTooLong:        "the request is larger than the model's context window",
	model.ErrProviderUnavailable:   "the provider is unavailable",
	model.ErrInvalidRequest:        "the provider rejected the request",
	model.ErrModelNotFound:         "the provider does not know this model",
	model.ErrToolFormatUnsupported: "the model does not support tool calling",
	model.ErrContentFiltered:       "the provider's content filter blocked the request",
	model.ErrTimeout:               "the provider did not respond in time",
}

var (
	reContext  = regexp.MustCompile(`(?i)maximum context length|context length|context window|too many tokens|prompt is too long|input is too long|exceeds the model`)
	reNoTools  = regexp.MustCompile(`(?i)does not support tools|tools? (are|is) not supported|tool calling is not supported|tool_choice.*not supported|does not support function`)
	reNotFound = regexp.MustCompile(`(?i)model .*(not found|does not exist)|no such model`)
)

type wireErr struct {
	Message    string          `json:"message"`
	Type       string          `json:"type"`
	Code       json.RawMessage `json:"code"`
	InnerError *struct {
		Code string `json:"code"`
	} `json:"innererror"`
}

func (w wireErr) code() string {
	var s string
	if json.Unmarshal(w.Code, &s) == nil {
		return s
	}
	return strings.Trim(string(w.Code), `"`)
}

// classify maps status and error body (A11 §8 rows 15–26). status 0 means
// an error inside the stream.
func classify(status int, we wireErr, body string) (model.ErrorCode, bool) {
	text := we.Message + " " + body
	code := we.code()
	inner := ""
	if we.InnerError != nil {
		inner = we.InnerError.Code
	}
	switch {
	case (status == 400 || status == 422 || status == 0) && (code == "context_length_exceeded" || reContext.MatchString(text)):
		return model.ErrContextTooLong, false
	case (status == 400 || status == 422 || status == 0) && reNoTools.MatchString(text):
		return model.ErrToolFormatUnsupported, false
	case (status == 400 || status == 0) && (code == "content_filter" || inner == "ResponsibleAIPolicyViolation"):
		return model.ErrContentFiltered, false
	case status == 400 || status == 422:
		return model.ErrInvalidRequest, false
	case status == 401 || status == 403:
		return model.ErrAuthFailed, false
	case status == 404 && (code == "model_not_found" || code == "DeploymentNotFound" || reNotFound.MatchString(text)):
		return model.ErrModelNotFound, false
	case status == 404:
		return model.ErrProviderUnavailable, false
	case status == 408:
		return model.ErrTimeout, true
	case status == 429 && code == "insufficient_quota":
		return model.ErrRateLimited, false
	case status == 429:
		return model.ErrRateLimited, true
	case status >= 500 || status == 0:
		return model.ErrProviderUnavailable, true
	case status >= 400:
		return model.ErrInvalidRequest, false
	}
	return model.ErrProviderUnavailable, true // a 2xx that is not a usable answer
}

func parseErr(raw []byte) wireErr {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	var we wireErr
	if json.Unmarshal(raw, &env) == nil && len(env.Error) > 0 {
		if json.Unmarshal(env.Error, &we) != nil {
			_ = json.Unmarshal(env.Error, &we.Message) // {"error": "text"} (Ollama)
		}
	}
	return we
}

func normalizeHTTP(resp *http.Response, raw []byte, now time.Time) *model.Error {
	we := parseErr(raw)
	code, retry := classify(resp.StatusCode, we, string(raw))
	e := &model.Error{Code: code, Message: errorMessages[code], Retryable: retry, RetryAfter: wire.RetryAfter(resp.Header, now)}
	d := wire.Details{Status: resp.StatusCode, ProviderErrorType: we.Type, ProviderErrorCode: we.code(), RequestID: resp.Header.Get("x-request-id"), Body: string(raw)}
	if resp.StatusCode == 404 && code == model.ErrProviderUnavailable {
		d.Note = "endpoint not found; check base_url ends with /v1"
	}
	e.Details = wire.DetailsJSON(d)
	return e
}

// normalizeBody maps an in-stream {"error": …} payload (row 26).
func normalizeBody(status int, raw json.RawMessage) *model.Error {
	var we wireErr
	if json.Unmarshal(raw, &we) != nil {
		_ = json.Unmarshal(raw, &we.Message)
	}
	code, retry := classify(status, we, "")
	e := &model.Error{Code: code, Message: errorMessages[code], Retryable: retry}
	e.Details = wire.DetailsJSON(wire.Details{ProviderErrorType: we.Type, ProviderErrorCode: we.code(), Body: string(raw)})
	return e
}

// EndpointHash identifies the endpoint a probe result belongs to (A11 §9.4).
func EndpointHash(cfg model.ProviderConfig) string {
	h := sha256.Sum256([]byte(cfg.Protocol + "\n" + cfg.BaseURL + "\n" + cfg.Auth.Mode + "/" + cfg.Auth.Kind))
	return "sha256:" + hex.EncodeToString(h[:])
}

// Probe runs provider.test (A11 §9): P0 model listing, P4 context metadata
// and the shared steps per catalog model.
func (c *Client) Probe(ctx context.Context) (model.ProbeResult, error) {
	res := model.ProbeResult{ProviderID: c.cfg.ID, ProbedAt: c.now().UTC(), EndpointHash: EndpointHash(c.cfg), Models: []model.ModelProbe{}}
	start := c.now()
	listed, meta, err := c.listModels(ctx)
	res.LatencyMS = int(c.now().Sub(start).Milliseconds())
	if err != nil {
		if me, ok := err.(*model.Error); !ok || me.Code != model.ErrProviderUnavailable || !strings.Contains(string(me.Details), "endpoint not found") {
			res.Error = probe.FirstError(err)
			return res, nil
		}
	}
	res.ModelsListed = listed
	res.OK = true
	for _, m := range c.cfg.Models {
		c.mu.Lock()
		delete(c.quirks, m.ID) // probe from defaults, not from a stale cache
		d := defaultQuirks()
		c.quirks[m.ID] = &d
		c.mu.Unlock()
		mp := probe.Model(ctx, c.Generate, m.ID, probe.Options{Tier: c.cfg.Tier, DeclaredCtx: m.Capabilities.MaxContext, SkipP4t: c.cfg.Tier != model.T0 && c.cfg.Tier != model.T1})
		mp.MaxContext = m.Capabilities.MaxContext
		if v := meta[m.Model]; v > 0 {
			mp.MaxContextMeta = &v
		} else if v := c.contextMeta(ctx, m.Model); v > 0 {
			mp.MaxContextMeta = &v
		}
		// Learned quirks (shape retries during the probe) join the record.
		q := c.quirksFor(m.ID)
		mp.Quirks["stream_tools"] = q.StreamTools
		mp.Quirks["response_format"] = q.ResponseFormat
		mp.Quirks["max_tokens_field"] = q.MaxTokensField
		mp.Quirks["omit_temperature"] = q.OmitTemperature
		mp.Quirks["system_role"] = q.SystemRole
		mp.Quirks["tool_choice_none"] = q.ToolChoiceNone
		if mp.StructuredOutput {
			mp.StructuredMode = q.ResponseFormat
		}
		res.Models = append(res.Models, mp)
	}
	if c.cache != nil {
		_ = c.cache.Store(res)
	}
	return res, nil
}

// listModels is P0; it also returns vLLM's max_model_len per model.
func (c *Client) listModels(ctx context.Context) ([]string, map[string]int, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/models"), nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		return nil, nil, wire.TransportError(err, ctx, nil)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, normalizeHTTP(resp, wire.ReadErrorBody(resp.Body), c.now())
	}
	var v struct {
		Data []struct {
			ID          string `json:"id"`
			MaxModelLen int    `json:"max_model_len"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&v); err != nil {
		return nil, nil, model.NewError(model.ErrProviderUnavailable, "invalid model list")
	}
	out, meta := []string{}, map[string]int{}
	for _, d := range v.Data {
		if len(out) < 500 {
			out = append(out, d.ID)
		}
		if d.MaxModelLen > 0 {
			meta[d.ID] = d.MaxModelLen
		}
	}
	return out, meta, nil
}

// contextMeta reads LM Studio's and Ollama's native model metadata from the
// host root of base_url (A11 §9.2 P4). Errors mean "unknown".
func (c *Client) contextMeta(ctx context.Context, wireModel string) int {
	u, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return 0
	}
	root := u.Scheme + "://" + u.Host
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	get := func(method, path string, body []byte, v any) bool {
		r, err := http.NewRequestWithContext(ctx, method, root+path, bytes.NewReader(body))
		if err != nil {
			return false
		}
		resp, err := c.hc.Do(r)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v) == nil
	}
	var lm struct {
		Loaded int `json:"loaded_context_length"`
		Max    int `json:"max_context_length"`
	}
	if get(http.MethodGet, "/api/v0/models/"+url.PathEscape(wireModel), nil, &lm) {
		if lm.Loaded > 0 {
			return lm.Loaded
		}
		if lm.Max > 0 {
			return lm.Max
		}
	}
	var ol struct {
		ModelInfo  map[string]any `json:"model_info"`
		Parameters string         `json:"parameters"`
	}
	body, _ := json.Marshal(map[string]string{"model": wireModel})
	if get(http.MethodPost, "/api/show", body, &ol) {
		for _, line := range strings.Split(ol.Parameters, "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == "num_ctx" {
				var n int
				if _, err := fmt.Sscanf(f[1], "%d", &n); err == nil && n > 0 {
					return n
				}
			}
		}
		for k, v := range ol.ModelInfo {
			if strings.HasSuffix(k, ".context_length") {
				if f, ok := v.(float64); ok && f > 0 {
					return int(f)
				}
			}
		}
	}
	return 0
}

// ListModels returns the model names an endpoint lists (GET {base_url}/models);
// the daemon uses it when a provider is added without models (discovery
// for local servers such as Ollama, docs/DECISIONS-poc.md D-015).
func ListModels(ctx context.Context, cfg model.ProviderConfig, creds model.CredentialSource) ([]string, error) {
	p, err := New(cfg, creds, nil)
	if err != nil {
		return nil, err
	}
	names, _, err := p.(*Client).listModels(ctx)
	return names, err
}
