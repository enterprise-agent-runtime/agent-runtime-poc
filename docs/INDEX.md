# Documentation index

Inventory of everything in `docs/` of the Warden PoC repository: one line per file, with its layer. Built in Session 0 (PROMPT-Claude-Code.md, Part 2, step 1). Paths are relative to `docs/`.

## How to read this index

Layers:

| Layer | Meaning | Who may edit |
|---|---|---|
| specification | WRD-00 to WRD-16 and the diagrams in `docs/docs/img/`. Defines the product and the PoC scope. WRD-16 wins over the companion WRD for a subsystem (WRD-02 to WRD-11), which wins over WRD-00. | Nobody in the PoC repo (read-only; copied 1:1) |
| design | `design/`: A01 to A18 (system and backend), B01 to B09 (UI/UX), `00-*`, `OPEN-QUESTIONS.md`, `README.md`. Says how to build it. Can never add scope or weaken an invariant; a contradiction with `CLAUDE.md` or WRD-16 is a defect to record in `CONFLICTS.md`. | Nobody in the PoC repo (read-only) |
| other | Design brief (input to the design), ADR templates, doc-build tooling, READMEs. | Nobody in the PoC repo |
| working | Files the implementer writes and maintains. | The implementer |

Directory structure of the copy (kept 1:1 from the source set):

```
docs/
  INDEX.md CONFLICTS.md DECISIONS-poc.md PROGRESS.md POC-REPORT.md PROMPT-Claude-Code.md   (working)
  README.md                                   root README of the documentation set (stale, see notes)
  PROMPT-Claude-Design.md                     design brief
  WRD-16-PoC-Concept-and-Build-Plan.md        PoC specification (root of the set)
  design/                                     A01-A18, B01-B09, 00-*, OPEN-QUESTIONS, README
  docs/                                       WRD-00 ... WRD-15 and docs/README.md
  docs/img/                                   21 diagrams (.dot + .png)
  decisions/                                  ADR template and README
  tools/                                      Markdown to Word build tooling
```

Counts: the source set has 99 files (excluding `.git`): 3 at the root + 2 in `decisions/` + 31 in `design/` + 17 in `docs/` (16 WRD + README) + 42 in `docs/img/` (21 `.dot`/`.png` pairs) + 4 in `tools/`. With the 6 working files below, `docs/` holds 105 files; the 42 image files appear as 21 pair rows.

## Notes and findings from the inventory

1. **Path nesting.** WRD-16 sits at the root of the set, but WRD-00 to WRD-15 are in the `docs/` subfolder of the set, so in the PoC repo they are at `docs/docs/WRD-xx-...`. `PROMPT-Claude-Code.md` writes `docs/WRD-16-PoC-Concept-and-Build-Plan.md` (correct) and refers to WRD-02 and the others without paths. Links in this index follow the real layout.
2. **Five WRD-16 figures are missing.** WRD-16 embeds `img/poc_scope.png`, `img/poc_components.png`, `img/poc_access_modes.png`, `img/poc_workflow.png` and `img/poc_request_flow.png`. None exists in this copy: `docs/docs/img/` holds only the 21 diagrams used by WRD-00 to WRD-15, and neither a `.dot` source nor an entry in `tools/diagrams.py` produces them. Because WRD-16 is at the root, its relative `img/` path would also point at a folder that does not exist (`docs/img/`); the existing images are in `docs/docs/img/`. The design set says "26 diagrams" (`design/00-SCOPE-AND-CONFLICTS.md`, line 3): 21 present plus 5 missing is 26. Record in `CONFLICTS.md` as a documentation defect; the text of WRD-16 is complete without the figures.
3. **Stale root `README.md`.** It lists `docs/00-product-vision.md`, `01-poc-specification.md` and others, and `docs/versioning.md`; none exist. The real index of the WRD set is `docs/docs/README.md`. Do not rely on the root README.
4. **Stale paths in `tools/`.** `tools/README.md`, `build.sh` and `diagrams.py` expect `../markdown/` (and write to `../out/`); in this layout the sources are `docs/docs/` and `docs/docs/img/`. The tools are reference only; the PoC does not need to build Word files.
5. **Design brief mentions `markdown/`.** `PROMPT-Claude-Design.md` says to attach "the whole `markdown/` folder"; it is the same content as the WRD files here.
6. **Design set status.** `design/README.md` (26 September 2026): about 25,000 lines in 31 files, 67 Mermaid diagrams, 44 recorded conflicts between the sources (CF-01 to CF-44, four of them defects inside WRD-16: Seatbelt rule order, deny-list vs worktree path, git without the main `.git`, figures) and 36 open questions (OQ-xx), each with a recommended answer already assumed. `design/00-SCOPE-AND-CONFLICTS.md` is the first input for `CONFLICTS.md`.
7. **Session 0 reading list omits two design files.** Part 2 step 2 names A01 to A05, A08, A10, A12, A13, B01, B03 and B04, but not `design/00-DESIGN-CORE.md` (the identifier registry every deliverable cites) nor `design/00-SCOPE-AND-CONFLICTS.md`. `design/README.md` recommends reading both first. Read them with the list.
8. **Windows.** The design brief fixes macOS and Linux as targets and puts Windows out; WRD-16 §2.3 lists L2 as optional on macOS/Linux and default on Windows. `PROMPT-Claude-Code.md` adds Windows work (named pipes, Docker Desktop L2, installer) in M1, M2, M4, M6 and M7, which no design document covers. Check against `CLAUDE.md` and record in `CONFLICTS.md`.
9. **A wrong section reference in the prompt.** M2 cites "WRD-16 §10.4" for the first-class OCI sandbox, but WRD-16 §10.4 is "Egress proxy" (§10.2 Seatbelt, §10.3 bubblewrap, §10.7 escape check). L2 is covered in A06 and WRD-10 §5.

