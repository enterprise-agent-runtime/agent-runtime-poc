---
title: MVP Product Requirements Document
subtitle: Scope, personas, primary user journey, functional and non-functional requirements, acceptance criteria, non-goals
docid: WRD-01
version: 0.5
status: Working specification
date: September 25, 2026
owner: Product
audience: Founders, product, engineering, design
---

# 1. Objective

Prove that a developer can request a bounded change in a local repository and receive a **verified, reviewable result** through a **secure, model-neutral runtime**, with a complete audit trail, on their own machine, using the model access they already have (API key, cloud identity, local model, or an officially supported vendor subscription).

The MVP is the vertical slice that exercises every layer of the architecture once: runtime API, agent loop, policy engine, sandbox, egress proxy, secrets broker, canonical model API with three adapters, router with trust tiers, workflow template with worktrees, verification, events and artifacts, evaluation suite.

# 2. Personas

| Persona | Goal | What they need from the MVP |
|---|---|---|
| Individual developer (primary) | Delegate bounded coding work without giving an agent free rein over the machine | Fast setup, clear plan approval, visible verification, a diff they trust, cost visibility |
| Platform engineer (secondary) | Standardize how agents run and which models they use | Policy files, provider configuration, local model support, CLI for scripting |
| Security engineer (secondary) | Understand and constrain what agents can do | Sandbox guarantees, approval gates, audit export, deny-lists that cannot be overridden by repositories |
| Engineering manager (observer) | Judge value | Time to verified change, cost per task, intervention rate |

# 3. Primary user journey

1. The developer opens a repository (workspace) in the desktop app or runs `warden` in the repository directory.
2. First run: the setup wizard configures at least one model provider (API key stored in the OS keychain, cloud identity, or a local server such as Ollama), sets the workspace classification (default `confidential`), and selects the sandbox level (L1 native on macOS/Linux, L2 container on Windows).
3. The developer types a request such as "Add OAuth login with Google and GitHub, with tests."
4. The runtime classifies the request, explores the repository (read-only), and produces a **plan artifact** listing tasks, files expected to change, risk, and estimated cost. The chosen model and the reason for the choice are displayed.
5. Approval gate G1: the developer approves, edits, or cancels the plan.
6. Implementation tasks run in isolated worktrees inside sandboxes. Approval prompts appear inline for actions outside the granted capabilities (for example, a command outside the allowlist or network egress to a new domain), each with a plain-language reason and a scope selector (once, task, session, workspace).
7. The integrator merges worktrees into a session branch; build, tests, static analysis and a read-only security review run; failures create repair tasks; affected verification re-runs.
8. Approval gate G2: the developer reviews the diff, the verification reports, cost and the audit trace, and chooses to apply to a branch, commit, push (approval required), or iterate.
9. The session, its artifacts and its events remain available for inspection and export.

# 4. Functional requirements

Requirement keywords follow RFC 2119 (MUST, SHOULD, MAY). Each requirement has an identifier used by the evaluation suite and the test plan.

## 4.1 Workspace and session

| ID | Requirement |
|---|---|
| F-WS-1 | The runtime MUST treat a git repository directory as a workspace and MUST refuse to operate on directories that are not inside an explicitly opened workspace. |
| F-WS-2 | A workspace MUST have a data classification (`public`, `internal`, `confidential`, `restricted`), defaulting to `confidential`. |
| F-WS-3 | Sessions MUST persist across restarts, including the transcript summary, plan, task states, artifacts and events. |
| F-WS-4 | The user MUST be able to cancel any running task within 5 seconds; child processes MUST be terminated. |

## 4.2 Models and providers

