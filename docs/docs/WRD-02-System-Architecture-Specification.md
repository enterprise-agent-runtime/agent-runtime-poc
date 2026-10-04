---
title: System Architecture Specification
subtitle: Components, process topology, trust boundaries, control and execution planes, runtime API, data flow, repository structure
docid: WRD-02
version: 0.5
status: Working specification
date: September 25, 2026
owner: Architecture
audience: Architects, engineers, security
---

# 1. Architectural constraints (invariants)

These constraints are checked by design review and, where possible, by automated tests.

1. **Clients contain no agent logic.** Desktop, CLI and CI talk to the runtime through the Runtime API only.
2. **Agents contain no provider code.** Agents see the Canonical Model API; adapters live behind the router.
3. **Providers cannot execute tools.** Adapters return proposals; only the agent loop can turn a proposal into a PDP request.
4. **Nothing executes without a decision.** Every tool execution is preceded by a `policy.decision` event with `effect: allow` and a matching `approval` when required.
5. **Sandboxes hold no secrets and have no direct network.** Credentials are injected host-side; egress goes through the proxy.
6. **Every execution has an owner, session, task and policy context.** Anonymous execution is impossible.
7. **Local and remote execution share the same contracts.** Remote workers run the same `wardend` in server mode.

# 2. Component view

![Figure 1. Layered architecture.](img/architecture_layers.png)

## 2.1 Experience layer (clients)

| Client | Technology | Responsibilities |
|---|---|---|
| Desktop app | Tauri 2 shell, React + TypeScript UI, `wardend` as sidecar | Request entry, timeline, plan review, approvals, diff and artifact viewers, settings |
| CLI (`warden`) | Go, same module as the daemon | Scripting, headless runs (`--non-interactive`), audit export, eval runner, provider setup |
| CI | CLI in headless mode | Pre-approved policy, non-interactive approvals resolve to deny |
| Remote API (later) | WebSocket + mTLS | Control plane and worker communication |

## 2.2 Runtime kernel (`wardend`)

| Component | Responsibility | Key interfaces |
|---|---|---|
| Session manager | Sessions, transcripts, persistence, resume | Runtime API `session.*` |
| Orchestrator | Instantiates workflows, schedules ready tasks, joins, gates, retries, repair | WRD-07 |
| Agent loop | Context assembly, model call, proposal handling, budgets, compaction | WRD-05, WRD-08 |
| Policy Decision Point (PDP) | Capability check, invariants, rule evaluation, obligations, approvals | WRD-08 |
| Model router | Candidate filtering by tier and capabilities, ranking, health, fallback | WRD-06 |
| Context manager | Selection, classification, redaction, provenance | WRD-09 |
| Secrets broker | Keychain access, `secret://` resolution, injection into adapters and proxy | WRD-10 |
| Egress proxy | HTTP CONNECT and SOCKS5 listener on a Unix socket per task; allowlist; credential injection | WRD-10 |
| Sandbox manager | Creates and destroys sandboxes (L1/L2), launches `warden-exec` | WRD-10 |
| Worktree manager | Creates, pins, merges and cleans worktrees | WRD-07 |
| Event and artifact store | SQLite, hash chain, content-addressed blobs | WRD-09 |
| Telemetry | Usage, cost, durations | WRD-09 |

## 2.3 Ports and adapters

| Port | Adapters (MVP) | Adapters (later) |
|---|---|---|
| Model provider | anthropic-messages, openai-chat, openai-responses, openai-compatible | gemini, bedrock-converse, vertex, foundry (auth variants), ollama-native |
| External harness | copilot-sdk (optional) | codex-sdk, claude-code-headless (API-key billing) |
| Tool executor | `warden-exec` (fs, proc, git) | MCP stdio bridge, browser, cloud, database |
| Sandbox backend | bubblewrap (Linux), seatbelt (macOS), OCI container (Docker/Podman) | gVisor, Firecracker, Apple containers |
| Secret store | macOS Keychain, Windows Credential Manager, libsecret; encrypted file fallback | HashiCorp Vault, cloud secret managers (control plane) |

# 3. Process topology and IPC