## Working files (written by the implementer)

| File | Layer | Description |
|---|---|---|
| [INDEX.md](INDEX.md) | working | This file: inventory of `docs/`, design deliverable mapping, milestone reading table. |
| [CONFLICTS.md](CONFLICTS.md) | working | Every contradiction between the design output and `CLAUDE.md`, WRD-16 or an invariant, with both references, the precedence rule applied and the resolution or "needs owner". Started in Session 0, extended by the Copilot spike. |
| [DECISIONS-poc.md](DECISIONS-poc.md) | working | Decision log of the implementation: environment decisions of `CLAUDE.md` section 3 as recorded facts, then every answer to a question and every decide-and-record choice. |
| [PROGRESS.md](PROGRESS.md) | working | Per-milestone checklists, the acceptance commands run and their results; updated at the end of every session. |
| [POC-REPORT.md](POC-REPORT.md) | working | Not yet written (M8): pass/fail and numbers for hypotheses H1 to H6 and the list of simplifications to upgrade for the MVP (WRD-16 §2.3 and §18). |
| [PROMPT-Claude-Code.md](PROMPT-Claude-Code.md) | working | The implementer's build prompt: one-time setup, Session 0 and M1 kickoff, milestones M1 to M8 with acceptance criteria, overriding rules, session start block. Copied from `C:\Users\pallo\Downloads\PROMPT-Claude-Code.md`. |

## Specification layer

### PoC specification (root)

| File | Layer | Description |
|---|---|---|
| [WRD-16-PoC-Concept-and-Build-Plan.md](WRD-16-PoC-Concept-and-Build-Plan.md) | specification | v0.1, 26 Sep 2026. **Defines PoC scope and overrides everything larger.** 18 sections: hypotheses and proof (1), scope in/out and simplifications register (2), demo script (3), fixtures, tasks T1 to T6, security scenarios S1 to S4 (4), architecture and repo layout (5), model access and routing (6), agents (7), workflow (8), tools and executor (9), security incl. Seatbelt, bubblewrap, proxy, policy file, escape check (10), store, events, artifacts DDL (11), API subset (12), UI/UX brief (13), CLI (14), acceptance checklist (15), 8-week build plan (16), design handoff (17), hand-off to the MVP (18). References 5 figures that are missing (note 2). |

### WRD-00 to WRD-15 (`docs/docs/`)

