# Prompt for Claude Design: system, backend and UI/UX design of the Warden PoC

Attach the whole `markdown/` folder (WRD-00 to WRD-16 plus `img/`). If folders are not accepted, attach the 17 `.docx` files instead (diagrams embedded). Paste the text below.

---

You are the system designer, backend architect and UI/UX designer for the Proof of Concept of "Warden" (working codename), an enterprise AI agent runtime: a secure execution and governance layer that runs AI agents against the models an organization already has (API keys, local models, models the company hosts itself in its data center or cloud tenant, enterprise gateways, vendor subscriptions through their official CLIs), inside a sandbox the agent cannot escape, with every action authorized by a policy engine and recorded in a hash-chained audit trail. The first product is a desktop app with a Claude Code-like developer workflow; the desktop is only a client of an independent runtime daemon.

Your job is to turn the attached specification set into a complete, implementation-ready design for the PoC: system design, backend design and UI/UX design. Another engineer must be able to implement from your output without asking you questions.

# 1. Documents and reading order
Attached: WRD-00 to WRD-16 (Markdown, with diagrams in img/). Read in this order:
1. WRD-16 PoC Concept and Build Plan: this defines the PoC scope and overrides anything larger in the other documents.
2. WRD-11 MVP UX Specification, WRD-01 PRD.
3. WRD-02 System Architecture, WRD-03 Agent Manifest, WRD-04 Tool and Capability API, WRD-05 Canonical Model API, WRD-06 Model Routing, WRD-07 Workflow and Task DAG, WRD-08 Policy Engine, WRD-09 Artifact/Event/Audit Schema, WRD-10 Threat Model and Security.
4. WRD-00 Master Specification (reference; Part III is the decision log; Appendix A is the terminology you must use).
5. WRD-12 to WRD-15 only for context; they are out of PoC scope.
Precedence when documents disagree: WRD-16 > the companion document for that subsystem (WRD-02 to WRD-11) > WRD-00. List every conflict you find in a "Conflicts" section; do not silently resolve them.

# 2. Fixed decisions (do not reopen, do not propose alternatives)
- Runtime daemon `wardend`, CLI `warden`, in-sandbox executor `warden-exec`: Go. Desktop: Tauri 2 shell with `wardend` as a sidecar, React + TypeScript UI. The UI contains no agent logic and never links the runtime.
- IPC: JSON-RPC 2.0 with LSP-style framing over a Unix domain socket (owner-only) with a per-session token; server-push notifications for events.
- Storage: SQLite (WAL) under ~/.warden; events append-only and hash-chained; artifacts content-addressed with provenance; blobs on disk.
- Policy: YAML rules with CEL conditions; exactly three effects (allow, deny, approval_required) plus obligations; layers in the PoC: platform invariants/defaults and user policy; approval scopes once, task, session, workspace; R5 actions never persistable beyond once.
- Sandbox: L1 native (Seatbelt on macOS, bubblewrap + seccomp on Linux), L2 rootless Docker optional; no direct network from the sandbox; egress only through the runtime's per-task proxy with a deny-by-default allowlist; no credentials in the sandbox; model calls made host-side by the daemon.
- Model access: provider adapters `anthropic-messages` and `openai-compatible`. The `openai-compatible` adapter must serve five deployments with one code path: local servers (Ollama, LM Studio; tier T0), models the company hosts itself (vLLM, Ollama, TGI on a VPS, LAN or VPC, and internal LLM gateways; tier T1; auth `gateway` with bearer token or mTLS), frontier models inside a company cloud tenant (Azure OpenAI with the `api-key` header; tier T2), vendor APIs (tier T3; `api_key`). Harness adapters: `copilot-sdk` (required), `codex` app-server (optional), `claude-code` CLI (optional, personal mode only, API key in any shared build); tier T4. Provider = protocol × auth mode × trust tier. Admission for `confidential` workspaces is T0, T1, T2 only; fallback never widens the tier; default strategy prefer-internal.
- Agents: two declarative agents, coder (plan/implement/repair modes) and verifier. One fixed workflow: plan → gate G1 → implement → verify → repair (max 1) → gate G2 → deliver. One worktree per session, tasks sequential.
- Target OS: macOS and Linux. Windows is out.
- Terminology: use WRD-00 Appendix A exactly; keep the identifiers from the documents (hypotheses H1–H6, tasks T1–T6, security scenarios S1–S4, invariants INV-1–INV-9, risk classes R0–R6, tiers T0–T4, decisions D-xx, problems P-xx).

# 3. Non-negotiable invariants your design must visibly enforce
1. No tool executes without a preceding policy.decision event with effect allow for that call (and an approval.resolved when required).
2. The sandbox exposes only the worktree (read-write), a read-only toolchain, a scratch directory and the proxy socket; nothing from the user's home; environment cleared.
3. Secrets exist only in the OS keychain and are injected host-side; they never appear in events, artifacts, logs, sandbox environments or model context.
4. Every observation (file content, command output, harness output) enters model context as untrusted, provenance-tagged data.
5. Workspace/repository content can never widen permissions.
6. Clients (desktop, CLI) reach the runtime only through the JSON-RPC API; the CLI can do everything the desktop can.
7. Data classified `confidential` can only be sent to T0, T1 or T2 providers; the router and the UI must make this visible and enforce it.

