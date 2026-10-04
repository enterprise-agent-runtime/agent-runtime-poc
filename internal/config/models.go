// Package config loads and validates the user's configuration files:
// models.yaml (WRD-16 §6.1, design A11 §5.2, A05 providerSpec/modelEntry)
// and config.yaml. YAML is decoded into explicit structs with unknown
// fields rejected (CLAUDE.md §7). Secrets appear only as secret://
// references (CLAUDE.md §3, §9.3).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"warden.dev/warden/internal/model"
)

// Catalog is models.yaml.
type Catalog struct {
	Providers []Provider `yaml:"providers" json:"providers"`
	Harnesses []Harness  `yaml:"harnesses,omitempty" json:"harnesses"`
	Models    []Model    `yaml:"models" json:"models"`
}

// Provider is one providers[] entry.
type Provider struct {
	ID            string    `yaml:"id" json:"id"`
	Protocol      string    `yaml:"protocol" json:"protocol"`
	BaseURL       string    `yaml:"base_url" json:"base_url"`
	Auth          Auth      `yaml:"auth" json:"auth"`
	Tier          string    `yaml:"tier" json:"tier"`
	Enabled       *bool     `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Timeouts      *Timeouts `yaml:"timeouts,omitempty" json:"timeouts,omitempty"`
	DataAgreement *struct {
		ZeroRetention bool   `yaml:"zero_retention" json:"zero_retention"`
		Ref           string `yaml:"ref" json:"ref"`
	} `yaml:"data_agreement,omitempty" json:"data_agreement,omitempty"`
}

// Auth is the auth block (A11 §5.1).
type Auth struct {
	Mode     string `yaml:"mode" json:"mode"`
	Kind     string `yaml:"kind,omitempty" json:"kind,omitempty"`
	Header   string `yaml:"header,omitempty" json:"header,omitempty"`
	Secret   string `yaml:"secret,omitempty" json:"secret,omitempty"`
	CertFile string `yaml:"cert_file,omitempty" json:"cert_file,omitempty"`
	CAFile   string `yaml:"ca_file,omitempty" json:"ca_file,omitempty"`
}

// Timeouts override adapter timeouts (A11 §7.2).
type Timeouts struct {
	ConnectMS   int `yaml:"connect_ms" json:"connect_ms"`
	FirstByteMS int `yaml:"first_byte_ms" json:"first_byte_ms"`
	IdleMS      int `yaml:"idle_ms" json:"idle_ms"`
}

// Harness is one harnesses[] entry.
type Harness struct {
	ID          string `yaml:"id" json:"id"`
	Kind        string `yaml:"kind" json:"kind"`
	Billing     string `yaml:"billing" json:"billing"`
	VendorTerms string `yaml:"vendor_terms" json:"vendor_terms"`
	Tier        string `yaml:"tier" json:"tier"`
	Enabled     bool   `yaml:"enabled" json:"enabled"`
}

// Model is one models[] entry.
type Model struct {
	ID           string             `yaml:"id" json:"id"`
	Provider     string             `yaml:"provider" json:"provider"`
	Model        string             `yaml:"model" json:"model"`
	Capabilities Capabilities       `yaml:"capabilities" json:"capabilities"`
	Pricing      *Pricing           `yaml:"pricing" json:"pricing"`
	QualityPrior map[string]float64 `yaml:"quality_prior" json:"quality_prior"`
}

// Capabilities are the declared ceiling.
type Capabilities struct {
	ToolCalling      string `yaml:"tool_calling" json:"tool_calling"`
	StructuredOutput bool   `yaml:"structured_output" json:"structured_output"`
	Streaming        bool   `yaml:"streaming" json:"streaming"`
	MaxContext       int    `yaml:"max_context" json:"max_context"`
	MaxOutput        int    `yaml:"max_output,omitempty" json:"max_output,omitempty"`
	Vision           bool   `yaml:"vision,omitempty" json:"vision,omitempty"`
	Reasoning        bool   `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
	PromptCaching    bool   `yaml:"prompt_caching,omitempty" json:"prompt_caching,omitempty"`
}

// Pricing is per million tokens; nil means "pricing: null".
type Pricing struct {
	InputPerMTok       float64  `yaml:"input_per_mtok" json:"input_per_mtok"`
	OutputPerMTok      float64  `yaml:"output_per_mtok" json:"output_per_mtok"`
	CachedInputPerMTok *float64 `yaml:"cached_input_per_mtok,omitempty" json:"cached_input_per_mtok,omitempty"`
	Currency           string   `yaml:"currency" json:"currency"`
}