| File | Layer | Description |
|---|---|---|
| [docs/WRD-00-Master-Specification-v0.5.md](docs/WRD-00-Master-Specification-v0.5.md) | specification | Master spec v0.5 (921 lines): analysis of the drafts (Part I), refined spec in 33 sections (Part II: vision, principles, architecture, runtime primitives, manifest, agent loop, models, tools, policy, security, workflow, MVP, SDK, eval, control plane, roadmap), **decision log D-xx (Part III)**, **terminology (Appendix A)**. Lowest precedence. |
| [docs/WRD-01-MVP-Product-Requirements.md](docs/WRD-01-MVP-Product-Requirements.md) | specification | MVP PRD: objective, personas, primary user journey, functional and non-functional requirements, acceptance criteria, non-goals, risks, metrics. |
| [docs/WRD-02-System-Architecture-Specification.md](docs/WRD-02-System-Architecture-Specification.md) | specification | Architectural invariants, component view, process topology and IPC, runtime API summary (§4), trust boundaries, data flow per task, technology decisions, repository structure (§9), failure domains (§11). |
| [docs/WRD-03-Agent-Manifest-Specification-v0.1.md](docs/WRD-03-Agent-Manifest-Specification-v0.1.md) | specification | Agent package layout, manifest schema, full `coder` example, validation rules (§5), versioning, signing and trust levels. |
| [docs/WRD-04-Tool-and-Capability-API-Specification-v0.1.md](docs/WRD-04-Tool-and-Capability-API-Specification-v0.1.md) | specification | Tool descriptors, risk classes R0 to R6, capability grammar, `warden-exec` executor protocol (§5), built-in tools, result handling, command profiles, call examples. MCP bridge is Phase 2. |
| [docs/WRD-05-Canonical-Model-API-Specification-v0.1.md](docs/WRD-05-Canonical-Model-API-Specification-v0.1.md) | specification | Provider = protocol x auth x hosting; canonical request and stream events with error codes (§3–§4), adapter contract, protocol adapters, capability emulation, auth and billing modes, harness adapters (§9), local models. |
| [docs/WRD-06-Model-Routing-Policy-Specification.md](docs/WRD-06-Model-Routing-Policy-Specification.md) | specification | Data classifications, provider trust tiers T0 to T4, admission matrix, routing inputs, algorithm, tier-bounded fallback, health and quotas, routing policy syntax, worked examples, audit. |
| [docs/WRD-07-Workflow-and-Task-DAG-Specification.md](docs/WRD-07-Workflow-and-Task-DAG-Specification.md) | specification | Workflow schema, scheduler, task state machine (§4), retries, repair rounds, approval gates, cancellation, planner contract, worktree lifecycle, artifact-based communication. |
| [docs/WRD-08-Policy-Engine-Specification.md](docs/WRD-08-Policy-Engine-Specification.md) | specification | Action request and decision, layers and precedence with combination rules (§4), platform invariants (§5), YAML + CEL rule syntax, approvals and scopes, explainability, non-interactive mode, caching, testing. |
| [docs/WRD-09-Artifact-Event-and-Audit-Schema.md](docs/WRD-09-Artifact-Event-and-Audit-Schema.md) | specification | Event envelope and taxonomy (the event types every sequence diagram cites), hash chain and checkpoints (§4), artifact record and types, usage and cost records, storage layout, retention and export, privacy. |
| [docs/WRD-10-Threat-Model-and-Security-Requirements.md](docs/WRD-10-Threat-Model-and-Security-Requirements.md) | specification | Assets, attackers, trust boundaries, sandbox design per OS (§5), secret deny-list, egress proxy, threat catalog T-01 to T-24 (§8), prompt-injection defenses, testable security requirements, sandbox escape test suite. |
| [docs/WRD-11-MVP-User-Experience-Specification.md](docs/WRD-11-MVP-User-Experience-Specification.md) | specification | Experience principles, screens, states and empty states, CLI parity, errors and messages, accessibility and localization, UX instrumentation. |
| [docs/WRD-12-Evaluation-Framework-Specification.md](docs/WRD-12-Evaluation-Framework-Specification.md) | specification | Evaluation suite format, fixtures, graders, metrics, gates, first eval tasks. Context only; out of PoC scope. |
| [docs/WRD-13-Deployment-and-Operations-Guide.md](docs/WRD-13-Deployment-and-Operations-Guide.md) | specification | Installation per OS, directories, provider setup, operating the daemon, updates, backup, troubleshooting, later deployment models. Context only; out of PoC scope. |
| [docs/WRD-14-Agent-SDK-Specification.md](docs/WRD-14-Agent-SDK-Specification.md) | specification | Declarative vs coded agents, Agent Protocol, SDK surface (TypeScript, Go, Python), packaging, testing. Phase 4; out of PoC scope. |
| [docs/WRD-15-Enterprise-Control-Plane-Specification.md](docs/WRD-15-Enterprise-Control-Plane-Specification.md) | specification | Identity and RBAC, registries, policy distribution, worker fleet, audit ingestion, offline behavior. Phase 3; out of PoC scope. |
| [docs/README.md](docs/README.md) | other | Table of WRD-00 to WRD-15 with file names; says the Word files are generated from the Markdown; says `img/` holds the 21 diagrams (correct). |

### Diagrams (`docs/docs/img/`)

Each diagram is a Graphviz source (`.dot`) plus its rendered PNG, built by `tools/diagrams.py`. All 21 pairs are listed, with the WRD figures that embed them.