![Figure 2. Process topology.](img/process_topology.png)

- **Daemon lifecycle.** The desktop shell starts the sidecar and passes a random session token via stdin; the CLI reads the token from a mode-0600 file under `~/.warden/run/`. One daemon per user; multiple clients may attach.
- **Transport.** JSON-RPC 2.0 with `Content-Length` framing (LSP style) over a Unix domain socket (`~/.warden/run/wardend.sock`, mode 0600) or a Windows named pipe with an owner-only DACL. Server-initiated notifications carry streamed events.
- **Executor channel.** `wardend` launches `warden-exec` inside the sandbox with a socketpair; messages are JSON-RPC too (`exec.fs.read`, `exec.proc.spawn`, `exec.proc.signal`, …). The executor has no other communication channel.
- **Proxy channel.** The sandbox receives one Unix socket (bind-mounted) that terminates in the daemon's per-task proxy. Environment inside the sandbox sets `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` to that socket through a tiny in-sandbox forwarder (`warden-exec proxy`) exposing `127.0.0.1:<port>` inside the network namespace only.

# 4. Runtime API (summary)

| Namespace | Methods (selection) |
|---|---|
| `session` | `open(workspace)`, `list`, `resume(id)`, `close(id)`, `sendRequest(id, text)`, `cancel(id, taskId?)` |
| `workflow` | `get(runId)`, `approveGate(runId, gateId, decision)`, `editPlan(runId, plan)` |
| `approval` | `list(sessionId)`, `resolve(approvalId, decision, scope)` |
| `artifact` | `list(sessionId)`, `get(id)`, `read(id, range)` |
| `event` | `subscribe(sessionId, filter)`, `query(filter, cursor)` |
| `provider` | `list`, `configure(spec)`, `test(id)`, `models(id)` |
| `policy` | `explain(actionSpec)`, `list`, `reload` |
| `eval` | `run(suite, options)`, `report(runId)` |
| `system` | `version`, `capabilities`, `sandboxStatus`, `shutdown` |

All methods require the session token. Methods with side effects outside the daemon (`provider.configure`, `policy.reload`) also require an interactive confirmation in the client. The full API is generated from a schema in `schemas/runtime-api/`.

# 5. Trust boundaries

![Figure 3. Trust boundaries.](img/trust_boundaries.png)

| Boundary | Control |
|---|---|
| User ↔ client | OS user session; client shows what will be done before it is done |
| Client ↔ runtime | Owner-only socket, per-session token, no privileged methods without confirmation |
| Runtime ↔ agent package | Digest-pinned packages; manifest defines maximum capabilities; signature verification (warn in MVP, enforce in enterprise) |
| Runtime ↔ model provider | Host-side TLS; credentials from the broker; data classification enforced by the router; requests logged without secrets |
| Runtime ↔ sandbox | Capability grants become concrete mounts, seccomp/Seatbelt profiles, namespaces, resource limits |
| Sandbox ↔ network | Proxy only; allowlist; injected credentials never visible to processes |
| Runtime ↔ secrets | References only; values live in the keychain; access events recorded |
| Local ↔ remote (later) | mTLS worker identity; signed bundles; event sync with chain verification |
| Control plane ↔ execution plane (later) | Signed policy and registry bundles with TTL; offline behavior defined |

# 6. Data flow for one task

1. Orchestrator marks task ready; sandbox manager creates the sandbox and mounts the worktree; proxy allocates a per-task listener with the task's egress allowlist.
2. Agent loop assembles context (instructions, inputs, artifact summaries, workspace map) with provenance and redaction.
3. Router selects `(provider, model)`; adapter converts the request; the call is made from the daemon.
4. Streamed events update the UI and are persisted.
5. Tool proposals go to the PDP; decisions and approvals are events; approved actions are sent to `warden-exec`.
6. Results are tagged untrusted, secret-scanned, and appended to context.
7. On completion the output is validated; artifacts are stored; the sandbox is destroyed; the worktree remains until integration and cleanup.

# 7. Control plane and execution plane

The MVP contains only the execution plane. The interfaces that the control plane will use later are fixed now:

