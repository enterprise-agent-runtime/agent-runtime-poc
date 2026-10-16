---
title: MVP User Experience Specification
subtitle: Screens, states, approval experience, plan review, verification and diff review, settings, CLI parity, errors and cancellation
docid: WRD-11
version: 0.5
status: Working specification
date: September 25, 2026
owner: Product / Design
audience: Design, engineers
---

# 1. Experience principles

1. **Show the decision before the effect.** Nothing with an effect happens without the user having seen what will happen and why it is allowed.
2. **One request, one timeline.** Orchestration complexity is hidden behind a single timeline; details are one click away.
3. **Explain the machine.** Model choice, policy decisions and costs are explained in one sentence with a details view.
4. **Never trap the user.** Cancel is always available; nothing is applied to the repository without the final gate.
5. **Same power in the CLI.** Every desktop action has a CLI equivalent (scripting, CI, accessibility).

![Figure 1. The MVP journey.](img/ux_flow.png)

# 2. Screens

## 2.1 Workspace home

- Open repository (directory picker or recent list); shows classification badge, sandbox level, configured providers, and policy summary ("Agents can: read/write this repo, run test and build profiles; need approval for: installs, other commands, push").
- Sessions list with status, cost and last activity.

## 2.2 Session view (the main screen)

Layout: left, the timeline; right, a context panel that changes with the selected item (plan, task, approval, artifact, diff).

Timeline entries (each with status icon, elapsed time, cost badge):

| Entry | Content |
|---|---|
| Request | User text; classification and complexity chips |
| Explore | Progress ("Reading 142 files, 3 build commands detected"); repo-map artifact link |
| Model chosen | "local/qwen-coder-32b (prefer-internal; confidential data)" with details |
| Plan gate (G1) | Inline plan card: tasks, paths, risks, estimated cost; buttons Approve, Edit, Cancel |
| Task cards (parallel) | Live step counter, tool calls collapsed, approval prompts inline |
| Integration | Merge result; conflicts if any |
| Verification | Build, tests, lint, security review with pass/fail and counts |
| Repair (if any) | Failure analysis summary; new fix tasks |
| Final gate (G2) | Diff summary, reports, cost total, chain status; Apply, Commit, Push (approval), Iterate |

## 2.3 Approval prompt (inline card and OS notification)

Content order: **what** (exact command, path, host or model), **who** (agent and task), **why it needs approval** (rule reason, risk class, taint sources if any), **scope selector** (limited by policy), **Approve / Reject**, and "Explain" (opens the policy explanation). Keyboard: `A` approve, `R` reject, `1–4` scope. Prompts never auto-dismiss; a task waiting for approval shows a pulsing state; a session-level badge counts pending approvals.

## 2.4 Diff review

Side-by-side or unified; per-file accept/revert (reverting creates a note for the iterate loop); file list grouped by task; link from each hunk to the task and step that produced it (provenance); test results per file where mappable.

## 2.5 Artifacts and audit

Artifact browser by type; event stream with filters (policy, tool, model, approval); `Verify chain` button; export.

## 2.6 Settings

Providers (add API key stored in keychain, cloud identity sign-in, local server discovery, gateway, harness enablement with vendor-terms notice), models and pinning, policy viewer with `explain` tester, sandbox status (`doctor`), budgets, retention, telemetry (off by default).

# 3. States and empty states

| State | UX |
|---|---|
| No provider configured | Guided setup with three paths: API key, local model (detects Ollama), cloud/gateway |
| Sandbox prerequisites missing | Blocking banner with OS-specific instructions; no unsandboxed fallback |
| Provider unavailable / no admissible model | Task card in `waiting for input` with the reason and actions (configure provider, change strategy if allowed) |
| Budget exhausted | Card with spend, limit, and "raise limit for this session" (policy permitting) |
| Cancelled | Greyed entries; "Resume from last gate" action |
| Chain verification failed | Red banner in the session; export still possible with a warning |

# 4. CLI parity

| Desktop action | CLI |
|---|---|
| Open workspace | `warden open .` |
| New request | `warden run "Add OAuth login with Google and GitHub"` |
| Approve plan | `warden approve <gate-id>` or interactive prompt in the terminal |
| Resolve approval | `warden approvals` then `warden approve <id> --scope task` |
| Cancel | `warden cancel [<task-id>]` |
| Diff | `warden diff <session>` |
| Reports | `warden report <session> --json` |
| Audit | `warden audit export/verify` |
| Policy explanation | `warden policy explain --tool proc --argv "npm test"` |
| Providers | `warden provider add/list/test` |
| Non-interactive | `warden run --non-interactive --policy ci.yaml "..."` (exit codes: 0 verified, 2 needs approval, 3 failed) |

# 5. Errors and messages

Every error shows: what happened, what the runtime did (paused, retried, fell back), and what the user can do. Provider errors are mapped to plain language ("Rate limited by OpenAI; retrying in 20 s (2/3)"). Policy denials shown to the user include the rule id for support.

# 6. Accessibility and localization

Keyboard navigation for the timeline and prompts; ARIA roles; reduced-motion mode; English first, strings externalized; Romanian and German next.

# 7. Instrumentation for UX research

Prompts per task, time to first approval, plan edit rate, diff revert rate, cancel rate, time to verified change. Collected locally; shared only when the user opts in.