| Files | Layer | Description |
|---|---|---|
| [docs/img/architecture_layers.dot](docs/img/architecture_layers.dot) / [.png](docs/img/architecture_layers.png) | specification | Layered architecture: clients (desktop, CLI, CI, remote API) over the runtime API over the `wardend` kernel (the trust boundary) over providers, harnesses, executors. WRD-00 fig 1, WRD-02 fig 1. |
| [docs/img/process_topology.dot](docs/img/process_topology.dot) / [.png](docs/img/process_topology.png) | specification | Workstation process topology: desktop shell and CLI as clients of `wardend`, OS keychain, `~/.warden`, per-task sandbox with `warden-exec`. WRD-00 fig 2, WRD-02 fig 2. |
| [docs/img/manifest_structure.dot](docs/img/manifest_structure.dot) / [.png](docs/img/manifest_structure.png) | specification | Agent package structure (manifest, prompts, schemas, evals, signature), workflow definition, optional coded implementation. WRD-00 fig 3, WRD-03 fig 1. |
| [docs/img/agent_loop.dot](docs/img/agent_loop.dot) / [.png](docs/img/agent_loop.png) | specification | Agent loop: build context, budget check, router, canonical model call, response handling, every proposal through the policy decision point, output schema validation. WRD-00 fig 4. |
| [docs/img/model_stack.dot](docs/img/model_stack.dot) / [.png](docs/img/model_stack.png) | specification | Model integration stack: agent, canonical model API, router, provider (protocol x auth x tier), protocol adapters, harnesses. WRD-00 fig 5, WRD-05 fig 1. |
| [docs/img/harness_integration.dot](docs/img/harness_integration.dot) / [.png](docs/img/harness_integration.png) | specification | Vendor harness (Copilot, Codex, Claude Code) running inside the sandbox, governed through hooks and permission requests routed to the policy decision point. WRD-00 fig 6, WRD-05 fig 2. |
| [docs/img/routing_decision.dot](docs/img/routing_decision.dot) / [.png](docs/img/routing_decision.png) | specification | Routing decision flow: maximum tier per data classification, capability filter, ranking by strategy, health check, tier-bounded fallback. WRD-00 fig 7, WRD-06 fig 1. |
| [docs/img/policy_flow.dot](docs/img/policy_flow.dot) / [.png](docs/img/policy_flow.png) | specification | Policy evaluation: action request, capability check, platform invariants, layered rule sets, combination into effect plus obligations. WRD-00 fig 8, WRD-08 fig 1. |
| [docs/img/trust_boundaries.dot](docs/img/trust_boundaries.dot) / [.png](docs/img/trust_boundaries.png) | specification | Untrusted inputs (repository content, model output, tool results, third-party packages, UI requests) versus the trusted computing base. WRD-00 fig 9, WRD-02 fig 3, WRD-10 fig 1. |
| [docs/img/sandbox_levels.dot](docs/img/sandbox_levels.dot) / [.png](docs/img/sandbox_levels.png) | specification | Sandbox levels L0 to L3: none, OS-native (bubblewrap/Seatbelt), rootless container, microVM. WRD-00 fig 10, WRD-10 fig 2. |
| [docs/img/workflow_lifecycle.dot](docs/img/workflow_lifecycle.dot) / [.png](docs/img/workflow_lifecycle.png) | specification | Universal workflow lifecycle: intent, context, classify, plan, decompose, delegate, execute, observe, verify, repair, approve, deliver. WRD-00 fig 11, WRD-07 fig 1. |
| [docs/img/orchestration_dag.dot](docs/img/orchestration_dag.dot) / [.png](docs/img/orchestration_dag.png) | specification | MVP coding template as a DAG: exploration and analysis tasks, plan, gate G1, implementation tasks, verification, repair branch, gate G2. WRD-00 fig 12, WRD-07 fig 2. Shows parallel worktrees, which the PoC does not have. |
| [docs/img/task_state_machine.dot](docs/img/task_state_machine.dot) / [.png](docs/img/task_state_machine.png) | specification | Task state machine with attempts, approvals, cancellation and skip/block propagation. WRD-00 fig 13, WRD-07 fig 3. |
| [docs/img/data_model.dot](docs/img/data_model.dot) / [.png](docs/img/data_model.png) | specification | Core data model entities: workspace, session, workflow run, task, execution, event, artifact. WRD-00 fig 14, WRD-09 fig 1. |
| [docs/img/provenance_chain.dot](docs/img/provenance_chain.dot) / [.png](docs/img/provenance_chain.png) | specification | Hash-chained events with signed checkpoints, and an artifact's provenance pointing back into the chain. WRD-00 fig 15, WRD-09 fig 2. |
| [docs/img/ux_flow.dot](docs/img/ux_flow.dot) / [.png](docs/img/ux_flow.png) | specification | MVP user journey: request, exploring and planning, plan review (G1), implementation, verification, result review (G2), delivery. WRD-00 fig 16, WRD-11 fig 1. |
| [docs/img/registry_lifecycle.dot](docs/img/registry_lifecycle.dot) / [.png](docs/img/registry_lifecycle.png) | specification | Agent lifecycle: develop, run locally, evaluate, review, sign and publish, assign, deploy, monitor. WRD-00 fig 17, WRD-15 fig 2. Out of PoC scope. |
| [docs/img/eval_pipeline.dot](docs/img/eval_pipeline.dot) / [.png](docs/img/eval_pipeline.png) | specification | Evaluation pipeline: suite YAML, fresh sandbox per case, graders, report, gate. WRD-00 fig 18, WRD-12 fig 1. Out of PoC scope. |
| [docs/img/deployment_local.dot](docs/img/deployment_local.dot) / [.png](docs/img/deployment_local.png) | specification | Local single-workstation deployment: desktop/CLI, `wardend`, sandbox, `~/.warden`, model providers, local models, git host. WRD-00 fig 19, WRD-13 fig 1. |
| [docs/img/deployment_hybrid.dot](docs/img/deployment_hybrid.dot) / [.png](docs/img/deployment_hybrid.png) | specification | Hybrid deployment with an enterprise control plane (IdP, registries, policy service, audit ingestion). WRD-00 fig 20, WRD-13 fig 2. Out of PoC scope. |
| [docs/img/control_plane.dot](docs/img/control_plane.dot) / [.png](docs/img/control_plane.png) | specification | Control plane services (identity, registries, fleet, audit) and the execution plane of `wardend` instances. WRD-00 fig 21, WRD-15 fig 1. Out of PoC scope. |