# 4. Deliverables, Part A: system and backend design
Produce one Markdown file per item, numbered A01–A18, each ending with a traceability table (design element → WRD document and section, requirement id or invariant it satisfies).
A01 Context and container views (C4 level 1 and 2) for the PoC: desktop, CLI, wardend, warden-exec, sandbox, proxy, providers (local, company-hosted, tenant cloud, vendor API), harnesses, keychain, SQLite, fixtures.
A02 Component view of wardend: Go packages (follow WRD-02 §9 and WRD-16 §5.2), responsibilities, allowed import directions (providers/* and harness/* import only model/; exec imports nothing from the daemon; apps/desktop has no Go), and the lint rule that enforces them.
A03 Sequence diagrams (Mermaid), each annotated with the exact event types from WRD-09 emitted at each step: (a) full request flow T1 from session.open to deliver including both gates and one repair round; (b) a tool call with policy decision, approval_required, user approval with scope, execution, result tagging; (c) routing on a `confidential` workspace with a company-hosted model, provider fallback within tier and the "no admissible model" path; (d) Copilot harness session: spawn in sandbox, tool override, permission hook → policy engine, egress to vendor endpoints only; (e) cancellation during implement, including SIGTERM/SIGKILL timing and partial artifacts; (f) daemon restart and resume from events; (g) audit export and verify --strict.
A04 Data model: refine the SQLite DDL in WRD-16 §11 (indexes, constraints, migrations strategy), the event envelope, the artifact record, the provenance object, the approval record; blob layout; the hash-chain algorithm and the checkpoint signature; retention.
A05 Runtime API contract: every JSON-RPC method in WRD-16 §12 and WRD-02 §4 needed by the PoC, with JSON Schema for params and results, notification payloads, error codes, the token handshake, and versioning.
A06 Executor protocol and sandbox launch: warden-exec JSON-RPC methods with schemas; path canonicalization and root checks; the Seatbelt profile template and the bubblewrap argv (start from WRD-16 §10.2–10.3 and complete them); seccomp syscall list; resource limits; environment allowlist; the in-sandbox proxy forwarder; how the L2 Docker path maps to the same contract.
A07 Egress proxy: connection handling (CONNECT and plain HTTP), allowlist evaluation, per-task listener lifecycle, DNS handling, events, denial → approval hand-off, credential injection hook (present, unused in PoC).
A08 Policy engine: ActionRequest normalization, rule compilation with CEL, evaluation algorithm with the combination rules of WRD-08 §4, obligations merge, decision cache and invalidation, approvals lifecycle (request, resolve, expiry, revoke, scope matching), policy.explain, the platform invariants INV-1–INV-9 as code-level checks, and the golden-test corpus format.
A09 Model router: admission by tier and classification, capability filter, ranking per strategy, quality priors from models.yaml, health/circuit breaker, fallback state machine, per-session pin, routing.decision payload, and how the UI receives "why this model" and "why not that one".
A10 Agent loop: context assembly (order, budgets, redaction, provenance tags), rendering of granted capabilities as provider tool definitions (including the name-mapping rule for restricted characters), proposal handling, denied-call feedback to the model, emulated tool-calling protocol for local models without native tool calling, output schema validation and the single repair turn, compaction, streaming to clients, budget enforcement and the checkpoint artifact.
A11 Provider adapters: canonical ↔ Anthropic Messages and canonical ↔ OpenAI-compatible mapping tables for messages, content blocks, tools, tool results, response_format, usage and stop reasons; the auth-mode matrix for `openai-compatible` (none, api_key with Bearer, api_key with Azure `api-key` header, gateway bearer, gateway mTLS) and how each credential is injected host-side; stream parsing; error normalization to WRD-05 §4 codes; the capability probe run by provider.test; Go interface sketch for Provider.
A12 Harness adapters: Copilot SDK (session lifecycle, tool override, hooks → policy, quota accounting), Codex app-server (thread/turn/approval mapping), Claude Code CLI in personal mode (-p with stream-json, PreToolUse/PostToolUse hooks → policy, the shared-build lock that forces API-key billing); egress allowlists per harness; how a harness task produces the same code-diff and test-report artifacts.
A13 Workflow runner: template instantiation, task state machine with transition table and reason codes, gates, repair round, persistence as task.state events, resume, cancellation propagation, verifier result parsers (Vitest JSON, go test -json, pytest JSON).
A14 Worktree and git handling: session branch, worktree creation, hook neutralization, config isolation, checkpoint commits, commit on session branch, push as host tool, cleanup.
A15 Secrets broker and redaction pipeline: keychain integration, secret:// resolution, injection points, deny-list enforcement at three layers, redaction regex set, redaction events.
A16 Failure modes and security review: table of failure modes with detection, behavior and user-visible message (map to WRD-05 error codes and WRD-02 §11); mapping of WRD-10 threats T-01–T-24 to what the PoC covers, partially covers or defers; the escape-check script specification.
A17 Repository layout, build and CI: Go module layout, Tauri sidecar bundling, GitHub Actions pipeline (lint, unit tests, policy golden tests, escape-check matrix on macOS and Linux), release packaging, versioning.
A18 Implementation backlog: the 8-week plan of WRD-16 §16 broken into epics and stories with acceptance criteria, dependencies and estimates; the week-1 Copilot SDK spike and the week-1 VPS model deployment (Ollama or vLLM behind TLS and a bearer token) specified precisely.

# 5. Deliverables, Part B: UI/UX design
Produce one Markdown file per item, numbered B01–B09.
B01 Information architecture: the seven screens and six states of WRD-16 §13 as a screen map and a state map, navigation model, what lives in the timeline versus the context panel.
B02 User flows (Mermaid flowcharts) with every decision point: first run with the four setup paths (API key, local model, company-hosted endpoint with token or certificate, subscription harness); primary journey T1 including G1, one inline approval, one repair round, G2; classification change to `confidential` making vendor models inadmissible while the company-hosted model keeps working; cancellation and resume; the S1 security scenario as the user experiences it; audit verify and export.
B03 Wireframes: low-fidelity layout specifications for all seven screens and six states (structured layout descriptions with region maps; ASCII or Mermaid where useful); high-fidelity specifications for Session view, Plan review (G1), Approval prompt and Result review (G2), including exact content, hierarchy, sizes and responsive behavior between 1024 and 1920 px.
B04 Component inventory with props, states and data source (which API method or event feeds each field): timeline entry, task card (with live step counter and collapsed tool calls), tool call row, routing line ("Chosen: … because …"), plan card, approval card, diff viewer with hunk-to-task provenance link, test report, cost panel (tokens, quota units, currency), status badges (classification, sandbox level, chain status), model picker showing admissible models with tier labels and greyed-out reasons, capability summary chip, doctor checklist, provider card with tier, auth mode and vendor-terms notice.
B05 Interaction specification: keyboard model (A approve, R reject, 1–4 scope, navigation), focus management, streaming updates without layout jumps, optimistic versus confirmed states, non-dismissable approval prompts with a pending-approval badge, OS notifications, error surfaces, cancel always reachable; accessibility to WCAG 2.2 AA.
B06 Visual design: design tokens (light and dark palettes, semantic colors for allow/deny/approval/untrusted and for tiers T0–T4, typography scale, spacing, radii, elevation), iconography rules, density, motion with reduced-motion support; the tone is a professional developer tool, not a chat app and not a marketing surface.
B07 Content and microcopy: approval prompt texts (what, who, why, scope), routing explanations including "not admissible for confidential data" reasons, policy reasons per rule id, error messages mapped from the WRD-05 error codes and the failure modes in A16, empty states, the vendor-terms notices for harnesses, confirmation texts for Commit and Push.
B08 Frontend architecture: React app structure, server-state handling over the JSON-RPC event subscription (TanStack Query or equivalent), the Tauri bridge design (Rust commands exposing the socket and token to the webview, or a localhost WebSocket shim with token), reconnection and replay from event cursors, timeline virtualization, diff viewer choice and integration, i18n scaffolding, testing (unit, component, Playwright end-to-end against a mocked daemon).
B09 UX acceptance: how the design meets hypothesis H6 (a developer other than the author completes T1 in under 15 minutes with at most three approval prompts beyond the gates), the usability test script, and the metrics the UI must record locally (prompts per task, time to first approval, plan edit rate, cancel rate).

# 6. Format and conventions
- Output: Markdown files, one per deliverable, with a README.md index. Diagrams as Mermaid (C4, sequence, state, class, flowchart) with a one-paragraph description each. Contracts as JSON Schema draft 2020-12. Go as interface and struct sketches, not full implementations. YAML examples must be consistent with the examples in the documents (same field names).
- Every deliverable ends with a traceability table to the WRD documents and a "Deviations and assumptions" list.
- State assumptions and proceed; collect open questions in a final OPEN-QUESTIONS.md with your recommended answer for each.
- Do not add features beyond WRD-16 §2.1. Do not design the control plane, registries, MCP, parallel worktrees, SDK, Windows or a marketplace. Do not frame the product as a chat application: a request is a job with a plan, gates and evidence.
- Quality bar: (1) another engineer implements from the design without asking questions; (2) every sequence diagram names the events from WRD-09 in order; (3) every UI element that shows a decision, a routing choice or a cost has a named data source in the API; (4) every invariant in section 3 is visibly enforced somewhere in the design and referenced in the traceability tables.

# 7. Process
1. Read the documents in order; write a scope summary of at most 200 words and the Conflicts section.
2. Produce Part A (A01–A18), then Part B (B01–B09).
3. Finish with OPEN-QUESTIONS.md and the backlog (A18).
If you must choose between depth and breadth, prefer depth on A03, A05, A06, A08, A09, A10, A12, B03, B04 and B05: these are the parts the implementer will read most.

---

If the tool has an output-length limit, run it in two sessions with the same prompt: first "Produce Part A only", then "Produce Part B only, using Part A as an input".
