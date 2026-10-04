# A18 Implementation backlog

The 8-week plan of WRD-16 §16 broken into epics and stories with testable acceptance criteria, dependencies, ideal-day estimates, and the deliverable (A01–A18 / B01–B09) that specifies each story. Then: a one-person capacity check with honest overcommit flagging and cut lines; the week-1 Copilot SDK spike; the week-1 VPS model deployment; the milestone-to-acceptance-checklist mapping (WRD-16 §15 items 1–12); and a risk burndown. Names from `00-DESIGN-CORE.md`; conflicts cite `CF-xx`.

Estimates are **ideal days** (focused engineering days, no meetings/context-switch overhead). One person, 8 weeks ≈ 40 working days; applying a 0.7 focus factor gives ≈ **28 ideal days of real capacity**. The plan below totals more than that; §3 handles the overcommit.

## 1. Epics

| Epic | Title | Weeks (WRD-16 §16) | Specified mainly by |
|---|---|---|---|
| E1 | Skeleton: module, daemon, store, CLI, model adapters, VPS T1 | 1 | A01, A02, A04, A05, A11, A17 |
| E2 | Sandbox, executor, proxy, secrets, escape check | 2 | A06, A07, A15, A16 |
| E3 | Agent loop, coder agent, policy engine, first end-to-end run | 3 | A08, A10, A03 |
| E4 | Workflow runner, verifier, repair, router | 4 | A09, A13 |
| E5 | Harnesses (Copilot; Codex/Claude Code optional), classification demo, smoke eval | 5 | A12 |
| E6 | Desktop UI screens 1–5, live events, approvals/gates | 6 | B01–B08 |
| E7 | Second OS, settings + doctor screens, S1–S3 polish, docs | 7 | A16, B06, B07 |
| E8 | Buffer, H6 test, demo rehearsal, PoC report | 8 | B09, A18 |

## 2. Stories

Acceptance criteria are testable (a command, a passing test, a measurable outcome). Dependencies use story ids. "Spec" is the authoritative deliverable.

### E1; Skeleton (week 1)

| ID | Story | Acceptance criteria | Deps | Est (d) | Spec |
|---|---|---|---|---|---|
| E1-S1 | Go module + repo layout + Makefile + CI skeleton | `make build` produces `wardend`,`warden`,`warden-exec` (CGO off); `golangci-lint run` green; `internal/archtest` passes | n/a | 1.0 | A17 |
| E1-S2 | SQLite store + event envelope + hash chain | append event → row written; `prev_hash`/`hash` per core §5; `audit verify` on a synthetic log passes; unit test tampering detected | E1-S1 | 2.0 | A04 |
| E1-S3 | JSON-RPC server + `system.hello` token handshake | connect without hello → `-32001`; valid token → `system.version`; owner-only socket 0600 | E1-S1 | 1.0 | A05 |
| E1-S4 | Canonical model types (`ModelRequest`, `StreamEvent`, errors) | types compile; round-trip unit test; error codes match WRD-05 §4 | E1-S1 | 0.5 | A11 |
| E1-S5 | `anthropic-messages` adapter + streaming + usage | `provider.test anthropic` ok; a tool proposal parsed from a real call; usage recorded | E1-S4 | 1.5 | A11 |
| E1-S6 | `openai-compatible` adapter, 5 auth variants + capability probe | `provider.test ollama` and the VPS endpoint ok; probe detects native vs emulated tool calling; Azure `api-key` header variant unit-tested | E1-S4 | 2.0 | A11 |
| E1-S7 | `models.yaml` load/validate + keychain secret store | catalog parses; `secret://` resolves from keychain; `warden provider add anthropic --api-key` stores and never echoes | E1-S6, E1-S2 | 1.0 | A15, A11 |
| E1-S8 | CLI `doctor`, `provider add/test`, `models`, `version` | commands run against the daemon; `doctor` reports backend/keychain/providers/disk | E1-S3, E1-S7 | 1.0 | A05 |
| E1-S9 | Neutrality unit test: one `ModelRequest` → tool proposal on Anthropic, Ollama, VPS | test proves identical request yields a valid tool proposal from all three | E1-S5, E1-S6 | 0.5 | A11 |
| E1-K1 | VPS model deployment (T1 endpoint) | `warden provider test company-vllm` passes a tool-call probe (see §5) | E1-S6 | 1.0 | A18 §5 |
| E1-K2 | Copilot SDK spike | exit criteria in §4 answered; go/no-go on split mode recorded | E1-S1 | 1.0 | A18 §4 |