| ID | Requirement |
|---|---|
| F-MD-1 | The MVP MUST ship three protocol adapters: Anthropic Messages, OpenAI (Chat Completions and Responses), and OpenAI-compatible (local servers and gateways). |
| F-MD-2 | Auth modes `api_key`, `none` (local) and `gateway` MUST work in the MVP; `cloud_iam` MAY be delivered in Phase 2. |
| F-MD-3 | Model selection MUST be made by the router according to the workspace classification and provider tiers; the user MAY pin a model per session within policy. |
| F-MD-4 | Every model call MUST be made by the runtime process; sandboxes MUST NOT have provider credentials or direct network access. |
| F-MD-5 | The UI MUST show which model and provider handled each task and why. |
| F-MD-6 | The external harness adapter for the GitHub Copilot SDK SHOULD ship in the MVP as an optional backend for implementation tasks; other harnesses are Phase 2. |

## 4.3 Agents and workflow

| ID | Requirement |
|---|---|
| F-AG-1 | Built-in agents: `planner`, `coder`, `verifier`, `security-reviewer` (read-only), `integrator`. Each MUST have a manifest with explicit capabilities and limits. |
| F-AG-2 | The coding workflow MUST follow the fixed template: explore → plan → gate G1 → implement (up to 3 parallel worktrees) → integrate → verify → review → gate G2. |
| F-AG-3 | The planner MAY create between 1 and 3 implementation tasks; each MUST list target paths. |
| F-AG-4 | Sub-agents MUST exchange information only through artifacts. |
| F-AG-5 | Verification failures MUST create a repair task with a failure-analysis artifact; a task MUST NOT be retried identically after a verification failure. |

## 4.4 Tools and sandbox

| ID | Requirement |
|---|---|
| F-TL-1 | MVP tools: `fs.read`, `fs.list`, `fs.search`, `fs.write`, `fs.patch`, `proc.exec`, `git.status`, `git.diff`, `git.log`, `git.commit`, `test.run`. |
| F-TL-2 | Every tool invocation MUST pass the Policy Decision Point and MUST produce a decision event before execution. |
| F-TL-3 | Tool execution for agent-requested operations MUST occur inside the task sandbox. |
| F-TL-4 | The sandbox MUST expose only the task worktree (read-write), a read-only toolchain, and the egress proxy socket. |
| F-TL-5 | Files matching the platform secret deny-list MUST be unreadable and unwritable by agents regardless of manifest grants. |
| F-TL-6 | Network egress from the sandbox MUST be impossible except through the proxy with a per-task allowlist. |
| F-TL-7 | Git hooks MUST NOT execute inside sandboxes; global and system git configuration MUST be ignored. |

## 4.5 Policy and approvals

| ID | Requirement |
|---|---|
| F-PL-1 | Policy MUST be layered: platform defaults, user policy, workspace policy (restrict-only), session grants. |
| F-PL-2 | The actions listed in WRD-00 D-16 MUST require approval by default. |
| F-PL-3 | Approvals MUST carry a scope (`once`, `task`, `session`, `workspace`) and expiry, and MUST be recorded as events. |
| F-PL-4 | Every decision MUST include a human-readable reason and the rules that matched. |
| F-PL-5 | Non-interactive mode MUST treat `approval_required` as `deny` unless pre-approved in policy. |

## 4.6 Artifacts, events, cost

| ID | Requirement |
|---|---|
| F-AU-1 | Events MUST be append-only, hash-chained and stored in SQLite under the user profile. |
| F-AU-2 | Artifacts MUST be content-addressed and carry provenance per WRD-09. |
| F-AU-3 | The session view MUST show tokens, quota units where applicable, estimated cost, duration and tool-call counts per task. |
| F-AU-4 | `warden audit export --session <id>` MUST export events and artifact metadata as JSON Lines. |

## 4.7 Evaluation

| ID | Requirement |
|---|---|
| F-EV-1 | `warden eval run` MUST execute the first suite (WRD-12) in fresh sandboxes and produce a report. |
| F-EV-2 | The release of any built-in agent MUST be gated by the suite thresholds. |

# 5. Non-functional requirements