## Design layer (`design/`)

Produced from `PROMPT-Claude-Design.md` and the WRD set. Every deliverable ends with `## Traceability` and `## Deviations and assumptions`. Line counts are from the files.

### Entry points

| File | Layer | Description |
|---|---|---|
| [design/README.md](design/README.md) | design | Index of the design set: tables of A01 to A18 and B01 to B09 with contents and line counts, which files are the "depth" files, checks performed, suggested implementer reading order (00-SCOPE-AND-CONFLICTS, 00-DESIGN-CORE, A01, A02, A03, A05, A04, A08, A09, A10, A06, A07, A13, A14, A15, A11, A12, A16, A17, A18, then B01, B03, B04, B05, B02, B06, B07, B08, B09). |
| [design/00-SCOPE-AND-CONFLICTS.md](design/00-SCOPE-AND-CONFLICTS.md) | design | Scope summary (at most 200 words) and the Conflicts section: 44 conflicts CF-01 to CF-44 between the WRD documents, each with the deciding rule and the resolution used; four are defects in WRD-16. 58 lines. |
| [design/00-DESIGN-CORE.md](design/00-DESIGN-CORE.md) | design | Registry used by every deliverable: conventions, identifiers, enums, event types and payload fields, API methods, error codes, rule ids, Go packages, UI names, token names, binding behavioural decisions (§13), post-draft integration decisions ID-01 to ID-18 (§15). 321 lines. |
| [design/OPEN-QUESTIONS.md](design/OPEN-QUESTIONS.md) | design | 36 open questions (OQ-xx), each with a recommended answer already assumed by the design, owner codes F/A/S/D and the files affected. 75 lines. |

### Part A: system and backend

| File | Layer | Description |
|---|---|---|
| [design/A01-context-and-containers.md](design/A01-context-and-containers.md) | design | C4 levels 1 and 2, container catalog, trust boundaries, where each brief invariant BI-1 to BI-7 is enforced. 279 lines. |
| [design/A02-wardend-components.md](design/A02-wardend-components.md) | design | Go packages of `wardend`, responsibilities, interfaces, events per package, import matrix, `internal/archtest` and depguard lint rules. 1,127 lines. |
| [design/A03-sequence-diagrams.md](design/A03-sequence-diagrams.md) | design | 15 Mermaid sequence diagrams with every WRD-09 event in order: full T1 flow with gates and repair, approval flow, confidential routing and fallback, Copilot harness, cancellation, restart and resume, audit verify. 1,343 lines. |
| [design/A04-data-model.md](design/A04-data-model.md) | design | SQLite DDL (17 tables, append-only triggers), event envelope and 45 payload schemas, artifacts and provenance, hash chains, signed checkpoints, blob layout, retention, migrations. 2,037 lines. |
| [design/A05-runtime-api.md](design/A05-runtime-api.md) | design | JSON-RPC transport, token handshake, versioning, every method with JSON Schema params and results, errors, notifications, examples, desktop and CLI parity tables. 2,214 lines. |
| [design/A06-executor-and-sandbox.md](design/A06-executor-and-sandbox.md) | design | `warden-exec` protocol, path canonicalization, full Seatbelt template, full bwrap argv, seccomp list, limits, env allowlist, forwarder, L2 Docker mapping, escape-check mechanism table. 1,445 lines. |
| [design/A07-egress-proxy.md](design/A07-egress-proxy.md) | design | Per-task proxy listener, CONNECT and plain HTTP, allowlist formula, DNS and SSRF protection, approval-held connections, events. 439 lines. |
| [design/A08-policy-engine.md](design/A08-policy-engine.md) | design | ActionRequest normalization, CEL environment, platform defaults and user policy YAML, evaluation algorithm, INV-1 to INV-9 checks, approvals and grants, cache, `policy.explain`, 67 golden cases. 1,993 lines. |
| [design/A09-model-router.md](design/A09-model-router.md) | design | Admission by tier and classification, filters, ranking keys, circuit breaker, fallback state machine, pin, `routing.decision` examples, why and why-not strings. 766 lines. |
| [design/A10-agent-loop.md](design/A10-agent-loop.md) | design | Context assembly and budgets, untrusted-data wrapper, tool rendering and name mapping, emulated tool calling, schema repair turn, compaction, streaming, checkpoint artifact, coder and verifier manifests. 1,455 lines. |
| [design/A11-provider-adapters.md](design/A11-provider-adapters.md) | design | Canonical to Anthropic and to OpenAI-compatible mappings, auth-mode matrix, stream parsing, error normalization, capability probe, deployment notes. 912 lines. |
| [design/A12-harness-adapters.md](design/A12-harness-adapters.md) | design | Copilot SDK split mode, Codex app-server, Claude Code personal mode and shared-build lock, credential exception HX-1, quota accounting, spike checklist. 862 lines. |
| [design/A13-workflow-runner.md](design/A13-workflow-runner.md) | design | Templates, task state machine and transition table, gates, repair, retries, resume, cancellation, delivery, verifier result parsers. 577 lines. |
| [design/A14-worktree-and-git.md](design/A14-worktree-and-git.md) | design | Private session git dir with alternates, hook neutralization, config isolation, checkpoints, host-side delivery (commit, push), cleanup. 478 lines. |
| [design/A15-secrets-and-redaction.md](design/A15-secrets-and-redaction.md) | design | Keychain integration, `secret://` resolution, injection points, three-layer deny-list, redaction regexes, canary tests. 433 lines. |
| [design/A16-failure-modes-and-security-review.md](design/A16-failure-modes-and-security-review.md) | design | 45 failure modes with detection and user-visible message, T-01 to T-24 coverage, accepted risks, escape-check script specification. 205 lines. |
| [design/A17-repo-build-ci.md](design/A17-repo-build-ci.md) | design | Go module layout, builds, Tauri sidecar bundling, GitHub Actions pipeline, release packaging, versioning. 437 lines. |
| [design/A18-implementation-backlog.md](design/A18-implementation-backlog.md) | design | Epics and stories for the 8 weeks with acceptance criteria, capacity check and cut lines, Copilot SDK spike, VPS model deployment. 315 lines. |

