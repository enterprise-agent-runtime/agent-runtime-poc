package model

import (
	"context"
	"crypto/tls"
	"time"
)

// Provider is the adapter contract (WRD-05 §5 plus Probe; design A02 §4.1,
// A11 §11.1). Adapters translate, stream, normalize errors and report usage;
// they never retry (except the openai-compatible shape retry), fall back,
// compact, estimate cost or execute tools.
type Provider interface {
	ID() string
	// Capabilities returns the effective catalog entry (declared ∧ probed).
	Capabilities(modelID string) (ModelCapabilities, error)
	// Generate always streams; errors before the first event are returned
	// as *Error, later ones arrive as an error event.
	Generate(ctx context.Context, req ModelRequest) (Stream, error)
	// CountTokens is exact where the provider offers it, an estimate otherwise.
	CountTokens(ctx context.Context, req ModelRequest) (int, error)
	Health() ProviderHealth
	// Probe is the provider.test capability probe (design A11 §9).
	Probe(ctx context.Context) (ProbeResult, error)
}

// ProviderFactory builds an adapter; cmd/wardend is the only caller.
type ProviderFactory func(cfg ProviderConfig, creds CredentialSource, cache ProbeCache) (Provider, error)

// Tool-calling modes, ordered none < emulated < native.
const (
	ToolCallingNone     = "none"
	ToolCallingEmulated = "emulated"
	ToolCallingNative   = "native"
)

// ToolCallingRank orders tool-calling modes; unknown values rank lowest.
func ToolCallingRank(s string) int {
	switch s {
	case ToolCallingNative:
		return 2
	case ToolCallingEmulated:
		return 1
	}
	return 0
}

// MinToolCalling returns the weaker of two tool-calling modes (A11 §9.3).
func MinToolCalling(a, b string) string {
	if ToolCallingRank(a) <= ToolCallingRank(b) {
		if a == "" {
			return ToolCallingNone
		}
		return a
	}
	return b
}

// ModelCapabilities is the effective capability set of one model.
type ModelCapabilities struct {
	ToolCalling      string `json:"tool_calling"`
	StructuredOutput bool   `json:"structured_output"`
	StructuredMode   string `json:"structured_mode,omitempty"` // json_schema | json_object | tool_forcing | none
	ToolChoiceNamed  bool   `json:"tool_choice_named"`
	Streaming        bool   `json:"streaming"`
	Vision           bool   `json:"vision,omitempty"`
	Reasoning        bool   `json:"reasoning,omitempty"`
	PromptCaching    bool   `json:"prompt_caching,omitempty"`
	MaxContext       int    `json:"max_context"`
	MaxOutput        int    `json:"max_output,omitempty"`
}

// ProviderHealth reports rate-limit headers and the last error; the circuit
// breaker itself lives in the router (design A09).
type ProviderHealth struct {
	RequestsRemaining int       `json:"requests_remaining"`
	TokensRemaining   int       `json:"tokens_remaining"`
	ResetAt           time.Time `json:"reset_at"`
	LastError         ErrorCode `json:"last_error,omitempty"`
	LastErrAt         time.Time `json:"last_error_at"`
}

// CredentialSource is implemented by internal/secrets and injected by
// cmd/wardend. consumer is recorded in secret.access ("adapter:<provider>").
type CredentialSource interface {
	Credential(ctx context.Context, ref string, consumer string) (*Credential, error)
}

// Credential is a resolved secret ready for injection into one request.
// Value is wiped after use; it is never stored, logged or serialized.
type Credential struct {
	Header string           // Authorization | x-api-key | api-key | "" (mTLS)
	Scheme string           // Bearer | ""
	Value  []byte           // wiped by Wipe
	Cert   *tls.Certificate // gateway kind mtls
}

// Wipe zeroes the credential value.
func (c *Credential) Wipe() {
	if c == nil {
		return
	}
	for i := range c.Value {
		c.Value[i] = 0
	}
	c.Value = nil
}

// String never reveals the value.
func (c *Credential) String() string { return "[REDACTED:credential]" }

// GoString never reveals the value.
func (c *Credential) GoString() string { return "[REDACTED:credential]" }

// ProviderConfig is one models.yaml providers[] entry with its models,
// parsed and validated by the daemon (design A11 §2).
type ProviderConfig struct {
	ID       string
	Protocol string // anthropic-messages | openai-compatible
	BaseURL  string
	Tier     Tier
	Auth     AuthConfig
	Timeouts TimeoutConfig
	Models   []ModelConfig
}

// Protocols.
const (
	ProtocolAnthropic    = "anthropic-messages"
	ProtocolOpenAICompat = "openai-compatible"
)

