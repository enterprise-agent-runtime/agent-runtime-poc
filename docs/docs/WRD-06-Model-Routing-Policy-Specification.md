---
title: Model Routing Policy Specification
subtitle: Data classifications, provider trust tiers, routing inputs and strategies, fallback rules, health and quotas, routing policy syntax, auditability
docid: WRD-06
version: 0.5
status: Working specification
date: September 25, 2026
owner: Architecture / Security
audience: Platform teams, security, engineers
---

# 1. Purpose

The router answers one question for every model call: **which provider and model may and should handle this request**. "May" is a security decision derived from data classification and provider trust; "should" is an optimization derived from capabilities, quality, cost, latency and availability. The router never trades the first for the second.

# 2. Data classifications (D-17)

| Classification | Typical content | Default assignment |
|---|---|---|
| `public` | Open-source code, public documentation | Explicit only |
| `internal` | Non-sensitive internal code and documents | Explicit only |
| `confidential` | Proprietary source code, customer-facing systems, internal designs | Default for every repository |
| `restricted` | Secrets-adjacent code, regulated data handling, security-sensitive components | Explicit; path overrides supported (`paths: ["services/payments/**"]`) |

Classification is a property of the workspace with optional path overrides; a task's classification is the maximum over the paths it reads. Reading a `restricted` path mid-task raises the task's classification and may force a re-route or a stop (`waiting_for_input`) if the current provider is no longer allowed.

# 3. Provider trust tiers

| Tier | Definition | Examples |
|---|---|---|
| T0 | Runs on the local machine, loopback only | Ollama, llama.cpp, LM Studio |
| T1 | Operated by the organization on its own network | vLLM in the data center, internal gateway to internal models |
| T2 | Enterprise cloud under the organization's agreements | Azure OpenAI / Foundry, Bedrock, Vertex AI |
| T3 | Vendor SaaS API with API key | Anthropic, OpenAI, Google, Mistral direct |
| T4 | Consumer subscription harness | Copilot SDK, Codex with ChatGPT plan |

A provider's tier is declared in the catalog and may be raised by policy (never lowered by a workspace).

# 4. Default admission matrix (D-18)

| Classification | Allowed tiers by default | Notes |
|---|---|---|
| `restricted` | T0, T1 | T2 only when policy explicitly lists the provider |
| `confidential` | T0, T1, T2 | T3 when the provider entry carries a `data_agreement` (for example zero-retention terms) and org policy allows; T4 never |
| `internal` | T0–T3 | T4 only for harnesses marked `permitted` |
| `public` | T0–T4 | |

The matrix is a policy artifact (`routing.admission`) and can be tightened by organization or user policy. Workspace policy may only remove tiers.

# 5. Routing inputs

| Input | Source |
|---|---|
| Task class | Manifest role and workflow task type (`plan`, `implement`, `verify`, `review`, `summarize`) |
| Data classification | Workspace plus paths read so far |
| Required capabilities and minimum context | Manifest `spec.model` |
| Allow/deny lists | Manifest, org policy, user preferences, session pin |
| Strategy | `quality-first`, `cost-first`, `latency-first`, `prefer-internal` (workspace default `prefer-internal` when a T0/T1 provider exists, else `quality-first`) |
| Budgets | Task `max_cost_usd`; session and daily budgets from policy |
| Health | Circuit-breaker state, rate-limit window, recent error rate, p50 latency |
| Quality priors | Evaluation results per task class and model (WRD-12), used as a ranking signal |

# 6. Algorithm

![Figure 1. Routing decision.](img/routing_decision.png)

1. **Admission**: candidates = models whose provider tier is admitted for the task's classification.
2. **Filter**: apply allow/deny lists; require capabilities and context size; exclude models over budget for the estimated request size.
3. **Rank** per strategy: `quality-first` sorts by quality prior then cost; `cost-first` sorts by cost then quality prior with a minimum quality threshold; `latency-first` by p50; `prefer-internal` prefers T0/T1 that meet a quality threshold, then the rest by quality.
4. **Health**: skip candidates whose circuit is open; a half-open circuit admits one probe request.
5. **Select** the first healthy candidate; emit `routing_decision` with all candidates and the reason each was rejected or ranked.
6. **No candidate**: the task enters `waiting_for_input` with a message explaining which constraint eliminated everything (for example "confidential data; no T0–T2 provider configured"), offering to configure a provider or to lower the strategy's quality threshold if policy permits. The router never selects an inadmissible provider.