E1 subtotal: **12.5 d**.

### E2; Sandbox and executor (week 2)

| ID | Story | Acceptance criteria | Deps | Est (d) | Spec |
|---|---|---|---|---|---|
| E2-S1 | `warden-exec` + `execproto` + socketpair JSON-RPC | `exec.hello` over fd 3; `exec.fs.*`, `exec.proc.spawn/io/wait/signal`, `exec.git.run`; re-checks roots independently | E1-S1 | 2.0 | A06 |
| E2-S2 | L1 sandbox on primary OS (Seatbelt or bwrap) | sandbox created per task; profile generated per CF-16/CF-17; worktree rw, home absent, env cleared | E2-S1 | 2.5 | A06 |
| E2-S3 | Egress proxy + in-sandbox forwarder | per-task listener; CONNECT + plain HTTP; allowlist eval; `proxy.connect`/`proxy.denied` events; DNS in daemon | E2-S2 | 2.0 | A07 |
| E2-S4 | Secrets broker + deny-list (3 layers) + redaction | `.env`/`.ssh` denied at PDP/executor/mount; redaction regex set replaces + counts; `redaction` events | E2-S1, E1-S7 | 1.5 | A15 |
| E2-S5 | `scripts/escape-check.sh` + `debug.runScript` (escapecheck tag) | escape check passes on primary OS; TAP output; canaries untouched | E2-S2, E2-S3, E2-S4 | 2.0 | A16 |

E2 subtotal: **10.0 d**.

### E3; Agent loop, policy, first run (week 3)

| ID | Story | Acceptance criteria | Deps | Est (d) | Spec |
|---|---|---|---|---|---|
| E3-S1 | Policy engine: CEL compile, ActionRequest, effects, obligations | rules from `platform-defaults.yaml`+`user.yaml` compile; allow/deny/approval; obligations merged | E1-S2 | 2.5 | A08 |
| E3-S2 | Invariants INV-1..INV-9 as L0 checks + golden corpus | `warden policy test policy/golden` 100%; S1 shell-string denied (CF-19); protected-branch denied | E3-S1 | 1.5 | A08 |
| E3-S3 | Approvals lifecycle (request/resolve/expiry/revoke/scope) + two-decision pattern (CF-40) | `approval_required`→`approval.resolved(approve)`→second `policy.decision(allow, resolved_by_approval)`; scope ≤ scope_max | E3-S1 | 1.5 | A08 |
| E3-S4 | Agent loop: context assembly, tool rendering, proposal handling, schema validation + one repair turn | tool defs from capabilities with `.`→`__` mapping; denied-call feedback; output schema validated | E1-S5, E3-S1 | 2.5 | A10 |
| E3-S5 | Emulated tool protocol for local models | 7B model without native tool calling produces valid proposals via injected protocol | E3-S4, E1-S6 | 1.0 | A10 |
| E3-S6 | `coder` manifest + prompts + input/output schemas | manifest validates; plan/implement/repair/summarize modes; write cap gated by `when` (CF-28, CF-43) | E3-S4 | 1.0 | A10, A13 |
| E3-S7 | Events for everything + terminal approvals in CLI | full event stream for a run; `warden approve/reject` in terminal | E3-S3 | 1.0 | A05, A08 |
| E3-S8 | First end-to-end `warden run` of T1 on Anthropic (single task) | T1 produces a diff on the session branch with a full audit trail; `audit verify` passes | E3-S4..S7, E2-S2 | 1.5 | A03 |
| E3-S9 | Model questions and answers (ID-05) + rejected approval returns to running (ID-10) | `approval.request` emits `policy.decision(allow)` → `tool.exec.start(executor: host)` → `approval.requested(kind: question)`, task `waiting_for_input`; `approval.resolve {approval_id, decision, answer}` (answer ≤ 4,000 chars, redacted) → `approval.resolved` → `tool.exec.end` with the answer as untrusted user input; `warden answer <id> "<text>"` works in the CLI (A05); rejecting an inline approval returns the task to `running` with tool result `approval_rejected`, and three identical rejections end the step (unit + CLI test) | E3-S3, E3-S4 | 1.0 | A08, A10, A13 §3.1 |

E3 subtotal: **13.5 d**.

### E4; Workflow, verifier, router (week 4)

