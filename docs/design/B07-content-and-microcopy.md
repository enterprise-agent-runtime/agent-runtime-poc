# B07 Content and microcopy

This file is the English message catalog of the Warden desktop app and the rules for writing new strings. Every user-visible string in B03, B04 and B05 comes from a key defined here. Messages use ICU MessageFormat (FormatJS / `react-intl`, B08 §9). The CLI prints its own terminal text but reuses the same wording for policy reasons, routing explanations and errors, so that a screenshot and a terminal transcript say the same thing (BI-6, WRD-11 §1.5).

## 1. Voice rules

| Rule | Do | Don't |
|---|---|---|
| Sentence case for everything: titles, buttons, menu items, column headers | "Review the plan" | "Review The Plan", "REVIEW PLAN" |
| No exclamation marks, no filler, no praise | "Tests passed: 43 of 43." | "Great news! All tests passed!" |
| The system is "the runtime" (mechanics) or "Warden" (the product); agents are named by their manifest name (`coder`, `verifier`). No first person, no anthropomorphism | "The runtime retried the call." "`coder` requested…" | "I think…", "Let me…", "The AI wants to…" |
| The user is "you"; actions are verbs | "Approve", "Push to origin" | "OK", "Yes", "Submit" |
| Numbers always carry units; durations in s, min, h; money with currency; tokens as "tokens" | "retrying in 20 s (2/3)", "0.42 USD" formatted by locale, "12,480 tokens" | "retrying in 20", "cost 0.42" |
| Identifiers in monospace, never paraphrased: rule ids, tool ids, model ids, provider ids, paths, commands, hosts, event seq, approval ids | "rule `user.package-install`" | "the package install rule" |
| Every error states **what happened**, **what the runtime did**, **what you can do** (WRD-11 §5), in that order, one sentence each where possible | "Rate limited by Anthropic. The runtime is retrying in 20 s (2/3). You can wait or pin another model." | "Error 429." |
| Say what is certain; no hedging, no blame | "The provider rejected the credential." | "Something went wrong, maybe your key?" |
| A request is a job, not a message | "Request", "Run", "Iterate" | "Chat", "Ask", "Conversation", "Reply" |
| Security language is plain and specific | "`coder` tried to read `.env`. Denied: files on the secret deny-list are never readable." | "Suspicious activity detected" |
| Avoid dashes as pauses; use a full stop, a semicolon or a middle dot (·) in compact lines | "Rule `user.git-push` · risk R5" | "Rule user.git-push; risk R5" |

Formatting conventions (applied by the ICU formatters, never hand-written):

- Counts: `{n, number}`; plurals with `{n, plural, one {# test} other {# tests}}`.
- Currency: `{usd, number, ::currency/USD}`. Local and company-hosted models show `cost.zero_local` rather than a bare zero.
- Durations: helper `formatDuration(ms)` produces "850 ms", "12 s", "4 min 10 s", "1 h 5 min" (keys `unit.*`); in ICU strings durations are passed pre-formatted as `{duration}`.
- Timestamps: relative for under 24 h ("3 min ago"), absolute otherwise; full ISO timestamp in the tooltip and in exports.
- Monospace segments use the rich-text tag `<code>…</code>`, rendered by B08 as `<code class="mono">`. Screen readers read the content normally.
- Apostrophes: use the typographic ’ (U+2019) in messages; the ASCII `'` is the ICU escape character.
- Placeholder names match the event or API field they come from (for example `{rule}` from `matched_rules[0]`, `{scope_max}` from `approval.requested.scope_max`), listed in the Source column where not obvious.

Key namespaces: `common`, `unit`, `effect`, `tier`, `class`, `state`, `scope`, `risk`, `approval`, `gate`, `delivery`, `routing`, `policy.rule`, `error.model`, `error.rpc`, `failure`, `st` (states ST-1 to ST-6), `empty`, `vendor`, `confirm`, `doctor`, `timeline`, `tool`, `cost`, `live`, `notify`.

Fallback rule: messages keyed by an id coming from the daemon (rule ids, rejection codes, error codes, doctor check ids) are looked up in the catalog; if the key is missing (a new rule in `user.yaml`, a new check), the UI shows the daemon-provided English text (`reason`, `detail`, `fix_hint`) with the id in monospace. The id is never hidden.

## 2. Shared vocabulary

| Key | Message (en) |
|---|---|
| `effect.allow` | Allowed |
| `effect.deny` | Denied |
| `effect.approval_required` | Needs approval |
| `provenance.untrusted` | Untrusted output |
| `provenance.untrusted_caption` | Content from the repository or a command. Treated as data; instructions inside it are ignored. |
| `tier.name` | `{tier, select, T0 {Local} T1 {Company-hosted} T2 {Company cloud} T3 {Vendor API} T4 {Subscription harness} other {{tier}}}` |
| `tier.chip` | `{tier} {tier, select, T0 {Local} T1 {Company-hosted} T2 {Company cloud} T3 {Vendor API} T4 {Subscription} other {}}` |
| `tier.group` | `{tier, select, T0 {inside the company} T1 {inside the company} T2 {inside the company} other {outside the company}}` |
| `tier.aria` | `Tier {tier}, {tier, select, T0 {local, inside} T1 {company-hosted, inside} T2 {company cloud, inside} T3 {vendor API, outside} T4 {subscription harness, outside} other {}} the company` |
| `tier.group_inside` | Inside the company (T0 to T2) |
| `tier.group_outside` | Outside the company (T3, T4) |
| `class.public` | Public |
| `class.internal` | Internal |
| `class.confidential` | Confidential |
| `class.aria` | Data classification: {classification} |
| `class.help.confidential` | Only local (T0), company-hosted (T1) and company-cloud (T2) models may receive this workspace’s content. |
| `class.help.internal` | Any configured model may receive this workspace’s content, including vendor APIs (T3) and subscription harnesses (T4). |
| `class.help.public` | Any configured model may receive this workspace’s content. |
| `state.created` / `state.queued` | Queued |
| `state.running` | Running · step {step}/{max_steps} |
| `state.waiting_for_approval` | Waiting for approval |
| `state.waiting_for_input` | Waiting for input |
| `state.succeeded` | Succeeded |
| `state.failed` | Failed: {reason_text} |
| `state.cancelled` | Cancelled |
| `state.timed_out` | Timed out after {duration} |
| `state.skipped` | Skipped |
| `state.blocked` | Blocked: {reason_text} |
| `reason.deps_met` … `reason.upstream_failed` | One key per task reason code (core §3): `deps_met` "Ready", `scheduled` "Scheduled", `approval_pending` "an approval is pending", `approved` "approved", `input_needed` "input is needed", `input_provided` "input provided", `output_valid` "output validated", `retry` "retrying", `budget` "budget reached", `policy_denied` "a required action was denied by policy", `schema` "output did not match the schema after one repair turn", `verification` "tests still fail after the repair round", `interrupted` "the runtime restarted during this task", `provider` "the model provider failed", `tool` "a tool failed", `resource` "a sandbox limit was hit (CPU, memory, processes or disk)", `timeout` "the time limit was reached", `cancelled` "cancelled by you", `rejected` "you rejected it", `approval_expired` "an approval expired after 24 h", `no_admissible_model` "no admissible model", `upstream_failed` "an earlier task failed" |
| `run.running` / `run.waiting` / `run.succeeded` / `run.failed` / `run.cancelled` | Running / Waiting for you / Delivered / Failed / Cancelled |
| `scope.once` | Once |
| `scope.task` | This task |
| `scope.session` | This session |
| `scope.workspace` | This workspace |
| `risk.R0` … `risk.R6` | R0 read · R1 write in the worktree · R2 profile command · R3 other command · R4 network or install · R5 affects the host or a remote · R6 forbidden |
| `sandbox.level` | `{level, select, L1 {L1 ({backend, select, seatbelt {Seatbelt} bwrap {bubblewrap} other {{backend}}})} L2 {L2 (Docker)} other {{level}}}` |
| `chain.verified` / `chain.unverified` / `chain.verifying` / `chain.failed` | Chain verified / Chain not verified yet / Verifying chain… / Chain verification failed |
| `capabilities.summary` | Agents can: {can, list, conjunction}. Need approval for: {needs, list, conjunction}. |
| `capabilities.can.*` | `read_write_repo` "read and write this repository", `profiles` "run build and test profiles" |
| `capabilities.needs.*` | `installs` "installs", `other_commands` "other commands", `network` "new network destinations", `push` "push" |
| `common.explain` | Explain |
| `common.details` | Details |
| `common.copy_id` | Copy id |
| `common.copied` | Copied |
| `common.cancel` | Cancel |
| `common.close` | Close |
| `common.retry` | Retry |
| `common.open_settings` | Open settings |
| `common.open_doctor` | Open doctor |
| `cost.zero_local` | 0.00 (local; infrastructure cost not tracked) |
| `cost.usd` | `{usd, number, ::currency/USD}` |
| `cost.quota` | `{units, plural, one {# premium request} other {# premium requests}}` |
| `cost.tokens` | `{input, number} in · {output, number} out{cached, plural, =0 {} other { · {cached, number} cached}}` |
| `cost.unknown` | Price unknown |
| `cost.budget_line` | {spent} of {limit} session budget |

## 3. Approval prompts (SCR-4, `ApprovalCard`)

Structure (WRD-11 §2.3, WRD-16 §13 screen 4): **What** (exact command, path, host or action), **Who** (agent and task), **Why** (rule reason, rule id, risk class, taint if any), **Scope** selector limited by `scope_max`, **Approve / Reject**, **Explain**. Sources: `approval.requested` (`display{what, who, why}`, `pattern`, `risk_class`, `scope_max`, `scopes_allowed[]`, `rule_ids[]`, `reason`, `expires_at`) and the preceding `policy.decision` (`action`, `obligations`). The UI renders its own sentences from the structured fields; `display.*` from the daemon is the fallback.

### 3.1 Common frame

| Key | Message (en) | Notes |
|---|---|---|
| `approval.title` | Approval needed | Card heading, `text-lg` |
| `approval.label.what` | What | Row label |
| `approval.label.who` | Requested by | |
| `approval.label.why` | Why it needs approval | |
| `approval.label.scope` | Remember this decision for | Scope selector legend (radiogroup) |
| `approval.who` | `<code>{agent}</code> in task <code>{task_key}</code>, step {step}` | `actor.name`, `task_key`, model step |
| `approval.why.rule` | Rule <code>{rule}</code> · {risk} | `rule_ids[0]`, `risk.{risk_class}` |
| `approval.why.taint` | This task has read untrusted content from {sources}. Only “Once” is offered. | Taint escalation (WRD-08 §4.5) |
| `approval.approve` | Approve | kbd hint `A` |
| `approval.reject` | Reject | kbd hint `R` |
| `approval.explain` | Explain this decision | Opens ExplainDrawer (`policy.explain`) |
| `approval.kbd_hint` | A approve · R reject · 1 to 4 scope · E explain | Footer hint, `text-xs` |
| `approval.expires` | Open until {time} (expires after 24 h) | From `expires_at` |
| `approval.pending_badge` | `{count, plural, one {# approval pending} other {# approvals pending}}` | PendingApprovalBadge |
| `approval.jump` | Go to pending approval (G then A) | Badge tooltip |

### 3.2 Scope options and explanations

Shown as four segments in order; segments above `scope_max` or not in `scopes_allowed` are disabled with the reason as their description. Default selection (core §15 ID-08): `once`, except approvals from `user.package-install`, which default to `workspace` when allowed (B09 §4: the main prompt-reduction lever). The consequence text of the selected scope is always visible before approving.

| Key | Message (en) |
|---|---|
| `scope.once.explain` | Only this call. The next identical request asks again. |
| `scope.task.explain` | Identical requests in task <code>{task_key}</code> are allowed until the task ends. |
| `scope.session.explain` | Identical requests are allowed until this session closes. |
| `scope.workspace.explain` | Identical requests are allowed in this workspace for you, in future sessions too. Stored in `~/.warden`, not in the repository. Revoke it any time in the session’s grants list. |
| `scope.pattern` | Applies to: <code>{pattern}</code> | `pattern.tool`, `pattern.operation`, `pattern.resource_pattern` |
| `scope.disabled.max` | Not available: rule <code>{rule}</code> allows at most “{scope_max}”. |
| `scope.disabled.agent` | Not available: agent <code>{agent}</code> allows only {allowed, list, disjunction}. |
| `scope.disabled.risk_r5` | Not available: actions that affect a remote are approved one at a time. |
| `scope.disabled.taint` | Not available: this task has read untrusted content. |

### 3.3 Variants

