// Package anthropic implements the anthropic-messages adapter (WRD-05 §5–§8,
// design A11 §3, §5, §8, §9). It is a hand-written client on net/http and
// the shared SSE reader: anthropic-sdk-go is not an allowed dependency
// (CLAUDE.md §4) and A11 names this wire-level path as its fallback
// (docs/DECISIONS-poc.md D-005). The adapter never retries, falls back or
// executes tools; the credential is injected host-side per request.
package anthropic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/probe"
	"warden.dev/warden/internal/model/wire"
)

// APIVersion is the anthropic-version header (A11 §3.1). No anthropic-beta.
const APIVersion = "2023-06-01"

// Adapter is the anthropic-messages provider.
type Adapter struct {
	cfg    model.ProviderConfig
	hc     *http.Client
	cache  model.ProbeCache
	health *wire.Health
	now    func() time.Time
}

// New builds the adapter; it matches model.ProviderFactory.
func New(cfg model.ProviderConfig, creds model.CredentialSource, cache model.ProbeCache) (model.Provider, error) {
	if cfg.Protocol != model.ProtocolAnthropic {
		return nil, fmt.Errorf("provider %s: protocol %q is not %s", cfg.ID, cfg.Protocol, model.ProtocolAnthropic)
	}
	if cfg.Auth.Mode != model.AuthAPIKey {
		return nil, fmt.Errorf("provider %s: anthropic-messages supports auth mode api_key only", cfg.ID)
	}
	hc, err := wire.NewHTTPClient(cfg, creds)
	if err != nil {
		return nil, err
	}
	return &Adapter{cfg: cfg, hc: hc, cache: cache, health: wire.NewHealth(), now: time.Now}, nil
}

// ID returns the provider id.
func (a *Adapter) ID() string { return a.cfg.ID }

// Health reports rate-limit headers and the last error.
func (a *Adapter) Health() model.ProviderHealth { return a.health.Snapshot() }

func (a *Adapter) modelConfig(id string) (model.ModelConfig, error) {
	for _, m := range a.cfg.Models {
		if m.ID == id {
			return m, nil
		}
	}
	return model.ModelConfig{}, &model.Error{Code: model.ErrModelNotFound, Message: "model is not in the catalog for this provider"}
}

// Capabilities returns the effective catalog entry (declared ∧ probed).
func (a *Adapter) Capabilities(modelID string) (model.ModelCapabilities, error) {
	mc, err := a.modelConfig(modelID)
	if err != nil {
		return model.ModelCapabilities{}, err
	}
	var mp *model.ModelProbe
	if a.cache != nil {
		if r, err := a.cache.Load(a.cfg.ID); err == nil && r != nil {
			for i := range r.Models {
				if r.Models[i].ModelID == modelID {
					mp = &r.Models[i]
				}
			}
		}
	}
	c := model.Effective(mc.Capabilities, mp)
	c.StructuredMode, c.ToolChoiceNamed = "tool_forcing", true
	return c, nil
}

func (a *Adapter) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.cfg.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("anthropic-version", APIVersion)
	r.Header.Set("content-type", "application/json")
	return r, nil
}

// Generate streams one Messages API call.
func (a *Adapter) Generate(ctx context.Context, req model.ModelRequest) (model.Stream, error) {
	mc, err := a.modelConfig(req.ModelID)
	if err != nil {
		return nil, err
	}
	body, forcing, err := buildRequest(req, mc, a.cfg.ID, true)
	if err != nil {
		return nil, &model.Error{Code: model.ErrInvalidRequest, Message: "the request cannot be expressed for this provider", Details: wire.DetailsJSON(wire.Details{Note: err.Error()})}
	}
	_, first, idle := wire.Timeouts(string(a.cfg.Tier), 0, a.cfg.Timeouts.FirstByteMS, a.cfg.Timeouts.IdleMS)
	wctx, wd := wire.StartWatchdog(ctx, first, idle)
	hr, err := a.newRequest(wctx, http.MethodPost, "/v1/messages", body)
	if err != nil {
		wd.Stop()
		return nil, model.NewError(model.ErrInvalidRequest, "invalid request")
	}
	hr.Header.Set("accept", "text/event-stream")
	resp, err := a.hc.Do(hr)
	if err != nil {
		wd.Stop()
		e := wire.TransportError(err, ctx, wd)
		a.health.Fail(e.Code, a.now())
		return nil, e
	}
	a.health.Observe(resp.Header, a.now())
	if resp.StatusCode/100 != 2 || !strings.HasPrefix(resp.Header.Get("content-type"), "text/event-stream") {
		defer wd.Stop()
		defer resp.Body.Close()
		e := normalizeHTTP(resp, a.now())
		a.health.Fail(e.Code, a.now())
		return nil, e
	}
	s := &eventStream{ctx: ctx, resp: resp, wd: wd, provider: a.cfg.ID, reqID: resp.Header.Get("request-id"), forcing: forcing, blocks: map[int]*blockState{}}
	s.sse = wire.NewSSEReader(resp.Body, wd.Touch)
	return s, nil
}