| ID | Story | Acceptance criteria | Deps | Est (d) | Spec |
|---|---|---|---|---|---|
| E4-S1 | Workflow runner: template load, instantiation, state machine, `task.state` events | `poc-coding` instantiates; transitions per A13 table; resumable projection | E3-S8 | 2.5 | A13 |
| E4-S2 | Gates G1/G2 + `resolveGate` approve/reject/edit (ID-01) | G1 edit stores a new plan version (`artifact.edited`); reject at G1 or G2 → run `cancelled(rejected)`; G2 approve emits, in order, `artifact.created(final-result)`, `chain.checkpoint(trigger: workflow_end)`, `workflow.end(succeeded)` | E4-S1 | 1.0 | A13 §4 |
| E4-S3 | Worktree + git handling (session branch, alternates, checkpoints, publish/squash/format-patch/push mechanics) | session worktree on `warden/<ulid>`; checkpoints at open, G1, end of implement/repair, cancel; host-side git functions for publish, squash-commit, format-patch and push with hooks disabled | E3-S8 | 2.0 | A14 |
| E4-S4 | `verifier` agent + result parsers (Vitest, go test -json, pytest/JUnit) + stack detection | parsers map to `test-report` (CF-29/30/31); `verify` succeeds only if build 0 and failed 0; a green run emits no `routing.decision`/`model.call.*` and gets the deterministic `analysis` (ID-09) | E4-S1 | 2.0 | A13 §8 |
| E4-S5 | Repair round (repair-1 → verify-2, no G2 on second failure) | one repair round; supersedes chain (CF-26); CF-24 no G2 on verify-2 failure | E4-S4 | 1.0 | A13 |
| E4-S6 | Router: admission by tier, ranking, health/circuit breaker, fallback, pin | prefer-internal ranks T0/T1; confidential admits T0–T2; pin overrides ranking; fallback ≤ tier | E4-S1, E1-S6 | 2.0 | A09 |
| E4-S7 | Budgets + `routing.decision`/`routing.fallback` events + resume after restart | session/daily/execution budgets; restart re-queues interrupted; `audit verify --strict` passes | E4-S1, E4-S6 | 1.5 | A09, A13 |
| E4-S8 | Full T1 on Anthropic and Ollama through the workflow incl. one repair | both runs reach G2 with 43 passed after a repair; `--strict` passes | E4-S1..S7 | 1.0 | A13 |
| E4-S9 | `session.setPin` + automatic re-route + paused tier-bounded fallback (ID-04, ID-16, CF-44) | with the local model failing on an `internal` workspace, fallback never leaves T0/T1: task goes `waiting_for_input(provider)` with a "Continue on anthropic/claude-sonnet (T3)" offer; `warden pin <model>` (`session.setPin`) re-routes the paused task and it resumes (`task.state(waiting_for_input → queued, input_provided)`, then `routing.decision {pin}`); `provider.configured(ok)`, a classification change and a circuit closing each trigger an automatic re-route; `session.setPin(null)` clears the pin | E4-S6 | 1.0 | A09, A05, A13 §3.1 rows 7b/8b |
| E4-S10 | Post-run delivery through the PDP + `workflow.delivered` (ID-02, ID-03) | `workflow.deliver` only on `succeeded` runs (plus `export_patch` on `failed(verification)`), else `-32003`; each action emits `policy.decision` (actor `user`; `platform.user-delivery` for `git.apply_branch`/`git.export_patch`, `user.git-commit-session-branch` for commit) → `tool.exec.start(executor: host)` → `tool.exec.end` → `workflow.delivered`; `push` before any `commit`/`apply_branch` → `-32003`; push asks approval scope `once`, rejection leaves the run `succeeded` with no `tool.exec.start` and nothing sent; `audit verify --strict` passes over a session with commit + rejected push | E4-S2, E4-S3, E3-S3 | 1.5 | A13 §10, A14, A08 |

E4 subtotal: **15.5 d**.

### E5; Harnesses, classification, smoke eval (week 5)

| ID | Story | Acceptance criteria | Deps | Est (d) | Spec |
|---|---|---|---|---|---|
| E5-S1 | Copilot SDK harness, split mode, hooks→PDP, vendor-only egress | T1 completes on Copilot; every harness tool call has a `policy.decision`; egress only to vendor hosts | E4-S8, E1-K2 | 3.0 | A12 |
| E5-S2 | Harness egress allowlists + quota accounting | premium-request count per task recorded; unlisted host denied | E5-S1 | 1.0 | A12 |
| E5-S3 | Classification demo: switch to `confidential`, greys T3/T4 | Anthropic + Copilot inadmissible with `tier_not_admitted`; company-hosted completes | E4-S6 | 1.0 | A09 |
| E5-S4 | `warden eval smoke` (T1, T2) | runs T1/T2 on fixtures with configured models; prints pass/fail + cost | E4-S8 | 1.0 | A13 |
| E5-S5 | Codex + Claude Code harnesses (colocated); OPTIONAL | if enabled, Codex/Claude Code complete T1 on internal; shared-mode lock refuses claude-code | E5-S1 | 2.0 | A12 |

