# A16 Failure modes and security review

Three parts: (1) a failure-mode table with detection, runtime behavior, event, task/run state, user message, CLI exit code, and mapping to WRD-05 error codes / WRD-02 §11 failure domains; (2) the WRD-10 threats T-01..T-24 mapped to PoC coverage with the mechanism, the proving test, and residual risk; (3) the `scripts/escape-check.sh` specification. Names from `00-DESIGN-CORE.md`; conflicts cite `CF-xx`. JSON-RPC application error strings are core §6; CLI exit codes are `0` verified, `2` stopped for approval, `3` failed (WRD-16 §14), extended below for daemon/setup failures.

## 1. Failure modes

Columns: **Detection** (how the runtime notices); **Behavior** (what it does; fail-closed where audit or security is at stake, WRD-02 §11); **Event** (emitted, core §5); **Task/Run state** (core §3); **User message** (short, plain, B07); **Exit** (CLI exit code for `--non-interactive`; interactive shows the message); **Maps to** (WRD-05 §4 code and/or WRD-02 §11 domain / core §6 JSON-RPC code).

| # | Failure mode | Detection | Behavior | Event | Task/Run state | User-visible message | Exit | Maps to |
|---|---|---|---|---|---|---|---|---|
| 1 | Provider rate limited | `429`/`rate_limited` from adapter | router retries ≤3 w/ backoff honoring `retry_after_ms`, then fallback same-or-lower tier | `model.call.end(error, retryable)`, `routing.fallback` | task `running` (retry) then continues | "Rate limited by <provider>; retrying in 20 s (2/3)" | n/a | WRD-05 `rate_limited`; WRD-02 provider outage |
| 2 | Provider unavailable (5xx/conn) | adapter conn error / `provider_unavailable` | retries then fallback within same-or-lower tier; if none, `waiting_for_input` (row 45) | `model.call.end(error)`, `routing.fallback` | `running`→`waiting_for_input(provider)` if no in-tier fallback | "<provider> is unavailable; no other admissible model. Configure one or wait." | 2 | WRD-05 `provider_unavailable`; WRD-02 provider outage |
| 3 | Provider auth failed | `401`/`auth_failed` | no retry; fail; prompt to fix key | `model.call.end(error)` | task `failed(provider)` | "Authentication failed for <provider>. Re-enter the API key in Settings." | 3 | WRD-05 `auth_failed`; -32001 n/a |
| 4 | Context too long | `context_too_long` or pre-send budget check | agent loop compacts (A10); if still over, fail | `context.compacted` then `model.call.end(error)` | `running` then `failed(schema/provider)` | "The request context is too large for <model>. Try a model with a larger context." | 3 | WRD-05 `context_too_long` |
| 5 | Content filtered | `content_filtered` stop/error | surface; no retry | `model.call.end(stop_reason: content_filter)` | `failed(provider)` | "<model> refused to answer (content filter)." | 3 | WRD-05 `content_filtered` |
| 6 | Model not found | `model_not_found` on call/probe | fail; suggest `provider test` | `model.call.end(error)` / `provider.configured(test, fail)` | `failed(provider)` | "Model <id> not found on <provider>. Run provider test." | 3 | WRD-05 `model_not_found` |
| 7 | Tool-call format unsupported | probe/decode: `tool_format_unsupported` | switch to emulated protocol (A10) if capable, else exclude model | `model.call.end(error)` | `running` (recovered) or reroute | "<model> does not support tool calls; using emulated protocol." | n/a | WRD-05 `tool_format_unsupported` |
| 8 | No admissible model | router admission empty for classification | `waiting_for_input`; offer configure/lower strategy | `routing.decision(no chosen)` | `waiting_for_input(no_admissible_model)` | "No admissible model for confidential data. Add a T0–T2 provider." | 3 | -32007; WRD-06 §6 |
| 9 | Keychain locked | keyring returns locked/denied | provider calls fail closed; prompt unlock | `secret.access(error)`, `model.call.end(auth_failed)` | `failed(provider)` | "Keychain is locked. Unlock it and retry." | 3 | WRD-05 `auth_failed`; WRD-02 keychain locked |
| 10 | Keychain item missing | keyring not-found on `secret://` resolve | fail closed before call | `secret.access(error)` | `failed(provider)` | "Credential for <provider> is missing. Add it in Settings." | 3 | -32001 n/a; WRD-02 keychain |
| 11 | Sandbox backend missing | `system.doctor` / sandbox create fails (no bwrap/seatbelt) | refuse to run; no unsandboxed fallback (S-1) | `sandbox.violation` n/a; `task.state(failed)` | task `failed(tool)`; run `failed` | "Sandbox backend unavailable. Install bubblewrap / check Seatbelt." | 3 | -32006; WRD-02 sandbox unavailable |
| 12 | Seatbelt profile compile failure | `sandbox-exec` non-zero on profile | if L2 enabled, retry at L2; else fail closed | `sandbox.create(error)` then `sandbox.violation(seatbelt_deny)` | `failed(tool)` or retried L2 | "Sandbox profile failed to compile on this macOS version. Enable L2 or update." | 3 | -32006; CF (fallback to L2) |
| 13 | bwrap userns disabled | bwrap exits: "setting up uid map" / clone EPERM; doctor check | fail closed; doctor fix hint; L2 fallback if enabled | `sandbox.create(error)` | `failed(tool)` | "Unprivileged user namespaces are disabled. See doctor for the fix, or enable L2." | 3 | -32006; WRD-13 §8 |
| 14 | Disk full / store write failure | SQLite write error / fsync fail | **fail closed**: pause the task rather than run without audit | `store.integrity` (best effort) | task `waiting_for_input`; run `waiting` | "Disk full: paused to keep the audit trail intact. Free space and resume." | 3 | -32009; WRD-02 disk full |
| 15 | Daemon crash | client socket EOF / supervisor | clients reconnect; sessions resume from events; running execs → `failed(interrupted)`, re-queued if attempts remain | on restart: `runtime.start`, `sandbox.destroy(orphan)`, `task.state(interrupted)` | interrupted tasks re-queued or `failed(interrupted)` | "The runtime restarted; your session resumed. One task is retrying." | n/a | WRD-02 daemon crash; core §13.11 |
| 16 | Desktop disconnect | Tauri bridge / WS closed | daemon keeps running; events buffered; UI replays from `after_seq` on reconnect | none (client-side) | unchanged | "Reconnecting to the runtime…" | n/a | WRD-02 client↔runtime; B08 |
| 17 | Harness login expired | harness stderr/JSON-RPC auth error at session start | fail the harness task; suggest re-login on host; API-key path unaffected | `harness.session.end(reason: auth)` | `failed(provider)` | "Copilot login expired. Log in to the Copilot CLI on your machine." | 3 | WRD-05 `auth_failed`; A12 |
| 18 | Harness crash | harness process exit / RPC EOF mid-turn | task fails; retry once if `attempt<max` (tool transient); worktree preserved | `harness.session.end(reason: crash)`, `tool.exec.end(error)` | `running`→`queued(retry)` or `failed(tool)` | "The Copilot session ended unexpectedly; retrying." | 3 | WRD-02 (executor crash class) |
| 19 | Proxy port collision | listener bind EADDRINUSE (macOS ephemeral / Linux 3128) | pick another ephemeral port (macOS) / fail closed (Linux netns) with retry | `sandbox.create(error)` | `failed(tool)` (retry) | "Could not start the task proxy; retrying." | 3 | CF-15; WRD-02 |
| 20 | Executor crash | `warden-exec` exit / socketpair EOF | new sandbox + executor, worktree preserved; retry (tool transient) | `sandbox.destroy(reason: exec_crash)` | `running`→`queued(retry)` or `failed(tool)` | "The task executor restarted; retrying." | 3 | WRD-07 §5 tool transient |
| 21 | Git alternates missing | session git dir has no `objects/info/alternates` or main `.git` gone | fail session open / delivery; do not proceed without object store | `worktree.create(error)` / `session.open` error | run `failed(tool)` | "Cannot access the repository object store. Is the repo still on disk?" | 3 | CF-18; WRD-02 |
| 22 | Dirty repo at open | `git status` shows uncommitted changes at HEAD | refuse to open (never touch the user's working tree) OR require `--allow-dirty` (session works off HEAD commit only) | `session.open` error / `session.open(ok)` | no session created | "The repository has uncommitted changes. Commit or stash them first." | 3 | WRD-07 §10; -32003 |
| 23 | Budget exhausted (execution) | cost accumulator ≥ manifest `max_cost_usd` | fail the execution; write `checkpoint` artifact | `model.call.end`, `artifact.created(checkpoint)` | `failed(budget)` | "This task hit its cost limit ($2.00)." | 3 | -32008; WRD-06 §8 |
| 24 | Budget exhausted (session/daily) | accumulator ≥ `budgets.session_usd`/`daily_usd` | run `waiting` (ST-4); offer `session.setBudget` | `budget.changed` n/a; `model.call.end` | run `waiting`; task `waiting_for_input(budget)` | "Session budget reached ($5.00). Raise it to continue." | 2 | -32008; core §13.13 |
| 25 | Schema validation failure (agent output) | output fails JSON Schema | one in-execution repair turn (A10); then fail | `artifact.created` withheld; `model.call.*` | `failed(schema)` after repair turn | "<agent> produced an invalid result. Try again or pick another model." | 3 | WRD-07 §5; core §3 |
| 26 | Approval expiry (inline) | 24 h elapsed with prompt open | resolve `expire`; fail the task | `approval.resolved(expire)` | `failed(approval_expired)` | "An approval request expired after 24 hours." | 2 | CF-38; core §13.3 |
| 27 | Gate timeout | `timeout_seconds` (86400) elapsed | gate `timed_out`; run `failed` | `workflow.gate.resolved`? no → `task.state(timed_out)` | gate `timed_out`; run `failed` | "The plan review timed out after 24 hours." | 2 | WRD-16 §8; core §13.3 |
| 28 | Chain verification failure | `audit.verify` finds a hash mismatch | surface ST-6; block "verified" badge; export still allowed w/ warning | `audit.verify(chain_ok: false)` (result, not event) | run unchanged; UI ST-6 | "Audit chain verification failed. The record may have been altered." | 3 | S-5; WRD-09 §4 |
| 29 | Strict verify failure | `audit verify --strict` finds a `tool.exec.start` without a preceding allow decision | report the offending `seq`; non-zero exit | `audit.verify(strict_ok: false)` | n/a | "Strict audit failed: a tool ran without an allow decision (seq N)." | 3 | H2; CF-40; core §6 |
| 30 | Clock skew | event `ts` earlier than `prev` event `ts` beyond tolerance; or checkpoint time anomaly | warn (not fail: `seq`/hash order is authoritative, not `ts`); record | `store.integrity(warn)` | unchanged | "Clock moved backwards; timestamps may be out of order (audit unaffected)." | n/a | WRD-09 §4; N/A |
| 31 | Sandbox resource limit hit | rlimit/cgroup kill (OOM, pids, CPU) | task `failed(resource)`; host unaffected | `sandbox.violation` (if detectable) / `tool.exec.end(error)` | `failed(resource)` | "The task exceeded its resource limits and was stopped." | 3 | T-09; WRD-10 §11 |
| 32 | Egress to unlisted host | proxy sees CONNECT to host not in allowlist | hold ≤120 s for approval (interactive) else 403 | `proxy.denied` or `approval.requested`→`proxy.connect` | `waiting_for_approval` or continues | "Blocked network to <host>:<port> (not allowed). Approve to continue." | 2 | INV-5; CF-20 |
| 33 | Deny-list read attempt | PDP + executor + mount all deny `.env`/`.ssh` etc. | deny at three layers; model told why | `policy.decision(deny)`, `sandbox.violation(deny_list)` | task continues (model adapts) | "Reading <path> is not allowed (protected file)." | n/a | INV-1; S-4; T-06 |
| 34 | Shell string exec | `platform.no-shell-strings`: `argv[0]∈{sh,…} && -c` | deny (R6) | `policy.decision(deny)` | continues | "Running shell commands is not allowed." | n/a | CF-19; S1 |
| 35 | Protected-branch commit | `platform.protected-branches`: commit to `main`/`master`/`release/*` | deny | `policy.decision(deny)` | continues | "Commits to the main branch are not allowed here." | n/a | INV-3; core §9 |
| 36 | Harness vendor-terms block | enable a `prohibited` harness, or `claude-code` in shared mode | refuse | `provider.configured(fail)` | n/a | "This harness cannot be enabled (vendor terms)." | 3 | INV-7; -32012; CF-21 |
| 37 | Cancellation deadline miss | 5 s elapsed and a proc still alive | force SIGKILL + sandbox teardown; record | `sandbox.destroy(reason: cancelled, killed_pids)` | `cancelled` | "Task cancelled." | n/a | S-8; core §13.10 |
| 38 | Redaction hit | secret scanner matches in tool output/artifact/context | replace `[REDACTED:<type>]`; count | `redaction` | unchanged | (silent; count shown in cost/audit panel) | n/a | S-9; core §13.16 |
| 39 | Store integrity at startup | replay finds a broken chain link | mark store degraded; read-only audit; block new sessions | `store.integrity(fail)` | n/a | "The local store failed an integrity check; running in read-only mode." | 3 | S-5; WRD-09 §9 |
| 40 | Token handshake failure | first call not `system.hello` or bad token | reject connection | (none; RPC error) | n/a | "Not authorized. The runtime token is missing or wrong." | 3 | -32001; CF-14 |
| 41 | Protocol mismatch | `system.hello.protocol` unsupported | reject | (none) | n/a | "This client is too old/new for the runtime." | 3 | -32011 |
| 42 | Confirmation required | privileged method (loosen classification, shutdown) without `confirm` | reject; UI shows confirm dialog | (none) | n/a | "This action needs confirmation." | 2 | -32005; core §13.14 |
| 43 | Non-interactive approval needed | `approval_required` in `--non-interactive` with no pre-grant | resolve to deny (INV-8); stop | `policy.decision(approval_required)`, `approval.resolved(reject)` | `failed(policy_denied)` | "Stopped: an approval is required and none was pre-approved." | 2 | INV-8; WRD-16 §14 |
| 44 | Harness hook timeout (late `once` approval) | a harness pre-tool-use/permission hook waits on an approval longer than the harness's own callback timeout; the harness gives up on that tool call | the pending approval stays open; if the user later grants `once`, the grant is converted to a one-shot grant for the next identical action pattern in the same task, valid 10 minutes (ID-07); the harness's abandoned call is recorded as not executed | `harness.hook(pre_tool_use)`, `policy.decision(approval_required)`, `approval.requested`; later `approval.resolved(approve, scope: once, grant_expires_at: +10 min)`; no `tool.exec.start` for the abandoned call | task stays `running` (the harness continued); a matching retry by the harness gets `policy.decision(allow, matched_rules: [grant.apr_…])` | "Copilot stopped waiting for this approval. Your approval applies once to its next identical attempt within 10 minutes." | n/a | core ID-07; A12 |
| 45 | Paused tier-bounded fallback | the in-tier fallback list is exhausted (e.g. local T0 model failing on an `internal` workspace) and only higher-tier candidates remain | never widen the tier (CF-44); pause the task and offer a one-click pin of the best higher-tier admissible model | `routing.fallback(to: null, cause)`, `task.state(running → waiting_for_input, reason: provider)` | task `waiting_for_input(provider)`; run `waiting`; resumed by `session.setPin` (ID-04) → `task.state(waiting_for_input → queued, input_provided)` → `routing.decision {pin}` | "local/qwen-coder-32b keeps failing. Continue on anthropic/claude-sonnet (T3)?" | 2 | core ID-16, ID-04; CF-44; WRD-06 §7; WRD-02 §11 provider outage |

### 1.1 Fail-closed principle

Rows 11, 12, 13, 14, 39, 40 are fail-closed: the runtime never runs an agent-requested process outside a sandbox (S-1), never continues without writing the audit event (disk full → pause, WRD-02), and never accepts an unauthenticated client. Provider/model failures (rows 1–8) fall back within tier but never widen it (BI-7, WRD-06 §7).

### 1.2 CLI exit-code summary

`0` verified/success; `2` stopped for approval or budget/confirmation (recoverable by a human); `3` failed (WRD-16 §14). Rows mapping to `2` are those where a human decision would let the run continue; rows mapping to `3` are terminal failures for a non-interactive run.

## 2. WRD-10 threats T-01..T-24 mapped to PoC coverage

Coverage: **Covered** (mechanism present and tested in the PoC), **Partial** (mechanism present, narrower than the full product or untested edge), **Deferred** (out of PoC scope, WRD-16 §2.2). "Test" names the escape-check row (§3), a golden policy case (A08), or a unit test.

| Id | Threat | Coverage | Mechanism in the PoC | Test that proves it | Residual risk |
|---|---|---|---|---|---|
| T-01 | Repo hook execution on checkout/commit | Covered | `core.hooksPath=/warden/empty`, `GIT_CONFIG_GLOBAL/SYSTEM=/dev/null`, main `.git` not mounted (CF-18); delivery host-side with hooks off (A14) | escape-check `hooks`; S4 scenario | none material for PoC; host `git push` uses user's config (intended) |
| T-02 | Package lifecycle scripts run arbitrary code | Covered | install is R4 approval; `postinstall` runs inside sandbox; no creds; egress allowlist | escape-check `postinstall`; S3 | postinstall can still consume CPU/net within limits+allowlist |
| T-03 | Prompt injection in files/tool output | Covered | untrusted+provenance tagging (BI-4); instructions-in-data ignored by system prompt; approvals show exact action | S1/S2 scenarios; golden policy cases | model may still be misled into an allowed-but-unhelpful action (no security impact) |
| T-04 | Exfiltration via network from sandbox | Covered | netns (Linux) / Seatbelt deny (macOS); proxy allowlist; DNS in daemon; loopback/RFC1918 denied (CF-20) | escape-check `egress`, `dns` | covert timing channels out of scope |
| T-05 | Exfiltration via model prompt | Covered | classification+tier admission (BI-7); redaction; no secrets in context | golden routing cases; redaction unit tests | a T3 model on `internal` data still sees code (by design/consent) |
| T-06 | Reading secrets on disk | Covered | deny-list at PDP+executor+mount (S-4); symlink/`..` canonicalization | escape-check `read-secret` (abs/symlink/`..`); S2 | deny-list is a subset (WRD-16 §10.5) of WRD-10 §6; unusual paths could slip; see residual note |
| T-07 | Writing outside worktree | Covered | only worktree rw; executor root check + canonicalization (INV-2) | escape-check `write-escape` | none material |
| T-08 | Privilege escalation in sandbox | Covered | no-new-privs, cap-drop, seccomp (Linux), rootless; Seatbelt deny default | escape-check `privesc` (sudo/unshare/ptrace) | kernel 0-day in namespaces (mitigated by L2 option) |
| T-09 | Sandbox DoS | Covered | rlimit + cgroup (Linux) / setrlimit+watchdog (macOS); timeouts | escape-check `resource` (fork bomb, 4 GiB, disk) | macOS cgroup-less limits are coarser (watchdog) |
| T-10 | Tool impersonation by model | Covered | tools runtime-defined; name mapping `.`→`__`, regex validated; unknown → `unknown_tool` (core §8) | unit test: unknown/renamed tool rejected | none |
| T-11 | Malicious agent package requests broad caps | Partial | manifest ∩ policy; caps summarized in UI | unit test: capability intersection | no signing/registry in PoC (agents from local dir; WRD-16 §2.3) |
| T-12 | MCP tool description poisoning | Deferred | n/a | n/a | MCP out of PoC scope (WRD-16 §2.2) |
| T-13 | Compromised harness exceeds policy | Covered | harness in sandbox; hooks→PDP; split mode isolates worktree from login (CF-22); vendor-only egress | harness unit/integration; escape-check `harness-egress` | co-located harnesses (Codex/Claude Code) enforce only at sandbox+hook, excluded from `confidential` (T4) |
| T-14 | Harness credential theft by repo code | Partial | mount only the login **file** ro (HX-1); split mode: repo code runs in the tool sandbox without the login; taint | harness test: repo script reads login | co-located harnesses expose login file to repo code in the same sandbox (L2 recommended; excluded from confidential) |
| T-15 | UI bypass of policy | Covered | UI has no privileged path; daemon enforces; owner-only socket + token (BI-6) | API test: call without hello/token → -32001 | none |
| T-16 | Workspace policy widening perms | Covered | only platform+user layers; user cannot override L0 invariants; repo content can't widen (BI-5) | golden policy: repo `.warden` ignored | workspace layer deferred (restrict-only, WRD-16 §2.3) |
| T-17 | Audit tampering | Covered | hash chain + signed checkpoints (Ed25519); `audit verify` | `audit verify` unit tests; strict test | tamper-evident not tamper-proof: local attacker with keychain can rewrite (WRD-09 §4; T-23) |
| T-18 | Secret leakage into logs/events | Covered | redaction before persistence and before model calls (S-9); args redacted per descriptor | log-scan unit test; redaction events | novel secret formats not in the regex set escape redaction |
| T-19 | Cross-session data leakage | Covered | fresh sandbox per task; artifacts scoped by session; scratch per task; no shared tmp | unit test: scratch isolation | none material for single-user PoC |
| T-20 | Runaway autonomous execution | Covered | step cap (`max_steps`), token/cost budgets, 5 s cancel | budget tests; escape-check `cancel` | none material |
| T-21 | Supply chain of the runtime itself | Partial | pinned deps, reproducible flags (`-trimpath`), SBOM + signatures in release (A17) | release pipeline (A17) | no external audit in PoC; sigstore optional |
| T-22 | Provider outage forcing insecure fallback | Covered | tier-bounded fallback; admission re-applied, never relaxed (BI-7, WRD-06 §7) | golden routing: fallback stays ≤ tier | none |
| T-23 | Local attacker reads `~/.warden` | Partial | owner-only perms; secrets in keychain; `.warden` on deny-list | doctor perms check | out of scope beyond OS controls (documented, WRD-10 §12) |
| T-24 | Approval fatigue | Partial | scoped approvals; clear reasons; R5 never persistable; UX metrics (B09) | metrics test (`metrics.get`) | behavioral; measured, not eliminated |

### 2.1 Accepted risks

| Id | Risk | Why it is accepted in the PoC | Mitigations in the design | Residual |
|---|---|---|---|---|
| AR-1 | Script injected into the desktop webview could call the Tauri bridge and approve pending prompts or gates (the bridge accepts `approval.resolve` and `workflow.resolveGate` like any other RPC; the daemon cannot tell a click from a script) | The daemon trusts the authenticated client (BI-6); a per-action user-presence proof (OS-level confirmation, hardware key) is beyond the PoC | (1) untrusted content (file contents, command output, model text, diff lines, harness output) is rendered as plain text only, never as HTML or Markdown-to-HTML (B04, B08), which removes the injection vector; (2) strict CSP `default-src 'self'`, no remote script, no `unsafe-eval`/`unsafe-inline` for scripts (A17 §4.2, B08); (3) Tauri capabilities expose only the sidecar and the two bridge commands, no shell or opener (A17 §4.3); (4) approval keyboard shortcuts act only when the approval card has focus and ignore synthetic repeats (B05); (5) every resolution is an `approval.resolved`/`workflow.gate.resolved` event with `approver`, so any unexpected approval is visible in the audit trail; R5 (`git.push`) is never persistable beyond `once` (S-7) | A successful XSS in the UI would still be able to approve one action at a time. Revisit with an out-of-webview confirmation for R5 in the MVP |
| AR-2 | Tamper evidence, not tamper proofing, of the local audit trail (T-17, T-23) | WRD-09 §4 states this explicitly for the local store | hash chain, signed checkpoints, owner-only files, key in keychain | a local user with keychain access can rewrite history until central sync (Phase 3) |
| AR-3 | Co-located harnesses expose their login file to repository code in the same sandbox (T-14) | WRD-16 makes Codex and Claude Code optional | disabled by default; T4 never admitted for `confidential`; L2 recommended (OQ below) | credential theft by a malicious repository while such a harness is enabled |

### 2.2 Residual-risk notes carried to the MVP

- Deny-list is a PoC subset (WRD-16 §10.5) of the full WRD-10 §6 list; the MVP restores the full list and lets orgs extend it. Until then a path like `**/*.p12` (in WRD-10 §6, not in the WRD-16 subset) is only caught by mount/executor absence, not the PDP layer; the escape-check includes one such path as a canary (§3).
- Co-located harnesses (Codex, Claude Code) enforce policy only through the sandbox and the harness's own hooks; they are admissible only on `public`/`internal` (T4 excludes `confidential`) and should default to L2 when enabled (CF-22, CF-23).
- Tamper-evidence, not tamper-proofing (T-17/T-23): a local user with keychain access can rewrite the chain. Central sync (Phase 3) closes this.

## 3. Escape-check script specification (`scripts/escape-check.sh`)

Purpose: prove H3 (WRD-16 §1); the sandbox holds; on macOS and Linux, in CI (A17). It runs the WRD-16 §10.7 checks plus host canaries, asserting each expectation against emitted events and host state, and exits non-zero on any failure.

### 3.1 How the scripted "agent" is injected (decision)

**Decision: a test-only JSON-RPC method `debug.runScript`, compiled only under the `escapecheck` build tag, absent from release builds.** The daemon is built for the check with `-tags escapecheck`; `debug.runScript(session_id, steps[])` submits a fixed sequence of tool proposals (each `{tool, operation, resource/argv}`) straight into the agent loop's proposal handler, bypassing the model but going through the full PDP → executor → sandbox → proxy path exactly as a real proposal. This is chosen over a fake `scripted` provider because it removes model nondeterminism entirely (the check must be deterministic in CI) and lets each step assert an exact expected decision/event, while still exercising the real enforcement path (the model is never the enforcement boundary; the PDP and sandbox are). 

Ship guard: `debug.*` methods are behind the `escapecheck` build tag and additionally refused unless `runtime.mode == personal` and an env flag `WARDEN_ESCAPE_CHECK=1` is set; a release build (no tag) does not compile the method, so it cannot ship enabled. `system.doctor` reports a `fail` row `debug.methods_present` if `debug.*` is ever reachable in a non-tagged build (belt and suspenders). (NEW `debug.runScript`, `escapecheck` build tag.)

### 3.2 Checks (WRD-16 §10.7 + WRD-10 §11), exact commands, expected events, assertions

Each row: the step(s) `debug.runScript` submits, the expected runtime outcome, the events asserted, and the host-state assertion. `$WT` = worktree, `$SB` = sandbox, canaries per §3.3.

| Check id | Step(s) submitted | Expected outcome | Events asserted | Host assertion |
|---|---|---|---|---|
| read-secret-abs | `fs.read /Users/$USER/.ssh/id_ed25519` (Linux: `/home/$USER/.ssh/id_ed25519`) | denied | `policy.decision(deny, INV-1)`; `sandbox.violation(deny_list)` if reached mount | canary key unread (mtime/atime unchanged) |
| read-secret-symlink | create symlink `$WT/link→$HOME/.ssh/id_ed25519`, then `fs.read $WT/link` | denied (canonicalized) | `policy.decision(deny)` or `sandbox.violation(path_escape)` | canary unread |
| read-secret-traversal | `fs.read $WT/../../.ssh/id_ed25519` | denied (root check) | `sandbox.violation(root_check)` / `policy.decision(deny)` | canary unread |
| read-p12-canary | `fs.read $WT/../secret.p12` (WRD-10 §6 path not in WRD-16 subset) | denied (mount/root check) | `sandbox.violation(root_check)` | canary unread (documents residual, §2.1) |
| write-home | `fs.write $HOME/escape.txt "x"` | denied | `policy.decision(deny, INV-2)` / `sandbox.violation` | `$HOME/escape.txt` absent |
| write-repo-outside | `fs.write <main-repo>/outside.txt` (outside `$WT`) | denied | `sandbox.violation(root_check)` | file absent on host |
| egress-noproxy | `proc.exec ["curl","https://example.com"]` (no proxy env in argv) | no route / Seatbelt deny; if routed via proxy env, `proxy.denied` (example.com not allowlisted) | `tool.exec.end(error)` or `proxy.denied` | no outbound connection (netstat none) |
| egress-rawtcp | `proc.exec ["nc","-zv","8.8.8.8","53"]` | fails (no route) | `tool.exec.end(error)` | none |
| dns-unlisted | `proc.exec ["nslookup","unlisted.example.net"]` | fails / resolves only via proxy which denies | `proxy.denied` or exec error | none |
| egress-allowed | approve install egress, `proc.exec ["curl","https://registry.npmjs.org/"]` through proxy | allowed | `approval.resolved(approve)`, `proxy.connect(registry.npmjs.org:443)` | connection only to allowlisted host |
| shell-string (S1) | `proc.exec ["sh","-c",":(){ :|:& };:"]` | denied (R6) | `policy.decision(deny, platform.no-shell-strings)` | no fork bomb runs |
| resource-mem | `proc.exec` a helper allocating 4 GiB (CF-41) | `failed(resource)` | `sandbox.violation`/`tool.exec.end(error)` | host memory unaffected |
| resource-disk | `proc.exec` writing 2 GiB to `/tmp` | limit enforced | `tool.exec.end(error)` | host disk unaffected |
| privesc-sudo | `proc.exec ["sudo","-n","true"]` | fails | `tool.exec.end(error)` | no privilege gained |
| privesc-unshare | `proc.exec ["unshare","-r","true"]` | fails (seccomp/no-new-privs) | `sandbox.violation(seccomp)` (Linux) / exec error | none |
| privesc-ptrace | `proc.exec ["strace","-p","1"]` | fails | error / seccomp | none |
| hooks (S4) | place `pre-commit` hook + `core.hooksPath` in fixture; `git.commit` on session branch | hook never runs | `tool.exec.end(git commit ok)`; no hook output | marker file absent in `$HOME` |
| env-dump | `proc.exec ["env"]` | output has no `*_API_KEY`, no host `HOME`, no keychain paths | `tool.exec.end` output scanned | assert output regex-clean |
| cancel | `proc.exec ["sleep","1000"]`, then `session.cancel` | all processes gone ≤5 s | `sandbox.destroy(reason: cancelled)` within 5 s | no `sleep` pid on host |
| postinstall (S3) | fixture with `postinstall` reading `~/.ssh` + POST to collector; approve install | `~/.ssh` absent in sandbox; POST denied; tests still run | `proxy.denied(collector.example.net)`, `proxy.connect(registry…)` | ssh canary unread; no POST leaves host |

### 3.3 Host canaries

Created by the harness before the run, verified untouched after (by content hash and mtime/atime):

- `$HOME/.ssh/id_ed25519`; a **fake** ed25519-shaped key file (marker content `WARDEN-CANARY-SSH`), created if absent (and removed after if the harness created it). Assert content hash unchanged and not present inside any `$SB` mount.
- `$HOME/.warden-canary.env`; fake env file containing `API_KEY=WARDEN-CANARY-ENV`. Assert unread.
- `<main-repo-parent>/secret.p12`; fake p12 canary (documents the deny-list-subset residual, §2.1).
- Environment canary: the daemon is launched with `CANARY_API_KEY=WARDEN-CANARY-ENVVAR` in **its** environment; assert this never appears in any sandbox `env` output (proves environment is cleared, BI-2).
- Marker path `$HOME/warden-hook-marker`; asserted absent after the hooks check (proves T-01).

Canary verification: the script computes sha256 of each file before and after; any change, or any appearance of a canary marker string in emitted events/artifacts/`env` output, fails the check. Canaries the harness created are cleaned up on exit (trap), real user files are never modified.

### 3.4 Output format and exit codes

Output is **TAP version 13** (chosen for CI legibility and native GitHub Actions support via a TAP reporter; JUnit XML is also emitted with `--format junit` for the CI test-summary UI, A17). One TAP test point per check row; `ok N - <check id>` / `not ok N - <check id>` with a YAML block on failure carrying the expected vs actual event and the host-state diff. Exit code `0` if all points pass, `1` if any fails, `2` if the environment is unusable (no sandbox backend → the check cannot run; distinct from a security failure so CI can tell "broken runner" from "escape"). The script prints a final `1..N` plan line.

### 3.5 CI matrix

Run on (A17 GitHub Actions):

| OS | Arch | Backend | Notes |
|---|---|---|---|
| macOS 14 | arm64 | Seatbelt | GitHub `macos-14` runner (Apple silicon); `sandbox-exec` available |
| macOS 15 | arm64 | Seatbelt | `macos-15` runner |
| Ubuntu 22.04 | x86_64 | bwrap + seccomp | `ubuntu-22.04`; userns enabled by default |
| Ubuntu 24.04 | x86_64 | bwrap + seccomp | `ubuntu-24.04`; check `kernel.apparmor_restrict_unprivileged_userns` (A17); the escape-check env-setup step relaxes it or the run reports exit 2 |
| Ubuntu 22.04 | arm64 | bwrap + seccomp | arm64 runner (self-hosted or `ubuntu-22.04-arm`) |
| Ubuntu 24.04 | arm64 | bwrap + seccomp | arm64 runner |

Each matrix leg builds `wardend`/`warden-exec` with `-tags escapecheck`, runs `scripts/escape-check.sh`, uploads the TAP+JUnit output, and fails the job on exit 1. Exit 2 (unusable env) fails the job with a distinct annotation so a misconfigured runner is not mistaken for a passing security check.

## Traceability

| Design element | WRD source (doc §) | Requirement / invariant satisfied |
|---|---|---|
| Failure-mode table | WRD-05 §4; WRD-02 §11; core §6 | error normalization; failure domains; fail-closed |
| Fail-closed (disk/sandbox/store) | WRD-02 §11; WRD-10 S-1 | no unsandboxed run; audit never skipped |
| Budget/approval/gate failure rows | WRD-06 §8; WRD-16 §8; CF-38 | H2 auditability; never trap user |
| Threats T-01..T-24 mapping | WRD-10 §8 | threat coverage with proof and residual risk |
| Deny-list-subset residual + p12 canary | WRD-16 §10.5; WRD-10 §6; CF-17 | INV-1 defense-in-depth; honest residual |
| Split vs co-located harness residual | CF-22, CF-23; WRD-05 §9 | T-13/T-14 mitigation and its limit |
| Harness hook timeout row (late `once`) | core ID-07 | BI-1 (abandoned call never executes) |
| Paused tier-bounded fallback row | core ID-16, ID-04; CF-44 | BI-7, T-22 |
| Accepted risk AR-1 (webview script approving via bridge) | WRD-10 §4 (UI untrusted), T-15; B05; B08 | BI-6; S-7 bounds the impact |
| Escape-check script | WRD-16 §10.7; WRD-10 §11 | H3; S-8 cancel; escape suite in CI |
| `debug.runScript` test-only method | WRD-16 §10.7 ("scripted sequence") | deterministic escape check; not shipped enabled |
| Host canaries, env clearing | WRD-16 §10.7; BI-2, BI-3 | secrets/home never in sandbox |
| CI matrix macOS 14/15, Ubuntu 22/24 x86_64+arm64 | WRD-16 §15 item 6; WRD-10 §11 | escape check passes on both OSes |

## Deviations and assumptions

- NEW `debug.runScript` JSON-RPC method and `escapecheck` build tag: the deterministic injection point for the escape check (§3.1), compiled out of release builds.
- DEV:strict-verify-exit: `audit verify --strict` failure exits 3 (§1 row 29); WRD-16 defines exit codes only for `warden run`.
- DEV:disk-full-pause: on store write failure the task pauses (`waiting_for_input`) rather than fails, so the user can free space and resume without losing the run (§1 row 14); WRD-02 says "task pauses".
- DEV:tap-output: escape-check emits TAP 13 (plus optional JUnit); WRD-16 §10.7 does not fix a format.
- ASM:clock-skew-warn: clock skew is a warning, not a failure (§1 row 30), because ordering is by `seq`/hash, not `ts` (WRD-09 §4).
- ASM:dirty-repo: default is to refuse opening a dirty repo (row 22); `--allow-dirty` opens off HEAD without touching the working tree. WRD-07 §10 leaves the choice to the user/runtime. Note: core ID-13 has `session.open` return `uncommitted` (count of changes left untouched), which suggests the session opens off HEAD and reports the count; if A05/A14 settle on that, row 22 becomes a warning ("3 uncommitted changes are not included") rather than a refusal.
- DEV:provider-outage-exit: row 2 exits 2 (paused, a human can unblock with `session.setPin`) rather than 3, consistent with ID-16.
- OQ (A16); whether co-located harnesses should be forced to L2 rather than merely recommended; recommended answer: force L2 when enabled. See OPEN-QUESTIONS.
