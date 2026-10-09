---
title: Agent Manifest Specification v0.1
subtitle: Package layout, manifest schema, capabilities, model policy, limits, approvals, artifacts, delegation, evaluation references, versioning and signing
docid: WRD-03
version: 0.5
status: Working specification
date: September 25, 2026
owner: Architecture
audience: Engineers, agent authors, security
---

# 1. Scope

The Agent Manifest is the packaging and permission contract of an agent. It declares **what an agent is, what it may request, which models it may use, what it produces, and how much it may consume**. It contains no code. Behavior comes from instructions, granted tools and, in Phase 4, an optional implementation running inside the sandbox (WRD-14).

Separation of concerns:

| Artifact | Answers | Owner |
|---|---|---|
| Agent Manifest (this document) | Identity, capabilities, model policy, limits, contracts | Agent author, reviewed by security |
| Workflow definition (WRD-07) | Which agents run in which order with which gates | Workflow author or planner |
| Policy (WRD-08) | What the organization, user and workspace additionally allow or deny | Platform and security teams |

The effective permission of a task is the **intersection** of its manifest capabilities with the policy layers. A manifest can never grant more than policy allows; policy can never grant a tool the manifest does not request.

# 2. Package layout

![Figure 1. Agent package structure.](img/manifest_structure.png)

```
security-reviewer/
  manifest.yaml
  prompts/
    system.md              # instructions (Markdown; may include {{placeholders}})
    task.md                # optional per-task template
  schemas/
    input.json             # JSON Schema (draft 2020-12)
    security-report.json
  evals/
    suite.yaml             # WRD-12 format
    fixtures/...
  README.md
  SIGNATURE                # detached signature over the canonical digest (optional in MVP)
```

The **package digest** is the SHA-256 of a canonical tar (sorted paths, fixed metadata) of all files except `SIGNATURE`. Packages are addressed as `name@version` for humans and `name@sha256:<digest>` for execution and audit. Distribution as OCI artifacts is specified in WRD-15.

# 3. Manifest schema

`apiVersion: warden.dev/v1alpha1`, `kind: Agent`. The authoritative JSON Schema lives at `schemas/agent-manifest.v1alpha1.json`. Fields:

## 3.1 `metadata`

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string, DNS-label | yes | Unique within a registry namespace |
| `version` | semver | yes | Breaking changes to input or output schemas require a major bump |
| `description` | string | yes | One or two sentences shown in the UI |
| `labels` | map | no | Free-form, used for search and policy matching (`team`, `domain`) |
| `authors` | list | no | |
| `license` | string | no | SPDX id |

## 3.2 `spec.role` and `spec.instructions`

`role` is a short classifier (`planner`, `coder`, `verifier`, `security-review`, `integrator`, `custom`). `instructions` is either an inline string or `{file: prompts/system.md}`. Placeholders available: `{{workspace.root}}`, `{{task.input.*}}`, `{{artifact(<id>).summary}}`. Instructions are trusted content at the agent's own trust level; they can never raise permissions.

## 3.3 `spec.input` and `spec.output`

Both reference JSON Schemas. The runtime validates task inputs before starting and outputs before completing. Outputs that fail validation trigger one repair attempt (the validation error is returned to the model) and then `failed(schema)`.

## 3.4 `spec.capabilities` (grammar in WRD-04)

```
capabilities:
  - tool: fs
    operations: [read, list, search, write, patch]
    paths:
      allow: ["${worktree}/src/**", "${worktree}/tests/**", "${worktree}/package.json"]
      deny:  ["${worktree}/.github/**"]
    max_file_bytes: 2000000
  - tool: proc
    operations: [exec]
    commands:
      allow: ["npm", "pnpm", "node", "go", "pytest", "python3"]
      profiles: [node-test, go-test]         # named command profiles from policy
    cwd: "${worktree}"
    timeout_seconds: 600
    egress:
      allow: ["registry.npmjs.org:443", "proxy.golang.org:443"]
  - tool: git
    operations: [status, diff, log, commit]
```

Variables: `${workspace}` (repository root on the host, read-only inside sandbox unless a worktree is not used), `${worktree}` (task worktree, read-write), `${home}` (never allowed for agents; present for deny rules only). Capabilities are additive within a manifest; the platform deny-list (WRD-10 §6) is applied afterwards and cannot be removed.

## 3.5 `spec.model`

| Field | Meaning |
|---|---|
| `required_capabilities` | Catalog flags the model must have: `tool_calling`, `structured_output`, `vision`, `reasoning`, `long_context` |
| `min_context_tokens` | Minimum usable context |
| `prefer_tier` | Hint (`T0`–`T3`); the router obeys classification limits first |
| `allow_models`, `deny_models` | Catalog ids or globs (`openai/*`, `local/*`) |
| `strategy` | `quality-first`, `cost-first`, `latency-first`, `prefer-internal`; overrides the workspace default only if policy permits |
| `generation` | Defaults for `temperature`, `max_output_tokens`, `reasoning_effort` |

## 3.6 `spec.limits`

| Field | Default | Meaning |
|---|---|---|
| `max_steps` | 50 | Model calls per execution |
| `max_tokens` | 300000 | Total input plus output tokens per execution |
| `timeout_seconds` | 900 | Wall clock per execution |
| `max_cost_usd` | 2.00 | Estimated cost ceiling where prices are known |
| `max_tool_calls` | 200 | |
| `max_parallel_tools` | 1 | Tool calls executed concurrently within one step |