E5 subtotal (required): **6.0 d**; +2.0 d optional.

### E6; Desktop UI (week 6)

| ID | Story | Acceptance criteria | Deps | Est (d) | Spec |
|---|---|---|---|---|---|
| E6-S1 | React app shell + Tauri bridge + event subscription/replay | UI connects via bridge/WS with token; replays from `after_seq`; reconnects | E1-S3 | 2.0 | B08 |
| E6-S2 | Session view (SCR-2): timeline, task cards, routing line, cost panel | live step counter; collapsed tool calls; routing line from `routing.decision` | E6-S1, E4-S7 | 2.5 | B03, B04 |
| E6-S3 | Plan review G1 (SCR-3) + approval card (SCR-4) + keyboard model | Approve/Edit/Reject; `A`/`R`/`1–4`; non-dismissable prompt; pending badge | E6-S2, E4-S2 | 2.0 | B03, B05 |
| E6-S4 | Result review G2 (SCR-5): diff viewer, test report, delivery bar | diff with hunk→task provenance; Accept/Discard result; a delivery button pressed while G2 is open calls `resolveGate(approve)` then `workflow.deliver`; Push shows the `once` approval; Iterate | E6-S2, E4-S10 | 2.5 | B03, B04 |
| E6-S5 | Workspace home (SCR-1) + classification change + capability summary | recent workspaces; classification dropdown (confirm on loosen); capability sentence | E6-S1 | 1.0 | B03 |
| E6-S6 | T1 through the UI end to end | a developer completes T1 in the UI incl. G1, one approval, repair, G2 | E6-S2..S5 | 1.0 | B09 |

E6 subtotal: **11.0 d**.

### E7; Second OS, settings/doctor, security polish, docs (week 7)

| ID | Story | Acceptance criteria | Deps | Est (d) | Spec |
|---|---|---|---|---|---|
| E7-S1 | Second OS: sandbox + escape check on the other OS | escape check passes on both macOS and Linux (matrix green) | E2-S5 | 2.5 | A06, A16 |
| E7-S2 | Settings screen (SCR-6): providers, add key, detect local, enable harness | add/test/remove provider; enable Copilot with vendor-terms notice | E6-S1, E1-S8 | 1.5 | B03, B04 |
| E7-S3 | Doctor + audit screen (SCR-7): checks, verify chain, export | doctor checklist with fix hints; Verify chain button; export | E7-S2, E1-S2 | 1.0 | B03 |
| E7-S4 | Security scenarios S1–S3 polished with events visible in UI | S1 deny, S2 three-layer deny, S3 postinstall sandboxed + collector denied; all visible | E5-S1, E6-S2 | 1.5 | A16 |
| E7-S5 | States ST-1..ST-6 + microcopy + visual tokens | six states render; error messages per B07; light/dark tokens | E6-S2 | 1.5 | B06, B07 |
| E7-S6 | Docs: README, demo script, setup guide | `scripts/demo.sh` runs; setup guide covers 4 provider paths | E7-S4 | 1.0 | A18 |

E7 subtotal: **9.0 d**.

### E8; Buffer, H6, rehearsal, report (week 8)

| ID | Story | Acceptance criteria | Deps | Est (d) | Spec |
|---|---|---|---|---|---|
| E8-S1 | UX metrics recording + `metrics.get` | prompts/task, time-to-first-approval, plan-edit-rate, cancel-rate recorded | E6-S2 | 1.0 | B09 |
| E8-S2 | H6 test with a second developer | a non-author completes T1 < 15 min with ≤3 approvals beyond gates; recorded | E6-S6, E8-S1 | 1.0 | B09 |
| E8-S3 | Demo rehearsal (§3 runs twice) + hardening from findings | 10-min demo runs twice without improvisation | E7-S6 | 2.0 | A18 |
| E8-S4 | PoC report: pass/fail per hypothesis with numbers | one-page report H1–H6 with measured numbers | E8-S2, E8-S3 | 1.0 | A18 |
| E8-S5 | Buffer | absorbs slippage | n/a | 3.0 | n/a |