// CountTokens uses POST /v1/messages/count_tokens (exact); on failure it
// falls back to the 3.2-bytes-per-token estimate.
func (a *Adapter) CountTokens(ctx context.Context, req model.ModelRequest) (int, error) {
	mc, err := a.modelConfig(req.ModelID)
	if err != nil {
		return 0, err
	}
	body, _, err := buildRequest(req, mc, a.cfg.ID, false)
	if err != nil {
		return 0, model.NewError(model.ErrInvalidRequest, err.Error())
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(body, &m)
	delete(m, "max_tokens")
	delete(m, "stream")
	body, _ = json.Marshal(m)
	hr, err := a.newRequest(ctx, http.MethodPost, "/v1/messages/count_tokens", body)
	if err == nil {
		if resp, err := a.hc.Do(hr); err == nil {
			defer resp.Body.Close()
			var v struct {
				InputTokens int `json:"input_tokens"`
			}
			if resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v) == nil {
				return v.InputTokens, nil
			}
		}
	}
	return (len(body)*10 + 31) / 32, nil
}

// errorMessages are the short fixed sentences per code (B07).
var errorMessages = map[model.ErrorCode]string{
	model.ErrRateLimited:         "the provider is rate limiting requests",
	model.ErrAuthFailed:          "the provider rejected the credentials; run warden provider test",
	model.ErrContextTooLong:      "the request is larger than the model's context window",
	model.ErrProviderUnavailable: "the provider is unavailable",
	model.ErrInvalidRequest:      "the provider rejected the request",
	model.ErrModelNotFound:       "the provider does not know this model",
}

var contextTooLong = regexp.MustCompile(`(?i)prompt is too long|too many tokens|exceed.*context|context window`)

// normalizeType maps an Anthropic error type (A11 §8 rows 6–14).
func normalizeType(we wireError, status int) *model.Error {
	code := model.ErrProviderUnavailable
	retry := true
	switch {
	case we.Type == "invalid_request_error" && contextTooLong.MatchString(we.Message):
		code, retry = model.ErrContextTooLong, false
	case we.Type == "invalid_request_error":
		code, retry = model.ErrInvalidRequest, false
	case we.Type == "authentication_error" || we.Type == "permission_error" || status == 401 || status == 403:
		code, retry = model.ErrAuthFailed, false
	case we.Type == "not_found_error" || status == 404:
		code, retry = model.ErrModelNotFound, false
	case we.Type == "request_too_large" || status == 413:
		code, retry = model.ErrContextTooLong, false
	case we.Type == "rate_limit_error" || status == 429:
		code, retry = model.ErrRateLimited, true
	case we.Type == "overloaded_error", we.Type == "api_error", status >= 500:
		code, retry = model.ErrProviderUnavailable, true
	case status >= 400:
		code, retry = model.ErrInvalidRequest, false
	}
	e := &model.Error{Code: code, Message: errorMessages[code], Retryable: retry}
	if e.Message == "" {
		e.Message = string(code)
	}
	return e
}

func normalizeHTTP(resp *http.Response, now time.Time) *model.Error {
	body := wire.ReadErrorBody(resp.Body)
	var env struct {
		Error wireError `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	e := normalizeType(env.Error, resp.StatusCode)
	if resp.StatusCode/100 == 2 {
		e = &model.Error{Code: model.ErrProviderUnavailable, Message: errorMessages[model.ErrProviderUnavailable], Retryable: true}
	}
	e.RetryAfter = wire.RetryAfter(resp.Header, now)
	e.Details = wire.DetailsJSON(wire.Details{Status: resp.StatusCode, ProviderErrorType: env.Error.Type, RequestID: resp.Header.Get("request-id"), Body: string(body)})
	return e
}

// Probe runs provider.test (A11 §9): P0 model listing, then the shared
// steps per catalog model. P4t is skipped for anthropic (T3).
func (a *Adapter) Probe(ctx context.Context) (model.ProbeResult, error) {
	res := model.ProbeResult{ProviderID: a.cfg.ID, ProbedAt: a.now().UTC(), EndpointHash: EndpointHash(a.cfg), Models: []model.ModelProbe{}}
	start := a.now()
	listed, err := a.listModels(ctx)
	res.LatencyMS = int(a.now().Sub(start).Milliseconds())
	if err != nil {
		var me *model.Error
		if e, ok := err.(*model.Error); ok {
			me = e
		}
		if me == nil || me.Code != model.ErrModelNotFound { // 404: some gateways do not list; continue
			res.Error = probe.FirstError(err)
			return res, nil
		}
	}
	res.ModelsListed = listed
	res.OK = true
	for _, m := range a.cfg.Models {
		mp := probe.Model(ctx, a.Generate, m.ID, probe.Options{Tier: a.cfg.Tier, DeclaredCtx: m.Capabilities.MaxContext, SkipP4t: true})
		mp.MaxContext = m.Capabilities.MaxContext
		if mp.StructuredOutput {
			mp.StructuredMode = "tool_forcing"
		}
		res.Models = append(res.Models, mp)
	}
	if a.cache != nil {
		_ = a.cache.Store(res)
	}
	return res, nil
}

func (a *Adapter) listModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	hr, err := a.newRequest(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.hc.Do(hr)
	if err != nil {
		return nil, wire.TransportError(err, ctx, nil)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, normalizeHTTP(resp, a.now())
	}
	var v struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&v); err != nil {
		return nil, model.NewError(model.ErrProviderUnavailable, "invalid model list")
	}
	out := []string{}
	for _, d := range v.Data {
		if len(out) < 500 {
			out = append(out, d.ID)
		}
	}
	return out, nil
}

// EndpointHash identifies the endpoint a probe result belongs to: a change
// of protocol, base URL or auth mode invalidates the cache (A11 §9.4).
func EndpointHash(cfg model.ProviderConfig) string {
	h := sha256.Sum256([]byte(cfg.Protocol + "\n" + cfg.BaseURL + "\n" + cfg.Auth.Mode + "/" + cfg.Auth.Kind))
	return "sha256:" + hex.EncodeToString(h[:])
}
