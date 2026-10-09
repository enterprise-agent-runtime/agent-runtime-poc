package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warden.dev/warden/internal/model"
)

// TestSchema_WRD16CatalogParses: the reference catalog of WRD-16 §6.1
// loads, round-trips through Save and converts for the adapters.
func TestSchema_WRD16CatalogParses(t *testing.T) {
	b, _ := os.ReadFile(filepath.Join("testdata", "wrd16-models.yaml"))
	c, err := ParseCatalog(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 6 || len(c.Harnesses) != 3 || len(c.Models) != 4 {
		t.Fatalf("catalog = %d/%d/%d", len(c.Providers), len(c.Harnesses), len(c.Models))
	}
	pc, err := c.ProviderConfig("ollama")
	if err != nil || len(pc.Models) != 2 || pc.Tier != model.T0 || pc.Models[1].Capabilities.ToolCalling != "emulated" {
		t.Fatalf("ollama = %+v %v", pc, err)
	}
	a, _ := c.ProviderConfig("anthropic")
	if a.Models[0].Pricing == nil || a.Models[0].Pricing.OutputPerMTok != 15 || a.Auth.Secret != "secret://providers/anthropic/api_key" {
		t.Fatalf("anthropic = %+v", a)
	}
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatal("no backup kept")
	}
	back, err := LoadCatalog(path)
	if err != nil || len(back.Models) != 4 || back.Provider("company-vllm").Auth.Kind != "bearer" {
		t.Fatalf("reload = %+v %v", back, err)
	}
	if empty, err := LoadCatalog(filepath.Join(t.TempDir(), "missing.yaml")); err != nil || len(empty.Providers) != 0 {
		t.Fatal("missing file must be an empty catalog")
	}
	if c.Harness("copilot") == nil || c.Harness("nope") != nil || !c.Providers[0].IsEnabled() {
		t.Fatal("lookups")
	}
	if _, err := c.ProviderConfig("nope"); err == nil {
		t.Fatal("unknown provider converted")
	}
}

// TestValidate_A11Rules: each load rule of A11 §5.2 has a rejecting case.
func TestValidate_A11Rules(t *testing.T) {
	base := func() Provider {
		return Provider{ID: "vps", Protocol: model.ProtocolOpenAICompat, BaseURL: "https://llm.example/v1", Tier: "T1",
			Auth: Auth{Mode: model.AuthGateway, Kind: model.KindBearer, Secret: "secret://providers/vps/token"}}
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.pem")
	_ = os.WriteFile(keyFile, []byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), 0o600)
	notPEM := filepath.Join(dir, "ca.txt")
	_ = os.WriteFile(notPEM, []byte("hello"), 0o600)
	cases := map[string]struct {
		mut  func(*Provider)
		want string
	}{
		"plain http to a remote host": {func(p *Provider) { p.BaseURL = "http://llm.example/v1" }, "loopback"},
		"T0 on a remote host":         {func(p *Provider) { p.Tier = "T0"; p.Auth = Auth{Mode: "none"} }, "T1"},
		"T4 provider":                 {func(p *Provider) { p.Tier = "T4" }, "harnesses"},
		"secret of another provider":  {func(p *Provider) { p.Auth.Secret = "secret://providers/anthropic/token" }, "secret://providers/vps/token"},
		"inline secret value":         {func(p *Provider) { p.Auth.Secret = "sk-plain-value" }, "references only"},
		"cloud_iam":                   {func(p *Provider) { p.Auth = Auth{Mode: "cloud_iam"} }, "unsupported_in_poc"},
		"sso-oidc":                    {func(p *Provider) { p.Auth.Kind = "sso-oidc" }, "unsupported_in_poc"},
		"unknown protocol":            {func(p *Provider) { p.Protocol = "gemini" }, "unsupported_in_poc"},
		"private key in ca_file":      {func(p *Provider) { p.Auth.CAFile = keyFile }, "private key"},
		"ca_file not PEM":             {func(p *Provider) { p.Auth.CAFile = notPEM }, "not a PEM"},
		"cert_file without mtls":      {func(p *Provider) { p.Auth.CertFile = filepath.Join(dir, "missing.crt") }, "missing.crt"},
		"userinfo in base_url":        {func(p *Provider) { p.BaseURL = "https://u:p@llm.example/v1" }, "credentials"},
		"bad id":                      {func(p *Provider) { p.ID = "Bad_ID" }, "invalid id"},
		"bad header": {func(p *Provider) {
			p.Auth = Auth{Mode: "api_key", Header: "x-key", Secret: "secret://providers/vps/api_key"}
		}, "api-key"},
		"anthropic gateway": {func(p *Provider) { p.Protocol = model.ProtocolAnthropic }, "openai-compatible"},
	}
	for name, c := range cases {
		p := base()
		c.mut(&p)
		err := p.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid provider rejected: %v", err)
	}
	p := base()
	p.Auth = Auth{Mode: "gateway", Kind: "sso-oidc"}
	if err := p.Validate(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported must wrap ErrUnsupported: %v", err)
	}
}

