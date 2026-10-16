# 00 Design core: canonical identifiers and registries

This file is the single source of names for the whole design set (A01 to A18, B01 to B09). Every deliverable uses these identifiers exactly. If a deliverable needs a name that is not here, it adds it in its own "Deviations and assumptions" list and marks it `NEW`. Conflict ids (CF-xx) refer to `00-SCOPE-AND-CONFLICTS.md`.

## 1. Conventions for every deliverable

- One Markdown file per deliverable, named `A01-context-and-containers.md` and so on (file list in `README.md`).
- Diagrams: Mermaid (`C4Context`/`C4Container` or `flowchart`, `sequenceDiagram`, `stateDiagram-v2`, `classDiagram`), each followed by a one-paragraph description.
- Contracts: JSON Schema draft 2020-12 (`"$schema": "https://json-schema.org/draft/2020-12/schema"`), `$id` under `https://schemas.warden.dev/poc/`.
- Go: interface and struct sketches only.
- YAML: field names exactly as in WRD-16 §6.1, §7, §8, §10.6.
- Every file ends with two sections: `## Traceability` (table: Design element | WRD source (doc §) | Requirement / invariant satisfied) and `## Deviations and assumptions` (bullets; prefix `DEV:` for a deviation from a WRD document, `ASM:` for an assumption, `OQ-xx` to point at OPEN-QUESTIONS.md, `NEW` for a new identifier).
- Invariants from the brief are cited as `BI-1` … `BI-7` (brief §3); platform invariants as `INV-1` … `INV-9` (WRD-08 §5); requirements as `F-…`, `N-…`, `S-…`; hypotheses `H1`–`H6`; demo tasks `T1`–`T6`; scenarios `S1`–`S4`; tiers `T0`–`T4` (context disambiguates tier T1 from task T1: write "tier T1" or "task T1" where ambiguous).
- Writing style: plain, precise, no marketing; avoid overusing em-dashes.

Brief invariants (BI):

| Id | Invariant |
|---|---|
| BI-1 | No tool executes without a preceding `policy.decision` with `effect: allow` for that call (and an `approval.resolved` when required) |
| BI-2 | Sandbox exposes only worktree (rw), read-only toolchain, scratch, proxy socket; nothing from home; environment cleared |
| BI-3 | Secrets only in the OS keychain, injected host-side; never in events, artifacts, logs, sandbox env or model context |
| BI-4 | Every observation enters model context as untrusted, provenance-tagged data |
| BI-5 | Workspace/repository content can never widen permissions |
| BI-6 | Clients reach the runtime only through JSON-RPC; the CLI can do everything the desktop can |
| BI-7 | `confidential` data only to T0, T1, T2; router and UI make it visible and enforce it |

## 2. Identifiers