E8 subtotal: **8.0 d**.

## 3. Capacity check and cut lines (honest)

Required stories (excluding E5-S5 optional): **12.5 + 10.0 + 13.5 + 15.5 + 6.0 + 11.0 + 9.0 + 8.0 = 85.5 ideal days** (the integration pass added E3-S9, E4-S9 and E4-S10, 3.5 d). Real capacity for one person over 8 weeks at a 0.7 focus factor is **≈ 28 ideal days**. **The plan is overcommitted by roughly 3×.** This is not a scheduling artifact; WRD-16 §16.1 already flags "eight weeks is tight for one person" and offers the cut line.

This overcommit is expected for a from-scratch runtime + sandbox + UI in Go (a language WRD-16 §16.1 notes is being learned) in 8 weeks. Rather than pretend, the plan adopts the WRD-16 §16.1 cut line explicitly.

### 3.1 Cut lines (consistent with WRD-16 §16.1 "the CLI-only PoC proves H1–H5")

**Cut line 1; CLI-only PoC (weeks 1–5).** Drop E6 (desktop, 11 d) and the UI parts of E7 (E7-S2, E7-S3, E7-S5 UI, ≈ 4 d) and E8-S2 (H6). This removes ≈ 16 d and delivers H1–H5 via the CLI and `warden eval smoke`, which is the WRD-16 §16.1 fallback. H6 (the UI hypothesis) is explicitly deferred; WRD-16 §1 says if H6 fails the UX changes before the MVP, not the architecture, so shipping the CLI PoC without H6 is coherent.

**Cut line 2; defer optional harnesses.** E5-S5 (Codex, Claude Code) is already optional (WRD-16 §6.1, `enabled: false`). Cut unless time remains. Copilot (E5-S1) is required for H1.

**Cut line 3; single-OS demo.** E7-S1 (second OS) can slip to "primary OS green, second OS best-effort" if week 7 is short; acceptance item 1 then holds for one OS and the second is a known gap in the PoC report.

**Cut line 4; optional fixtures.** `py-fastapi-service` (T5) and its pytest parser path are optional (WRD-16 §4.1); the node+go parsers (CF-29) cover H4. Python parser can be a stub that reports `no_stack` until needed.

With cuts 1–4 the required set falls to ≈ 66 ideal days: weeks 1–5 (E1–E5, ≈ 57.5 d) plus a trimmed E7/E8; still above 28 d for one person. **Honest conclusion: even the CLI-only PoC is a stretch for one person in 8 weeks; a realistic single-person target is H1–H3 and H5 by week 6–7, with H4 (repair) and H6 (UI) as the parts most likely to slip.** The buffer (E8-S5, 3 d) and the focus factor are the only slack. Recommended mitigations: (a) treat weeks as "vertical slices" and stop each week at a demoable state (WRD-16 §16 principle); (b) prioritize the audit/policy/sandbox core (E1–E3) which is the thesis, over breadth (extra models, extra harnesses); (c) bring the desktop UI in only after H1–H5 pass on the CLI.

## 4. Week-1 Copilot SDK spike (precise)

**Goal.** De-risk the Copilot harness (WRD-16 §16.1 risk) before week 5 by proving, or disproving, that `github.com/github/copilot-sdk/go` can run the Copilot CLI in server mode with runtime-registered tools and permission hooks, in **split mode** (harness process without worktree access; CF-22). Time-boxed to 1 ideal day.

**The 50-line Go program (outline).**

```go
// spike/copilot/main.go; throwaway; not part of the module build
package main

func main() {
    // 1. Spawn Copilot CLI in server mode (JSON-RPC over stdio).
    sess := copilot.NewSession(copilot.Options{
        ServerMode: true,
        Cwd:        os.Args[1],           // Q6: can cwd be outside the repo / a scratch dir?
        ExcludeBuiltinTools: true,        // Q3: can built-in tools be turned off?
    })
    // 2. Register one custom runtime tool ("fs__read") that the SDK exposes to the model.
    sess.RegisterTool(copilot.Tool{
        Name: "fs__read", Schema: schemaFor("fs.read"),
        Handler: func(args json.RawMessage) (any, error) {
            // Q2: does the model actually call this instead of a built-in?
            log.Println("TOOL CALL", string(args)); return execViaWardenExec(args)
        },
    })
    // 3. Install pre/post tool-use and permission hooks.
    sess.OnPermission(func(req copilot.PermissionRequest) copilot.PermissionDecision {
        log.Println("PERMISSION", req.Tool)     // Q4: does this fire before every tool call?
        return copilot.Allow                    // Q5: can we return deny and see the model adapt?
    })
    // 4. Send one prompt that forces a tool call ("read package.json and summarize").
    turn, _ := sess.SendPrompt(ctx, "Read package.json and tell me the test script.")
    // 5. Drain events; assert our tool ran, the hook fired, and print quota usage.
    for ev := range turn.Events() {
        switch ev.Kind {
        case "tool_call": log.Println("tool", ev.Tool)
        case "usage":     log.Println("quota", ev.Quota)   // Q7: is quota/premium-request count reported?
        }
    }
}
```