### Part B: UI/UX

| File | Layer | Description |
|---|---|---|
| [design/B01-information-architecture.md](design/B01-information-architecture.md) | design | Seven screens (SCR-1 to SCR-7) and six states (ST-1 to ST-6) as screen map and state map, routes, navigation, timeline vs context panel. 387 lines. |
| [design/B02-user-flows.md](design/B02-user-flows.md) | design | Mermaid flows F1 to F6 with every decision point: first run (four setup paths), T1 journey, switch to `confidential`, cancel and resume, scenario S1, audit verify and export. 454 lines. |
| [design/B03-wireframes.md](design/B03-wireframes.md) | design | Low-fi layouts for 7 screens and 6 states; hi-fi for Session view, Plan review (G1), Approval prompt and Result review (G2) at 1024 to 1920 px. 1,315 lines. |
| [design/B04-component-inventory.md](design/B04-component-inventory.md) | design | 27 components with props, states and the API method or event field feeding each field; hunk-to-task provenance. 2,129 lines. |
| [design/B05-interaction-specification.md](design/B05-interaction-specification.md) | design | Keyboard model (A, R, 1 to 4), focus, streaming without layout jumps, optimistic vs confirmed, approvals, gates, notifications, cancel, WCAG 2.2 AA; rules IR-n. 885 lines. |
| [design/B06-visual-design.md](design/B06-visual-design.md) | design | Design tokens (light and dark, contrast-checked), semantic colors for effects, tiers and classifications, type, spacing, icon map, density, motion. 597 lines. |
| [design/B07-content-and-microcopy.md](design/B07-content-and-microcopy.md) | design | ICU message catalog: voice rules, approvals, routing reasons, policy reasons, errors, empty states, vendor-terms notices, commit and push confirmations. 1,030 lines. |
| [design/B08-frontend-architecture.md](design/B08-frontend-architecture.md) | design | React 19 + TypeScript app structure, typed JSON-RPC client generated from A05, TanStack Query + event store, Tauri Rust bridge, replay, virtualization, diff viewer, i18n, tests. 565 lines. |
| [design/B09-ux-acceptance.md](design/B09-ux-acceptance.md) | design | How the design meets hypothesis H6, usability test script, local metrics (prompts per task, time to first approval, plan edit rate, cancel rate), report template. 342 lines. |

## Other

| File | Layer | Description |
|---|---|---|
| [PROMPT-Claude-Design.md](PROMPT-Claude-Design.md) | other | The design brief (input to the design set, not an implementation spec): role, reading order and precedence, fixed decisions, seven non-negotiable invariants, Part A deliverables A01 to A18 (§4), Part B deliverables B01 to B09 (§5), format and process. |
| [README.md](README.md) | other | Root README of the documentation repository; stale (lists `docs/00-product-vision.md` etc. and `docs/versioning.md`, which do not exist). See note 3. |
| [decisions/README.md](decisions/README.md) | other | ADR naming (`ADR-XXX-short-title.md`) and rationale. |
| [decisions/ADR-000-template.md](decisions/ADR-000-template.md) | other | ADR template: status, date, owners, related docs, context, decision, consequences, alternatives, follow-up. |
| [tools/README.md](tools/README.md) | other | Documentation-build tooling usage (Markdown to Word, diagram rendering); expects the old `../markdown/` layout. |
| [tools/build.sh](tools/build.sh) | other | Regenerates diagrams and all Word files from `../markdown/WRD-*.md` into `../out/`; needs Node `docx` and Graphviz. |
| [tools/diagrams.py](tools/diagrams.py) | other | Python script holding the Graphviz sources and rendering the 21 diagrams to `../markdown/img`; has no source for the 5 missing WRD-16 figures. |
| [tools/md2docx.js](tools/md2docx.js) | other | Node script converting one constrained Markdown file (front matter, tables, code, images, callouts) to a styled `.docx` with `docx-js`. |