func TestParseCatalog_RejectsUnknownFieldsAndBadModels(t *testing.T) {
	cases := map[string]string{
		"unknown field":      "providers: []\nmodels: []\nextra: 1\n",
		"unknown nested":     "providers:\n  - id: ollama\n    protocol: openai-compatible\n    base_url: http://127.0.0.1:11434/v1\n    auth: { mode: none, token: x }\n    tier: T0\n",
		"model bad provider": "providers: []\nmodels:\n  - { id: m/x, provider: nope, model: x, capabilities: { tool_calling: native, structured_output: true, streaming: true, max_context: 1 }, pricing: null, quality_prior: {} }\n",
		"bad tool calling":   "providers:\n  - { id: o, protocol: openai-compatible, base_url: 'http://127.0.0.1:1/v1', auth: { mode: none }, tier: T0 }\nmodels:\n  - { id: m/x, provider: o, model: x, capabilities: { tool_calling: maybe, structured_output: true, streaming: true, max_context: 1 }, pricing: null, quality_prior: {} }\n",
		"bad prior":          "providers:\n  - { id: o, protocol: openai-compatible, base_url: 'http://127.0.0.1:1/v1', auth: { mode: none }, tier: T0 }\nmodels:\n  - { id: m/x, provider: o, model: x, capabilities: { tool_calling: native, structured_output: true, streaming: true, max_context: 1 }, pricing: null, quality_prior: { plan: 2 } }\n",
		"bad currency":       "providers:\n  - { id: o, protocol: openai-compatible, base_url: 'http://127.0.0.1:1/v1', auth: { mode: none }, tier: T0 }\nmodels:\n  - { id: m/x, provider: o, model: x, capabilities: { tool_calling: native, structured_output: true, streaming: true, max_context: 1 }, pricing: { input_per_mtok: 1, output_per_mtok: 1, currency: EUR }, quality_prior: {} }\n",
		"harness tier":       "providers: []\nharnesses:\n  - { id: copilot, kind: copilot-sdk, billing: subscription, vendor_terms: permitted, tier: T3, enabled: true }\nmodels: []\n",
		"prohibited enabled": "providers: []\nharnesses:\n  - { id: x, kind: copilot-sdk, billing: subscription, vendor_terms: prohibited, tier: T4, enabled: true }\nmodels: []\n",
		"duplicate provider": "providers:\n  - { id: o, protocol: openai-compatible, base_url: 'http://127.0.0.1:1/v1', auth: { mode: none }, tier: T0 }\n  - { id: o, protocol: openai-compatible, base_url: 'http://127.0.0.1:2/v1', auth: { mode: none }, tier: T0 }\nmodels: []\n",
	}
	for name, y := range cases {
		if _, err := ParseCatalog([]byte(y)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if c, err := ParseCatalog(nil); err != nil || c == nil {
		t.Fatalf("empty file: %v", err)
	}
}
