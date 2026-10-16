---
title: Canonical Model API Specification v0.1
subtitle: Provider-neutral request and streaming contract, capability catalog, protocol adapters, authentication and billing modes, local models, external harness adapters
docid: WRD-05
version: 0.5
status: Working specification
date: September 25, 2026
owner: Architecture
audience: Engineers, platform teams
---

# 1. Goals

1. Agents and the orchestrator depend on one interface; adding a provider never touches agent code.
2. Every real access mode is supported as a first-class configuration: API keys, cloud identity, enterprise gateways, local servers, and officially supported vendor subscriptions through their agent engines.
3. Differences between models (tool calling, JSON output, reasoning, context size) are declared in a catalog and handled by the runtime, so that local and frontier models are both usable.
4. All calls originate in the runtime process. Sandboxes never hold model credentials.

![Figure 1. Model integration stack.](img/model_stack.png)

# 2. Provider definition: protocol × auth × hosting

A **provider** entry in `~/.warden/models.yaml` (or the enterprise registry) combines three independent axes.

| Axis | Values |
|---|---|
| `protocol` | `anthropic-messages`, `openai-chat`, `openai-responses`, `gemini`, `bedrock-converse`, `ollama-native`; `openai-compatible` is `openai-chat` with relaxed feature detection |
| `auth` | `api_key`, `cloud_iam` (`entra`, `aws-sigv4`, `gcp-adc`), `gateway` (`bearer`, `mtls`, `sso-oidc`), `none` |
| `hosting` / trust tier | `T0 local` (loopback), `T1 private-hosted` (LAN/VPC, organization-operated), `T2 enterprise cloud` (Azure/AWS/GCP under the organization's agreements), `T3 vendor API` (direct SaaS), `T4 consumer subscription harness` |

Example catalog:

```
providers:
  - id: anthropic
    protocol: anthropic-messages
    base_url: https://api.anthropic.com
    auth: { mode: api_key, secret: "secret://providers/anthropic/api_key" }
    tier: T3
    data_agreement: { zero_retention: true, ref: "DPA-2026-03" }   # optional; lets policy admit confidential data
  - id: azure-foundry
    protocol: openai-responses
    base_url: https://myorg.openai.azure.com
    auth: { mode: cloud_iam, kind: entra, scope: "https://cognitiveservices.azure.com/.default" }
    tier: T2
  - id: bedrock
    protocol: bedrock-converse
    region: eu-central-1
    auth: { mode: cloud_iam, kind: aws-sigv4, profile: "myorg-ai" }
    tier: T2
  - id: gateway
    protocol: openai-chat
    base_url: https://llm-gateway.internal
    auth: { mode: gateway, kind: sso-oidc, audience: "llm-gateway" }
    tier: T1
  - id: ollama
    protocol: openai-compatible
    base_url: http://127.0.0.1:11434/v1
    auth: { mode: none }
    tier: T0
  - id: vllm-dc
    protocol: openai-compatible
    base_url: https://vllm.corp.internal/v1
    auth: { mode: gateway, kind: mtls }
    tier: T1
models:
  - id: anthropic/claude-sonnet          # catalog id used in manifests and policy
    provider: anthropic
    model: claude-sonnet-<version>
    capabilities: { tool_calling: native, structured_output: true, streaming: true, vision: true,
                    reasoning: true, prompt_caching: true, max_context: 200000, max_output: 64000 }
    pricing: { input_per_mtok: 3.00, output_per_mtok: 15.00, cached_input_per_mtok: 0.30, currency: USD }
  - id: local/qwen-coder-32b
    provider: ollama
    model: qwen2.5-coder:32b
    capabilities: { tool_calling: native, structured_output: true, streaming: true, vision: false,
                    reasoning: false, prompt_caching: false, max_context: 32768, max_output: 8192 }
    pricing: null
```

Catalog entries are validated at load and probed on first use (`provider.test`), which also detects `tool_calling` support empirically for `openai-compatible` servers.

# 3. Canonical request

```
ModelRequest {
  model_id: string                          # catalog id, chosen by the router
  messages: Message[]                       # roles: system | user | assistant | tool
  tools?: ToolDefinition[]                  # name, description, input_schema (JSON Schema)
  tool_choice?: "auto" | "none" | "required" | { name }
  response_format?: { type: "json_schema", schema, strict?: bool } | { type: "text" }
  generation?: { max_output_tokens, temperature, top_p, stop[], seed, reasoning_effort: low|medium|high }
  cache?: { hint: "system_prefix" | "none" }  # adapters map to provider caching where available
  budget?: { max_input_tokens }             # enforced by the agent loop before sending
  trace: { session_id, task_id, execution_id, step }
  provider_options?: { <protocol>: object } # escape hatch, ignored by other adapters
}
Message { role, content: ContentBlock[] }
ContentBlock =
  | { type: "text", text }
  | { type: "image", media_type, data | uri }
  | { type: "tool_use", id, name, input }
  | { type: "tool_result", tool_use_id, content: ContentBlock[], is_error?: bool }
  | { type: "reasoning", opaque: string }   # provider-specific reasoning blocks, passed back verbatim
  | { type: "document", media_type, data, title? }
```

Rules:

- The first `system` message is the only place for instructions; adapters map it to the provider's system field.
- `reasoning` blocks are opaque: the runtime never reads or edits them, but returns them to the same provider in subsequent turns when the provider requires it (Responses API, Anthropic thinking). They are dropped when switching providers mid-task (a task pinned to one provider avoids this).
- `tools` names are the tool ids from WRD-04; adapters rename where a provider restricts characters (`fs.write` → `fs__write`) and map back.

# 4. Streamed response events

```
StreamEvent =
  | { type: "message_start", model, provider, request_id }
  | { type: "text_delta", index, text }
  | { type: "reasoning_delta", index, opaque }
  | { type: "tool_use_start", index, id, name }
  | { type: "tool_use_delta", index, partial_json }
  | { type: "tool_use_end", index }
  | { type: "usage", input_tokens, output_tokens, cached_input_tokens?, reasoning_tokens? }
  | { type: "message_end", stop_reason: end_turn | tool_use | max_tokens | stop_sequence | content_filter }
  | { type: "error", code, message, retryable: bool, retry_after_ms? }
```

Normalized error codes: `rate_limited`, `auth_failed`, `context_too_long`, `provider_unavailable`, `content_filtered`, `invalid_request`, `tool_format_unsupported`, `model_not_found`, `timeout`, `cancelled`. Adapters attach the raw provider error in `details` for diagnostics; it is stored in the event but never shown to the model.

# 5. Adapter contract (Go interface, illustrative)

```
type Provider interface {
    ID() string
    Capabilities(modelID string) (ModelCapabilities, error)
    Generate(ctx context.Context, req ModelRequest) (Stream, error)   // streaming always; buffered for non-streaming providers
    CountTokens(ctx context.Context, req ModelRequest) (int, error)   // exact if supported, estimate otherwise
    Health() ProviderHealth                                            // circuit breaker state, rate-limit window
}
```

Adapter responsibilities: message and tool translation, streaming parsing, usage extraction, error normalization, cancellation on context cancel, retry metadata. Adapters never log request bodies at info level; debug logging redacts content.

# 6. Protocol adapters

| Adapter | Covers | Notes |
|---|---|---|
| `anthropic-messages` | Anthropic API, Bedrock (via converse or native), Vertex, Microsoft Foundry | Tool use blocks, thinking as `reasoning`, cache control on system prefix |
| `openai-chat` | OpenAI Chat Completions, Azure OpenAI, most gateways, vLLM, Ollama, llama.cpp server, LM Studio, LiteLLM, and the Gemini API through its OpenAI-compatible endpoint (API key) | Detects `tools`/`response_format` support by probe; strict JSON schema where available |
| `openai-responses` | OpenAI Responses API, Azure Foundry | Preserves reasoning items across turns; built-in tools disabled (runtime tools only) |
| `gemini` | Gemini API and Vertex AI | Function declarations, `responseSchema` |
| `bedrock-converse` | Any Bedrock model | Unified tool config |
| `ollama-native` | Ollama-specific features (keep-alive, model pull) | Optional; `openai-compatible` suffices for inference |

Phase 2 delivers the native `gemini` adapter (Vertex AI, context caching, full reasoning controls), `bedrock-converse` and the cloud identity auth modes; the MVP ships the first three, which already reach Gemini with an API key through the OpenAI-compatible endpoint (D-27).

# 7. Capability handling and emulation

| Capability | If missing | Runtime behavior |
|---|---|---|
| `tool_calling: native` | `emulated` | The adapter injects a compact tool protocol into the system prompt (JSON blocks fenced with a sentinel), parses proposals, and validates them against the schema; the agent loop treats them identically |
| `tool_calling: none` | — | Model excluded from tasks whose manifest requires `tool_calling` (all built-in agents except summarization helpers) |
| `structured_output` | false | Output schema enforced by post-validation plus one repair turn |
| `max_context` small | — | Context manager compacts more aggressively; repo map trimmed; large files read in ranges |
| `vision` | false | Image blocks replaced by a textual note; tasks requiring vision are routed elsewhere |
| `reasoning` | false | `reasoning_effort` ignored |
| `prompt_caching` | false | `cache` hint ignored |

Evaluation (WRD-12) records success per model class so that routing strategies can be tuned with data instead of assumptions.

# 8. Authentication and billing modes

| Mode | How credentials flow | Billing | Tier |
|---|---|---|---|
| `api_key` | Keychain → secrets broker → adapter header; never logged | Per token (catalog prices) | T3 |
| `cloud_iam` | Provider SDK credential chain on the host (Entra ID device/managed identity; AWS profile/SSO; GCP ADC) | Cloud account | T2 |
| `gateway` | OIDC token or mTLS cert from the organization; gateway may add its own routing | Organization-internal | T1/T2 |
| `none` | No credentials; loopback or private network | Infrastructure cost only | T0/T1 |
| `harness_subscription` | The vendor's engine authenticates itself (its own login stored in its own profile directory, mounted read-only into the sandbox of harness tasks) | Vendor subscription quota (premium requests, plan limits) | T4 |

The runtime records `billing_mode` and, for harnesses, quota units consumed in `usage` events (WRD-09). Cost estimates are shown only where a price is known.

# 9. External harness adapters (subscription-backed vendor agents)

Some vendors expose their **agent engine**, not only their inference API, and allow it to be billed to a user's subscription. Rather than spoofing their clients, the platform integrates those engines as **task backends** running inside the sandbox.

![Figure 2. External harness integration: the vendor engine runs inside the sandbox; the runtime governs it through hooks and the sandbox boundary.](img/harness_integration.png)

| Harness | Integration | Vendor terms (September 2026) | Default |
|---|---|---|---|
| GitHub Copilot SDK | Copilot CLI in server mode launched in the sandbox; JSON-RPC session; built-in tools overridden by runtime tools; `pre/post tool use` and permission hooks routed to the PDP; BYOK possible | Permitted; generally available; billed as premium requests | Enabled (optional install) |
| OpenAI Codex SDK / app-server | Codex app-server in the sandbox; approval callbacks routed to the PDP; ChatGPT-plan login or API key | Tolerated for ChatGPT-plan OAuth (no explicit guarantee); API key fully supported | Shipped in Phase 2; ChatGPT-plan mode opt-in with a notice and approval at session start (D-26) |
| Claude Code (headless, `claude -p`) | Claude Code CLI in the sandbox with API-key billing (Agent SDK terms) | Consumer-subscription OAuth in third-party tools prohibited; API key, extra usage, Bedrock/Vertex/Foundry permitted | Shipped in Phase 2; enabled only with `billing: api_key` (D-26) |
| Gemini CLI | Same pattern | Consumer subscription restricted since February 2026; API key permitted | API key only |

Harness contract inside the runtime:

1. A harness task is a normal task: it has a manifest (`role: harness`, `harness: copilot`), capabilities, limits and produces the same artifacts (`code-diff`, `test-report`).
2. The harness process is spawned by `warden-exec` inside the sandbox; its network is restricted to the vendor's endpoints through the proxy; its configuration directory is mounted read-only; the worktree is the only writable path.
3. Where the harness offers tool override or permission hooks, the runtime registers them; a harness tool call becomes a PDP request like any other, executed by `warden-exec`. Where a harness lacks hooks, the sandbox is the only enforcement, and policy may forbid such harnesses for `confidential` and `restricted` workspaces.
4. The harness's own credential is exposed to processes in its sandbox; a malicious repository could attempt to read it. Mitigations: mount only the token file the harness needs, run harness tasks at L2 by default, and mark harness output tainted.
5. The platform never implements token replay, client spoofing, or localhost proxies that impersonate vendor CLIs. Vendor terms are recorded in the catalog (`vendor_terms: permitted | tolerated | prohibited`) and `prohibited` entries cannot be enabled.

# 10. Local models

- Discovery: `warden provider add ollama` probes `/v1/models` and creates catalog entries with capability probes; the same for vLLM, llama.cpp server and LM Studio.
- Recommended classes for the MVP evaluation: 7B–14B coder models for planning summaries and quick edits; 30B-class coder models for implementation tasks; anything larger routed by the organization's own serving infrastructure.
- Context budgets: the router requires `max_context ≥ min_context_tokens` from the manifest; the context manager reserves 25% of the window for output and tool results.
- Local endpoints are `T0` only when bound to loopback; a LAN or VPC endpoint is `T1` and must be declared as such.

# 11. Observability of model calls

Every call produces `model.call.start`, zero or more `model.call.delta` (not persisted; UI only), `model.call.end` with usage, latency, stop reason, provider request id, and `routing_decision` (WRD-06). Request bodies are persisted only when `observability.log_full_args` is on and never with secret values; a content hash is always stored for reproducibility.

# 12. Compatibility and versioning

The canonical types are versioned (`model.v1`). Adapters declare which canonical version they implement. Provider API changes are absorbed in adapters; a change in canonical types requires a major runtime version.