var (
	// Provider and harness ids use the secret-reference pattern (CONFLICTS C-58).
	reCatalogID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	reModelID   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}(/[A-Za-z0-9._:-]{1,128})?$`)
)

// ErrUnsupported marks a value outside the PoC (A05 -32010 unsupported_in_poc).
var ErrUnsupported = errors.New("unsupported_in_poc")

// ParseCatalog decodes models.yaml strictly and validates it.
func ParseCatalog(b []byte) (*Catalog, error) {
	var c Catalog
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) { // an empty file is an empty catalog
		return nil, fmt.Errorf("models.yaml: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// LoadCatalog reads models.yaml; a missing file is an empty catalog.
func LoadCatalog(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Catalog{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseCatalog(b)
}

// Save writes models.yaml atomically, keeping the previous file as
// models.yaml.bak (A05 provider.add).
func (c *Catalog) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# Warden model catalog (WRD-16 §6.1). Secrets are secret:// references only.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak", old, 0o600)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Validate applies the load rules of A11 §5.2 and A05.
func (c *Catalog) Validate() error {
	ids := map[string]bool{}
	for i, p := range c.Providers {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("providers[%d] %s: %w", i, p.ID, err)
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate provider id %s", p.ID)
		}
		ids[p.ID] = true
	}
	for i, h := range c.Harnesses {
		if !reCatalogID.MatchString(h.ID) || ids[h.ID] {
			return fmt.Errorf("harnesses[%d]: invalid or duplicate id %q", i, h.ID)
		}
		ids[h.ID] = true
		if h.Tier != "T4" {
			return fmt.Errorf("harness %s: tier must be T4", h.ID)
		}
		switch h.Kind {
		case "copilot-sdk", "codex-app-server", "claude-code-cli":
		default:
			return fmt.Errorf("harness %s: kind %q: %w", h.ID, h.Kind, ErrUnsupported)
		}
		switch h.VendorTerms {
		case "permitted", "tolerated", "personal_use_only", "prohibited":
		default:
			return fmt.Errorf("harness %s: vendor_terms %q", h.ID, h.VendorTerms)
		}
		if h.VendorTerms == "prohibited" && h.Enabled {
			return fmt.Errorf("harness %s: prohibited harnesses cannot be enabled (INV-7)", h.ID)
		}
	}
	mids := map[string]bool{}
	for i, m := range c.Models {
		if !reModelID.MatchString(m.ID) || mids[m.ID] || ids[m.ID] {
			return fmt.Errorf("models[%d]: invalid or duplicate id %q", i, m.ID)
		}
		mids[m.ID] = true
		if c.Provider(m.Provider) == nil {
			return fmt.Errorf("model %s: unknown provider %q", m.ID, m.Provider)
		}
		if m.Model == "" || len(m.Model) > 200 {
			return fmt.Errorf("model %s: model name required", m.ID)
		}
		switch m.Capabilities.ToolCalling {
		case "native", "emulated", "none":
		default:
			return fmt.Errorf("model %s: tool_calling must be native, emulated or none", m.ID)
		}
		if m.Capabilities.MaxContext < 1 {
			return fmt.Errorf("model %s: max_context must be at least 1", m.ID)
		}
		if p := m.Pricing; p != nil && (p.Currency != "USD" || p.InputPerMTok < 0 || p.OutputPerMTok < 0) {
			return fmt.Errorf("model %s: pricing must be non-negative USD", m.ID)
		}
		for k, v := range m.QualityPrior {
			if v < 0 || v > 1 {
				return fmt.Errorf("model %s: quality_prior.%s outside [0,1]", m.ID, k)
			}
		}
	}
	return nil
}

// Validate checks one provider (A11 §5.2).
func (p Provider) Validate() error {
	if !reCatalogID.MatchString(p.ID) {
		return fmt.Errorf("invalid id")
	}
	switch p.Protocol {
	case model.ProtocolAnthropic, model.ProtocolOpenAICompat:
	default:
		return fmt.Errorf("protocol %q: %w", p.Protocol, ErrUnsupported)
	}
	switch p.Tier {
	case "T0", "T1", "T2", "T3":
	default:
		return fmt.Errorf("tier %q must be T0..T3 (T4 is for harnesses)", p.Tier)
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("base_url %q is not an http(s) URL", p.BaseURL)
	}
	if u.User != nil {
		return errors.New("base_url must not contain credentials")
	}
	loop := isLoopback(u.Hostname())
	if u.Scheme == "http" && !loop {
		return errors.New("http:// is accepted only for loopback hosts; use https (with ca_file for a private CA)")
	}
	if p.Tier == "T0" && !loop {
		return errors.New("T0 providers must listen on loopback (WRD-05 §10); declare this endpoint as T1")
	}
	a := p.Auth
	wantSecret := ""
	switch a.Mode {
	case model.AuthNone:
	case model.AuthAPIKey:
		wantSecret = "api_key"
		if a.Header != "" && a.Header != "api-key" {
			return fmt.Errorf("auth.header %q: only api-key is supported", a.Header)
		}
		if p.Protocol == model.ProtocolAnthropic && a.Header != "" {
			return errors.New("anthropic-messages uses x-api-key; auth.header is not allowed")
		}
	case model.AuthGateway:
		if p.Protocol != model.ProtocolOpenAICompat {
			return errors.New("gateway auth is for openai-compatible providers")
		}
		switch a.Kind {
		case model.KindBearer:
			wantSecret = "token"
		case model.KindMTLS:
			wantSecret = "client_key"
		default:
			return fmt.Errorf("auth.kind %q: %w", a.Kind, ErrUnsupported)
		}
	default:
		return fmt.Errorf("auth.mode %q: %w", a.Mode, ErrUnsupported)
	}
	if p.Protocol == model.ProtocolAnthropic && a.Mode != model.AuthAPIKey {
		return errors.New("anthropic-messages requires auth mode api_key")
	}
	if wantSecret != "" {
		if want := "secret://providers/" + p.ID + "/" + wantSecret; a.Secret != want {
			return fmt.Errorf("auth.secret must be %s (references only, never values)", want)
		}
	}
	for _, f := range []string{a.CAFile, a.CertFile} {
		if f == "" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if strings.Contains(string(b), "PRIVATE KEY") {
			return fmt.Errorf("%s contains a private key; keys live only in the keychain", f)
		}
		if !strings.Contains(string(b), "BEGIN CERTIFICATE") {
			return fmt.Errorf("%s is not a PEM certificate", f)
		}
	}
	if a.CertFile != "" && a.Kind != model.KindMTLS {
		return errors.New("auth.cert_file is only for gateway kind mtls")
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Provider returns the provider with id, or nil.
func (c *Catalog) Provider(id string) *Provider {
	for i := range c.Providers {
		if c.Providers[i].ID == id {
			return &c.Providers[i]
		}
	}
	return nil
}

// Harness returns the harness with id, or nil.
func (c *Catalog) Harness(id string) *Harness {
	for i := range c.Harnesses {
		if c.Harnesses[i].ID == id {
			return &c.Harnesses[i]
		}
	}
	return nil
}

// ModelsOf returns the catalog models of a provider.
func (c *Catalog) ModelsOf(providerID string) []Model {
	var out []Model
	for _, m := range c.Models {
		if m.Provider == providerID {
			out = append(out, m)
		}
	}
	return out
}

// IsEnabled reports whether the provider is enabled (default true).
func (p Provider) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

// ProviderConfig converts an entry and its models for the adapters.
func (c *Catalog) ProviderConfig(id string) (model.ProviderConfig, error) {
	p := c.Provider(id)
	if p == nil {
		return model.ProviderConfig{}, fmt.Errorf("unknown provider %s", id)
	}
	pc := model.ProviderConfig{ID: p.ID, Protocol: p.Protocol, BaseURL: p.BaseURL, Tier: model.Tier(p.Tier),
		Auth: model.AuthConfig{Mode: p.Auth.Mode, Kind: p.Auth.Kind, Header: p.Auth.Header, Secret: p.Auth.Secret, CertFile: p.Auth.CertFile, CAFile: p.Auth.CAFile}}
	if t := p.Timeouts; t != nil {
		pc.Timeouts = model.TimeoutConfig{ConnectMS: t.ConnectMS, FirstByteMS: t.FirstByteMS, IdleMS: t.IdleMS}
	}
	for _, m := range c.ModelsOf(id) {
		mc := model.ModelConfig{ID: m.ID, Model: m.Model, QualityPrior: m.QualityPrior,
			Capabilities: model.DeclaredCapabilities{ToolCalling: m.Capabilities.ToolCalling, StructuredOutput: m.Capabilities.StructuredOutput,
				Streaming: m.Capabilities.Streaming, Vision: m.Capabilities.Vision, Reasoning: m.Capabilities.Reasoning,
				PromptCaching: m.Capabilities.PromptCaching, MaxContext: m.Capabilities.MaxContext, MaxOutput: m.Capabilities.MaxOutput}}
		if pr := m.Pricing; pr != nil {
			mc.Pricing = &model.Pricing{InputPerMTok: pr.InputPerMTok, OutputPerMTok: pr.OutputPerMTok, CachedInputPerMTok: pr.CachedInputPerMTok, Currency: pr.Currency}
		}
		pc.Models = append(pc.Models, mc)
	}
	return pc, nil
}