| Key | What | Why | Scope default / max | Extra line |
|---|---|---|---|---|
| **Install** (`user.package-install`, R4; demo `apr_9`) | `approval.what.install`: Run <code>{argv}</code> and allow network access to {hosts, list, conjunction} | `approval.why.install`: Installs download packages and run their install scripts inside the sandbox. Egress to <code>{host}</code> is not in the task allowlist. | `workspace` / `workspace` | `approval.extra.install`: Install scripts run inside the sandbox with no credentials and no access to your home directory. Hosts other than the registries stay blocked. |
| **Other command** (`user.other-commands` or `platform.r3-default`, R3) | `approval.what.command`: Run <code>{argv}</code> in the worktree | `approval.why.command`: Command is outside the approved profiles ({profiles, list, conjunction}). | `once` / `task` | `approval.extra.command`: It runs inside the sandbox with network access limited to the task allowlist. |
| **Egress host** (`user.egress-other`, R4) | `approval.what.egress`: Connect to <code>{host}:{port}</code> from task <code>{task_key}</code> | `approval.why.egress`: <code>{host}</code> is not in the task allowlist. The connection was started by <code>{process}</code>. | `once` / `session` | `approval.extra.egress_hold`: The connection is held for {seconds} s while you decide. After that it is refused; if you approve later, the next identical connection in this task within 10 min is allowed (or all of them, for a wider scope). |
| **Git push** (`user.git-push` / `platform.r5-default`, R5) | `approval.what.push`: Push branch <code>{branch}</code> to <code>{remote}</code> ({commits, plural, one {# commit} other {# commits}}) | `approval.why.push`: Pushing changes a remote repository. It runs on your machine with your git credentials. | `once` / `once` (fixed) | `approval.extra.push`: Scope is always “Once”. A push is never remembered. |
| **Tolerated harness session** (`user.harness-tolerated`, R4; Codex) | `approval.what.harness`: Start a <code>{harness_id}</code> session for task <code>{task_key}</code> | `approval.why.harness`: {harness_name} is used under terms the vendor tolerates without a guarantee. Its network access is limited to {hosts, list, conjunction}. | `once` / `session` | `approval.extra.harness`: The session runs in the sandbox with your {harness_name} login mounted read-only. Tool calls it makes are checked by policy one by one. |
| **Clarifying question** (`approval.requested` with `kind: question`, task `waiting_for_input`, ID-05) | `approval.what.question`: <code>{agent}</code> needs an answer to continue | `approval.why.question`: The request is ambiguous: {question} | none: answer field instead of the scope selector (§16.6) | `approval.answer_placeholder`: Type an answer. It is passed to the agent as your input. |
| **File** (`fs` action requiring approval; not produced by the PoC defaults, kept for completeness) | `approval.what.file`: {operation, select, write {Write} patch {Patch} read {Read} other {Access}} <code>{path}</code> | `approval.why.file`: {reason} | `once` / per rule | none |

### 3.4 Resolved, expired and revoked lines

| Key | Message (en) |
|---|---|
| `approval.resolved.approve` | Approved by you for {scope} · {time} |
| `approval.resolved.reject` | Rejected by you · {time}. <code>{agent}</code> was told the action was rejected and may try another way. |
| `approval.resolved.expire` | Expired after 24 h without a decision. Task <code>{task_key}</code> failed. |
| `approval.resolved.cancel` | Closed because task <code>{task_key}</code> was cancelled. |
| `approval.revoke` | Revoke |
| `approval.revoked` | Revoked · {time}. The next matching request asks again. |
| `approval.grant_used` | Allowed by your earlier approval <code>{approval_id}</code> ({scope}) |
| `approval.proxy_refused_pending` | The held connection to <code>{host}</code> was refused after {seconds} s. Approving now allows the next identical connection in this task within 10 min. |

### 3.5 OS notification (`notify.*`)

Sent only while the window is not focused (B05). No secrets: `argv` is already redacted.

| Key | Message (en) |
|---|---|
| `notify.approval.title` | Warden: approval needed |
| `notify.approval.body` | {what_short} · task {task_key} in {workspace} |
| `notify.gate.title` | `{gate_key, select, gate-plan {Warden: plan ready for review} gate-final {Warden: result ready for review} other {Warden: review needed}}` |
| `notify.gate.body` | {workspace} · {request_short} |
| `notify.failed.title` | Warden: run failed |
| `notify.failed.body` | {workspace} · {reason_text} |

## 4. Gates and delivery

| Key | Message (en) |
|---|---|
| `gate.g1.title` | Review the plan |
| `gate.g1.subtitle` | Nothing changes in the repository until you approve. |
| `gate.g1.approve` | Approve plan |
| `gate.g1.edit` | Edit plan |
| `gate.g1.save_edit` | Save and approve |
| `gate.g1.discard_edit` | Discard edits |
| `gate.g1.cancel` | Cancel run |
| `gate.g1.steps` | `{n, plural, one {# step} other {# steps}}` · `{files, plural, one {# expected file} other {# expected files}}` |
| `gate.g1.estimate` | Estimated {cost} · {steps, plural, one {# step} other {# steps}} |
| `gate.g1.model` | Planned on <code>{model_id}</code> ({tier_chip}) · {routing_line} |
| `gate.g1.risks` | Risks |
| `gate.g1.edited_note` | Edited by you. The original plan is kept as artifact <code>{artifact_id}</code>. |
| `gate.g1.invalid_edit` | The plan is not valid: {error}. Fix it or discard your edits. |
| `gate.g2.title` | Review the result |
| `gate.g2.subtitle` | Verified on the session branch. Accepting ends the run as succeeded; nothing reaches your repository until you choose a delivery action. |
| `gate.g2.accept` | Accept result |
| `gate.g2.runSucceeded` | Run succeeded. Choose how to deliver the change. |
| `gate.g2.tests` | Tests: {passed, number} passed, {failed, number} failed, {skipped, number} skipped |
| `gate.g2.repair_note` | One repair round ran: {analysis_short} |
| `gate.g2.iterate` | Iterate |
| `gate.g2.reject` | Discard result |
| `gate.failed_verification.title` | Verification failed after the repair round |
| `gate.failed_verification.body` | The run stopped without the final gate. The diff and the failing report are kept. You can export the patch or iterate with a new request. |
| `delivery.apply_branch` | Apply to branch |
| `delivery.commit` | Commit to session branch |
| `delivery.push` | Push |
| `delivery.export_patch` | Export patch |
| `delivery.push_needs_approval` | Push always asks for approval, once. |
| `delivery.unavailable_failed` | Not available: verification did not pass. |
| `delivery.done.apply_branch` | Branch <code>{branch}</code> created in your repository at <code>{commit_short}</code>. |
| `delivery.done.commit` | Committed <code>{commit_short}</code> and published branch <code>{branch}</code> in your repository. |
| `delivery.done.push` | Pushed <code>{branch}</code> to <code>{remote}</code>. |
| `delivery.done.export_patch` | Patch saved to <code>{path}</code>. |
| `delivery.push_rejected` | Push rejected by you. The commit stays on the session branch. |
| `resume.action` | Resume from last gate |
| `resume.explain` | Starts a new run from the approved plan (G1). The partial changes of the cancelled run stay on <code>{ref}</code>. |

## 5. Routing explanations (`RoutingLine`, `ModelPicker`, `ExplainDrawer`)

Source: `routing.decision` (`strategy`, `classification`, `candidates[]`, `chosen`, `pin`, `explanation`, `budget_remaining_usd`), `routing.fallback`, `provider.models` (`admissible`, `reason_code`, `reason`).

| Key | Message (en) |
|---|---|
| `routing.chosen` | Chosen: {model} ({strategy}; {classification} data; {n, plural, one {# candidate} other {# candidates}}) |
| `routing.pinned` | Pinned: {model} ({classification} data; a pin overrides ranking, never admission) |
| `routing.pinned_fallback` | Pinned {pinned} failed ({cause}); now on {model}, same or lower tier |
| `routing.fallback` | Fell back from {from} ({from_tier}) to {to} ({to_tier}) after {cause_text}. Fallback never leaves the admitted tiers. |
| `routing.fallback_none` | No fallback within tier {tier} or lower. The task is waiting for input. |
| `routing.fallback_limit` | Three fallbacks in this task. The runtime stopped the task; it is waiting for input. |
| `routing.strategy.prefer-internal` | prefer-internal |
| `routing.strategy.quality-first` | quality-first |
| `routing.strategy.cost-first` | cost-first |
| `routing.strategy.latency-first` | latency-first |
| `routing.strategy.explain.prefer-internal` | Local and company-hosted models come first when their quality prior for {task_class} is at least {threshold}. |
| `routing.strategy.explain.cost-first` | The cheapest admissible model comes first; known-zero cost before priced, quota-billed last. |
| `routing.strategy.explain.quality-first` | The model with the highest quality prior for {task_class} comes first. |
| `routing.strategy.explain.latency-first` | The model with the lowest median latency comes first. |
| `routing.candidate.chosen` | Chosen |
| `routing.candidate.admitted` | Admissible · ranked {rank, selectordinal, one {#st} two {#nd} few {#rd} other {#th}} |
| `routing.candidate.rejected` | Not admissible |
| `routing.candidate.unhealthy` | Unavailable |
| `routing.prior` | Quality prior for {task_class}: {prior, number, ::.00} |
| `routing.below_threshold` | Quality prior {prior} for {task_class} is below {threshold}; ranked after models that meet it. |
| `routing.est_cost` | Estimated {cost} for this call |
| `routing.budget_remaining` | {remaining} left in the session budget |
| `routing.picker.label` | Model for this session |
| `routing.picker.auto` | Automatic (routing policy) |
| `routing.picker.clear_pin` | Clear pin |
| `routing.picker.hint` | Only admissible models can be pinned. Others show why. |
| `routing.picker.harness_hint` | Subscription harnesses run only when pinned. |

Per rejection code (core §3). These lines appear under an inadmissible model in ModelPicker and in the routing details table.

| Key (`routing.reject.<code>`) | Message (en) |
|---|---|
| `tier_not_admitted` | Not admissible for {classification} data: {tier} {tierName} |
| `tier_not_admitted.detail` | {classification} data may go only to {allowed, list, conjunction}. This is set by the routing policy and cannot be overridden by a pin. |
| `capability_missing` | Missing capability required by <code>{agent}</code>: {capability, select, tool_calling {tool calling} structured_output {structured output} streaming {streaming} other {{capability}}} |
| `context_too_small` | Context window {max_context, number} tokens; <code>{agent}</code> needs at least {min_context, number} |
| `denied_by_policy` | Excluded by policy rule <code>{rule}</code> |
| `over_budget` | Estimated {est_cost} exceeds the {remaining} left in the session budget |
| `circuit_open` | Temporarily unavailable after repeated failures; next attempt in {seconds} s |
| `harness_not_pinned` | Harnesses run only when pinned. Pin <code>{model_id}</code> to use it. |
| `harness_disabled` | Disabled. Enable <code>{provider_id}</code> in Settings to use it. |
| `harness_locked_shared_mode` | Locked in shared mode: <code>{provider_id}</code> is for personal use on this machine only |
| `provider_unconfigured` | Provider <code>{provider_id}</code> is not configured |
| `credential_missing` | No credential for <code>{provider_id}</code> in the keychain |

Examples (demo, core §12): internal workspace, plan task: "Chosen: local/qwen-coder-32b (prefer-internal; internal data; 3 candidates)". After switching to confidential, under `anthropic/claude-sonnet`: "Not admissible for confidential data: T3 Vendor API"; under `copilot`: "Not admissible for confidential data: T4 Subscription harness".

## 6. Policy reasons per rule id (`policy.rule.<id>`)

Shown in ToolCallRow detail, ApprovalCard "Why", ExplainDrawer and denial notices. Format in a row: `{reason}` then "Rule `{id}`". When several rules matched, the deciding rule (deny first, then approval) is named first and the others are listed in ExplainDrawer.

| Rule id | Effect | Message (en) |
|---|---|---|
| `invariant.INV-1` | deny | <code>{path}</code> is on the secret deny-list. Files like this are never readable or writable by agents. |
| `invariant.INV-2` | deny | Writing outside the task worktree is not allowed. Target: <code>{path}</code>. |
| `invariant.INV-3` | deny | Git hooks never run, and <code>.git/config</code>, <code>.git/hooks</code> and <code>.gitmodules</code> cannot be changed by agents. |
| `invariant.INV-4` | deny | Processes requested by an agent run only inside the sandbox. |
| `invariant.INV-5` | deny | Network access goes only through the runtime’s proxy; a wildcard allowlist is not allowed for {classification} data. |
| `invariant.INV-6` | deny | Secret values cannot be passed to the sandbox as arguments or environment variables. |
| `invariant.INV-7` | deny | <code>{harness_id}</code> has vendor terms “prohibited” and cannot be enabled. |
| `invariant.INV-8` | deny | In non-interactive mode an approval cannot be granted without a pre-recorded grant. |
| `invariant.INV-9` | deny | The task reached its manifest limit ({limit}). Policy can lower limits, never raise them. |
| `capability.not_granted` | deny | <code>{agent}</code> is not granted <code>{tool}</code> on this resource{mode, select, plan { in plan mode} summarize { in summarize mode} other {}}. |
| `capability.granted` | allow | Within <code>{agent}</code>’s granted capabilities. |
| `platform.no-shell-strings` | deny | Shell command strings (<code>{shell} -c …</code>) are not allowed. Commands must be given as an argument list. |
| `platform.protected-branches` | deny | Commits to <code>{branch}</code> are not allowed. Changes go to the session branch. |
| `platform.r3-default` | approval | Command is outside the approved profiles. |
| `platform.r4-default` | approval | This action reaches the network or installs packages. |
| `platform.r5-default` | approval | This action affects a remote or your machine outside the sandbox. |
| `platform.harness-egress-only` | deny | Harness tasks can reach only {harness_name} endpoints. <code>{host}</code> is not one of them. |
| `platform.harness-prohibited` | deny | <code>{harness_id}</code> is marked prohibited by its vendor terms. |
| `platform.personal-mode-lock` | deny | <code>claude-code</code> with your personal login is locked because Warden runs in shared mode. |
| `user.reads-in-worktree` | allow | Reading inside the worktree. |
| `user.writes-in-worktree` | allow | Writing inside the worktree. |
| `user.profile-commands` | allow | Command matches profile <code>{profile}</code>. |
| `user.package-install` | approval | Installing packages needs network access to package registries. |
| `user.other-commands` | approval | Command is outside the approved profiles. |
| `user.egress-other` | approval | <code>{host}:{port}</code> is not in the task allowlist. |
| `user.git-commit-session-branch` | allow | Committing on the session branch <code>{branch}</code>. |
| `user.git-push` | approval | Pushing changes a remote repository. |
| `user.harness-tolerated` | approval | <code>{harness_id}</code> runs under terms the vendor tolerates without a guarantee. |
| `grant.<approval_id>` (`policy.rule.grant`) | allow | Allowed by your approval <code>{approval_id}</code> ({scope}), given {time}. |
| `policy.rule.unknown` | any | {reason} (rule <code>{rule}</code>) |

Denial notice in the timeline (`policy.denied_row`): "Denied: {reason} Rule <code>{rule}</code>." Model feedback text is produced by the daemon (A10), not by this catalog.

Three-layer denial (S2, WRD-16 §3 step 8) (`policy.deny_layers`): "Blocked at three layers: policy (<code>{rule}</code>), executor path check, and sandbox mount (file not present)."

## 7. Errors

### 7.1 Model errors (WRD-05 §4; source `model.call.end.error`, `routing.fallback.cause`)

Each code has up to three variants: `.retrying` (the runtime retries), `.fallback` (switched model), `.stopped` (task waits or fails). Placeholders: `{provider}` display name, `{attempt}`/`{max}` retry counter, `{seconds}` wait.

| Key (`error.model.<code>`) | What happened · what the runtime did · what you can do |
|---|---|
| `rate_limited.retrying` | Rate limited by {provider}. Retrying in {seconds} s ({attempt}/{max}). |
| `rate_limited.fallback` | Rate limited by {provider} after {max} retries. Switched to <code>{to}</code> ({to_tier}). |
| `rate_limited.stopped` | Rate limited by {provider} after {max} retries, and no other model is admissible. The task is waiting. Retry later or pin another model. |
| `auth_failed.stopped` | {provider} rejected the credential. The runtime stopped calling it. Update the key in Settings, or unlock your keychain if it is locked. |
| `context_too_long.retrying` | The request exceeded the context window of <code>{model}</code> ({max_context, number} tokens). The runtime compacted older steps and is retrying. |
| `context_too_long.stopped` | The request is still too large for <code>{model}</code> after compaction. The task failed. Pin a model with a larger context window or narrow the request. |
| `provider_unavailable.retrying` | {provider} is unavailable. Retrying in {seconds} s ({attempt}/{max}). |
| `provider_unavailable.fallback` | {provider} is unavailable. Switched to <code>{to}</code> ({to_tier}) within the admitted tiers. |
| `provider_unavailable.stopped` | {provider} is unavailable and no other admissible model is configured. The task is waiting. Check the provider in Settings or retry. |
| `content_filtered.stopped` | {provider} filtered the response. The runtime does not retry filtered output. Rephrase the request or pin another model. |
| `invalid_request.stopped` | {provider} rejected the request as invalid. The task failed. This usually indicates an adapter problem; the provider’s message is in event <code>#{seq}</code>. |
| `tool_format_unsupported.fallback` | <code>{model}</code> did not accept native tool definitions. The runtime switched to emulated tool calling for this model and continued. |
| `model_not_found.fallback` | {provider} does not recognise model <code>{model_name}</code>. Switched to <code>{to}</code>. Check the model id in <code>models.yaml</code> or run a provider test. |
| `model_not_found.stopped` | {provider} does not recognise model <code>{model_name}</code>. The task is waiting. Check the model id or pin another model. |
| `timeout.retrying` | {provider} did not respond within {seconds} s. Retrying ({attempt}/{max}). |
| `timeout.fallback` | {provider} timed out {max} times. Switched to <code>{to}</code> ({to_tier}). |
| `cancelled` | Model call stopped because the task was cancelled. |
| `error.model.cause.<code>` | Short causes for `routing.fallback`: `rate_limited` "rate limiting", `provider_unavailable` "provider unavailable", `timeout` "timeout", `model_not_found` "unknown model" |

Actions on model error cards: `common.retry` ("Retry"), `error.action.pin_other` ("Pin another model"), `error.action.provider_settings` ("Open provider settings").

### 7.2 Runtime API errors (core §6; source JSON-RPC `error.data.code`)

| Key (`error.rpc.<code>`) | Message (en) |
|---|---|
| `unauthorized` (-32001) | This window could not authenticate to the runtime. Warden is reconnecting. If this repeats, restart Warden. |
| `not_found` (-32002) | <code>{id}</code> no longer exists. It may have been purged or belong to another session. The view was refreshed. |
| `invalid_state` (-32003) | This action is no longer possible: {detail}. The view was refreshed to show the current state. |
| `policy_denied` (-32004) | Refused by policy: {detail}. Rule <code>{rule}</code>. |
| `confirmation_required` (-32005) | This action needs your confirmation. |
| `sandbox_unavailable` (-32006) | The sandbox is not available, so no task can start. Warden never runs agent commands without a sandbox. Open Doctor for the fix. |
| `no_admissible_model` (-32007) | No model is admissible for {classification} data with the current settings. See the options in the session view. |
| `budget_exhausted` (-32008) | The {scope, select, session {session} daily {daily} other {}} budget of {limit} is used up. The run is paused. |
| `store_unavailable` (-32009) | The runtime could not write to its event store. It paused all tasks rather than continue without an audit record. |
| `unsupported_in_poc` (-32010) | {feature, select, restricted {The “restricted” classification is not supported in this version. Use “confidential”.} other {This feature is not supported in this version.}} |
| `protocol_mismatch` (-32011) | This app (protocol {client}) and the runtime (protocol {daemon}) are not compatible. Update Warden so both match. |
| `vendor_terms` (-32012) | {detail} Vendor terms for <code>{provider_id}</code>: {terms}. |
| `parse_error` (-32700) / `invalid_request` (-32600) / `method_not_found` (-32601) / `invalid_params` (-32602) / `internal_error` (-32603) | The runtime could not process a request from the app ({code}). The app and runtime versions may differ. Details are in <code>~/.warden/logs/wardend.log</code>. |

### 7.3 Failure modes (A16; banners and cards)

| Key (`failure.<id>`) | Surface | Message (en) | Actions |
|---|---|---|---|
| `sandbox_unavailable` | ST-2 blocking banner | The sandbox backend is not ready: {check_title}. No task can start until it is fixed; Warden has no unsandboxed mode. | "Show fix" (Doctor), "Run checks again" |
| `keychain_locked` | Banner + provider card | The keychain is locked, so provider credentials cannot be read. Calls to {providers, list, conjunction} are paused. Unlock your keychain; Warden retries automatically. | "Retry now" |
| `keychain_unavailable` | Doctor fail + ST-1 | No keychain service was found. API keys and tokens cannot be stored. {os, select, linux {Install and start a Secret Service provider such as GNOME Keyring or KWallet.} other {Check that the login keychain exists.}} | "Open doctor" |
| `store_unavailable` | Red banner, all tasks paused | The event store cannot be written ({detail}). All tasks are paused so that nothing happens without an audit record. Free disk space or check permissions on <code>~/.warden/db</code>, then resume. | "Run doctor", "Resume tasks" |
| `disk_full` | Same banner | The disk holding <code>~/.warden</code> is full ({free} free). Tasks are paused. Free space, then resume. | "Resume tasks" |
| `daemon_disconnected` | Header status + banner after 3 s | Connection to the runtime lost. Reconnecting ({attempt}); running tasks continue in the runtime. | none while retrying |
| `daemon_reconnected` | Transient status (live region only) | Reconnected. Timeline caught up from event #{seq}. | |
| `daemon_unreachable` | Banner after 30 s | The runtime is not responding. Tasks may have stopped. Restart the runtime; sessions resume from their last recorded state. | "Restart runtime", "Copy log path" |
| `daemon_restarted` | Timeline entry | The runtime restarted. {n, plural, one {# task was} other {# tasks were}} interrupted and {requeued, plural, =0 {none were re-queued} one {# was re-queued} other {# were re-queued}}. Open gates are still open. | |
| `harness_login_expired` | Task card + provider card | {harness_name} is not logged in on this machine (its login expired or was removed). The task is waiting. Run <code>{login_command}</code> in a terminal, then retry. | "Retry", "Pin another model" |
| `no_admissible_model` | ST-3 (§8) | See `st.3.*` | |
| `budget_exhausted` | ST-4 (§8) | See `st.4.*` | |
| `chain_failed` | ST-6 (§8) | See `st.6.*` | |
| `sandbox_violation` | Timeline row (effect deny style) | The sandbox blocked {kind, select, path_escape {a path outside the allowed roots} deny_list {access to a deny-listed file} root_check {a path outside the worktree} seatbelt_deny {an operation the sandbox profile forbids} seccomp {a forbidden system call} other {an operation}}: <code>{detail}</code>. | "Explain" |
| `proxy_denied` | ToolCallRow child | Network access to <code>{host}:{port}</code> refused: {reason}. | "Explain" |
| `cancel_slow` | Task card | Stopping is taking longer than expected ({seconds} s). The runtime is force-stopping sandbox processes. | |
| `gate_expired` | Gate card | This gate expired after 24 h without a decision. Resume from the last gate to continue. | "Resume from last gate" |

## 8. Screen states ST-1 to ST-6

| Key | Message (en) |
|---|---|
| `st.1.title` | Connect a model to start |
| `st.1.body` | Warden runs agents with models you already have. Choose one way to connect; you can add more later. |
| `st.1.path.api_key` | API key · Anthropic or OpenAI. Stored in your OS keychain. |
| `st.1.path.local` | Local model · Ollama or LM Studio on this machine. Nothing leaves your computer. |
| `st.1.path.company` | Company-hosted endpoint · a model your company runs (vLLM, Ollama, gateway), with a token or a client certificate. |
| `st.1.path.harness` | Subscription harness · GitHub Copilot through its official SDK. Codex and Claude Code are optional. |
| `st.1.detect_local` | Detecting local servers… |
| `st.1.detected` | Found {name} at <code>{url}</code> with {n, plural, one {# model} other {# models}}. |
| `st.1.none_detected` | No local server found on ports 11434 or 1234. Start Ollama or LM Studio, then detect again. |
| `st.2.title` | Sandbox not ready |
| `st.2.body` | Agents can run only inside a sandbox, and {failed, plural, one {# prerequisite is} other {# prerequisites are}} missing. There is no unsandboxed mode. |
| `st.2.action` | Show how to fix |
| `st.3.title` | No admissible model for {classification} data |
| `st.3.body` | {classification} data may go only to {allowed, list, conjunction}. Configured models are {configured, list, conjunction}. Task <code>{task_key}</code> is waiting. |
| `st.3.action.add_company` | Add a company-hosted model |
| `st.3.action.add_local` | Add a local model |
| `st.3.action.change_class` | Change classification |
| `st.3.note` | A pin cannot make a model admissible. |
| `st.4.title` | Session budget reached |
| `st.4.body` | This session has spent {spent} of its {limit} budget. The current task stopped with a checkpoint; the run is paused. |
| `st.4.daily` | Daily budget: {spent_daily} of {limit_daily}. |
| `st.4.action.raise` | Raise session budget |
| `st.4.action.local` | Continue on a local model (no cost) |
| `st.4.max_note` | Policy allows up to {max} per session. |
| `st.5.title` | Run cancelled |
| `st.5.body` | Cancelled at {time} during task <code>{task_key}</code>. All sandbox processes stopped in {duration}. Partial changes are kept on <code>{ref}</code>. |
| `st.5.action.resume` | Resume from last gate |
| `st.5.action.new` | New request |
| `st.6.title` | Chain verification failed |
| `st.6.body` | The audit trail of this session does not verify: {violations, plural, one {# problem} other {# problems}}, first at event #{seq} ({kind}). Events after that point cannot be trusted as a record. |
| `st.6.export_warning` | You can still export the session; the export is marked as failing verification. |
| `st.6.action.details` | Show problems |
| `st.6.action.export` | Export anyway |
| `st.6.kind.<kind>` (A05 `audit.verify` violation kinds) | `hash_mismatch` "event content does not match its hash", `prev_hash_mismatch` "link to the previous event is broken", `seq_not_increasing` "events out of order", `chain_mismatch` "event recorded on the wrong chain", `unknown_event_type` "unknown event type", `payload_invalid` "event payload is invalid", `projection_mismatch` "stored state disagrees with the events", `blob_missing` "stored content is missing", `blob_mismatch` "stored content does not match its hash", `artifact_record_mismatch` "artifact record disagrees with its content", `checkpoint_mismatch` "checkpoint does not match the chain", `checkpoint_signature_invalid` "checkpoint signature is invalid", `checkpoint_unsigned` "checkpoint is not signed" (warning), `unknown_key` "checkpoint signed with an unknown key", `anchor_missing` "session is missing from the system chain", `anchor_mismatch` "system chain disagrees with the session", `missing_decision` "a tool ran without a preceding decision", `decision_not_allow` "a tool ran after a decision that was not allow", `missing_approval` "a cited approval was not found", `approval_scope_mismatch` "a grant was used outside its scope", `orphan_exec_end` "a tool end has no start", `external_grant` "a grant from outside this export was used" (warning) |

## 9. Empty states

| Key | Where | Message (en) | Action |
|---|---|---|---|
| `empty.workspaces` | SCR-1 | No workspaces yet. Open a git repository to start. Warden works on a session branch and never changes your checkout until you deliver. | "Open repository" |
| `empty.workspaces.not_git` | SCR-1 open dialog error | <code>{path}</code> is not a git repository. Open a directory that contains a <code>.git</code> folder. | |
| `empty.sessions` | Workspace row detail | No sessions in this workspace. | "New session" |
| `empty.timeline` | SCR-2 | Describe a change for this repository. Warden plans it, asks you to approve the plan, implements and verifies it on a session branch. | Focus composer |
| `empty.timeline.example` | Composer placeholder | For example: Add a GET /users/:id endpoint returning the user or 404, with tests. | |
| `empty.context_panel` | SCR-2 right panel | Select an entry in the timeline to see its details, evidence and provenance. | |
| `empty.approvals` | Grants list | No approvals in this session. | |
| `empty.grants` | Settings grants | No remembered approvals for this workspace. | |
| `empty.plan_failed` | SCR-3 when plan failed | No plan was produced: {reason_text}. | "Retry", "Pin another model" |
| `empty.diff` | SCR-5 | The run finished without changing any files. | "Iterate" |
| `empty.test_report` | SCR-5 | No tests were detected for this workspace ({stack}). Verification ran the build profile only. | |
| `empty.cost` | Cost panel | No model calls yet. | |
| `empty.providers` | SCR-6 | No providers configured. | "Add provider" |
| `empty.models` | SCR-6 model list | This provider reported no models. Run a test to probe it again. | "Test" |
| `empty.harnesses` | SCR-6 | No harnesses detected. Install the GitHub Copilot CLI and log in to use your subscription. | |
| `empty.doctor` | SCR-7 before first run | Checks have not run yet. | "Run checks" |
| `empty.audit` | SCR-7 | Choose a session to verify or export. | |
| `empty.event_log` | SCR-7 / event log | No events match these filters. | "Clear filters" |
| `empty.artifacts` | Artifact list | No artifacts yet. Artifacts appear as tasks complete. | |

## 10. Vendor-terms notices (ProviderCard, enable dialog, session start)

| Key | Message (en) |
|---|---|
| `vendor.permitted.badge` | Vendor terms: permitted |
| `vendor.copilot.notice` | GitHub permits using your Copilot subscription through the official Copilot SDK. Warden starts the Copilot CLI inside the sandbox; usage counts as premium requests on your plan. Network access is limited to GitHub endpoints, and every tool call is checked by policy. |
| `vendor.tolerated.badge` | Vendor terms: tolerated |
| `vendor.codex.notice` | OpenAI tolerates using a ChatGPT plan login with the Codex app-server but does not guarantee it. Warden asks for your approval each time a Codex session starts. With an API key instead, usage is billed to that key. |
| `vendor.codex.enable_ack` | I understand that this use is tolerated, not guaranteed, and may stop working if the vendor changes its terms. |
| `vendor.personal.badge` | Vendor terms: personal use only |
| `vendor.claude_code.notice` | Claude Code with your own login is for your personal use on this machine only. Warden runs the unmodified official CLI in headless mode. It is locked in shared builds, where the Anthropic API key provider is used instead. |
| `vendor.claude_code.enable_ack` | I will use this only myself, on this machine, and not in a build shared with anyone. |
| `vendor.claude_code.locked` | Locked: Warden runs in shared mode, where Claude Code is available only with API-key billing. |
| `vendor.prohibited.badge` | Vendor terms: prohibited |
| `vendor.prohibited.notice` | The vendor does not allow this use. It cannot be enabled. |
| `vendor.session_start.codex` | Starting a Codex session needs your approval (terms: tolerated). |
| `vendor.terms_date` | Terms as recorded on {date}. Vendors change their terms; check before relying on a subscription harness. |

## 11. Confirmations

Confirmation dialogs name the object and the consequence; the confirm button repeats the verb. `Enter` confirms only when focus is on the confirm button; `Esc` cancels (B05).

| Key prefix | Title | Body | Confirm / Cancel |
|---|---|---|---|
| `confirm.commit` | Commit and publish the branch | One commit with {files, plural, one {# file} other {# files}} (+{add, number} −{del, number}) on the session branch, published to your repository as <code>{branch_name}</code> [input, default `warden/<ulid>`]. Your checked-out branch and working tree are not changed and no hooks run. Message: [input, prefilled with the plan summary] | "Commit" / "Cancel" |
| `confirm.apply_branch` | Apply to a branch in your repository | Creates branch <code>{branch_name}</code> in <code>{repo}</code> from the session’s changes. Your current checkout is not touched and no hooks run. [input: branch name, prefilled `warden/{short}`] | "Create branch" / "Cancel" |
| `confirm.apply_branch.exists` | | Branch <code>{branch_name}</code> already exists. Choose another name. | |
| `confirm.push` | Push to <code>{remote}</code> | Pushes <code>{branch}</code> ({commits, plural, one {# commit} other {# commits}}) to <code>{remote_url}</code> using your git credentials. This leaves your machine. Next you approve it once in the approval card. Available after Commit or Apply to branch (`delivery.push.needsCommit`). | "Continue to approval" / "Cancel" |
| `confirm.export_patch` | Export patch | Writes a patch with {files, plural, one {# file} other {# files}} to <code>{path}</code>. | "Export" / "Cancel" |
| `confirm.class_loosen` | Change classification to {to}? | Content of <code>{workspace}</code> may then be sent to {added_tiers, list, conjunction} models ({example_models, list, conjunction}). Tasks already running keep their model until they finish; new tasks use the new rules. This change is recorded in the audit trail. | "Change to {to}" / "Keep {from}" |
| `confirm.class_tighten.note` | (no dialog) | Classification is now {to}. {affected, plural, =0 {} one {# running task will pause at its next step if its model is no longer admissible.} other {# running tasks will pause at their next step if their model is no longer admissible.}} | |
| `confirm.provider_remove` | Remove provider <code>{provider_id}</code>? | Its credential is deleted from the keychain and its models disappear from routing. {pinned, plural, =0 {} other {# sessions pinned to it will fall back to automatic routing.}} Past events keep their records. | "Remove provider" / "Cancel" |
| `confirm.budget_raise` | Raise the session budget | From {from} to [input] for this session only. Policy allows up to {max}. The daily budget of {daily} still applies. | "Raise to {to}" / "Cancel" |
| `confirm.budget_raise.over_max` | | {to} is above the {max} policy maximum. | |
| `confirm.policy_reload` | Reload policy | Reloads <code>{path}</code>. New rules apply to the next decision; remembered approvals stay valid. | "Reload" / "Cancel" |
| `confirm.shutdown` | Stop the runtime | {running, plural, =0 {No tasks are running.} one {# running task will be interrupted.} other {# running tasks will be interrupted.}} Open gates stay open and resume when the runtime starts. | "Stop runtime" / "Cancel" |
| `confirm.discard_plan_edit` | Discard your plan edits? | The original plan stays as it was. | "Discard" / "Keep editing" |
| `confirm.harness_enable` | Enable <code>{harness_id}</code> | [vendor notice from §10] [checkbox with the `*_enable_ack` text when terms are tolerated or personal use only] | "Enable" (disabled until acknowledged) / "Cancel" |

Cancel of a running task has no confirmation dialog (never trap the user, WRD-11 §1.4); the cancel button label is `cancel.action` "Cancel run" and the result is ST-5.

## 12. Doctor checks (`SCR-7`, `DoctorChecklist`; source `system.doctor.checks[]`)

Check ids and groups are those of A05 §8.1 (authoritative). Each id has a title, a fail or warn text and a fix hint; `{detail}` is the daemon's `detail` string and is appended when present. OS-specific text uses `{os, select, darwin {…} linux {…} other {…}}` with `os` from `bridge_status` (B08 §6.4).

| Check id (A05) | Title (`doctor.<id>.title`) | Fail or warn text (`.fail` / `.warn`) | Fix hint (`.fix`) |
|---|---|---|---|
| `sandbox.backend` | Sandbox backend | {os, select, darwin {<code>sandbox-exec</code> is not available.} other {bubblewrap (<code>bwrap</code> 0.8 or later) is not installed.}} Blocking. | {os, select, darwin {<code>sandbox-exec</code> ships with macOS; check that <code>/usr/bin/sandbox-exec</code> exists and is not blocked by a management profile.} other {Install it: <code>sudo apt install bubblewrap</code> (Debian, Ubuntu) or <code>sudo dnf install bubblewrap</code> (Fedora), then run checks again.}} |
| `sandbox.userns` (Linux) | Unprivileged user namespaces | Unprivileged user namespaces are disabled, so bubblewrap cannot create a sandbox. Blocking. | On Ubuntu 23.10 or later, allow them for bubblewrap with an AppArmor profile granting <code>userns</code> to <code>/usr/bin/bwrap</code> (see the setup guide), or set <code>kernel.apparmor_restrict_unprivileged_userns=0</code>. On Debian: <code>sudo sysctl kernel.unprivileged_userns_clone=1</code>. Check that <code>user.max_user_namespaces</code> is above 0. |
| `sandbox.seccomp` (Linux) | Seccomp filter | The kernel rejected the seccomp filter. Blocking. | Use a kernel with seccomp BPF (<code>CONFIG_SECCOMP_FILTER=y</code>); most distribution kernels have it. |
| `sandbox.probe` | Sandbox probe | {os, select, darwin {The generated Seatbelt profile did not compile, or the probe sandbox could not start.} other {The probe sandbox could not start.}} Blocking. | {os, select, darwin {This usually means a toolchain path the profile does not know. Run <code>warden doctor</code> in a terminal for the full profile error and report it. As a workaround, enable L2 (Docker) if installed.} other {Check the messages above for bubblewrap and user namespaces; if they pass, report the detail shown here.}} |
| `sandbox.l2` | L2 containers (optional) | Rootless Docker is not available; L2 is off. Warning only. | Install Docker (rootless recommended) if you want L2. L1 is enough for all PoC tasks. |
| `exec.binary` | Sandbox executor | <code>warden-exec</code> is missing, not executable, or a different version from the runtime. Blocking. | Reinstall Warden; the executor ships with the app. |
| `keychain` | OS keychain | {os, select, darwin {The login keychain is locked or unavailable.} other {No Secret Service provider is running.}} Blocking. | {os, select, darwin {Unlock the login keychain (Keychain Access) and run checks again.} other {Install and start GNOME Keyring or KWallet, then run checks again.}} |
| `checkpoint.key` | Checkpoint signing key | The key that signs audit checkpoints is not loaded; checkpoints are written unsigned. Warning. | Unlock the keychain; the key is created in the keychain on first use. |
| `store` | Event store | The store at <code>~/.warden/db</code> is not usable (migrations, integrity check or permissions). Blocking. | Check that <code>~/.warden</code> belongs to you and has mode 0700; free disk space; then run checks again. |
| `disk` | Disk space | {free} free on the volume holding <code>~/.warden</code>. {level, select, warn {Below 512 MiB; warning.} fail {Below 128 MiB; tasks cannot run.} other {}} | Free space, or remove old sessions. |
| `policy` | Policy files | The platform or user policy did not compile. Blocking. | Fix the rule named in the detail in <code>~/.warden/policy/user.yaml</code>, then reload policy. |
| `catalog` | Model catalog | <code>~/.warden/models.yaml</code> does not parse or validate. Blocking. | Fix the entry named in the detail, or re-add the provider in Settings. |
| `providers` | Model providers | No provider or harness is enabled with a credential. | Add one in Settings: API key, local model, company-hosted endpoint or subscription harness. |
| `providers.local` | Local model servers | {found, plural, =0 {No local server found on ports 11434 or 1234.} one {Found # local server.} other {Found # local servers.}} | Start Ollama or LM Studio to use local models; optional. |
| `git` | Git | Git {version} found; version 2.38 or later is required. Blocking. | Install or update git. |
| `toolchain.node`, `toolchain.go`, `toolchain.python` | Toolchain: {name} | {name} was not found in a path the sandbox can mount. Tasks for this stack will fail. Warning. | Install {name}, or add its directory to the toolchain paths in <code>config.yaml</code>. |
| `socket` (desktop only, not from `system.doctor`) | Runtime socket | The socket <code>~/.warden/run/wardend.sock</code> or the token file has unsafe permissions ({mode}); the app refused to connect. | Restart the runtime; it recreates both with mode 0600. |

Group titles (A05 groups): `doctor.group.sandbox` "Sandbox", `doctor.group.keychain` "Secrets", `doctor.group.store` "Event store", `doctor.group.disk` "Disk", `doctor.group.providers` "Models", `doctor.group.policy` "Policy", `doctor.group.git` "Git", `doctor.group.toolchain` "Toolchains". Summary: `doctor.summary` "{blocking, plural, =0 {All required checks passed.} one {# blocking problem.} other {# blocking problems.}} {warn, plural, =0 {} one {# warning.} other {# warnings.}}". Buttons: `doctor.run` "Run checks", `audit.verify` "Verify chain", `audit.verify_strict` "Verify (strict)", `audit.export` "Export session", `audit.export_blobs` "Include artifact contents". Results: `audit.verify_ok` "Chain verified: {events, number} events, checkpoint signature valid{strict, select, true {; every tool call has a preceding allow decision} other {}}." `audit.export_done` "Exported {events, number} events and {artifacts, number} artifacts to <code>{path}</code> (sha256 <code>{sha_short}</code>)." `audit.export_metrics` "Export UX metrics"; `audit.export_metrics_done` "UX metrics for {runs, plural, one {# run} other {# runs}} saved to <code>{path}</code>. The file contains counts and durations only; nothing was sent anywhere." (B09 §5.3)

## 13. Timeline entry labels (`timeline.*`, `tool.*`)

| Source event | Key | Label (en) |
|---|---|---|
| `session.request` | `timeline.request` | Request |
| `worktree.create` | `timeline.branch_created` | Session branch <code>{branch}</code> created from <code>{base_short}</code> |
| `task.state` → running, task `plan` | `timeline.task.plan` | Plan |
| task `implement` | `timeline.task.implement` | Implement |
| task `verify`, `verify-2` | `timeline.task.verify` | Verify{round, select, 2 { (after repair)} other {}} |
| task `repair-1` | `timeline.task.repair` | Repair |
| task `summarize` | `timeline.task.summarize` | Summarize repository |
| `routing.decision` | `timeline.routing` | Uses `routing.chosen` or `routing.pinned` |
| `routing.fallback` | `timeline.fallback` | Uses `routing.fallback` |
| `workflow.gate.presented` | `timeline.gate.presented` | `{gate_key, select, gate-plan {Plan ready for review (G1)} gate-final {Result ready for review (G2)} other {Review}}` |
| `workflow.gate.resolved` | `timeline.gate.resolved` | `{gate_key, select, gate-plan {Plan {decision, select, approve {approved} reject {rejected; run cancelled} other {}}{edited, select, true { with edits} other {}}} gate-final {Result {decision, select, approve {accepted; run succeeded} reject {discarded; nothing delivered} other {}}} other {Gate}}` (ID-01) |
| `workflow.delivered` | `timeline.delivered` | `{action, select, commit {Committed <code>{commit_short}</code>; branch <code>{branch}</code> published} apply_branch {Branch <code>{branch}</code> published} push {Pushed <code>{branch}</code> to <code>{remote}</code>} export_patch {Patch exported to <code>{patch_path}</code>} other {Delivered}}` (ID-02) |
| `approval.requested` | `timeline.approval.requested` | Approval needed: {what_short} |
| `approval.resolved` | `timeline.approval.resolved` | Uses §3.4 |
| `test-report` artifact | `timeline.tests` | Tests: {passed, number} passed, {failed, number} failed |
| `workflow.end` | `timeline.run_end` | `{status, select, succeeded {Run succeeded} failed {Run failed: {reason_text}} cancelled {{reason, select, rejected {Run ended: rejected by you} other {Run cancelled}}} other {Run ended}}` |
| `chain.checkpoint` | `timeline.checkpoint` | Audit checkpoint signed at event #{last_seq} |
| `budget.changed` | `timeline.budget` | Session budget changed from {from} to {to} |
| `workspace.classification` | `timeline.classification` | Classification changed from {from} to {to} |
| `session.resume` | `timeline.resumed` | Resumed from {gate, select, gate-plan {the approved plan} other {the last gate}} |
| `redaction` | `timeline.redaction` | `{count, plural, one {# secret} other {# secrets}}` redacted from {source} |
| `harness.session.start` | `timeline.harness_start` | <code>{harness_id}</code> session started ({run_mode}; terms {vendor_terms}) |
| `tool.exec.*` `fs.read` | `tool.fs.read` | Read <code>{path}</code> |
| `fs.list` | `tool.fs.list` | Listed <code>{path}</code> |
| `fs.search` | `tool.fs.search` | Searched <code>{query}</code> ({matches, plural, one {# match} other {# matches}}) |
| `fs.write` | `tool.fs.write` | Wrote <code>{path}</code> |
| `fs.patch` | `tool.fs.patch` | Patched <code>{path}</code> (+{add} −{del}) |
| `proc.exec` | `tool.proc.exec` | Ran <code>{argv}</code>{profile, select, none {} other { · profile <code>{profile}</code>}} · exit {exit_code} |
| `git.status` / `git.diff` | `tool.git.status` / `tool.git.diff` | git status / git diff |
| `git.commit` | `tool.git.commit` | Committed on <code>{branch}</code> |
| `git.push` | `tool.git.push` | Pushed to <code>{remote}</code> |
| `proxy.connect` | `tool.proxy.connect` | Connected to <code>{host}:{port}</code> |
| `proxy.denied` | `tool.proxy.denied` | Refused connection to <code>{host}:{port}</code> |
| `approval.request` (tool) | `tool.approval.request` | Asked a question |
| collapsed group | `timeline.tool_group` | `{n, plural, one {# tool call} other {# tool calls}}`{denied, plural, =0 {} other { · # denied}}{pending, plural, =0 {} other { · # waiting}} |
| step counter | `timeline.step` | Step {step} of {max_steps} |
| elapsed | `timeline.elapsed` | {duration} |
| output truncated | `tool.truncated` | Output truncated at {limit} |

## 14. Screen-reader announcements (`live.*`)

Two live regions exist in AppShell (B05 §12.4, which is normative for politeness): `polite` and `assertive`. Incoming events never move focus (core §15 ID-15); approvals and gates are announced politely with "Press G then A to review", never "Press A to approve" (`A` works only with focus in the card). `assertive` is reserved for blocking and error banners and the lost-connection notice. Announcements are throttled to at most one polite message per 2 s (bursts are coalesced into a count), are silent during replay, and step increments and streaming text are never announced.

| Key | Region | Message (en) |
|---|---|---|
| `live.approval_needed` | polite | Approval needed: {what_short}. Press G then A to review. |
| `live.approvals_needed` | polite | {n, plural, one {# approval needed.} other {# approvals needed.}} Press G then A to review. |
| `live.gate_presented` | polite | `{gate_key, select, gate-plan {The plan is ready for review.} gate-final {The result is ready for review.} other {Review needed.}}` Press G then A to review. |
| `live.gate_resolved` | polite | `{gate_key, select, gate-plan {Plan {decision, select, approve {approved{edited, select, true { with edits} other {}}.} other {rejected. The run ended.}}} gate-final {Result {decision, select, approve {accepted. The run succeeded.} other {discarded. Nothing was delivered.}}} other {Gate resolved.}}` |
| `live.task_state` | polite | {task_label}: {state_label}. |
| `live.tests` | polite | Tests finished: {passed} passed, {failed} failed. |
| `live.repair` | polite | Tests failed. A repair round started. |
| `live.denied` | polite | Denied: {what_short}. {reason} |
| `live.approval_resolved` | polite | `{decision, select, approve {Approved for {scope}.} reject {Rejected.} expire {Approval expired.} other {Approval closed.}}` {pending, plural, =0 {} one {# approval still pending.} other {# approvals still pending.}} |
| `live.approval_resolved_elsewhere` | polite | `{decision, select, approve {Approved} reject {Rejected} other {Closed}}` from the CLI by {approver}. |
| `live.run_end` | polite | `{status, select, succeeded {Run succeeded.} failed {Run failed: {reason_text}.} cancelled {Run cancelled.} other {Run ended.}}` |
| `live.cancel_requested` | polite | Stopping {task_label}. |
| `live.cancelled` | polite | Cancelled. All sandbox processes stopped. You can resume from the last gate. |
| `live.delivery` | polite | `{action, select, commit {Committed {commit_short}.} apply_branch {Branch {branch} published.} push_pending {Push needs approval. Press G then A to review.} push {Pushed to {remote}.} push_rejected {Push rejected. Nothing was sent.} export_patch {Patch exported.} other {Delivered.}}` |
| `live.caught_up` | polite | Caught up. {pending, plural, =0 {} one {# approval pending.} other {# approvals pending.}} |
| `live.error` | assertive (blocking and error banners only: BN-01, 02, 03, 05, 06, 10) | {error_title}. {runtime_action} |
| `live.disconnected` | assertive | Connection to the runtime lost. Reconnecting. |
| `live.reconnected` | polite | Reconnected. |
| `live.chain` | polite | `{status, select, verified {Chain verified.} failed {Chain verification failed.} other {}}` |
| `live.classification` | polite | Classification changed to {to}. {n, plural, =0 {} one {# model is no longer admissible.} other {# models are no longer admissible.}} |
| `live.copied` | polite | Copied {what}. |
| `live.coalesced` | polite | {n, plural, one {# more update} other {# more updates}} in the timeline. |

## 15. Units (`unit.*`)

`unit.ms` "{n, number} ms", `unit.s` "{n, number} s", `unit.min` "{n, number} min", `unit.h` "{n, number} h", `unit.tokens` "{n, plural, one {# token} other {# tokens}}", `unit.bytes` uses `formatBytes` ("256 KiB", "2 MB" as in the tool limits), `unit.ago` "{duration} ago".

## 16. Keys for B03, B04 and B05 components

Keys added in the integration pass for strings the wireframes (B03 §15), components (B04 §8) and interaction spec (B05) need. Keys B03, B04 or B05 named differently for a string that already existed are listed in §17 instead of being duplicated. Keys ending in `.<x>` are families: one key per listed value.

### 16.1 Shell, connection and banners (`shell.*`, `banner.*`, `common.*`)

| Key | Message (en) |
|---|---|
| `shell.header.aria` | Session header: {workspace}, {classification}, {sandbox}, {chain} |
| `shell.connection.connecting` | Starting the Warden runtime… |
| `shell.connection.live` | Live |
| `shell.connection.replaying` | Catching up… |
| `shell.connection.reconnecting` | Reconnecting… |
| `shell.connection.disconnected` | Not connected |
| `shell.mode.shared` | Shared mode |
| `common.disconnected` | Not available while reconnecting to the runtime. |
| `common.confirmTimeout` | The runtime did not confirm within 30 s. The view was refreshed from the runtime; check the current state before trying again. |
| `common.sending` | Sending… |
| `common.retry_now` | Retry now |
| `banner.BN-04.title` / `.body` | Connection to the runtime lost / Reconnecting (attempt {attempt}). Running tasks continue in the runtime; actions are paused until the connection is back. |
| `banner.BN-05.title` / `.body` | The runtime cannot write its audit log / All tasks are paused so that nothing happens without a record ({detail}). Free disk space or fix permissions, then run checks. |
| `banner.BN-06.unauthorized.title` / `.body` | This app is not authorized by the runtime / {owned, select, true {Restart the runtime from this app to issue a new token.} other {The runtime was started from the CLI; its token is in <code>~/.warden/run/token</code>. Restart the runtime or the app.}} |
| `banner.BN-06.protocol.title` / `.body` | App and runtime versions do not match / App protocol {client}, runtime protocol {daemon} (version {daemon_version}). Update Warden so both match. |
| `banner.BN-07.body` | Warden runs in shared mode. Subscription harnesses for personal use are locked. |
| `banner.BN-08.title` / `.body` | Classification loosened to {to} / Content of {workspace} may now go to {added_tiers, list, conjunction} models. {n, plural, =0 {} one {# session is affected.} other {# sessions are affected.}} |
| `banner.BN-08.action` | View affected sessions |
| `banner.BN-10.title` / `.body` | Cancel not confirmed / The runtime has not confirmed that task <code>{task_key}</code> stopped after {seconds} s. It is still force-stopping sandbox processes. |
| `banner.BN-10.action.retry` | Retry cancel |
| `banner.BN-11.title` | Task <code>{task_key}</code> is paused |
| `banner.BN-11.body` | `{reason, select, no_admissible_model {No admissible model for {classification} data.} provider {{model_id} ({tier}) failed and only higher tiers remain. Fallback never moves to a higher tier on its own.} other {The task is waiting.}}` |

BN-01, BN-02, BN-03 and BN-09 use `st.2.*`, `st.4.*`, `st.6.*` and `st.1.*`.

### 16.2 Workspace home and first open (`workspace.*`, `firstopen.*`)

| Key | Message (en) |
|---|---|
| `workspace.empty` | Alias of `empty.workspaces` (§17) |
| `firstopen.title` | Classify this workspace |
| `firstopen.body` | Classification decides which models may see this code. The default is confidential: only local, company-hosted and company-cloud models. |
| `firstopen.option.<classification>` | Label from `class.<classification>`, description from `class.help.<classification>` |
| `firstopen.confirm` | Open workspace |
| `firstopen.uncommitted` | `{n, plural, one {# uncommitted change} other {# uncommitted changes}}` in your working tree will be left untouched. Warden works on its own session branch. |

### 16.3 Setup wizard (`setup.*`)

| Key | Message (en) |
|---|---|
| `setup.title` | Alias of `st.1.title` (§17) |
| `setup.step` | Step {n} of {total} |
| `setup.path.api_key.consequence` | Code you send goes to the vendor’s API (tier T3). Not admissible for confidential workspaces. |
| `setup.path.local.consequence` | Runs on this machine (tier T0). Admissible for every classification; speed depends on your hardware. |
| `setup.path.company.consequence` | Runs on a server your company controls (tier T1). Admissible for confidential workspaces. |
| `setup.path.harness.consequence` | Uses your subscription through the vendor’s own agent (tier T4). Not admissible for confidential workspaces; runs only when pinned. |
| `setup.field.provider` | Provider |
| `setup.field.api_key` | API key |
| `setup.field.base_url` | Endpoint URL |
| `setup.field.auth_kind` | Authentication |
| `setup.field.token` | Bearer token |
| `setup.field.client_cert` | Client certificate (PEM) |
| `setup.field.client_key` | Client private key (PEM) |
| `setup.field.azure_header` | Send the key as <code>api-key</code> header (Azure OpenAI) |
| `setup.field.tier` | Where this endpoint runs |
| `setup.field.id` | Provider id |
| `setup.secretHelp` | Stored in your OS keychain. Warden never shows it again, writes it to a file, or passes it to a sandbox. |
| `setup.detect` | Detect local servers |
| `setup.test.ok` | Connected in {latency} ms. {n, plural, one {# model} other {# models}}, {tools, plural, =0 {none with tool calling} one {# with tool calling} other {# with tool calling}}. |
| `setup.test.failed.<model_error_code>` | Uses `error.model.<code>` with the `.stopped` variant where it exists; `auth_failed` reads "The endpoint rejected the credential. Check the key or token and try again." |
| `setup.local.no_tools` | No model with tool calling found. You can use it for read-only questions; changes need a model with tool calling. |
| `setup.company.tls_error` | Could not establish a trusted TLS connection to <code>{host}</code> ({detail}). Check the URL, the server certificate, or the client certificate. |
| `setup.done` | Ready. {provider} is configured ({tier_chip}). |

### 16.4 Request composer (`composer.*`)

| Key | Message (en) |
|---|---|
| `composer.label` | Request |
| `composer.aria` | Describe the change for this repository |
| `composer.placeholder` | Describe the change to make… |
| `composer.submit` | Start |
| `composer.submit_auto` | Start on Auto |
| `composer.empty` | Type a request first. |
| `composer.counter` | {length, number} / {max, number} |
| `composer.readonly.label` | Read-only question |
| `composer.readonly_toggle` | Alias of `composer.readonly.label` (§17) |
| `composer.readonly.help` | Answers from the repository without changing files and without gates. |
| `composer.redactionHelp` | Secrets in your request are replaced by <code>[REDACTED:type]</code> before they are stored or sent to a model. |
| `composer.block.run_active` | A run is in progress. Wait for it to finish or cancel it. |
| `composer.block.no_provider` | Configure a model provider first. |
| `composer.block.sandbox_blocked` | The sandbox is not ready. Open Doctor for the fix. |
| `composer.block.budget_exhausted` | The session budget is used up. Raise it to continue. |
| `composer.block.disconnected` | Not connected to the runtime. |
| `composer.block.no_session` | Open a workspace to start. |
| `composer.disabled.no_provider` / `composer.disabled.run_active` | Aliases of `composer.block.no_provider` / `composer.block.run_active` (§17) |
| `composer.pin_inadmissible` | The pinned model is not admissible for {classification} data. Choose another model or start on Auto. |

### 16.5 Timeline and task cards (`timeline.*`, `task.*`, `toolcall.*`, `context.*`)

| Key | Message (en) |
|---|---|
| `timeline.aria` | Timeline of session {session_short} |
| `timeline.empty` | Alias of `empty.timeline` (§17) |
| `timeline.jumpToLatest` | Jump to latest · {n, plural, one {# new} other {# new}} |
| `timeline.loading_earlier` | Loading earlier events… |
| `context.empty` | Select an entry in the timeline to see its details. J and K move, Enter opens. |
| `task.title.<task_key>` | `plan` "Plan", `implement` "Implement", `verify` "Verify", `repair-1` "Repair", `verify-2` "Verify after repair", `summarize` "Summarize repository", `gate-plan` "Plan review (G1)", `gate-final` "Result review (G2)" |
| `task.agent` | <code>{agent}</code> {version} |
| `task.step` | Step {step} of {max_steps} |
| `task.step.noMax` | Step {step} |
| `task.repairRound` | Repair round {round} of {max_rounds} |
| `task.toolCalls.summary` | `{n, plural, one {# tool call} other {# tool calls}}`{denied, plural, =0 {} other { · # denied}}{pending, plural, =0 {} other { · # waiting}} |
| `task.live.label.model_text` | Model output (live) |
| `task.live.label.model_tool_args` | Tool call being written (live) |
| `task.live.label.tool_output` | Command output (live) |
| `task.live.hide` | Hide live output |
| `task.live.show` | Show live output |
| `task.waitingFirstToken` | Waiting for the model’s first token (local models can take longer). |
| `task.failed.<reason>` | "Failed: " + `reason.<reason>` (§2), for `budget`, `policy_denied`, `schema`, `verification`, `provider`, `tool`, `resource`, `timeout`, `approval_expired`, `upstream_failed`, `interrupted` |
| `task.waiting.no_admissible_model` | Waiting: no admissible model for {classification} data. |
| `task.waiting.provider` | Waiting: {model_id} ({tier}) failed and only higher tiers remain. |
| `task.waiting.budget` | Waiting: the session budget is used up. |
| `task.waiting.input_needed` | Waiting for your answer. |
| `task.waiting.approval_pending` | Waiting for your approval. |
| `task.interrupted.retrying` | The runtime restarted during this task; retrying (attempt {attempt} of {max_attempts}). |
| `task.input.question` | Alias of `approval.what.question` (§17) |
| `routing.verify_no_model` | No model needed: all checks passed. |
| `toolcall.phase.deciding` / `.waiting` / `.running` / `.done` / `.failed` / `.denied` | Checking policy… / Waiting for approval / Running / Done / Failed / Denied |
| `toolcall.exit` | exit {code} |
| `toolcall.truncated` | Alias of `tool.truncated` (§17) |
| `toolcall.egress.connected` | Alias of `tool.proxy.connect` (§17) |
| `toolcall.egress.denied` | Alias of `tool.proxy.denied` (§17) |
| `toolcall.violation.<kind>` | Uses `failure.sandbox_violation` with `{kind}` (§7.3) |
| `toolcall.output.load` | Load full output ({size}) |
| `redaction.count` | `{count, plural, one {# secret redacted} other {# secrets redacted}}` |

### 16.6 Approval card states and questions (`approval.*`)

| Key | Message (en) |
|---|---|
| `approval.whatDetail.egress` | Requested by <code>{process}</code> in task <code>{task_key}</code>; port {port}{method, select, CONNECT { (TLS)} other {}}. |
| `approval.sending` | Sending… |
| `approval.slow` | Waiting for the runtime to confirm… |
| `approval.hint.focusFirst` | Press G then A to review the pending approval first. |
| `approval.hint.unfocused` | Press G then A to review. |
| `approval.unknownOutcome` | Your decision may not have reached the runtime. Press A or R again. |
| `approval.resolvedElsewhere` | `{decision, select, approve {Approved} reject {Rejected} other {Closed}}` from the CLI by {approver}{scope, select, none {} other { · {scope}}}. |
| `approval.revoke.nextDecision` | Revoked. It applies from the next decision; a call already running is not interrupted. |
| `approval.cancelling` | Cancelling… (the task is being cancelled) |

Question approvals (`kind: question`, ID-05). The card replaces the scope selector with an answer field; "Send answer" calls `approval.resolve {decision: approve, answer}`, "Decline" calls it with `reject`.

| Key | Message (en) |
|---|---|
| `approval.question.title` | Question from <code>{agent}</code> |
| `approval.question.label` | Your answer |
| `approval.question.send` | Send answer |
| `approval.question.decline` | Decline to answer |
| `approval.question.kbd_hint` | Mod+Enter send · Esc leave the field |
| `approval.question.counter` | {length, number} / 4,000 |
| `approval.question.too_long` | Answers are limited to 4,000 characters. |
| `approval.question.empty` | Type an answer, or decline. |
| `approval.question.redaction_note` | Secrets in your answer are redacted before it is stored or sent to the model. |
| `approval.resolved.answered` | Answered by you · {time} |
| `approval.resolved.declined` | Declined by you · {time}. <code>{agent}</code> continues without an answer. |
| `notify.question.body` | <code>{agent}</code> asks a question in {workspace} |

### 16.7 Plan review (`plan.*`, `gate.*`)

| Key | Message (en) |
|---|---|
| `plan.title` | Alias of `gate.g1.title` (§17) |
| `plan.modelWritten` | Written by <code>{model_id}</code> ({tier_chip}) from the repository’s content. Review it before approving. |
| `plan.summary` | Summary |
| `plan.steps` | Steps |
| `plan.step.title` | Step title |
| `plan.step.files` | Files (one per line) |
| `plan.step.rationale` | Why |
| `plan.expectedFiles` | `{n, plural, one {# expected file} other {# expected files}}` |
| `plan.risks` | Alias of `gate.g1.risks` (§17) |
| `plan.estimate` | Alias of `gate.g1.estimate` (§17) |
| `plan.intentNote` | File lists describe intent. They do not grant or change what agents may do. |
| `plan.edit.addStep` | Add step |
| `plan.edit.removeStep` | Remove step {n} |
| `plan.edit.moveUp` / `plan.edit.moveDown` | Move step {n} up / Move step {n} down |
| `plan.edit.unsaved` | Unsaved edits |
| `plan.edit.resolvedElsewhere` | This plan was resolved from the CLI by {approver}. Your unsaved edits are kept below for copying. |
| `plan.edit.problems` | `{n, plural, one {# problem} other {# problems}}` |
| `plan.error.required` | Required. |
| `plan.error.too_long` | Too long: at most {max, number} characters. |
| `plan.error.min_items` | At least one step is required. |
| `plan.error.max_items` | At most {max, number} {field, select, steps {steps} other {files}}. |
| `plan.error.path_absolute` | Use a path relative to the repository root (no leading / or ~). |
| `plan.error.path_traversal` | Paths cannot contain <code>..</code>. |
| `plan.error.path_invalid` | Not a valid path: no backslashes, control characters or wildcards. |
| `plan.error.duplicate_path` | This file is already listed in this step. |
| `plan.error.path_denied` | <code>{path}</code> is on the secret deny-list and can never be changed by agents. |
| `plan.error.path_not_writable` | Warning: agents cannot write under <code>.git/</code> or <code>.github/</code>; this step will not be able to change it. |
| `plan.error.schema` | The plan does not match its schema at <code>{pointer}</code>: {detail} |
| `plan.approvedBy` | Approved by {approver} · {time} |
| `plan.editedBeforeApproval` | Edited before approval. Compare with the original (<code>{artifact_id}</code>). |
| `gate.gate-plan.aria` / `gate.gate-final.aria` | Plan review, gate G1, {state} / Result review, gate G2, {state} |
| `gate.reject` | Reject |
| `gate.reject.comment` | Reason (optional, recorded in the audit trail) |
| `gate.reject.confirm.gate-plan` | Reject the plan? The run ends; nothing in the repository was changed. |
| `gate.reject.confirm.gate-final` | Discard this result? The run ends and nothing is delivered. The diff and test report stay readable. |
| `gate.editing.status` | Editing the plan. The gate stays open until you save or discard. |
| `gate.approvedBy` | Approved by {approver} · {time} |
| `gate.approvedWithEdits` | Approved with edits by {approver} · {time} |
| `gate.rejectedBy` | Rejected by {approver} · {time} |
| `gate.expired` | Alias of `failure.gate_expired` (§17) |
| `delivery.accepted` | Result accepted by you · {time}. Run succeeded. |
| `delivery.discarded` | Result discarded by you · {time}. Nothing was delivered. |

### 16.8 Result review, diff, tests and cost (`diff.*`, `test.*`, `cost.*`, `delivery.*`)

| Key | Message (en) |
|---|---|
| `diff.title` | Changes |
| `diff.version` | Version {n} of the diff |
| `diff.cumulative` | Cumulative from the session base <code>{base_short}</code> |
| `diff.supersedes` | Includes and replaces the diff of task <code>{task_key}</code> |
| `diff.supersededBy` | Replaced by a newer diff from task <code>{task_key}</code> |
| `diff.newerAvailable` | A newer version of this diff exists. Show the latest |
| `diff.partial` | Partial diff: the task was cancelled before it finished. |
| `diff.readonly.cancelled` | Read-only: this run was cancelled. |
| `diff.layout.unified` / `diff.layout.split` | Unified / Side by side |
| `diff.split_unavailable` | Side by side needs a wider window. |
| `diff.group.task` / `diff.group.file` | By task / By file |
| `diff.collapsed.lockfile` / `.large` / `.binary` | Lockfile collapsed / Large file collapsed ({lines, number} lines) / Binary file, not shown |
| `diff.showDiff` | Show diff |
| `diff.provenance.chip` | <code>{task_key}</code> · step {step} · <code>{tool}</code> |
| `diff.provenance.more` | and {n, plural, one {# more call} other {# more calls}} |
| `diff.provenance.taskOnly` | From task <code>{task_key}</code> (step not recorded) |
| `diff.provenance.unattributed` | Not attributed: the hunk does not match the recorded write log. |
| `diff.provenance.unverified` | Provenance not verified: no matching tool call found in this session. |
| `diff.hunk.aria` | Hunk {n} of {total} in {path}, lines {start} to {end}, from {provenance} |
| `test.title` | Tests |
| `test.outcome.passed` / `.failed` / `.running` / `.cancelled` | Passed / Failed / Running / Cancelled |
| `test.counts` | Alias of `gate.g2.tests` (§17) |
| `test.exitCode` | Build exit code {code} |
| `test.duration` | Took {duration} |
| `test.failures` | `{n, plural, one {# failing test} other {# failing tests}}` |
| `test.analysis` | Failure analysis (written by <code>{agent}</code>{model, select, none { from the parsed report} other { on <code>{model}</code>}}) |
| `test.previous` | Previous run: {passed} passed, {failed} failed |
| `test.afterRepair` | After repair |
| `cost.title` | Cost and usage |
| `cost.column.task` / `.model` / `.input` / `.output` / `.cached` / `.quota` / `.cost` / `.time` / `.calls` | Task / Model / Input tokens / Output tokens / Cached / Quota / Cost / Time / Calls |
| `cost.total` | Total |
| `cost.note.quotaBilled` | Billed as subscription quota, not in currency. |
| `cost.note.estimate` | Estimated from list prices; the provider’s invoice is authoritative. |
| `cost.budget.used` | {spent} of {limit} used this session |
| `cost.budget.left` | {remaining} left |
| `cost.budget.warning` | 80 % of the session budget is used. |
| `cost.budget.exhausted` | Alias of `st.4.title` (§17) |
| `cost.budget.raise` | Alias of `st.4.action.raise` (§17) |
| `cost.budget.raise.max` | Alias of `st.4.max_note` (§17) |
| `cost.empty` | Alias of `empty.cost` (§17) |
| `cost.quota.premium_requests` | Alias of `cost.quota` (§17) |
| `delivery.confirming` | Delivering… |
| `delivery.acceptAnd.commit` / `.apply_branch` / `.push` / `.export_patch` | Accept and commit / Accept and apply to branch / Accept and push / Accept and export patch |
| `delivery.push.needsCommit` | Commit or apply to a branch first. |
| `delivery.unavailable_discarded` | Not available: the result was discarded. |
| `delivery.error.<action>` | Could not {action, select, commit {commit} apply_branch {create the branch} push {push} export_patch {export the patch} other {deliver}}: {detail}. {action, select, push {Nothing was sent.} other {Your repository was not changed.}} |
| `deliver.chain_failed_note` | The audit chain of this session failed verification. The delivered change will not have a verified record. |

### 16.9 ST-3 continue and fallback pause (ID-04, ID-16)

| Key | Message (en) |
|---|---|
| `st.3.action.continue_on` | Continue on {model_id} ({tier}) |
| `st.3.fallback_body` | {model_id} ({tier}) is unavailable ({cause}). Fallback never moves to a higher tier on its own; continuing on {next_model} ({next_tier}) needs your choice. |
| `st.3.continue_note` | This pins {model_id} for the rest of the session. You can clear the pin in the model picker. |
| `routing.picker.pinInvalidated` | The pinned model is no longer admissible for {classification} data. The next request runs on Auto unless you choose another model. |
| `routing.picker.pinNotAllowed` | Pinning is disabled by policy (<code>routing.pins.allow_user_pin</code>). |
| `routing.picker.free` | No cost |
| `routing.picker.price` | {input} in / {output} out per million tokens |
| `routing.picker.prior` | Alias of `routing.prior` (§17) |
| `routing.picker.capability.native` / `.emulated` / `.none` | Tool calling: native / emulated / none |
| `routing.because.generic` | Chosen by {strategy} for {classification} data among {n, plural, one {# candidate} other {# candidates}}. |
| `routing.choosing` | Choosing a model… |
| `routing.noCandidate` | No admissible model for {classification} data. {rejected, plural, =0 {} one {# model not admitted.} other {# models not admitted.}} |
| `routing.retrying` | Alias of `error.model.<code>.retrying` (§17) |
| `routing.details.column.model` / `.tier` / `.status` / `.reason` / `.prior` / `.cost` | Model / Tier / Status / Reason / Quality prior / Est. cost |

### 16.10 Cancel and resume (`cancel.*`, `resume.*`)

| Key | Message (en) |
|---|---|
| `cancel.button` | Alias of `cancel.action` "Cancel run" (§17) |
| `cancel.action` | Cancel run |
| `cancel.warning` | Stops now. Partial changes stay on the session branch; you can resume from the last gate. |
| `cancel.stopping` | Stopping… |
| `cancel.stoppingSlow` | Still stopping… processes are being force-stopped. |
| `cancel.unconfirmed` | Alias of `banner.BN-10.title` (§17) |
| `cancel.nothingRunning` | Nothing is running. |
| `resume.noGate` | No approved plan to resume from. Start a new request. |

### 16.11 Notifications (`notify.*`)

| Key | Message (en) |
|---|---|
| `notify.approval.body_confidential` | An action in {workspace} (confidential) needs approval |
| `notify.run_blocked.title` / `.body` | Warden: run needs input / {workspace} · {reason_text} |
| `notify.input.title` / `.body` | Alias of `notify.run_blocked.*` (§17) |
| `notify.succeeded.title` / `.body` | Warden: run succeeded / {workspace} · the result is ready to deliver |
| `notify.cancelled.title` / `.body` | Warden: run cancelled / {workspace} · cancelled from {origin, select, cli {the CLI} other {another client}} |
| `notify.permission.explain` | Warden can notify you when an approval or review is waiting while the window is in the background. Notifications never include secrets; for confidential workspaces they omit what the action is. |
| `notify.permission.denied` | Notifications are off. You can turn them on in your system settings. |

For confidential workspaces every `notify.*.body` omits the request text and action details (B05 §10.2): gate and input bodies show only the workspace name.

### 16.12 Explain drawer, event log, providers and doctor chrome (`explain.*`, `events.*`, `provider.*`, `doctor.*`, `badge.aria`)

| Key | Message (en) |
|---|---|
| `explain.title.decision` / `.approval` / `.routing` | Why this decision / Why this needs approval / Why this model |
| `explain.recorded` | Recorded decision (event #{seq}) |
| `explain.current` | If requested now |
| `explain.differs` | The current policy would decide differently ({current}), for example after a grant or a revoke. The recorded decision is what happened. |
| `explain.layer.invariant` / `.capability` / `.platform` / `.user` / `.grant` | Platform invariant / Agent capability / Platform default / Your policy / Your approval |
| `explain.obligations` | Conditions attached |
| `explain.evidence` | Evidence in the audit trail |
| `explain.cli` | Same in the terminal: |
| `explain.tester.title` | Try a decision |
| `explain.tester.run` | Explain |
| `events.title` | Event log |
| `events.filter.<family>` | `policy` Policy, `tool` Tools, `model` Models, `approval` Approvals, `routing` Routing, `sandbox` Sandbox, `workflow` Workflow, `audit` Audit |
| `events.search` | Filter by id, type or rule |
| `events.column.seq` / `.time` / `.type` / `.task` / `.summary` | Seq / Time / Type / Task / Summary |
| `events.verify` | Alias of `audit.verify` (§17) |
| `events.verify.strict` | Alias of `audit.verify_strict` (§17) |
| `events.verify.ok` | Alias of `audit.verify_ok` (§17) |
| `events.verify.failed` | Alias of `st.6.body` (§17) |
| `events.export` / `events.export.done` | Aliases of `audit.export` / `audit.export_done` (§17) |
| `events.export.withFailedChain` | Alias of `st.6.export_warning` (§17) |
| `events.loadMore` | Load more |
| `events.empty` | Alias of `empty.event_log` (§17) |
| `provider.status.ok` / `.untested` / `.failing` / `.disabled` / `.locked` / `.unconfigured` | Ready / Not tested / Failing / Disabled / Locked / Not configured |
| `provider.auth.none` / `.api_key` / `.api_key_azure` | No credential / API key / API key (<code>api-key</code> header) |
| `provider.auth.gateway.bearer` / `.mtls` | Gateway token / Client certificate (mTLS) |
| `provider.credentialStored` | Credential stored in the OS keychain (<code>{secret_ref}</code>) |
| `provider.billing.api_key` / `.none` / `.gateway` / `.subscription` / `.chatgpt_login` / `.subscription_personal` | Billed per token to your key / No billing / Billed by your company / Your subscription (premium requests) / Your ChatGPT plan / Your personal subscription |
| `provider.terms.acknowledge` | Alias of `vendor.<harness>.enable_ack` (§17) |
| `provider.locked.shared` | Alias of `vendor.claude_code.locked` (§17) |
| `provider.admissibleFor` | Admissible for {classes, list, conjunction} data |
| `provider.test` / `provider.enable` / `provider.disable` / `provider.remove` | Test / Enable / Disable / Remove |
| `provider.remove.confirm` | Alias of `confirm.provider_remove` (§17) |
| `doctor.title` | Doctor and audit |
| `doctor.running` | Running checks… |
| `doctor.status.ok` / `.warn` / `.fail` | OK / Warning / Blocking |
| `doctor.blocking` | Blocked: a required sandbox check fails. Open Doctor for the fix. |
| `doctor.copyFix` | Copy fix |
| `doctor.lastRun` | Last checked {time} |
| `badge.aria` | `{approvals, plural, =0 {} one {# approval pending} other {# approvals pending}}`{gate, select, none {} other {, gate {gate} open}}. Press G then A to review. |

## 17. Key alias mapping

Names used in B03, B04 or B05 for strings this catalog already defines. The authoritative key is on the right; implementations register the left-hand name as an alias in `i18n/aliases.ts` (B08 §9) or use the right-hand key directly. No text is duplicated.

| Name used in B03 / B04 / B05 | Authoritative B07 key |
|---|---|
| `approval.resolved.elsewhere` (B03) | `approval.resolvedElsewhere` |
| `approval.scope.consequence.<scope>` (B05), `approval.scope.help.<scope>` (B03) | `scope.<scope>.explain` |
| `approval.question.label` (B03), `task.input.question` (B04) | `approval.what.question` (heading) and `approval.question.label` (field) |
| `st.3.action.continue` (B04, B05) | `st.3.action.continue_on` |
| `composer.readonly_toggle` (B03) | `composer.readonly.label` |
| `composer.disabled.no_provider`, `composer.disabled.run_active` (B03) | `composer.block.no_provider`, `composer.block.run_active` |
| `workspace.empty` (B04) | `empty.workspaces` |
| `timeline.empty` (B04) | `empty.timeline` |
| `setup.title` (B03, B04) | `st.1.title` |
| `plan.title`, `plan.risks`, `plan.estimate` (B04) | `gate.g1.title`, `gate.g1.risks`, `gate.g1.estimate` |
| `gate.expired` (B04) | `failure.gate_expired` |
| `test.counts` (B04) | `gate.g2.tests` |
| `toolcall.truncated`, `toolcall.egress.connected`, `toolcall.egress.denied` (B04) | `tool.truncated`, `tool.proxy.connect`, `tool.proxy.denied` |
| `routing.fallback.none` (B04) | `routing.fallback_none` |
| `routing.retrying` (B04) | `error.model.<code>.retrying` |
| `routing.picker.group.inside`, `.outside` (B04); `modelpicker.section.*` (B03) | `tier.group_inside`, `tier.group_outside` |
| `routing.picker.prior` (B04) | `routing.prior` |
| `routing.reject.<reason_code>` (B04) | `routing.reject.<code>` |
| `cost.budget.exhausted`, `cost.budget.raise`, `cost.budget.raise.max` (B04) | `st.4.title`, `st.4.action.raise`, `st.4.max_note` |
| `cost.empty` (B04), `cost.quota.<kind>` (B04) | `empty.cost`, `cost.quota` |
| `cancel.button` (B04), `cancel.in_progress` (B03), `cancel.slow` (B03) | `cancel.action`, `cancel.stopping`, `cancel.stoppingSlow` |
| `cancel.unconfirmed` (B04) | `banner.BN-10.title` |
| `notify.input.*` (B05), `notify.run_blocked` (B03) | `notify.run_blocked.*` |
| `events.verify`, `events.verify.strict`, `events.verify.ok`, `events.verify.failed` (B04) | `audit.verify`, `audit.verify_strict`, `audit.verify_ok`, `st.6.body` |
| `events.export`, `events.export.done`, `events.export.withFailedChain`, `events.empty` (B04) | `audit.export`, `audit.export_done`, `st.6.export_warning`, `empty.event_log` |
| `provider.terms.permitted`, `.tolerated`, `.personalUseOnly`, `.prohibited` (B04) | `vendor.permitted.badge`, `vendor.tolerated.badge`, `vendor.personal.badge`, `vendor.prohibited.badge` |
| `provider.terms.acknowledge` (B04) | `vendor.codex.enable_ack`, `vendor.claude_code.enable_ack` |
| `provider.locked.shared` (B04), `harness.locked.shared_mode` (B03) | `vendor.claude_code.locked` |
| `provider.remove.confirm` (B04) | `confirm.provider_remove` |
| `effect.<v>`, `state.<v>`, `class.<v>`, `chain.<v>`, `tier.<T>` (B04) | `effect.*`, `state.*`, `class.*`, `chain.*`, `tier.chip` |
| `error.<code>.what`, `error.<code>.did`, `error.<code>.action` (B05 §11) | `error.rpc.<code>` and `error.model.<code>.<variant>`: each message already has the three parts as consecutive sentences in that order; components render the message as one paragraph |
| `deliver.*` (B03) | `delivery.*`, `confirm.*` (as listed in B03 §15) |
| `boot.connecting` (B03), `shell.connection.connecting` (B04) | `shell.connection.connecting` |

## Traceability

| Design element | WRD source (doc §) | Requirement / invariant satisfied |
|---|---|---|
| Voice rules, what / did / can do pattern (§1, §7) | WRD-11 §5 | Plain-language errors with runtime action and user options |
| Rule ids always shown in monospace (§1, §6) | WRD-11 §5 | "Policy denials include the rule id for support" |
| Approval frame what / who / why / scope / explain (§3) | WRD-11 §2.3, WRD-08 §7, WRD-16 §13 screen 4 | Approval prompt content; BI-1 made legible |
| Scope explanations and disabled reasons (§3.2) | WRD-08 §7, core §13.2 | Scope ≤ `scope_max`; R5 never persistable |
| Install, command, egress, push, harness variants (§3.3) | WRD-16 §3 steps 4, 6; §10.6; core §13.4, §13.5 | Demo approvals; push `once`; tolerated harness approval at session start (CF-03) |
| Egress hold text | core §13.4 | Proxy holds CONNECT up to 120 s |
| Routing line template and rejection reasons (§5) | WRD-06 §6, §11; WRD-16 §3 step 3, §6.3 | BI-7 visible: "Not admissible for confidential data: T3 Vendor API" |
| Pin text "overrides ranking, never admission" | WRD-16 §6.3 | BI-7 |
| Policy reasons per rule id incl. INV-1 to INV-9 (§6) | WRD-08 §5, WRD-16 §10.6; core §9; CF-19 | Explain the machine (WRD-11 §1.3); BI-5 (workspace cannot widen) implicit in fixed wording |
| Three-layer denial text (S2) | WRD-16 §3 step 8, §4.3 | S2 visible in UI (§15 item 7) |
| Model error messages (§7.1) | WRD-05 §4, WRD-06 §7 | Retry/fallback made visible; tier-bounded fallback stated (T-22) |
| RPC error messages (§7.2) | core §6, WRD-02 §5 | Client errors are informative; BI-6 |
| Failure modes (§7.3) | WRD-02 §11, WRD-16 §10, A16 | Disk full fails closed; no unsandboxed fallback; keychain locked |
| ST-1 to ST-6 texts (§8) | WRD-11 §3, WRD-16 §13; CF-36 | Four setup paths; states designed |
| Empty states (§9) | WRD-16 §13 screens 1 to 7 | Every screen has an empty state |
| Vendor-terms notices (§10) | WRD-05 §9, WRD-16 §6.1, §6.2; CF-21 | Copilot permitted; Codex tolerated with session-start approval; Claude Code personal use only, locked in shared builds; INV-7 |
| Confirmations (§11) | WRD-16 §13 screens 1, 5, 6; WRD-11 §3; core §13.14; WRD-02 §4 | Classification loosening confirmation; push via approval; budget raise within policy |
| Doctor checks and fix hints (§12) | WRD-16 §3 step 1, §10.3, §13 screen 7; WRD-02 §11 | H3 prerequisites explained; §15 item 1 |
| Timeline labels (§13) | WRD-11 §2.2, WRD-16 §13 screen 2 | One request, one timeline |
| Live announcements (§14) | WRD-11 §6, WRD-01 N-7 | Screen-reader support for approvals |
| ICU catalog with keys | WRD-11 §6 | Strings externalised; ro and de later |
| Accept result / Discard result, run end texts, `timeline.delivered`, commit publishes the branch, push after commit (§4, §11, §13, §16.7, §16.8) | core §15 ID-01, ID-02, ID-03 | G2 semantics; every delivery is a recorded, policy-checked action (BI-1) |
| Question approvals with answer field (§3.3, §16.6) | core §15 ID-05 | Answer passed as input, redacted (BI-3, BI-4) |
| Late `once` approval texts (§3.3, §3.4) | core §15 ID-07 | Accurate description of what a late approval allows |
| Install default scope `workspace` (§3.2) | core §15 ID-08 | H6 prompt budget (B09) |
| "No model needed" verify line (§16.5) | core §15 ID-09 | Explain the machine |
| Rejected approval returns task to running (§3.4) | core §15 ID-10 | S1 "continues or stops" |
| Polite approval and gate announcements with "Press G then A to review" (§14) | core §15 ID-15; B05 §12.4 | Focus never moved by events; WCAG 4.1.3 |
| Continue on a lower-tier model and fallback pause (§16.9, BN-11) | core §15 ID-04, ID-16; CF-44 | BI-7: fallback never widens the tier on its own |
| Confidential notification bodies (§16.11) | B05 §10.2; brief invariant 7 | BI-7 applied to OS notifications |
| Doctor texts keyed by A05 check ids (§12) | A05 §8.1; WRD-16 §13 screen 7 | H3 prerequisites explained |
| Alias mapping for B03/B04/B05 names (§17) | B03 §15, B04 §8, B05 | One string per concept |

## Deviations and assumptions

- NEW message keys only; no new API or event names are introduced. Placeholders reference fields defined in core §5, §6 and §15 (for example `workflow.delivered`, `approval.requested.kind`, `session.setPin`).
- NEW keys added in the integration pass (§16) cover every key B03 §15 marks "add to B07", every key B05 references and the NEW groups of B04 §8; names used differently there are mapped in §17 rather than duplicated.
- Politeness: `live.approval_needed`, `live.gate_presented` and `live.run_end` are polite, per core §15 ID-15 and B05 §12.4 (the first draft marked them assertive and said "Press A to approve", which B05 recorded as a conflict).
- ASM: `{os}` for OS-specific doctor texts comes from the bridge (`bridge_status`), and `{origin}` in `notify.cancelled.body` is derived from whether this client sent the cancel.
- `st.6.kind.*` keys follow the 22 violation kinds of A05 `audit.verify` (integration pass; the first draft's six proposed kinds are withdrawn).
- Doctor texts (§12) are keyed by the A05 §8.1 check ids and groups (integration pass). `socket` is a desktop-only check produced by the bridge (B08 §6.2), not by `system.doctor`.
- Disk thresholds follow A05: warning below 512 MiB, blocking below 128 MiB.
- The first draft's references to `warden session purge`, `warden doctor --fix` and `--verbose` were removed from fix hints because A05/A17 do not define them.
- Install approvals default to `workspace` scope (when allowed), all others to `once`, per core §15 ID-08 (was a DEV in the first draft); the scope consequence is always visible and the grant is revocable.
- DEV: WRD-16 §3 step 4 quotes the install prompt as "egress to registry.npmjs.org:443 is not in the task allowlist; rule user.package-install". This catalog phrases it as what + why with the host and the rule id, which carries the same facts.
- ASM: `{what_short}` is a UI-computed 60-character summary of `display.what` (command, path or host), used only in notifications and live regions.
- ASM: `{harness_name}` and `{login_command}` come from a small UI table per harness id (`copilot` → "GitHub Copilot", `copilot auth login`; `codex` → "Codex", `codex login`; `claude-code` → "Claude Code", `claude login`); the exact login commands are verified in the week-1 spike (A18).
- ASM: vendor-terms statements reflect WRD-05 §9 and WRD-16 §6.2 as of September 2026; `vendor.terms_date` shows the date recorded in `models.yaml` or the build.
- OQ candidate: whether denial reasons shown to the user should be the catalog text (localised) or the daemon's `reason` string (single source); this design uses the catalog with the daemon string as fallback.