## Design deliverables per PROMPT-Claude-Design.md §4–§5

Source: `PROMPT-Claude-Design.md` §4 (Part A) and §5 (Part B), plus the extras from §6 and §7. All 27 requested deliverables exist and none is missing. Every file is under `design/` and is named exactly as the brief numbers it (`A01-…md`, `B01-…md`), so no renaming is involved.

| Id | Requested deliverable | Actual file | Status |
|---|---|---|---|
| A01 | Context and container views (C4 level 1 and 2) | [design/A01-context-and-containers.md](design/A01-context-and-containers.md) | present |
| A02 | Component view of `wardend` (Go packages, import rules, lint) | [design/A02-wardend-components.md](design/A02-wardend-components.md) | present |
| A03 | Sequence diagrams (a) to (g) with WRD-09 events | [design/A03-sequence-diagrams.md](design/A03-sequence-diagrams.md) | present (15 diagrams; the brief asked for 7) |
| A04 | Data model (DDL, envelope, artifacts, hash chain, retention) | [design/A04-data-model.md](design/A04-data-model.md) | present |
| A05 | Runtime API contract (JSON Schema, errors, handshake) | [design/A05-runtime-api.md](design/A05-runtime-api.md) | present |
| A06 | Executor protocol and sandbox launch | [design/A06-executor-and-sandbox.md](design/A06-executor-and-sandbox.md) | present |
| A07 | Egress proxy | [design/A07-egress-proxy.md](design/A07-egress-proxy.md) | present |
| A08 | Policy engine | [design/A08-policy-engine.md](design/A08-policy-engine.md) | present |
| A09 | Model router | [design/A09-model-router.md](design/A09-model-router.md) | present |
| A10 | Agent loop | [design/A10-agent-loop.md](design/A10-agent-loop.md) | present |
| A11 | Provider adapters | [design/A11-provider-adapters.md](design/A11-provider-adapters.md) | present |
| A12 | Harness adapters | [design/A12-harness-adapters.md](design/A12-harness-adapters.md) | present |
| A13 | Workflow runner | [design/A13-workflow-runner.md](design/A13-workflow-runner.md) | present |
| A14 | Worktree and git handling | [design/A14-worktree-and-git.md](design/A14-worktree-and-git.md) | present |
| A15 | Secrets broker and redaction pipeline | [design/A15-secrets-and-redaction.md](design/A15-secrets-and-redaction.md) | present |
| A16 | Failure modes and security review | [design/A16-failure-modes-and-security-review.md](design/A16-failure-modes-and-security-review.md) | present |
| A17 | Repository layout, build and CI | [design/A17-repo-build-ci.md](design/A17-repo-build-ci.md) | present |
| A18 | Implementation backlog (8 weeks, spike, VPS model) | [design/A18-implementation-backlog.md](design/A18-implementation-backlog.md) | present |
| B01 | Information architecture | [design/B01-information-architecture.md](design/B01-information-architecture.md) | present |
| B02 | User flows | [design/B02-user-flows.md](design/B02-user-flows.md) | present |
| B03 | Wireframes | [design/B03-wireframes.md](design/B03-wireframes.md) | present |
| B04 | Component inventory | [design/B04-component-inventory.md](design/B04-component-inventory.md) | present |
| B05 | Interaction specification | [design/B05-interaction-specification.md](design/B05-interaction-specification.md) | present |
| B06 | Visual design | [design/B06-visual-design.md](design/B06-visual-design.md) | present |
| B07 | Content and microcopy | [design/B07-content-and-microcopy.md](design/B07-content-and-microcopy.md) | present |
| B08 | Frontend architecture | [design/B08-frontend-architecture.md](design/B08-frontend-architecture.md) | present |
| B09 | UX acceptance | [design/B09-ux-acceptance.md](design/B09-ux-acceptance.md) | present |

Other items the brief asks for (§6 and §7), and extras:

| Brief item | Actual file | Note |
|---|---|---|
| `README.md` index (§6) | [design/README.md](design/README.md) | present |
| `OPEN-QUESTIONS.md` with recommended answers (§6, §7 step 3) | [design/OPEN-QUESTIONS.md](design/OPEN-QUESTIONS.md) | present |
| Scope summary of at most 200 words and a "Conflicts" section (§1, §7 step 1) | [design/00-SCOPE-AND-CONFLICTS.md](design/00-SCOPE-AND-CONFLICTS.md) | present as one file named `00-…`; the brief names no file for it |
| Traceability table and "Deviations and assumptions" at the end of every deliverable (§6) | last two sections of each file | `design/README.md` states they are present; not re-checked file by file here |
| (not requested) | [design/00-DESIGN-CORE.md](design/00-DESIGN-CORE.md) | **Extra.** Shared identifier and decision registry (including the integration decisions ID-01 to ID-18); every A and B file cites it. Treat as part of the design layer and read it early. |

Missing: none. Extra: `00-DESIGN-CORE.md`. The brief also asked for an A03 with seven diagrams; it delivers fifteen. The brief's process step to finish with the backlog (A18) is met by the A18 file.

