# Warden PoC design set

System, backend and UI/UX design for the Warden Proof of Concept, produced from `PROMPT-Claude-Design.md` and the specification set WRD-00 to WRD-16 (26 September 2026). Precedence applied throughout: WRD-16 > the companion document for the subsystem (WRD-02 to WRD-11) > WRD-00.

About 25,000 lines in 31 Markdown files, 67 Mermaid diagrams (all parse with Mermaid 11), JSON Schema draft 2020-12 contracts (A04, A05, A06, A08 compile and their examples validate), and Go interface sketches.

## Start here

| File | What it is |
|---|---|
| [00-SCOPE-AND-CONFLICTS.md](00-SCOPE-AND-CONFLICTS.md) | Scope summary (≤ 200 words) and the **Conflicts** section: 44 conflicts between the source documents (CF-01 to CF-44), each with the rule that decides it and the resolution used. Four are internal defects in WRD-16 (CF-16 Seatbelt rule order, CF-17 deny-list vs worktree path, CF-18 git without the main `.git`, CF-33 figures). |
| [00-DESIGN-CORE.md](00-DESIGN-CORE.md) | The registry every deliverable uses: identifiers, enums, event types and payload fields, API methods, error codes, rule ids, packages, UI names, token names, the binding behavioural decisions (§13) and the post-draft integration decisions ID-01 to ID-18 (§15). |
| [OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) | 36 open questions, each with a recommended answer already assumed by the design. |

## Part A: system and backend design

| # | File | Contents | Lines |
|---|---|---|---|
| A01 | [A01-context-and-containers.md](A01-context-and-containers.md) | C4 levels 1 and 2, container catalog, trust boundaries, where each brief invariant (BI-1 to BI-7) is enforced | 279 |
| A02 | [A02-wardend-components.md](A02-wardend-components.md) | Go packages, interfaces, events per package, import matrix, `internal/archtest` and depguard rules | 1,127 |
| A03 | [A03-sequence-diagrams.md](A03-sequence-diagrams.md) | **Depth.** 15 sequence diagrams with every WRD-09 event in order: full T1 flow (E1–E168), approval flow, confidential routing and fallback, Copilot harness, cancellation, restart, audit verify | 1,343 |
| A04 | [A04-data-model.md](A04-data-model.md) | SQLite DDL (17 tables, append-only triggers), envelope and 45 payload schemas, artifacts and provenance, hash chains, signed checkpoints, retention | 2,037 |
| A05 | [A05-runtime-api.md](A05-runtime-api.md) | **Depth.** Transport, token handshake, versioning, all methods with params/result schemas, errors, events, examples; desktop and CLI parity tables (BI-6) | 2,214 |
| A06 | [A06-executor-and-sandbox.md](A06-executor-and-sandbox.md) | **Depth.** `warden-exec` protocol, path canonicalization, full Seatbelt template, full bwrap argv, seccomp list, limits, env allowlist, forwarder, L2 mapping, escape-check mechanism table | 1,445 |
| A07 | [A07-egress-proxy.md](A07-egress-proxy.md) | Per-task listener, CONNECT/HTTP, allowlist formula, DNS and SSRF protection, approval-held connections | 439 |
| A08 | [A08-policy-engine.md](A08-policy-engine.md) | **Depth.** ActionRequest normalization, CEL environment, platform defaults and user policy YAML, evaluation algorithm, INV-1–INV-9 checks, approvals and grants, cache, explain, 67 golden cases | 1,993 |
| A09 | [A09-model-router.md](A09-model-router.md) | **Depth.** Admission, filters, ranking keys, circuit breaker, fallback state machine, pin, `routing.decision` examples, "why / why not" strings | 766 |
| A10 | [A10-agent-loop.md](A10-agent-loop.md) | **Depth.** Context assembly and budgets, untrusted-data wrapper, tool rendering and name mapping, emulated tool calling, schema repair, compaction, streaming, checkpoint artifact, manifests | 1,455 |
| A11 | [A11-provider-adapters.md](A11-provider-adapters.md) | Canonical ↔ Anthropic and ↔ OpenAI-compatible mappings, auth-mode matrix, stream parsing, error normalization, capability probe, deployment notes | 912 |
| A12 | [A12-harness-adapters.md](A12-harness-adapters.md) | **Depth.** Copilot SDK split mode, Codex app-server, Claude Code personal mode and shared-build lock, credential exception HX-1, quota, spike checklist | 862 |
| A13 | [A13-workflow-runner.md](A13-workflow-runner.md) | Templates, task state machine and transition table, gates, repair, retries, resume, cancellation, delivery, verifier parsers | 577 |
| A14 | [A14-worktree-and-git.md](A14-worktree-and-git.md) | Private session git dir with alternates, hook neutralization, checkpoints, host-side delivery, cleanup | 478 |
| A15 | [A15-secrets-and-redaction.md](A15-secrets-and-redaction.md) | Keychain, `secret://`, injection points, three-layer deny-list, redaction regexes, canary tests | 433 |
| A16 | [A16-failure-modes-and-security-review.md](A16-failure-modes-and-security-review.md) | 45 failure modes, T-01 to T-24 coverage, accepted risks, escape-check script specification | 205 |
| A17 | [A17-repo-build-ci.md](A17-repo-build-ci.md) | Module layout, builds, Tauri sidecars, GitHub Actions pipeline, release and versioning | 437 |
| A18 | [A18-implementation-backlog.md](A18-implementation-backlog.md) | Epics and stories for the 8 weeks, capacity check and cut lines, Copilot SDK spike, VPS model deployment | 315 |