// AuthConfig is the auth block of a provider (design A11 §5.1).
type AuthConfig struct {
	Mode     string // none | api_key | gateway
	Kind     string // bearer | mtls (gateway only)
	Header   string // "" | api-key (Azure)
	Secret   string // secret://providers/<id>/<name>
	CertFile string // mTLS certificate chain (PEM, certificate only)
	CAFile   string // extra trusted roots (PEM)
}

// Auth modes and gateway kinds.
const (
	AuthNone    = "none"
	AuthAPIKey  = "api_key"
	AuthGateway = "gateway"
	KindBearer  = "bearer"
	KindMTLS    = "mtls"
)

// TimeoutConfig overrides the adapter timeouts; zero means default
// (design A11 §7.2).
type TimeoutConfig struct {
	ConnectMS   int
	FirstByteMS int
	IdleMS      int
}

// ModelConfig is one models.yaml models[] entry of a provider.
type ModelConfig struct {
	ID           string // catalog id (anthropic/claude-sonnet)
	Model        string // wire model name
	Capabilities DeclaredCapabilities
	Pricing      *Pricing
	QualityPrior map[string]float64
}

// DeclaredCapabilities are the catalog's ceiling; a probe can only lower them.
type DeclaredCapabilities struct {
	ToolCalling      string
	StructuredOutput bool
	Streaming        bool
	Vision           bool
	Reasoning        bool
	PromptCaching    bool
	MaxContext       int
	MaxOutput        int
}

// Pricing is the catalog price per million tokens.
type Pricing struct {
	InputPerMTok       float64
	OutputPerMTok      float64
	CachedInputPerMTok *float64
	Currency           string
}

// ProbeCache persists probe results and learned quirks (design A11 §9.4).
type ProbeCache interface {
	Load(providerID string) (*ProbeResult, error)
	Store(r ProbeResult) error
	LearnQuirk(providerID, modelID, flag string, value any) error
}

// ProbeResult is the outcome of Provider.Probe; it mirrors the probe-cache
// JSON (design A11 §9.4).
type ProbeResult struct {
	ProviderID     string       `json:"provider_id"`
	ProbedAt       time.Time    `json:"probed_at"`
	RuntimeVersion string       `json:"runtime_version"`
	EndpointHash   string       `json:"endpoint_hash"`
	OK             bool         `json:"ok"`
	LatencyMS      int          `json:"latency_ms"`
	ModelsListed   []string     `json:"models_listed,omitempty"`
	Error          *ProbeError  `json:"error"`
	Models         []ModelProbe `json:"models"`
}

// ProbeError is a probe failure with a WRD-05 §4 code.
type ProbeError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// ModelProbe is the probed capability set of one catalog model.
type ModelProbe struct {
	ModelID             string         `json:"model_id"`
	ToolCalling         string         `json:"tool_calling"`
	StructuredOutput    bool           `json:"structured_output"`
	StructuredMode      string         `json:"structured_mode,omitempty"`
	Streaming           bool           `json:"streaming"`
	MaxContext          int            `json:"max_context"`
	MaxContextMeta      *int           `json:"max_context_meta"`
	MaxContextEffective *int           `json:"max_context_effective"`
	TTFTMS              int            `json:"ttft_ms,omitempty"`
	Quirks              map[string]any `json:"quirks,omitempty"`
	Warnings            []string       `json:"warnings,omitempty"`
}

// Effective combines declared capabilities with a probe: tool calling and
// structured output can only go down, max_context is the minimum of the
// known values (design A11 §9.3). A nil probe returns the declared values.
func Effective(d DeclaredCapabilities, p *ModelProbe) ModelCapabilities {
	c := ModelCapabilities{
		ToolCalling: d.ToolCalling, StructuredOutput: d.StructuredOutput, Streaming: d.Streaming,
		Vision: d.Vision, Reasoning: d.Reasoning, PromptCaching: d.PromptCaching,
		MaxContext: d.MaxContext, MaxOutput: d.MaxOutput, ToolChoiceNamed: true,
	}
	if c.ToolCalling == "" {
		c.ToolCalling = ToolCallingNone
	}
	if p == nil {
		return c
	}
	if d.ToolCalling == "" {
		c.ToolCalling = p.ToolCalling
	} else {
		c.ToolCalling = MinToolCalling(d.ToolCalling, p.ToolCalling)
	}
	c.StructuredOutput = c.StructuredOutput && p.StructuredOutput
	c.StructuredMode = p.StructuredMode
	c.Streaming = c.Streaming && p.Streaming
	for _, v := range []*int{p.MaxContextMeta, p.MaxContextEffective} {
		if v != nil && *v > 0 && (c.MaxContext == 0 || *v < c.MaxContext) {
			c.MaxContext = *v
		}
	}
	if named, ok := p.Quirks["tool_choice_named"].(bool); ok {
		c.ToolChoiceNamed = named
	}
	return c
}
