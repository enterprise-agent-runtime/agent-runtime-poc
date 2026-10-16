// Package providertest holds the test doubles for the model layer
// (CLAUDE.md §8.4): FakeCredentials (a CredentialSource), FakeProbeCache and
// FakeProvider, which answers Generate from scripted event sequences. They
// are the only doubles tests may use for providers; no ad-hoc mocking.
package providertest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"

	"warden.dev/warden/internal/model"
)

// FakeKey is a synthetic, recognisable credential value. Tests assert it
// never appears in errors, logs, events or artifacts (INV-C).
// It deliberately does not match the real key format (length, suffix), so
// secret scanners never mistake it for a leaked key.
const FakeKey = "sk-ant-api03-FAKE-TEST-KEY-0123456789abcdefghij"

// Access records one credential resolution.
type Access struct{ Ref, Consumer string }

// FakeCredentials resolves secret:// references from a map.
type FakeCredentials struct {
	mu       sync.Mutex
	Values   map[string]string // ref → value
	Header   string            // header to use; default derived from the ref name
	Cert     *tls.Certificate  // returned for client_key refs
	Accesses []Access
	Err      error
}

// Credential implements model.CredentialSource.
func (f *FakeCredentials) Credential(_ context.Context, ref, consumer string) (*model.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Accesses = append(f.Accesses, Access{ref, consumer})
	if f.Err != nil {
		return nil, f.Err
	}
	if f.Cert != nil {
		return &model.Credential{Cert: f.Cert}, nil
	}
	v, ok := f.Values[ref]
	if !ok {
		return nil, errors.New("not found")
	}
	c := &model.Credential{Header: f.Header, Value: []byte(v)}
	if c.Header == "" {
		c.Header, c.Scheme = "Authorization", "Bearer"
	}
	return c, nil
}

// FakeProbeCache keeps probe results in memory.
type FakeProbeCache struct {
	mu      sync.Mutex
	Results map[string]model.ProbeResult
	Learned []string // "provider/model flag=value"
}

// Load implements model.ProbeCache.
func (c *FakeProbeCache) Load(id string) (*model.ProbeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.Results[id]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

// Store implements model.ProbeCache.
func (c *FakeProbeCache) Store(r model.ProbeResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Results == nil {
		c.Results = map[string]model.ProbeResult{}
	}
	c.Results[r.ProviderID] = r
	return nil
}

// LearnQuirk implements model.ProbeCache.
func (c *FakeProbeCache) LearnQuirk(providerID, modelID, flag string, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Learned = append(c.Learned, fmt.Sprintf("%s/%s %s=%v", providerID, modelID, flag, value))
	return nil
}

// FakeProvider answers each Generate call with the next scripted response.
type FakeProvider struct {
	mu        sync.Mutex
	ProvID    string
	Caps      model.ModelCapabilities
	Responses [][]model.StreamEvent
	Requests  []model.ModelRequest
	ProbeRes  model.ProbeResult
}

// ID implements model.Provider.
func (p *FakeProvider) ID() string { return p.ProvID }

// Capabilities implements model.Provider.
func (p *FakeProvider) Capabilities(string) (model.ModelCapabilities, error) { return p.Caps, nil }

// Generate returns the next scripted stream; when the script is exhausted
// it returns provider_unavailable.
func (p *FakeProvider) Generate(_ context.Context, req model.ModelRequest) (model.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Requests = append(p.Requests, req)
	if len(p.Responses) == 0 {
		return nil, model.NewError(model.ErrProviderUnavailable, "script exhausted")
	}
	evs := p.Responses[0]
	p.Responses = p.Responses[1:]
	return &model.SliceStream{Events: evs}, nil
}

// CountTokens implements model.Provider with a byte estimate.
func (p *FakeProvider) CountTokens(_ context.Context, req model.ModelRequest) (int, error) {
	n := 0
	for _, m := range req.Messages {
		for _, b := range m.Content {
			n += len(b.Text)
		}
	}
	return n / 4, nil
}

// Health implements model.Provider.
func (p *FakeProvider) Health() model.ProviderHealth { return model.ProviderHealth{} }

// Probe implements model.Provider.
func (p *FakeProvider) Probe(context.Context) (model.ProbeResult, error) { return p.ProbeRes, nil }

// TextResponse scripts a plain text answer.
func TextResponse(text string) []model.StreamEvent {
	return []model.StreamEvent{
		{Type: model.EvMessageStart, Model: "fake", Provider: "fake"},
		{Type: model.EvTextDelta, Text: text},
		{Type: model.EvUsage, Usage: &model.Usage{InputTokens: 10, OutputTokens: len(text) / 4}},
		{Type: model.EvMessageEnd, StopReason: model.StopEndTurn},
	}
}

// ToolResponse scripts one tool call.
func ToolResponse(id, name, inputJSON string) []model.StreamEvent {
	return []model.StreamEvent{
		{Type: model.EvMessageStart, Model: "fake", Provider: "fake"},
		{Type: model.EvToolUseStart, ToolUseID: id, ToolName: name},
		{Type: model.EvToolUseDelta, PartialJSON: inputJSON},
		{Type: model.EvToolUseEnd},
		{Type: model.EvUsage, Usage: &model.Usage{InputTokens: 10, OutputTokens: 5}},
		{Type: model.EvMessageEnd, StopReason: model.StopToolUse},
	}
}