## Part B: UI/UX design

| # | File | Contents | Lines |
|---|---|---|---|
| B01 | [B01-information-architecture.md](B01-information-architecture.md) | Screen and state maps, routes, timeline versus context panel | 387 |
| B02 | [B02-user-flows.md](B02-user-flows.md) | First run (four setup paths), T1 journey, confidential switch, cancel and resume, S1, audit | 454 |
| B03 | [B03-wireframes.md](B03-wireframes.md) | **Depth.** Low-fi for 7 screens and 6 states; hi-fi for Session view, Plan review (G1), Approval prompt, Result review (G2) at 1024–1920 px | 1,315 |
| B04 | [B04-component-inventory.md](B04-component-inventory.md) | **Depth.** 27 components with props, states, data sources (API method or event field for every decision, routing choice and cost), hunk provenance | 2,129 |
| B05 | [B05-interaction-specification.md](B05-interaction-specification.md) | **Depth.** Keyboard model, focus, streaming, optimistic vs confirmed, approvals, gates, notifications, cancel, WCAG 2.2 AA | 885 |
| B06 | [B06-visual-design.md](B06-visual-design.md) | Tokens (light/dark, contrast-checked), type, spacing, icons, motion | 597 |
| B07 | [B07-content-and-microcopy.md](B07-content-and-microcopy.md) | ICU message catalog: approvals, routing reasons, policy reasons, errors, empty states, vendor terms, confirmations | 1,030 |
| B08 | [B08-frontend-architecture.md](B08-frontend-architecture.md) | React app, typed JSON-RPC client, event store, Tauri Rust bridge, replay, virtualization, diff viewer, i18n, tests | 565 |
| B09 | [B09-ux-acceptance.md](B09-ux-acceptance.md) | H6 analysis, usability test script, local metrics, report template | 342 |

## How the set was checked

- Every file ends with `## Traceability` and `## Deviations and assumptions`.
- All 67 Mermaid diagrams parse; JSON Schemas compile; A05 examples validate; the A04 DDL and triggers run in SQLite; B06 contrast ratios computed for 89 pairs in both themes.
- Every event type and API method cited in any file exists in the core registry or is marked `NEW`.
- Coverage: BI-1 to BI-7, INV-1 to INV-9, H1 to H6, S1 to S4 and T-01 to T-24 are each traced in at least one deliverable.

## Suggested reading order for an implementer

00-SCOPE-AND-CONFLICTS → 00-DESIGN-CORE → A01 → A02 → A03 → A05 → A04 → A08 → A09 → A10 → A06 → A07 → A13 → A14 → A15 → A11 → A12 → A16 → A17 → A18, then B01 → B03 → B04 → B05 → B02 → B06 → B07 → B08 → B09.