# 7. Fallback rules (D-19)

- Fallback triggers: `provider_unavailable`, `rate_limited` beyond the retry budget, `timeout`, `model_not_found`.
- Fallback candidates are the remaining ranked list, restricted to **the same or lower tier** than the failed candidate's admitted maximum; the admission matrix is re-applied, never relaxed.
- Mid-task fallback keeps the transcript; opaque `reasoning` blocks from the previous provider are dropped, and a `context.note` tells the model that the conversation continues on a different model.
- Each fallback emits `routing_fallback` with the cause; three fallbacks in one task stop the task (`waiting_for_input`).
- Retries before fallback: up to 3 for `rate_limited` and `provider_unavailable` with exponential backoff and jitter, honoring `retry_after_ms`.

# 8. Health, quotas, budgets

- Circuit breaker per provider: opens after 5 consecutive failures or an error rate above 50% over 60 s; half-open after 30 s.
- Rate-limit windows are learned from provider headers where available.
- Budgets: task (`max_cost_usd`), session (policy `budgets.session_usd`), daily per user (policy `budgets.daily_usd`), and quota units for harnesses (`budgets.daily_premium_requests`). Exceeding a budget yields `failed(budget)` for the task and `waiting_for_input` for the session.

# 9. Routing policy syntax

Routing policy is part of the policy bundle (WRD-08) under the `routing` key.

```
routing:
  admission:
    restricted:   [T0, T1]
    confidential: [T0, T1, T2]
    internal:     [T0, T1, T2, T3]
    public:       [T0, T1, T2, T3, T4]
  exceptions:
    - when: 'provider.id == "anthropic" && provider.data_agreement.zero_retention'
      admit: { confidential: [T3] }
  strategy:
    default: prefer-internal
    by_task_class:
      plan: quality-first
      implement: quality-first
      verify: cost-first
      summarize: cost-first
    quality_threshold: 0.6           # minimum eval success prior to be considered under prefer-internal/cost-first
  pins:
    allow_user_pin: true              # user may pin a model within admission
  budgets:
    session_usd: 25
    daily_usd: 100
    daily_premium_requests: 300
  deny_models: ["*/preview-*"]
```

Conditions use CEL over the routing input object (`task`, `workspace`, `provider`, `model`, `user`).

# 10. Worked examples

| Situation | Outcome |
|---|---|
| Security review on a `confidential` repository; providers: Ollama (T0), Anthropic with zero-retention agreement (T3), OpenAI without agreement (T3) | Candidates: Ollama, Anthropic (exception). Strategy `prefer-internal`: Ollama chosen if its 32B coder model meets the quality threshold for `review`; otherwise Anthropic. OpenAI rejected: "confidential data; no data agreement". |
| Implementation task on a `public` repository, user pinned `openai/gpt-x` | Pin honored (admitted). Fallback list excludes T4 unless a permitted harness is configured. |
| Anthropic returns 529 repeatedly during a `restricted` task | No fallback beyond T1; if only T0/T1 candidates exist and none qualifies, the task waits for input with the explanation. |
| Task reads `services/payments/**` (restricted override) while running on a T2 provider | The task's classification rises; the router re-evaluates: T2 is not admitted for `restricted`; the task pauses (`waiting_for_input`) with options to continue on a T0/T1 model or to exclude the path. |

# 11. Audit

`routing_decision` event payload: task class, classification, strategy, candidates with per-candidate status (`admitted`, `rejected:<reason>`, `unhealthy`), chosen model, estimated cost, quality prior, budget remaining. The UI shows a one-line explanation ("Chosen: local/qwen-coder-32b (prefer-internal; confidential data; 2 candidates)") with a details view.