| Contract | MVP source | Control-plane source (Phase 3) |
|---|---|---|
| Policy bundle | `~/.warden/policy/*.yaml` | Signed bundle from the policy service, cached with TTL |
| Agent package | `~/.warden/agents/` | OCI registry, digest-pinned, signature required |
| Model catalog | `~/.warden/models.yaml` | Model and provider registry with tiers and credential refs |
| Events | local SQLite | Local SQLite plus streaming to audit ingestion |
| Identity | OS user | OIDC identity token bound to the daemon |

# 8. Technology decisions (see WRD-00 D-06, D-31)

| Concern | Choice | Alternatives considered |
|---|---|---|
| Runtime language | Go | Rust (stronger memory safety, slower iteration; revisit for `warden-exec` if audits demand), TypeScript (team familiarity, weaker process control) |
| Desktop shell | Tauri 2 | Wails (Go-native, fewer security features), Electron (heavier) |
| UI | React + TypeScript | — |
| Local store | SQLite (WAL) | Embedded KV stores; rejected for weaker querying |
| Policy expressions | CEL (cel-go) | Rego/OPA (heavier, available later as a backend) |
| IPC | JSON-RPC 2.0 | gRPC (poor fit for webview clients without a bridge) |
| Packaging | OCI artifacts + sigstore/Ed25519 | Custom formats |

# 9. Repository structure

```
warden/
  cmd/
    wardend/            # daemon entry point
    warden/             # CLI entry point
    warden-exec/        # in-sandbox executor
  internal/
    api/                # JSON-RPC server, schema, auth
    session/            # sessions, transcripts, persistence
    orchestrator/       # workflow instantiation, scheduler, gates, repair
    agentloop/          # context, model call, proposals, budgets, compaction
    policy/             # PDP, rule loading, CEL, approvals
    router/             # candidate selection, health, fallback
    model/              # canonical API types, streaming, normalization
    providers/          # anthropic/, openai/, openaicompat/, (gemini/, bedrock/, vertex/)
    harness/            # copilot/, (codex/, claudecode/)
    tools/              # descriptors, registry, executor client
    sandbox/            # linux_bwrap/, darwin_seatbelt/, oci/
    proxy/              # egress proxy, allowlists, credential injection
    secrets/            # keychain backends, redaction, scanning
    worktree/           # git worktree lifecycle, hook neutralization
    store/              # SQLite schema, events, artifacts, hash chain
    evals/              # suite runner and graders
  agents/               # built-in agent packages (planner, coder, verifier, security-reviewer, integrator)
  schemas/              # JSON Schemas: manifest, workflow, events, artifacts, runtime API, policy
  apps/desktop/         # Tauri 2 + React
  docs/                 # this documentation set
```

Import rules enforced by a lint step: `providers/*` and `harness/*` may import `model/` only; `tools/` may not import `providers/`; `apps/desktop` has no Go and calls only `api/`.

# 10. Context and memory architecture

| Layer | Content | Lifetime |
|---|---|---|
| Request context | User request, selected repository summary | Task |
| Task context | Inputs, artifact summaries, tool results (tagged) | Task |
| Session context | Compacted transcript, plan, decisions | Session |
| Workspace knowledge (Phase 2) | Conventions, architecture notes produced by the explorer | Workspace |
| Enterprise knowledge (Phase 4) | Retrieval over approved sources with classification | Organization |

Redaction applies before any content enters a model request: secret patterns (keys, tokens, private keys, connection strings) are replaced by placeholders and a `redaction` event records counts, never values.

# 11. Failure domains

| Failure | Behavior |
|---|---|
| Daemon crash | Clients reconnect; sessions resume from SQLite; running tasks become `failed(interrupted)` and can be retried |
| Sandbox backend unavailable | Tasks cannot start; UI explains prerequisites; no fallback to unsandboxed execution |
| Provider outage | Router fallback within tier; otherwise `waiting_for_input` |
| Disk full | Event writes fail closed: the task pauses rather than continuing without audit |
| Keychain locked | Provider calls fail with `auth_failed`; user prompted to unlock |