Exhausting a limit ends the execution with `failed(budget)` and a checkpoint artifact.

## 3.7 `spec.approvals`

Declares approval gates the agent itself requests in addition to policy (never fewer):

```
approvals:
  before_first_write: true          # ask once per task before the first file write
  on_command_outside_profile: true
  scopes_allowed: [once, task]      # this agent cannot request session/workspace scope
```

## 3.8 `spec.artifacts`

```
artifacts:
  produces: [plan, code-diff, test-report]
  consumes: [repo-map, plan]
  schemas:
    plan: schemas/plan.json
```

Artifact types are registered names (WRD-09). An agent that declares `produces` MUST emit those artifacts for a successful completion.

## 3.9 `spec.delegation`

```
delegation:
  allowed_agents: ["verifier@^1", "coder@^2"]
  max_depth: 1
  max_children: 3
  inherit: intersect                # child capabilities = child manifest ∩ parent grants
```

Only the orchestrator creates tasks. A delegating agent proposes child tasks through the `orchestrator.delegate` tool; the PDP checks this section.

## 3.10 `spec.evaluation`, `spec.runtime`, `spec.observability`

| Field | Meaning |
|---|---|
| `evaluation.suite` | Path to the suite; `min_success_rate` gates publication |
| `runtime.min_version` | Semver constraint on the runtime |
| `runtime.sandbox_min_level` | `L1` or `L2`; the runtime uses the higher of this and the policy's |
| `observability.log_full_args` | Whether tool arguments are logged in full (default true; policy may force redaction) |

# 4. Complete example: `coder` (built-in)

```
apiVersion: warden.dev/v1alpha1
kind: Agent
metadata:
  name: coder
  version: 1.0.0
  description: Implements a bounded change in an isolated worktree and returns a code-diff artifact.
  labels: { builtin: "true", domain: coding }
spec:
  role: coder
  instructions: { file: prompts/system.md }
  input:  { schema: schemas/input.json }        # { plan_task: object, repo_map: artifact-ref }
  output: { schema: schemas/output.json }       # { summary: string, changed_files: [string] }
  capabilities:
    - tool: fs
      operations: [read, list, search, write, patch]
      paths: { allow: ["${worktree}/**"] }
    - tool: proc
      operations: [exec]
      commands: { profiles: [build, test, lint] }
      timeout_seconds: 600
    - tool: git
      operations: [status, diff]
  model:
    required_capabilities: [tool_calling]
    min_context_tokens: 32000
    strategy: quality-first
  limits: { max_steps: 80, max_tokens: 600000, timeout_seconds: 1800, max_cost_usd: 4.00 }
  approvals: { before_first_write: false }
  artifacts:
    produces: [code-diff]
    consumes: [plan, repo-map]
  delegation: { allowed_agents: [], max_depth: 0 }
  evaluation: { suite: evals/suite.yaml, min_success_rate: 0.7 }
  runtime: { min_version: "0.4.0", sandbox_min_level: L1 }
```

# 5. Validation rules

1. Unknown fields are errors (no silent ignore) to prevent typo-based permission loss.
2. `operations` must belong to the tool's descriptor; `paths` patterns must be rooted in a variable.
3. `allow_models` entries must exist in the catalog at load time or match a glob.
4. `delegation.allowed_agents` may only reference agents with `delegation.max_depth` lower than the parent's remaining depth.
5. A manifest with `role: security-review` MUST NOT request `fs.write` or `fs.patch` (linted; policy also denies).
6. Prompts must not contain the strings of the platform deny-list variables in an `allow` context.

# 6. Versioning and compatibility

- `apiVersion` changes only for breaking schema changes; the runtime supports the current and previous version.
- Agent `version` follows semver; workflows reference ranges (`coder@^1`), executions pin digests.
- The runtime records `agent.name`, `agent.version` and `agent.digest` in every event of the execution.

# 7. Signing and trust levels

| Trust level | Meaning | MVP behavior | Enterprise behavior |
|---|---|---|---|
| `builtin` | Shipped with the runtime, signed by the vendor key | Verified | Verified |
| `local` | Authored locally, unsigned | Allowed with a visible badge | Denied unless policy allows `local` in the workspace |
| `signed` | Signed by a key trusted in policy (`trust.keys`) | Verified | Required for registry agents |

Signatures cover the package digest; keys are Ed25519 in the MVP with sigstore (cosign, keyless) supported for OCI distribution.

# 8. Comparison with the v0.3 example manifest

| v0.3 construct | Problem | v0.4 replacement |
|---|---|---|
| `permissions: {filesystem: read-only, network: disabled, process_write: false}` | Second permission system | Removed; UI derives a summary from capabilities |
| `approvals: {file_modification: denied}` | Denial expressed as approval | Absent `fs.write` capability; linted for review agents |
| `model_policy.allowed_models` | No capability requirements, no tier | `spec.model` with capabilities, tier preference, allow/deny |
| `network: disabled` on a tool that needs updates | Contradiction | Per-capability `egress.allow` through the proxy |
| No limits | Unbounded execution | `spec.limits` with defaults |
| No delegation section | Unbounded sub-agents | `spec.delegation` |