| Entity | Format | Example (abbreviated forms like `apr_9`, `call_31` are allowed in illustrative JSON) |
|---|---|---|
| Workspace | `wsp_` + ULID | `wsp_01JAXR7ZK3...` |
| Session | `ses_` + ULID | `ses_01JAXR8Q7M2V9KTC3F6YH5N0PB` |
| Session branch | `warden/` + lowercase ULID of the session | `warden/01jaxr8q7m2v9ktc3f6yh5n0pb` |
| Workflow run | `wfr_` + ULID | |
| Task | `tsk_` + ULID; `task_key` is the template id (`plan`, `gate-plan`, `implement`, `verify`, `repair-1`, `verify-2`, `gate-final`, `summarize`) | |
| Gate | the gate task's id (`tsk_…`); `gate_key` = `gate-plan` (G1) or `gate-final` (G2) | |
| Execution | `exe_` + ULID | |
| Event | `evt_` + ULID; `seq` store-global integer | |
| Artifact | `art_` + ULID; content address `sha256:<hex>` | |
| Approval | `apr_` + ULID | |
| Policy decision | `dec_` + ULID | |
| Tool call | `call_` + ULID (runtime-assigned; the provider's own id is kept as `provider_call_id`) | |
| Model call | `mc_` + ULID | |
| Routing decision | `rt_` + ULID | |
| Sandbox | `sb_` + ULID | |
| Subscription | `sub_` + ULID | |
| Chain | `ses_<ulid>` for a session chain, `sys` for the system chain (CF-09) | |
| Provider / harness id | catalog id from `models.yaml` (`anthropic`, `ollama`, `lmstudio`, `company-vllm`, `azure-openai`, `openai`, `copilot`, `codex`, `claude-code`) | |
| Model id | catalog id (`anthropic/claude-sonnet`, `local/qwen-coder-32b`, `company/qwen-coder-32b`, `local/qwen-coder-7b`); a harness is addressable as a model id equal to its harness id (`copilot`) | |

## 3. Enumerations

| Name | Values |
|---|---|
| Classification | `public`, `internal`, `confidential` (default). `restricted` rejected in the PoC with `unsupported_in_poc` (CF-02) |
| Trust tier | `T0` local loopback, `T1` company-hosted, `T2` company cloud tenant, `T3` vendor API, `T4` subscription harness |
| Admission (PoC) | `confidential: [T0,T1,T2]`, `internal: [T0..T4]`, `public: [T0..T4]` |
| Sandbox level | `L1` (Seatbelt / bubblewrap), `L2` (rootless Docker) |
| Sandbox backend | `seatbelt`, `bwrap`, `docker` |
| Risk class | `R0` read, `R1` write in worktree / commit on session branch, `R2` profile command, `R3` other command, `R4` egress outside allowlist / install profile / tolerated harness start, `R5` host effect (`git.push`), `R6` forbidden |
| Effect | `allow`, `deny`, `approval_required` |
| Approval scope | `once`, `task`, `session`, `workspace` (keyboard 1 to 4 in this order) |
| Approval decision | `approve`, `reject`, `expire`, `cancel` (task cancelled while pending) |
| Task state | `created`, `queued`, `running`, `waiting_for_approval`, `waiting_for_input`, `succeeded`, `failed`, `cancelled`, `timed_out`, `skipped`, `blocked` (terminal: succeeded, failed, cancelled, skipped, blocked; `timed_out` is terminal unless re-queued) |
| Task reason code | `deps_met`, `scheduled`, `approval_pending`, `approved`, `input_needed`, `input_provided`, `output_valid`, `retry`, `budget`, `policy_denied`, `schema`, `verification`, `interrupted`, `provider`, `tool`, `resource`, `timeout`, `cancelled`, `rejected`, `approval_expired`, `no_admissible_model`, `upstream_failed` |
| Workflow run status | `running`, `waiting` (gate or approval open), `succeeded` (G2 approved and delivered or closed), `failed`, `cancelled` |
| Gate decision | `approve`, `reject` |
| Delivery action | `apply_branch`, `commit`, `push`, `export_patch` |
| Task class | `plan`, `implement`, `verify`, `summarize` (repair routes as `implement`) |
| Coder mode | `plan`, `implement`, `repair`, `summarize` (summarize is NEW, CF-43) |
| Strategy | `prefer-internal`, `quality-first`, `cost-first`, `latency-first` |
| Protocol | `anthropic-messages`, `openai-compatible` |
| Auth mode (`auth.mode`) | `none`, `api_key` (optional `header: api-key` for Azure), `gateway` with `kind: bearer | mtls` |
| Harness kind | `copilot-sdk`, `codex-app-server`, `claude-code-cli` |
| Harness run mode | `split` (Copilot), `colocated` (Codex, Claude Code) (CF-22) |
| Billing mode (usage) | `api_key`, `none`, `gateway`, `harness_subscription` |
| Vendor terms | `permitted`, `tolerated`, `personal_use_only` (NEW, CF-21), `prohibited` |
| Model error code (WRD-05 §4) | `rate_limited`, `auth_failed`, `context_too_long`, `provider_unavailable`, `content_filtered`, `invalid_request`, `tool_format_unsupported`, `model_not_found`, `timeout`, `cancelled` |
| Stop reason | `end_turn`, `tool_use`, `max_tokens`, `stop_sequence`, `content_filter` |
| Artifact type (PoC) | `plan`, `code-diff`, `test-report`, `repo-map`, `final-result`, `checkpoint` |
| Chain status (UI) | `verified`, `unverified` (not yet checked), `verifying`, `failed` |
| Routing candidate status | `chosen`, `admitted` (ranked, not chosen), `rejected`, `unhealthy` |
| Routing rejection code | `tier_not_admitted`, `capability_missing`, `context_too_small`, `denied_by_policy`, `over_budget`, `circuit_open`, `harness_not_pinned`, `harness_disabled`, `harness_locked_shared_mode`, `provider_unconfigured`, `credential_missing` |

## 4. Go packages (A02 is authoritative for detail)

`cmd/wardend`, `cmd/warden`, `cmd/warden-exec`; `internal/api`, `internal/session`, `internal/orchestrator`, `internal/agentloop`, `internal/policy`, `internal/router`, `internal/model`, `internal/providers/anthropic`, `internal/providers/openaicompat`, `internal/harness/copilot`, `internal/harness/codex`, `internal/harness/claudecode`, `internal/tools` (CF-35), `internal/sandbox` (+ `darwin_seatbelt`, `linux_bwrap`, `oci`), `internal/exec`, `internal/execproto` (NEW leaf package: executor wire types shared by daemon and executor), `internal/proxy`, `internal/secrets`, `internal/worktree`, `internal/store`, `internal/audit` (NEW: export and verify on top of store), `internal/archtest` (NEW: import-rule test). Import rules: `providers/*` and `harness/*` import only `internal/model` (plus stdlib and their SDK); `internal/exec` imports only `internal/execproto` and stdlib; nothing in `internal/` imports `apps/`.

## 5. Event envelope and registry

Envelope (JSON, canonicalized with RFC 8785 JCS for hashing):

```
{ "v": 1, "seq": 4412, "id": "evt_…", "ts": "RFC3339 ms UTC", "type": "tool.exec.end",
  "chain": "ses_…",                                   # NEW (CF-09)
  "session_id": "ses_…"|null, "workflow_run_id": "wfr_…"|null, "task_id": "tsk_…"|null, "execution_id": "exe_…"|null,
  "actor": { "kind": "user|agent|runtime|harness", "name": "coder", "version": "1.0.0", "digest": "sha256:…" },
  "user": "local:<os-user>", "workspace_id": "wsp_…"|null, "classification": "internal"|null,
  "payload": { … }, "redactions": { "count": 0, "types": [] },
  "prev_hash": "sha256:…", "hash": "sha256:…" }
```

`hash = sha256(JCS(envelope without "hash"))`; genesis `prev_hash = sha256("warden-chain-v1:" + chain)`. Payloads over 64 KiB are stored as blobs and replaced by `{ "$blob": "sha256:…", "size": n }`.

Event types used by the PoC. Chain: `S` session chain, `Y` system chain. Payload field names are fixed; A04 gives the JSON Schemas.

| Type | Chain | Emitted by | Payload fields |
|---|---|---|---|
| `runtime.start` / `runtime.stop` | Y | api | `version`, `pid`, `mode` (`personal`/`shared`), `reason` (stop) |
| `policy.reload` | Y | policy | `files[{path, layer, digest}]`, `rules_count`, `errors[]` |
| `provider.configured` (NEW) | Y | api | `provider_id`, `action` (`add`/`remove`/`enable`/`disable`/`test`), `tier`, `protocol`/`kind`, `auth_mode`, `secret_ref` (never value), `result` |
| `workspace.classification` (NEW) | Y | session | `workspace_id`, `from`, `to`, `by` |
| `session.open` / `session.resume` | S | session | `workspace_id`, `workspace_root`, `classification`, `sandbox_level`, `branch`, `base_commit`, `capabilities_summary` |
| `session.request` | S | session | `run_id`, `kind` (`change`/`readonly`), `text` (redacted), `text_hash`, `pin_model` |
| `session.close` | S | session | `reason` |
| `session.purged` | Y | store | `session_id`, `last_hash`, `event_count` |
| `budget.changed` (NEW) | S | session | `scope` (`session`), `from_usd`, `to_usd`, `by` |
| `workflow.start` | S | orchestrator | `template`, `template_version`, `inputs_hash`, `resumed_from` (`{run_id, gate_key}` or null) |
| `workflow.end` | S | orchestrator | `status`, `reason`, `final_result` (artifact id or null) |
| `workflow.gate.presented` | S | orchestrator | `gate_id`, `gate_key`, `artifacts[]` |
| `workflow.gate.resolved` | S | orchestrator | `gate_id`, `gate_key`, `decision`, `approver`, `edited_artifact` (id or null), `comment` |
| `task.state` | S | orchestrator | `task_key`, `from`, `to`, `reason`, `attempt`, `detail` |
| `routing.decision` | S | router | `routing_id`, `task_class`, `classification`, `strategy`, `pin`, `candidates[{model_id, provider_id, tier, status, reason_code, reason, quality_prior, est_cost_usd}]`, `chosen{model_id, provider_id, tier}`, `explanation`, `budget_remaining_usd` |
| `routing.fallback` | S | router | `routing_id`, `from{model_id, tier}`, `to{model_id, tier}` or null, `cause` (model error code), `fallback_count` |
| `model.call.start` | S | agentloop | `model_call_id`, `routing_id`, `model_id`, `provider_id`, `tier`, `step`, `request_hash`, `est_input_tokens` |
| `model.call.end` | S | agentloop | `model_call_id`, `stop_reason`, `usage{input_tokens, output_tokens, cached_input_tokens, reasoning_tokens, billing_mode, quota{kind, units}, estimated_cost{amount, currency, basis}}`, `latency_ms`, `ttft_ms`, `provider_request_id`, `error{code, retryable}` |
| `context.assembled` | S | agentloop | `step`, `sources[{kind, ref, trust, tokens}]`, `total_tokens`, `budget_tokens` |
| `context.compacted` | S | agentloop | `before_tokens`, `after_tokens`, `turns_summarized` |
| `policy.decision` | S | policy | `decision_id`, `call_id`, `action{tool, operation, resource, risk_class, args_redacted}`, `effect`, `reason`, `matched_rules[]`, `obligations`, `approval{approval_id, scope_max, scopes_allowed[]}` or null, `resolved_by_approval` (id or null), `cache_hit` |
| `approval.requested` | S | policy | `approval_id`, `decision_id`, `call_id`, `pattern{tool, operation, resource_pattern}`, `risk_class`, `scope_max`, `scopes_allowed[]`, `reason`, `rule_ids[]`, `display{what, who, why}`, `expires_at` |
| `approval.resolved` | S | policy | `approval_id`, `decision`, `scope`, `approver`, `grant_expires_at` |
| `approval.revoked` | S | policy | `approval_id`, `by` |
| `tool.exec.start` | S | agentloop / harness | `call_id`, `decision_id`, `tool` (`fs.read` …), `executor` (`sandbox`/`host`/`harness`), `sandbox_id`, `args_redacted` |
| `tool.exec.end` | S | agentloop / harness | `call_id`, `ok`, `exit_code`, `bytes_out`, `truncated`, `duration_ms`, `error{code, message}`, `output_ref` |
| `sandbox.create` / `sandbox.destroy` | S | sandbox | `sandbox_id`, `level`, `backend`, `purpose` (`task`/`harness`), `mounts[{host_path_hash, sandbox_path, mode, kind}]`, `limits`, `env_keys[]`, `proxy{socket, port}`; destroy: `reason`, `killed_pids`, `duration_ms` |
| `sandbox.violation` | S | sandbox / exec | `sandbox_id`, `kind` (`path_escape`/`deny_list`/`root_check`/`seatbelt_deny`/`seccomp`), `detail` |
| `proxy.connect` / `proxy.denied` | S | proxy | `sandbox_id`, `host`, `port`, `method` (`CONNECT`/`GET`/…), `rule`, `decision_id`; connect adds `bytes_up`, `bytes_down` on close; denied adds `reason`, `held_ms` |
| `secret.access` | S or Y | secrets | `ref`, `consumer` (`adapter:<provider>`/`proxy`/`checkpoint`/`delivery`), `purpose` |
| `redaction` | S | secrets | `source` (`tool_output`/`artifact`/`context`/`request_text`/`log`), `count`, `types[]` |
| `artifact.created` / `artifact.edited` | S | store | `artifact_id`, `type`, `content_hash`, `size`, `partial`, `summary`, `supersedes`; edited adds `edited_by` |
| `worktree.create` / `worktree.checkpoint` / `worktree.remove` | S | worktree | `path_hash`, `branch`, `base_commit`; checkpoint `commit`, `label`; remove `reason` |
| `harness.session.start` / `harness.hook` / `harness.session.end` | S | harness | `harness_id`, `kind`, `run_mode`, `billing_mode`, `vendor_terms`, `egress_allow[]`; hook `hook` (`pre_tool_use`/`post_tool_use`/`permission`), `harness_tool`, `call_id`; end `quota{kind, units}`, `reason` |
| `workflow.delivered` (NEW, ID-02) | S | orchestrator | `run_id`, `action`, `commit`, `branch`, `remote`, `patch_path`, `approval_id` |
| `chain.checkpoint` | S and Y | store | `chain`, `last_seq`, `last_hash`, `event_count`, `key_id`, `signature` (Ed25519 over JCS of the other fields) |

Non-persisted notifications (never hashed): `stream.delta` with `kind` = `model_text` | `model_tool_args` | `tool_output`.

## 6. Runtime API registry (A05 is authoritative for schemas)

Transport: JSON-RPC 2.0, `Content-Length` framing, Unix socket `~/.warden/run/wardend.sock` (0600). First call on every connection: `system.hello`. `NEW` = not in WRD-16 §12 or WRD-02 §4 (CF-13).

| Method | Params | Result | CLI | Notes |
|---|---|---|---|---|
| `system.hello` NEW | `token`, `client{name, version}`, `protocol` (`"warden.poc/1"`) | `daemon_version`, `protocol`, `mode`, `features[]` | (implicit) | Must be first; else `-32001` |
| `system.version` | – | `version`, `commit`, `go_version` | `warden version` | |
| `system.doctor` | – | `checks[{id, group, status (ok/warn/fail), title, detail, fix_hint, blocking}]` | `warden doctor` | |
| `system.shutdown` | `confirm: true` | `ok` | `warden daemon stop` | Confirmation required |
| `workspace.list` NEW | – | `workspaces[{workspace_id, root, classification, last_opened_at, sessions_count}]` | `warden workspaces` | Home screen |
| `workspace.setClassification` NEW | `workspace_id`, `classification`, `confirm` | `workspace_id`, `classification`, `affected_sessions[]` | `warden open <dir> --classification X` | Loosening requires `confirm: true` |
| `session.open` | `workspace` (path), `classification?` | `session_id`, `workspace_id`, `classification`, `sandbox_level`, `branch`, `capabilities_summary{text, can[], needs_approval[]}`, `resumed` | `warden open <dir>` | |
| `session.list` | `workspace_id?` | `sessions[{session_id, status, created_at, last_activity_at, cost_usd, runs}]` | `warden status --all` | |
| `session.close` | `session_id` | `checkpoint{last_hash, signature}` | `warden close` | Writes `chain.checkpoint` |
| `session.request` | `session_id`, `text`, `pin_model?` (null clears), `kind?` (`change` default / `readonly`) | `run_id` | `warden run "<text>" [--pin] [--readonly]` | Pin persists for the session |
| `session.cancel` | `session_id`, `task_id?` | `cancelled_task_ids[]`, `run_status` | `warden cancel [<task-id>]` | ≤ 5 s |
| `session.setBudget` NEW | `session_id`, `session_usd` | `session_usd`, `max_allowed_usd` | `warden budget --session <usd>` | Bounded by policy `budgets.session_usd_max` (NEW, default 2× `session_usd`) |
| `workflow.get` | `run_id` | run with `tasks[]`, `gates[]`, `artifacts[]`, `status`, `cost` | `warden status` | Polling fallback |
| `workflow.resolveGate` | `run_id`, `gate_id`, `decision`, `edited_artifact?` (plan JSON), `comment?` | `gate_id`, `decision`, `artifact_id` | `warden approve <gate-id>` / `warden reject <gate-id>` | G1 edit = `edited_artifact` |
| `workflow.deliver` NEW | `run_id`, `action`, `branch_name?`, `message?`, `remote?`, `path?` | `status` (`done`/`approval_pending`), `approval_id?`, `commit?`, `branch?`, `patch_path?` | `warden deliver <run> --apply-branch|--commit|--push|--patch` | Push → R5 approval (`once`) |
| `workflow.resume` NEW | `run_id`, `from` (`last_gate`) | `run_id` (new), `resumed_from` | `warden resume <run>` | After cancel / interrupt |
| `approval.list` | `session_id`, `status?` (`pending`/`granted`/`all`) | `approvals[]` | `warden approvals` | |
| `approval.resolve` | `approval_id`, `decision`, `scope`, `answer?` (ID-05) | `approval_id`, `decision`, `scope`, `grant_expires_at` | `warden approve <id> --scope task` / `warden reject <id>` | Scope ≤ `scope_max` |
| `session.setPin` NEW (ID-04) | `session_id`, `pin_model` (id or null) | `pin_model`, `rerouted_task_ids[]` | `warden pin <model-id>|--clear` | Pin within admission only |
| `approval.revoke` NEW | `approval_id` | `approval_id`, `revoked_at` | `warden approvals revoke <id>` | |
| `artifact.list` | `session_id` or `run_id`, `type?` | `artifacts[]` | `warden artifacts` | |
| `artifact.get` | `id` | artifact record (WRD-09 §5 shape) | `warden artifact <id>` | |
| `artifact.read` | `id`, `range?{offset, length}`, `file?` (code-diff only) | `content` (utf-8 or base64), `encoding`, `total_size`, `eof` | `warden diff <session>` | |
| `event.subscribe` | `session_id` or `"*"`, `after_seq?`, `types?` | `subscription_id`, `head_seq` | `warden status --follow` | Replays from `after_seq` then streams |
| `event.unsubscribe` NEW | `subscription_id` | `ok` | – | |
| `event.query` | `session_id`, `after_seq`, `limit` (≤ 1000), `types?` | `events[]`, `next_seq` | `warden events` | |
| `provider.list` | – | `providers[]`, `harnesses[]` with `status`, `tier`, `auth_mode`/`billing`, `vendor_terms`, `last_test` | `warden provider list` | |
| `provider.add` | `spec` (models.yaml provider entry), `secret?{value}` or `secret_files?{cert, key}`, `confirm` | `provider_id`, `test` | `warden provider add …` | Secret stored in keychain; value never echoed |
| `provider.remove` NEW | `provider_id`, `confirm` | `ok` | `warden provider remove <id>` | |
| `provider.enable` NEW | `provider_id` (harness or provider), `enabled`, `acknowledge_terms?` | `enabled`, `vendor_terms` | `warden harness enable copilot` | Refuses `prohibited` (INV-7) |
| `provider.test` | `provider_id` | `ok`, `latency_ms`, `models[{model_id, tool_calling, structured_output, streaming, max_context}]`, `error?` | `warden provider test <id>` | Capability probe |
| `provider.models` | `session_id?`, `task_class?` | `models[{model_id, provider_id, tier, admissible, reason_code, reason, capabilities, pricing, quality_prior}]` | `warden models [--session]` | Feeds ModelPicker |
| `policy.explain` | `tool`, `operation`, `resource`, `session_id?` | decision preview + `layers[{layer, rule_id, effect}]` | `warden policy explain …` | No side effects |
| `policy.list` | – | `layers[{layer, files[], rules[{id, effect, when, reason}]}]` | `warden policy list` | |
| `policy.reload` | `confirm` | `rules_count`, `errors[]` | `warden policy reload` | |
| `audit.export` | `session_id`, `with_blobs?`, `path?` | `path`, `events`, `artifacts`, `sha256` | `warden audit export --session <id>` | |
| `audit.verify` | `session_id` or `file`, `strict` | `ok`, `chain_ok`, `strict_ok`, `checkpoint_ok`, `events`, `violations[{seq, kind, detail}]` | `warden audit verify --session <id> --strict` | |
| `metrics.get` NEW | `since?`, `session_id?` | `prompts_per_task`, `time_to_first_approval_ms`, `plan_edit_rate`, `cancel_rate`, `time_to_verified_ms` | `warden report --ux` | Derived from events (B09) |

Server notifications: `event` (params = full event envelope + `subscription_id`), `stream.delta` (`subscription_id`, `session_id`, `task_id`, `kind`, `seq_hint`, `data`).

JSON-RPC application errors (`error.data.code` carries the string):

| Code | String | Meaning |
|---|---|---|
| -32001 | `unauthorized` | Missing or bad token / `system.hello` not called |
| -32002 | `not_found` | Unknown id |
| -32003 | `invalid_state` | Action not valid in current state (e.g. gate already resolved) |
| -32004 | `policy_denied` | Request refused by policy (e.g. scope above `scope_max`) |
| -32005 | `confirmation_required` | Privileged method without `confirm: true` |
| -32006 | `sandbox_unavailable` | Doctor-blocking prerequisite missing |
| -32007 | `no_admissible_model` | Router has no candidate |
| -32008 | `budget_exhausted` | Session or daily budget reached |
| -32009 | `store_unavailable` | Event write failed; runtime fails closed |
| -32010 | `unsupported_in_poc` | Feature outside PoC scope (e.g. `restricted`) |
| -32011 | `protocol_mismatch` | Client protocol version unsupported |
| -32012 | `vendor_terms` | Harness terms forbid the action (prohibited, shared-mode lock) |

## 7. Executor protocol (A06 is authoritative)

JSON-RPC over the inherited socketpair (fd 3): `exec.hello`, `exec.fs.read`, `exec.fs.list`, `exec.fs.search`, `exec.fs.write`, `exec.fs.patch`, `exec.fs.stat`, `exec.proc.spawn`, `exec.proc.io` (executor → daemon notification), `exec.proc.wait` (NEW: final status), `exec.proc.signal`, `exec.git.run`, `exec.shutdown`. Forwarder: `warden-exec proxy --listen 127.0.0.1:<port> --upstream <socket>`.

## 8. Tools (model-facing names)

| Tool id | Provider-safe name | Risk | Executor | Notes |
|---|---|---|---|---|
| `fs.read` | `fs__read` | R0 | sandbox | ≤ 2 MB, untrusted output |
| `fs.list` | `fs__list` | R0 | sandbox | |
| `fs.search` | `fs__search` | R0 | sandbox | ≤ 200 matches, untrusted output |
| `fs.write` | `fs__write` | R1 | sandbox | inactive in plan/summarize mode |
| `fs.patch` | `fs__patch` | R1 | sandbox | unified diff |
| `proc.exec` | `proc__exec` | R2 / R3 / R4 (install) | sandbox | argv only; ≤ 256 KiB output |
| `git.status` | `git__status` | R0 | sandbox | |
| `git.diff` | `git__diff` | R0 | sandbox | |
| `git.commit` | `git__commit` | R1 | sandbox | session branch only |
| `git.push` | not model-facing | R5 | host | only via `workflow.deliver` |
| `approval.request` | `approval__request` | R0 | host | → `waiting_for_input` |

Name mapping rule: replace `.` with `__`; names must match `^[a-zA-Z0-9_-]{1,64}$`; reverse mapping by table lookup; unknown names are rejected as `unknown_tool` (T-10).

## 9. Policy identifiers

Rule ids in events are layer-qualified: `invariant.INV-1` … `invariant.INV-9` (L0), `capability.not_granted`, `capability.granted`, `platform.<id>` (L1, `policy/platform-defaults.yaml`), `user.<id>` (L3, `~/.warden/policy/user.yaml`), `grant.<approval_id>` (L5).

L1 platform-default rules (A08 gives the YAML): `platform.no-shell-strings` (deny, CF-19), `platform.protected-branches` (deny commit to `main|master|release/*`), `platform.r3-default` (approval, ≤ task), `platform.r4-default` (approval, ≤ session), `platform.r5-default` (approval, `once`), `platform.harness-egress-only` (deny proxy connect outside vendor allowlist in harness tasks), `platform.harness-prohibited` (deny), `platform.personal-mode-lock` (deny `claude-code` subscription in shared mode).
L3 user rules (WRD-16 §10.6 ids): `user.reads-in-worktree`, `user.writes-in-worktree`, `user.profile-commands`, `user.package-install`, `user.other-commands`, `user.egress-other`, `user.git-commit-session-branch`, `user.git-push`, `user.harness-tolerated`.

ActionRequest resource fields used by CEL: `action.tool` (`fs`, `proc`, `git`, `proxy`, `harness`, `approval`), `action.operation`, `action.resource.kind`, `.path` (canonical absolute host path), `.rel_path` (worktree-relative), `.argv` (list), `.executable` (resolved basename), `.command_profile` (`""` if none), `.host`, `.port`, `.branch`, `.remote`, `.harness_id`, `.vendor_terms`; `action.risk_class`; `context.worktree` (host absolute), `context.workspace.{id, classification, root}`, `context.task_classification`, `context.task_egress_allow` (list of `host:port`), `context.environment` (`interactive`/`non_interactive`), `context.sandbox_level`, `context.taint.untrusted_external`, `context.task.{key, class, mode}`, `context.session_id`.

## 10. Filesystem layout (`~/.warden`, owner-only)

```
~/.warden/
  run/{wardend.sock, token, wardend.pid}
  config.yaml  models.yaml
  policy/user.yaml                       # platform-defaults.yaml ships inside the binary (embedded) and in repo policy/
  db/warden.sqlite (+ -wal, -shm)
  blobs/sha256/<aa>/<hex>
  sessions/<ulid>/{worktree/, git/, scratch/<task_key>/}
  cache/<wsp_id>/{npm,go,pip}/            # per-workspace package cache (rw into sandbox)
  exports/
  logs/wardend.log
  keys/checkpoint.pub                    # private half in keychain
```

Keychain: service `warden`; accounts `providers/<id>/api_key`, `providers/<id>/token`, `providers/<id>/client_key`, `keys/checkpoint/ed25519`. References: `secret://providers/<id>/<name>`.

## 11. UI identifiers (B-series)

Screens: `SCR-1` Workspace home, `SCR-2` Session view, `SCR-3` Plan review (G1), `SCR-4` Approval prompt, `SCR-5` Result review (G2), `SCR-6` Settings: providers and models, `SCR-7` Doctor and audit.
States: `ST-1` no provider configured, `ST-2` sandbox prerequisites missing, `ST-3` no admissible model, `ST-4` budget exhausted, `ST-5` cancelled, `ST-6` chain verification failed.
Components (B04): `AppShell`, `WorkspaceList`, `WorkspaceRow`, `RequestComposer`, `Timeline`, `TimelineEntry`, `RoutingLine`, `TaskCard`, `ToolCallRow`, `PlanCard`, `ApprovalCard`, `PendingApprovalBadge`, `GateBar`, `ContextPanel`, `DiffViewer`, `TestReport`, `CostPanel`, `StatusBadge` (variants `classification`, `sandbox`, `chain`, `tier`, `effect`, `taskState`), `ModelPicker`, `CapabilitySummaryChip`, `DoctorChecklist`, `ProviderCard`, `SetupWizard`, `Banner`, `ExplainDrawer`, `DeliveryBar`, `EventLog`.
Keyboard (fixed): `A` approve, `R` reject, `1`–`4` scope, `E` explain, `J`/`K` next/previous timeline entry, `Enter` open in context panel, `Esc` close drawer, `Mod+Enter` submit request, `Mod+.` cancel running task, `G` then `A` jump to pending approval.
Design-token names (B06 defines values; others reference names only): colors `color-bg`, `color-surface-1..3`, `color-border`, `color-border-strong`, `color-text`, `color-text-muted`, `color-text-inverse`, `color-focus`, `color-accent`, `color-effect-allow`, `color-effect-deny`, `color-effect-approval`, `color-untrusted`, `color-tier-t0..t4`, `color-state-running`, `color-state-waiting`, `color-state-succeeded`, `color-state-failed`, `color-state-cancelled`, `color-diff-add`, `color-diff-del`, `color-class-public`, `color-class-internal`, `color-class-confidential`; spacing `space-1..8`; radius `radius-sm|md|lg`; type styles `text-xs|sm|md|lg|xl`, `mono-sm|md`; elevation `elevation-1..3`; motion `motion-fast|base|slow`.

## 12. Canonical demo values (use in examples)

Workspace `ts-express-api` (`internal`), request "Add a GET /users/:id endpoint returning the user or 404, with tests.", plan on `local/qwen-coder-32b`, approval `apr_9` for `npm install` via `user.package-install` scope `workspace`, fixture baseline 40 tests, final report 43 passed, repair round after the 404 test fails, push rejected at G2. Neutrality reruns pinned to `anthropic/claude-sonnet`, `copilot`, `company/qwen-coder-32b`; classification switch to `confidential` greys out `anthropic/claude-sonnet` (T3) and `copilot` (T4) with reason code `tier_not_admitted`.

## 13. Key behavioral decisions (binding for all deliverables)

1. **Two decisions around an approval (CF-40).** `policy.decision(approval_required)` → `approval.requested` → user → `approval.resolved(approve, scope)` → PDP re-evaluates → `policy.decision(allow, resolved_by_approval: apr_…)` → `tool.exec.start`. Grant reuse later yields `policy.decision(allow)` with `matched_rules` containing `grant.apr_…`. `audit verify --strict` checks this pattern for every `tool.exec.start` (sandbox, host and harness executors alike).
2. **Effective approval scope.** `rule_scope_max` is taken from the highest-layer matching approval rule (a user rule such as `user.package-install` overrides the fallback `platform.r*-default` rules, which apply only when no higher-layer approval rule matched; ID-06). Then `scope_max_effective = min(rule_scope_max, policy.defaults.approval_scope_max, max(manifest.approvals.scopes_allowed), risk cap)`; risk cap: R5 → `once`; R3/R4 → `workspace`. Order `once < task < session < workspace`. Gates are approvals with scope `once`.
3. **Wall clock pauses** while a task is `waiting_for_approval` or `waiting_for_input` (CF-38). Pending inline approvals expire after 24 h (`approval.resolved(expire)` → task `failed(approval_expired)`); gates expire after `timeout_seconds` (86,400).
4. **Egress (CF-20).** Task allowlist = (manifest `egress.allow` ∩ policy) ∪ `egress_allow` obligations of allowed calls in this task ∪ approved egress grants in scope. A CONNECT to any other host is an ActionRequest `{tool: proxy, operation: connect, resource: {host, port}}`; `user.egress-other` → approval (≤ session). The proxy holds the pending CONNECT for up to 120 s while the approval is open; on approve it proceeds, otherwise it answers 403 and emits `proxy.denied` (the approval card stays open; approving later still records the grant for subsequent connections). DNS resolution happens in the daemon; after resolution, loopback, link-local, RFC 1918, CGNAT and IPv6 ULA destinations are denied unless the allowlist entry names that literal IP. Harness tasks: only the vendor allowlist; everything else denied without prompt (`platform.harness-egress-only`).
5. **Install flow (demo step 4).** `proc.exec ["npm","install"]` matches profile `install` → `user.package-install` → `approval_required` (scope ≤ workspace) with obligation `egress_allow: [registry hosts]`; after approval the registry hosts join the task allowlist; `postinstall` runs inside the sandbox; any other host is denied/prompted per item 4 (S3: `collector.example.net` → `proxy.denied`).
6. **Shell strings are denied (CF-19).** `platform.no-shell-strings`: `argv[0]` basename ∈ {sh, bash, zsh, dash, ksh, fish} with `-c` → deny (R6). S1 therefore shows a deny, then the model continues or stops.
7. **Routing.** Admission → filter (allow/deny lists, capabilities, context, budget, harness rules) → rank per effective strategy (CF-06) → health → select. `prefer-internal`: tiers T0/T1 whose `quality_prior[task_class] ≥ 0.5` first (prior desc, then est cost asc), then the rest by prior desc then cost. `cost-first`: known-zero cost first, then priced ascending, quota-billed last; ties by prior; candidates below threshold excluded unless nothing else. Harnesses are **pin-only** in the PoC (auto-ranking rejects them with `harness_not_pinned`). Pin overrides ranking, never admission; if the pinned model fails, fallback proceeds within same-or-lower tier and the routing line says so. Router re-checks admission before every model call, so tightening classification pauses a running task at its next step (`waiting_for_input`, reason `no_admissible_model`) if the current model became inadmissible; loosening takes effect at the next task. Task-level stickiness: one model per execution unless fallback.
8. **Harness modes (CF-22).** Copilot = `split`: harness sandbox (Copilot CLI server mode, login file ro, scratch, egress to vendor only, no worktree) + tool sandbox (`warden-exec`, worktree, no login); Copilot built-in tools excluded, runtime tools registered; every tool call → PDP → `warden-exec`. Codex, Claude Code = `colocated`: one sandbox with worktree and login file ro; the harness executes its own tools after its hook/approval callback gets an allow from the PDP; the runtime emits `tool.exec.start/end` with `executor: harness` from the pre/post hooks.
9. **Git (CF-18).** Session git dir `~/.warden/sessions/<ulid>/git` with alternates to the main repo's `objects` (mounted ro); worktree `~/.warden/sessions/<ulid>/worktree` on branch `warden/<ulid>`; hooks path `/warden/empty`, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_SYSTEM=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`. Checkpoint commits `warden: checkpoint <label>` at: session open (base), G1 approved (`gate-plan`), end of implement, end of repair, cancel (`partial`). Delivery is host-side: `apply_branch` = host `git fetch <session gitdir> warden/<ulid>:<branch_name>` with `-c core.hooksPath=/dev/null`; `commit` = squash the session diff into one commit on the session branch with the user's message (author `warden <session-id>`, committer the same); `push` = R5 host tool, approval `once`, uses the user's git credential helper on the host (the only host process that sees user credentials; never the sandbox); `export_patch` = `git format-patch` to `~/.warden/exports/`.
10. **Cancellation.** `session.cancel` → context cancel to agent loop and adapter; `exec.proc.signal TERM` to the process group; after 3 s `KILL`; `exec.shutdown`; sandbox destroyed; hard deadline 5 s from request to `task.state(cancelled)`. Partial `code-diff` stored with `partial: true`; checkpoint commit `partial` kept on ref `refs/warden/partial/<task>`; worktree preserved. `workflow.resume {from: last_gate}` creates a new run that copies the approved plan (G1 already resolved) and starts `implement` from the `gate-plan` checkpoint.
11. **Restart.** On `wardend` start: replay `task.state` per run; `running` executions → `failed(interrupted)` and re-queued if `attempt < max_attempts`; open inline approvals of interrupted executions → `approval.resolved(cancel)`; gate tasks in `waiting_for_approval` stay open; sandboxes from the previous process are killed by pid file / cgroup and `sandbox.destroy(reason: orphan)` is recorded.
12. **Verify (CF-24, CF-29, CF-31).** The runtime executes the resolved build and test profiles deterministically (each as a normal policy-checked `proc.exec` with actor `verifier`) and parses results (Vitest JSON via `npm test -- --reporter=json --outputFile=<scratch>/vitest.json`, `go test -json ./...`, pytest JSON plugin or JUnit XML). The verifier model then gets the parsed report and may read files to write `analysis` (≤ 20 steps). `verify` succeeds only if build exit 0 and tests `failed == 0`. Failure with repair rounds left → `repair-1` (coder, mode repair) → `verify-2`. `verify-2` failure → run `failed(verification)`, no G2.
13. **Budgets.** Per execution: manifest `limits`; per session `budgets.session_usd` (5); daily `budgets.daily_usd` (15); costs only where price known; local and company-hosted are 0.00 with note "infrastructure cost not tracked"; harness costs are quota units. Exhaustion: execution `failed(budget)` + `checkpoint` artifact; session budget → run `waiting`, UI state ST-4 with `session.setBudget`.
14. **Classification changes** are events on the system chain (`workspace.classification`); loosening needs `confirm: true` and an explicit confirmation dialog in the UI; `affected_sessions` returned.
15. **Shared mode (CF-21).** `config.yaml runtime.mode: personal|shared` (default `personal`); binaries built with `-tags shared` force `shared`. In shared mode `claude-code` with `billing: subscription_personal` is locked (`harness_locked_shared_mode`).
16. **Redaction** runs on tool outputs, artifacts, request text and context before persistence and before any model call; replacements `[REDACTED:<type>]`; counts in `redaction` events and envelope `redactions`.
17. **Chain status in UI** comes from `audit.verify` (on demand and automatically at `session.close` and after G2 delivery); `failed` shows ST-6.

## 14. Deliverable file names

`A01-context-and-containers.md`, `A02-wardend-components.md`, `A03-sequence-diagrams.md`, `A04-data-model.md`, `A05-runtime-api.md`, `A06-executor-and-sandbox.md`, `A07-egress-proxy.md`, `A08-policy-engine.md`, `A09-model-router.md`, `A10-agent-loop.md`, `A11-provider-adapters.md`, `A12-harness-adapters.md`, `A13-workflow-runner.md`, `A14-worktree-and-git.md`, `A15-secrets-and-redaction.md`, `A16-failure-modes-and-security-review.md`, `A17-repo-build-ci.md`, `A18-implementation-backlog.md`, `B01-information-architecture.md`, `B02-user-flows.md`, `B03-wireframes.md`, `B04-component-inventory.md`, `B05-interaction-specification.md`, `B06-visual-design.md`, `B07-content-and-microcopy.md`, `B08-frontend-architecture.md`, `B09-ux-acceptance.md`, `OPEN-QUESTIONS.md`, `README.md`.

## 15. Integration decisions (post-draft reconciliation, binding)

These decisions settle disagreements found between the first drafts. Where a deliverable's earlier text differs, this section wins.

| Id | Decision |
|---|---|
| ID-01 | **Run lifecycle at gates.** G1 reject → run `cancelled`, reason `rejected`. G2 approve ("Accept result") → `gate-final` succeeded → `chain.checkpoint(trigger: workflow_end)`, `artifact.created(final-result)` (whose `chain_checkpoint` cites that checkpoint), `workflow.end(status: succeeded)` immediately. G2 reject ("Discard result") → run `cancelled`, reason `rejected` (nothing delivered; the verified evidence stays readable). A delivery action pressed while G2 is open first resolves the gate with `approve`, then delivers. |
| ID-02 | **Delivery is post-run.** `workflow.deliver` is valid only on a run with status `succeeded` (or `export_patch` on a `failed(verification)` run, CF-24). Each delivery passes the PDP as an ActionRequest with `actor.kind: user`: `git.apply_branch` and `git.export_patch` (NEW host tools, R1, allowed by NEW rule `platform.user-delivery`), `git.commit` on the session branch (`user.git-commit-session-branch`), `git.push` (R5, `user.git-push`, approval `once`). Each emits `policy.decision` → `tool.exec.start(executor: host)` → `tool.exec.end` → NEW event `workflow.delivered {run_id, action, commit, branch, remote, patch_path, approval_id}`. |
| ID-03 | **Commit publishes the branch.** `commit` squashes the session diff into one commit on the session branch and publishes the branch into the user's repository (default name `warden/<ulid>`, editable). `apply_branch` publishes without squashing. `push` requires a prior `commit` or `apply_branch` in the same session (else `-32003 invalid_state`). |
| ID-04 | **Unblocking a paused task.** NEW method `session.setPin {session_id, pin_model | null}` sets the session pin and immediately re-routes any task of that session in `waiting_for_input` with reason `no_admissible_model` or `provider`. The runtime also re-routes automatically after `provider.configured` with an ok result, a classification change or a circuit closing. No `workflow.continue` or `task.provideInput`. |
| ID-05 | **Model questions.** Tool `approval.request` is R0 host: `policy.decision(allow)` → `tool.exec.start(executor: host)` → `approval.requested` with NEW field `kind: question` (approval kinds: `action`, `gate`, `question`) → task `waiting_for_input` → `approval.resolve {approval_id, decision, answer}` (NEW `answer`, ≤ 4,000 chars, redacted before persistence) → `approval.resolved` → `tool.exec.end` whose output is the answer (untrusted-tagged as user input). |
| ID-06 | **Approval scope precedence** as rewritten in §13.2. |
| ID-07 | **Late `once` approvals.** If an approval with scope `once` is granted after the requesting operation already gave up (a held CONNECT past 120 s, a harness hook that timed out), the grant is converted to a one-shot grant valid for the next identical action pattern in the same task within 10 minutes (`grant_expires_at` set accordingly). |
| ID-08 | **Default scope selection in the UI.** `once`, except approvals from `user.package-install`, which default to `workspace` when allowed. The consequence text of the selected scope is always visible. |
| ID-09 | **Verify without a model call when green.** If build exit is 0 and `failed == 0`, `test-report.analysis` is generated deterministically and no `routing.decision`/`model.call.*` is emitted for `verify`. The model is called only to analyse failures. |
| ID-10 | **Rejected inline approval.** Returns the task to `running`; the model receives `{ok:false, error:{code:"approval_rejected"}}` as the tool result and may adapt (S1 "continues or stops"); three identical rejections/denials end the step per WRD-04 §7.4. |
| ID-11 | **Hunk provenance.** `code-diff` metadata `files[].hunks[].provenance{task_key, task_id, execution_id, step, call_id, tool, model_call_id, attribution: call|task, contributors[]}`. The per-call write log is persisted at `~/.warden/sessions/<ulid>/attrib/<task_key>.jsonl` so attribution survives a daemon restart. |
| ID-12 | **Harness egress rule scope.** `platform.harness-egress-only` applies only to connections from a harness sandbox (`context.sandbox_purpose == "harness"`, NEW context field); tool sandboxes of split-mode tasks follow the normal egress rules. |
| ID-13 | **API additions** (A05 owns schemas): `session.request.interactive` (bool, default true; CLI `--non-interactive` sets false), `session.request.client_request_id` (idempotency), `artifact.read.side` (`base`/`head` for code-diff files), `workflow.get` returns `tasks[].limits`, `tasks[].max_attempts`, `budget{session_usd, session_spent_usd, daily_usd, daily_spent_usd}` and `repository.remotes[]`, `session.open` returns `uncommitted` (count of uncommitted changes left untouched in the user's working tree), `provider.models` accepts `classification` without a session, `workspace.list` rows include `sandbox_level` and `capabilities_summary`. `wardend --token-stdin` reads the desktop-generated token from stdin (CF-14). Subscription overflow sends NEW notification `event.gap`. |
| ID-14 | **Checkpoint triggers**: every 1,000 events per chain, `workflow_end`, `session_close`, `export`. A closed session chain accepts only `worktree.remove`, `sandbox.destroy`, `approval.revoked`, each followed by a new checkpoint (A04). |
| ID-15 | **Focus and announcements.** Incoming events never move focus; approval arrivals are announced politely with "Press G then A to review" (B05 is authoritative for behaviour; B06/B07 follow). |
| ID-16 | **Fallback never widens tier** (CF-44). When a lower-tier model fails and only higher tiers remain, the task pauses with a one-click "Continue on <model> (<tier>)", which calls `session.setPin` (ID-04). |
| ID-17 | **Approval status values.** Stored values (A04) are authoritative: approval `status` ∈ `pending`, `granted`, `rejected`, `expired`, `cancelled`, `answered`; grant `state` ∈ `active`, `armed` (late once, ID-07), `consumed`, `revoked`, `expired`. A08's state-machine names map onto these (`approved` = `granted`). |
| ID-18 | **Waiting reasons.** `waiting_for_input` with reason `no_admissible_model` when admission or filters leave no candidate (including a classification tightening mid-run); reason `provider` when admissible candidates exist but all failed and tier-bounded fallback is exhausted. `session.setPin` and automatic re-route unblock both (ID-04). |