**Questions it must answer (exit criteria).**

| Q | Question | Pass evidence |
|---|---|---|
| Q1 | Server-mode spawn: can the SDK launch the CLI as a JSON-RPC server over stdio from Go? | session opens; a prompt returns events |
| Q2 | Custom tool registration: does the model call our `fs__read` instead of a built-in? | the registered handler logs a call |
| Q3 | Excluding built-in tools: can Copilot's own file/shell tools be disabled? | with builtins off, only our tool is called |
| Q4 | Hooks: do pre/post tool-use and permission hooks fire before execution? | `OnPermission` logs before the handler runs |
| Q5 | Permission callbacks: can we deny and have the model continue/adapt? | returning `Deny` yields a denied result the model reacts to |
| Q6 | cwd outside the repo, no worktree in the harness process (split mode): does the SDK work when the harness process has no repo access and all fs tools are delegated? | tool calls succeed while the harness cwd is an empty scratch dir |
| Q7 | Quota reporting: is premium-request/quota usage available per turn? | a `usage`/quota field is present |

**Exit criteria and fallback.** Split mode is confirmed if Q2–Q6 pass (the harness delegates every tool to `warden-exec` and never touches the worktree itself). **Fallback if split mode is impossible** (the SDK insists on executing its own fs/shell tools with direct disk access): fall back to **co-located mode** for Copilot too; one sandbox holding both the login file (ro) and the worktree, with enforcement via the permission hook plus the sandbox boundary, exactly as Codex/Claude Code (CF-22). Consequence recorded in the spike note: co-located Copilot would then be excluded from `confidential` workspaces (T4 already excludes confidential, so the demo's classification story is unaffected), and L2 becomes the recommended level for it (CF-23). If even hooks are unavailable (Q4 fails), Copilot is enforced by the sandbox only and is treated like a hook-less harness (WRD-05 §9.3): admissible on `public`/`internal` at L2, never `confidential`. The go/no-go and the chosen mode feed A12 before week 5.

## 5. Week-1 VPS model deployment (precise)

**Goal.** Stand up the T1 company-hosted endpoint so `warden provider test company-vllm` passes a tool-call probe (H1's enterprise case). Time-boxed to 1 ideal day.

**VPS sizing for Qwen2.5-Coder-32B.**

- **GPU path (preferred):** one GPU with ≥ 48 GB VRAM (e.g. A6000/L40S/A100-40G is marginal at 4-bit) runs Qwen2.5-Coder-32B-Instruct at usable speed. AWQ/GPTQ 4-bit fits in ~20–24 GB and serves comfortably on a 24 GB card (RTX 4090/L4) with vLLM; FP16 needs ~64 GB (2× 40 GB). Target: a single 48 GB GPU VPS, 4-bit quant, `--max-model-len 32768` (matches `max_context` in `models.yaml`).
- **CPU fallback (smaller model):** if no GPU VPS is available, deploy **Qwen2.5-Coder-7B-Instruct** (the `local/qwen-coder-7b`-class model) on CPU via Ollama or llama.cpp; slow but proves the T1 path end to end. The `models.yaml` entry then points `company/qwen-coder-7b` at the VPS; the demo's neutrality proof still holds (H1 is about access modes, not model size), and T6 (summarize) is the natural task for a 7B model.

**Install (choose one).**

- **vLLM (GPU):** `pip install vllm`; `vllm serve Qwen/Qwen2.5-Coder-32B-Instruct-AWQ --port 8000 --max-model-len 32768 --enable-auto-tool-choice --tool-call-parser hermes` (tool-call parser required so the OpenAI-compatible `tools` field works). vLLM exposes `/v1` OpenAI-compatible.
- **Ollama (GPU or CPU):** `ollama pull qwen2.5-coder:32b` (or `:7b` on CPU); Ollama serves `/v1` OpenAI-compatible on `:11434`; tool calling supported for these models.

**TLS + reverse proxy.** Put **Caddy** in front (simplest Let's Encrypt automation): `caddy` with a site block `llm.your-vps.example { reverse_proxy 127.0.0.1:8000 }` obtains and renews a cert automatically. (nginx + certbot is the equivalent if Caddy is not wanted.)

**Bearer token at the proxy.** Caddy checks a static bearer token before proxying, so the model server itself needs no auth:

```
llm.your-vps.example {
  @authed header Authorization "Bearer {env.WARDEN_LLM_TOKEN}"
  handle @authed { reverse_proxy 127.0.0.1:8000 }
  respond 401
}
```

The token lives in the VPS environment and in the client keychain (`secret://providers/company-vllm/token`); `models.yaml` `auth: {mode: gateway, kind: bearer, secret: ...}` (WRD-16 §6.1, matches the daemon-side injection in A11).

**Optional mTLS variant (private CA).** For `kind: mtls`: create a small private CA (`step-ca` or openssl), issue a server cert for `llm.your-vps.example` and a client cert for the daemon; Caddy `tls` block requires client auth (`client_auth { mode require_and_verify; trusted_ca_cert_file ca.pem }`); the client cert+key go in the keychain (`providers/company-vllm/client_key`), injected host-side by the proxy path in A11/A15. This is the enterprise-realistic variant; bearer is the demo default.

**Firewall.** VPS firewall (ufw/security group): allow inbound `443` only (and `22` for admin from a known IP); block `8000` from the internet (only Caddy on loopback reaches it). Outbound unrestricted.

**Health check.** `curl -H "Authorization: Bearer $TOK" https://llm.your-vps.example/v1/models` returns the model list; a liveness probe hits `/health` (vLLM) behind the same auth.

**`models.yaml` entry (provider `company-vllm`).** As in WRD-16 §6.1:

```yaml
- id: company-vllm
  protocol: openai-compatible
  base_url: https://llm.your-vps.example/v1
  auth: { mode: gateway, kind: bearer, secret: "secret://providers/company-vllm/token" }  # or kind: mtls
  tier: T1
```

with model `company/qwen-coder-32b` → `Qwen/Qwen2.5-Coder-32B-Instruct` (or `-7b` on CPU fallback).

**Acceptance.** `warden provider test company-vllm` passes: connectivity ok, capability probe reports `tool_calling: native`, and a probe request with a `tools` field returns a tool proposal (the tool-call probe). This is the H1 "company-hosted model" leg.

## 6. Milestone → acceptance-checklist mapping (WRD-16 §15 items 1–12)

| WRD-16 §15 item | Acceptance | Stories that satisfy it | Milestone (week) |
|---|---|---|---|
| 1 | `warden doctor` green on macOS + Linux | E1-S8, E2-S2, E7-S1 | 7 |
| 2 | T1 on Anthropic, local, Copilot, company-hosted; identical manifests (H1) | E4-S8, E4-S9, E5-S1, E5-S3, E1-K1 | 5 |
| 3 | T2, T3, T6 on ≥1 mode; T4 on Anthropic/Copilot | E5-S4, E4-S4 (parsers), E3-S6 (summarize/T6) | 5 |
| 4 | One T1 run: failing verify → repair → pass (H4) | E4-S5, E4-S8 | 4 |
| 5 | `audit verify --strict` on every session; export re-verifies elsewhere (H2, H5) | E1-S2, E4-S7, E4-S10 (deliveries covered by strict), E7-S3 | 4 |
| 6 | `escape-check.sh` on both OSes (H3) | E2-S5, E7-S1 | 7 |
| 7 | S1, S2, S3 behave per §4.3, events visible | E3-S2, E2-S4, E5-S1, E7-S4 | 7 |
| 8 | Switch to `confidential` greys T3/T4; T1 completes on company/local | E5-S3 | 5 |
| 9 | Non-author completes T1 < 15 min, ≤3 approvals beyond gates (H6) | E6-S6, E8-S2 | 8 |
| 10 | Cancel during implement stops sandbox < 5 s; resume from G1 | E4-S3, E4-S7 (resume), E2-S5 (cancel check) | 4 |
| 11 | 10-min demo runs twice without improvisation (step 6: commit then rejected push) | E4-S10, E7-S6, E8-S3 | 8 |
| 12 | One-page PoC report, pass/fail per hypothesis with numbers | E8-S4 | 8 |

Items 2, 4, 5, 8, 10 (H1–H5) are met by week 5 on the CLI; the cut-line-1 floor. Items 1, 6, 7 land in week 7; items 9, 11, 12 (H6 + polish) in week 8, which is where slippage concentrates.

## 7. Risk burndown

| Risk (WRD-16 §16.1) | Earliest retire | How retired | Fallback if it materializes |
|---|---|---|---|
| Seatbelt/bwrap profile breaks a toolchain | Week 2 (E2-S2, E2-S5) | escape check + T1 build/test run under the profile | start permissive on read paths, tighten via escape check; L2 fallback (CF-23) |
| Local model fails at tool calling / invalid JSON | Week 1–3 (E1-S6 probe, E3-S5 emulation) | capability probe + emulated protocol; T2/T6 as showcase | report honestly; use Anthropic/Copilot for the strict-JSON tasks |
| Copilot SDK server mode/hooks differ | Week 1 (E1-K2 spike) | the spike (§4) answers Q1–Q7 before week 5 | co-located mode; or sandbox-only enforcement on public/internal at L2 |
| Vendor terms change for a harness | ongoing | every harness optional/flagged; API-key path is the demo path | disable the harness; personal-mode locked in shared builds (CF-21) |
| Codex login path changes/blocked | Week 5 (optional) | Codex is optional (E5-S5) | API-key mode; or drop Codex |
| 8 weeks tight for one person | Week 0 (this plan) | cut lines §3; CLI-only PoC proves H1–H5 by week 5 | ship CLI PoC; defer UI/H6 |
| Go learning curve | Week 1 (E1 doubles as the exercise) | small standard surface (net, os/exec, JSON, SQLite) | lean on stdlib; the skeleton is the learning slice |
| VPS/GPU unavailable for 32B | Week 1 (E1-K1) | CPU fallback with 7B (§5) | `company/qwen-coder-7b` on CPU; H1 leg still proven |

Burndown shape: the two highest-uncertainty risks (Copilot SDK, sandbox profiles) are retired in weeks 1–2 by design, so the schedule risk after week 2 is dominated by the raw volume of work (§3), which the cut lines address rather than eliminate.

## Traceability

| Design element | WRD source (doc §) | Requirement / invariant satisfied |
|---|---|---|
| Epics E1–E8 mapped to weeks | WRD-16 §16 | 8-week build plan |
| Stories with acceptance criteria | WRD-16 §15, §16 | testable definitions of done |
| Capacity check + cut lines | WRD-16 §16.1 | honest one-person scope; CLI-only proves H1–H5 |
| Copilot SDK spike | WRD-16 §16.1 (risk), §6.2; CF-22 | de-risk harness before week 5 |
| VPS model deployment | WRD-16 §16 wk1, §6.1, §6.2; A11 | T1 company-hosted endpoint; `provider test` passes |
| Milestone → §15 mapping | WRD-16 §15 items 1–12 | acceptance traceability |
| Risk burndown | WRD-16 §16.1 | risks retired earliest possible |
| E3-S9 questions/answers, rejected approval → running | core ID-05, ID-10 | BI-1, BI-4 |
| E4-S9 `session.setPin`, re-route, paused fallback | core ID-04, ID-16; CF-44 | BI-7, T-22 |
| E4-S10 post-run delivery via PDP, `workflow.delivered` | core ID-01, ID-02, ID-03 | BI-1, S-7, H2 |

## Deviations and assumptions

- ASM:focus-factor: real capacity estimated at 0.7 × calendar (≈ 28 ideal days / 8 weeks) for one person; used to size the overcommit honestly (§3).
- ASM:estimates: ideal-day estimates are the author's; they are relative sizing for planning, not commitments.
- DEV:cut-lines: the plan adopts WRD-16 §16.1's own cut line (CLI-only PoC) as the primary mitigation rather than claiming the full scope fits (§3).
- ASM:vllm-tool-parser: vLLM needs `--enable-auto-tool-choice --tool-call-parser hermes` (or the model's matching parser) for OpenAI-compatible tool calls; the exact parser is confirmed against the deployed Qwen build in week 1 (§5).
- ASM:gpu-vps: 48 GB GPU assumed for 32B at 4-bit; CPU 7B fallback specified if unavailable (§5).
- OQ (A18); whether to run the optional Codex/Claude Code harnesses in the PoC demo; recommended answer: keep them disabled by default, enable Codex only if week 5 has slack. See OPEN-QUESTIONS.