| ID | Requirement |
|---|---|
| N-1 | Platforms: macOS 14+ (Apple silicon and Intel), Ubuntu 22.04+/Fedora 39+ (x86_64, arm64), Windows 11 with Docker Desktop, Podman Desktop or WSL2. |
| N-2 | Cold start of the daemon under 2 s; first token of a model response streamed to the UI within 300 ms of the provider's first byte. |
| N-3 | Sandbox creation under 1.5 s (L1) and under 6 s (L2, warm image). |
| N-4 | No secret value ever appears in events, artifacts, logs or model context (verified by tests). |
| N-5 | All persistent state under a single directory (`~/.warden`), removable in one operation. |
| N-6 | Offline: local models and read-only tools continue to work without internet. |
| N-7 | Accessibility: keyboard-navigable approval prompts; screen-reader labels on all controls. |

# 6. Acceptance criteria (definition of done for the MVP)

1. On each supported OS, a new user configures one provider and completes the primary journey on the TypeScript fixture repository in under 20 minutes, with no manual edits to the diff required to pass the fixture's tests.
2. The first evaluation suite (WRD-12) passes its thresholds: coding success rate ≥ 70% with a frontier model and ≥ 40% with a 30B-class local model; 100% of security cases pass.
3. Sandbox escape tests (WRD-10 §8) all pass: no read of deny-listed files, no write outside the worktree, no network egress bypassing the proxy, no hook execution, no privilege escalation.
4. Every tool call in a full session has a preceding decision event; the audit export validates its hash chain.
5. The same request completes with Anthropic, OpenAI and an OpenAI-compatible local server without changes to any manifest.
6. Cancelling a running implementation task terminates all child processes within 5 s and leaves the worktree recoverable.
7. Cost and token figures in the session view match the providers' usage reports within 2% for API-key providers.

# 7. Non-goals for the MVP

- Enterprise control plane, SSO, RBAC, central audit.
- Agent marketplace or third-party agents.
- Dynamic DAG planning beyond the fixed template (Phase 2).
- MCP client (Phase 2), browser tool, cloud, Kubernetes, ticketing or messaging tools.
- Remote workers, CI integration beyond the headless CLI.
- Durable long-term memory or enterprise knowledge retrieval.
- Custom model training; production deployment automation.
- Token-replay or proxy-spoofing access to consumer subscriptions (never).

# 8. Risks specific to the MVP

| Risk | Mitigation |
|---|---|
| Local models fail at tool calling | Emulated tool-calling protocol and evaluation thresholds per model class |
| Approval fatigue | Scoped approvals, workspace profiles, clear reasons; measure prompts per task |
| Sandbox friction with real toolchains (Node, Go, Python, Java) | Toolchain profiles mounting the host toolchain read-only; L2 images per language |
| Windows experience lags | Ship Windows with L2 only and document the prerequisites |
| Vendor policy changes for harnesses | Harnesses optional; API-key and local paths primary |

# 9. Success metrics for the MVP

- Median time from request to verified diff on the fixture tasks.
- Cost per successful task by provider tier.
- Percentage of tasks completed without any approval prompt beyond G1 and G2.
- Number of policy denials per session (a proxy for both safety and friction).
- Percentage of executions with complete provenance (target 100%).

# 10. First ten customer discovery questions (D-24, proposed)

1. Which AI models and providers does your organization use today, and through which access modes (API keys, cloud platform, internal gateway, self-hosted, vendor subscriptions)?
2. Who decides which model a developer may use for which code, and how is that decision enforced today?
3. What do your security or platform teams currently prohibit developers from doing with AI coding tools, and why?
4. Have you experienced or do you fear data leaving the organization through AI tools? What data classifications matter?
5. How do you currently isolate what an AI agent can touch on a developer machine or in CI?
6. Which workflows beyond coding (security review, QA, DevOps, data) would you want agents to run, and what would need to be true to trust them?
7. What evidence would a reviewer need to accept an agent-produced change (tests, scans, diff limits, audit trail)?
8. Do you run internal or self-hosted models? What would make you route more work to them?
9. How do you distribute internal developer tooling today (registries, package managers, MDM), and how would you want to distribute approved agents?
10. Who would own this product internally, what budget line would it come from, and how would you measure success in the first quarter?