## Which documents each milestone reads

Source: `PROMPT-Claude-Code.md` Part 2, "Session 0" and "Milestones". The columns "Design (per prompt)" and "WRD sections (per prompt)" come from the prompt's text for that milestone. The last column adds documents this index suggests that the prompt does not name; they are the index author's inference, not instructions.

Every session also reads `CLAUDE.md`, `docs/PROGRESS.md`, `docs/DECISIONS-poc.md` and `docs/CONFLICTS.md` (Part 4 session start block). Paths are those of the tables above (`design/…`, `docs/WRD-xx-…`).

| Milestone | Week | Design (per prompt) | WRD sections (per prompt) | Also suggested (inference, not in prompt) |
|---|---|---|---|---|
| Session 0 (inventory, reading, conflicts) | - | A01, A02, A03, A04, A05, A08, A10, A12, A13; B01, B03, B04 (skim the rest, read fully when their milestone comes) | WRD-16 in full; WRD-02, 03, 04, 05, 06, 07, 08, 09, 10, 11 (the sections referenced by WRD-16); WRD-00 Part III (decision log) and Appendix A (terminology) | `design/00-SCOPE-AND-CONFLICTS.md` (feeds CONFLICTS.md), `design/00-DESIGN-CORE.md`, `design/OPEN-QUESTIONS.md` |
| M1 Skeleton, platform layer, store, providers, daemon, CLI, Copilot spike | 1 | A01, A02, A04, A05, A11, A17 (A12 for the spike, named in the spike bullet) | WRD-16 §16 week 1; WRD-05 §3–§4 (model types, stream events, errors); WRD-16 §11 (DDL fallback if A04 is missing); WRD-16 §6.2 (Copilot spike) | WRD-09 §2–§4 (envelope, hash chain, checkpoints); WRD-02 §4 (API); A15 (secrets, redaction); A18 (week-1 spike, VPS model); WRD-10 §6 (deny-list) |
| M2 Sandbox, executor, proxy | 2 | A06, A07 | WRD-16 §9–§10 (incl. §10.2 Seatbelt, §10.3 bubblewrap, §10.7 escape-check rows; the prompt's "§10.4" for OCI is actually the egress proxy, note 9); WRD-10 §5–§7 (sandbox, deny-list, egress proxy) | WRD-04 §5 (executor protocol); A16 (escape-check script specification) |
| M3 Policy engine, agent loop, first end-to-end run | 3 | A08, A10 | WRD-16 §7 (agents), §10.6 (user policy), §9 (command profiles); WRD-08 (all, incl. §4 combination rules); WRD-03 (manifest; §5 validation) | A15 (redaction in context assembly); A03 (b) tool-call sequence; WRD-04 §3–§4 (risk classes, capabilities) |
| M4 Workflow runner, verifier, repair, router, artifacts, fixtures | 4 | A09, A13, A14 | WRD-16 §6.3 (routing rules), §8 (workflow), §11 (store, artifacts); WRD-07 §3–§8; WRD-06 (routing policy); WRD-09 §5 (artifact record) | WRD-16 §4 (fixtures, tasks T1 to T6); A03 (c), (e), (f) (routing, cancellation, resume); A04 (artifacts, provenance) |
| M5 Harnesses and the company-hosted endpoint | 5 | A12 | WRD-16 §6 (model access); WRD-05 §9 (external harness adapters) | A11 (`gateway` auth mode, `openai-compatible`); A18 (VPS model deployment); A03 (d) Copilot harness sequence |
| M6 Desktop app | 6 | B01 to B08 (B03 and B04 named for screens 1 to 5) | WRD-16 §13 (UI/UX for the PoC: seven screens, six states); WRD-11 | A05 (client contract and parity tables); A17 (Tauri sidecar bundling); B09 (H6 metrics recorded locally) |
| M7 macOS, settings and doctor screens, security scenarios, documentation | 7 | B03 screens 6 to 7 (settings, doctor and audit) | WRD-16 §10.2 (Seatbelt profile); §4.3 (security scenarios S1 to S4); §3 (demo script) | A06 (Seatbelt template); A16 (failure modes, escape checks); A07; B07 (vendor-terms notices) |
| M8 Hardening and PoC report | 8 | none named | WRD-16 §15 items 9 to 12 (acceptance), §2.3 (simplifications), §18 (hand-off to the MVP); hypotheses H1 to H6 | B09 (H6 test script and metrics); A18 (cut lines); `design/OPEN-QUESTIONS.md` |

Notes on the table:

- M6 cites "design B01–B08"; B09 is not listed, but its metrics are needed for the H6 recording requirement in the same bullet. M7 cites "design B03 screens 6–7".
- M1, M2, M4, M6 and M7 include Windows work (named pipes, Docker Desktop L2, installer) that no design document covers (note 8).
- The prompt's milestone acceptance criteria "restate WRD-16 §15–§16"; WRD-16 is read in full in Session 0, so the WRD-16 references above are the sections each milestone cites by number.
