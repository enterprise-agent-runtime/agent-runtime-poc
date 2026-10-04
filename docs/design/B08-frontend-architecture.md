# B08 Frontend architecture

This file specifies how the desktop app (`apps/desktop`) is built: the React application structure and routing, the typed JSON-RPC client generated from the A05 schemas, server state with TanStack Query and an event store fed by `event.subscribe`, the Tauri bridge between the webview and `wardend`, reconnection and replay, timeline virtualization, the diff viewer, i18n, testing against a mocked daemon, and performance budgets. The UI contains no agent logic and holds no privileged path to the runtime: everything it does is a JSON-RPC call that the CLI can also make (BI-6, WRD-10 T-15).

## 1. Decisions at a glance

| Topic | Decision | Section |
|---|---|---|
| Stack | React 19, TypeScript 5 (strict), Vite 6, pnpm, Node 22 for tooling | §2 |
| Routing | TanStack Router, code-based route tree, hash history | §3 |
| RPC types | Generated from A05 JSON Schemas with `json-schema-to-typescript` into a `MethodMap` and an `EventMap` | §4 |
| Server state | TanStack Query v5 for request/response; a per-session event store (`useSyncExternalStore`) with incremental projections; events patch or invalidate query caches | §5 |
| Bridge | **Option (a)**: Rust commands own the Unix socket and the token; the webview calls `invoke('rpc_call')` and receives events through a Tauri `Channel`. No localhost WebSocket. | §6 |
| Replay | Initial load via `event.query` pages, then `event.subscribe {after_seq}`; gap detection by per-chain `prev_hash` linkage; cursors kept in memory only | §7 |
| Timeline | TanStack Virtual with dynamic measurement, sticky task headers via `rangeExtractor`, sticky GateBar outside the list | §8 |
| i18n | FormatJS (`react-intl`) with ICU messages from B07, compiled at build time; `en` first, `ro` and `de` later | §9 |
| Diff viewer | Monaco diff editor (`monaco-editor`, bundled locally, lazy-loaded) | §10 |
| Tests | Vitest (unit), Testing Library + axe (component), Playwright against a mock daemon replaying recorded T1 fixtures with a deterministic clock; Rust bridge tests with tokio | §11 |
| Performance | `stream.delta` rendered within 50 ms of arrival at the bridge (N-2); other budgets in §12 | §12 |

## 2. Application structure

```
apps/desktop/
  package.json  pnpm-lock.yaml  vite.config.ts  tsconfig.json  index.html
  src-tauri/
    Cargo.toml  build.rs  tauri.conf.json
    capabilities/main.json                 # the only capability file (§6.6)
    binaries/wardend-<target-triple>       # sidecar, produced by the Go build (A17)
    src/
      main.rs  lib.rs                      # builder, plugin setup, command registration
      bridge/
        mod.rs          # Bridge struct, state machine
        codec.rs        # Content-Length framing (LSP style)
        connection.rs   # UnixStream, reader/writer tasks, request id map
        subscriptions.rs# local subscription ids, multiplexing, re-subscribe
        sidecar.rs      # daemon discovery, spawn via tauri-plugin-shell, token on stdin
        token.rs        # token generation, token file checks, zeroize
        allowlist.rs    # methods the webview may call
        commands.rs     # #[tauri::command] rpc_call, rpc_subscribe, ...
        error.rs        # BridgeError
  src/
    main.tsx                               # createRoot, providers
    app/        AppShell.tsx  router.tsx  providers.tsx  quit-guard.ts
    routes/     home.tsx  session.tsx  settings.tsx  doctor.tsx
    rpc/
      bridge.ts                            # invoke/Channel wrapper (Tauri); bridge.ws.ts (tests only, §11.4)
      client.ts                            # typed call<M>(), subscribe()
      errors.ts                            # RpcError, BridgeError mapping to B07 keys
      generated/  methods.ts  events.ts  schemas/…json   # codegen output, committed
      queries/    keys.ts  workspace.ts  session.ts  workflow.ts  approvals.ts  artifacts.ts  providers.ts  policy.ts  system.ts  audit.ts  metrics.ts
      mutations/  gates.ts  approvals.ts  delivery.ts  classification.ts  providers.ts  budget.ts  session.ts
    events/
      session-store.ts                     # normalized events + projections for one session
      global-store.ts                      # "*" subscription: badges, notifications, invalidations
      projections/  timeline.ts  tasks.ts  approvals.ts  gates.ts  routing.ts  costs.ts  tests.ts  chain.ts  artifacts.ts
      loader.ts                            # event.query paging + subscribe handoff
      linkage.ts                           # prev_hash gap detection
      cache-sync.ts                        # event → TanStack Query cache updates (§5.4)
      streams.ts                           # stream.delta buffers (not persisted)
    features/                              # one folder per screen / B04 component group
      workspace-home/  session/  timeline/  plan-review/  approval/  result-review/  diff/  settings/  doctor-audit/  setup-wizard/
    ui/
      tokens.css  icons.ts  primitives/ (Button, Chip, StatusBadge, Banner, Dialog, Drawer, Tooltip, Kbd)
      a11y/ LiveRegions.tsx  focus.ts  roving-tabindex.ts
      keyboard/ shortcuts.ts               # single key router (B05 keyboard model)
    i18n/       index.ts  messages/en.json  compiled/en.json
    prefs/      prefs.ts                   # theme, density, reduce motion, locale (localStorage)
    perf/       marks.ts                   # performance marks for §12
  scripts/      gen-rpc.ts  tokens-to-css.ts  check-contrast.py
  e2e/          playwright.config.ts  tests/  mock-daemon/  fixtures/
```

Rules enforced by ESLint (`no-restricted-imports`): `features/*` never import `rpc/bridge*` directly (only `rpc/client`, `rpc/queries`, `rpc/mutations`, `events/*`); `lucide-react` is imported only by `ui/icons.ts`; `@tauri-apps/api` only by `rpc/bridge.ts`, `app/quit-guard.ts` and the notification helper; no `dangerouslySetInnerHTML` anywhere (§6.7).

UI-only state: selection and open panels live in the URL search params (§3), so back/forward and reload restore them; expand/collapse state of task cards lives in a small in-memory map per session; user preferences (theme, density, reduce motion, locale; B06 §6.2, §9.4) live in `localStorage` under the key `warden.prefs.v1`. No daemon data is persisted by the webview.

## 3. Routing

Decision: **TanStack Router** (code-based route tree). Reasons: typed path and search params (the selected timeline entry and panel are URL state), loaders that call `queryClient.ensureQueryData` so a screen renders with data on first paint, and no dependency on file-based conventions. **Hash history** (`createHashHistory`) because the app is served from Tauri's custom protocol, where a reload of a deep path must not depend on server-side fallback.

| Route | Screen (core §11) | Search params (validated with a schema) | Loader |
|---|---|---|---|
| `/` | SCR-1 Workspace home (and ST-1 when no provider) | – | `workspace.list`, `provider.list`, `system.doctor` (cached 60 s) |
| `/sessions/$sessionId` | SCR-2 Session view; SCR-3, SCR-4, SCR-5 render inside it as context-panel views and inline cards | `entry?` (event id, task id, approval id, gate id), `view?` (`timeline` \| `plan` \| `result` \| `events`), `run?` (run id; default latest) | `session.list` entry, latest `workflow.get`, `approval.list {status: pending}`; starts the session event loader (§7) |
| `/settings` | SCR-6 Settings (tabs `providers`, `models`, `grants`, `appearance`) | `tab?` | `provider.list`, `provider.models` |
| `/doctor` | SCR-7 Doctor and audit | `session?` (audit target) | `system.doctor`, `session.list` |

SCR-3 (G1) and SCR-5 (G2) are `view=plan` and `view=result` of the session route: the gate card is in the timeline and the full review uses the context panel expanded to the review layout (B03 is authoritative for layout; ASM that B01 models them the same way). SCR-4 is the inline ApprovalCard; there is no route for it. `G` then `A` navigates to `?entry=<oldest pending approval id>`.

States ST-1 to ST-6 are not routes; they are rendered by the screen that owns them from query and projection state (ST-1 on `/` when `provider.list` is empty, ST-2 as a blocking banner in `AppShell` when `system.doctor` has a blocking sandbox check, ST-3 to ST-6 inside the session view).

## 4. Typed RPC client

### 4.1 Code generation

A05 publishes one JSON Schema per method params and result, one per event payload, and an index (`schemas/runtime-api/index.json`: method → `{params, result}` schema refs; event type → payload schema ref). ASM: A05 uses this layout under `schemas/runtime-api/` (WRD-02 §4 says the API is generated from schemas there); if A05 differs, only `gen-rpc.ts` changes.

`scripts/gen-rpc.ts` (run by `pnpm gen:rpc`, checked in CI with `git diff --exit-code`):

1. Reads the index; compiles every schema with `json-schema-to-typescript` (`strictIndexSignatures: true`, `additionalProperties: false` respected, `unreachableDefinitions: false`) into `src/rpc/generated/types.ts`.
2. Emits `methods.ts`:

```ts
// generated, do not edit
export interface MethodMap {
  'system.version':            { params: Record<string, never>; result: SystemVersionResult };
  'workspace.list':            { params: Record<string, never>; result: WorkspaceListResult };
  'workspace.setClassification': { params: WorkspaceSetClassificationParams; result: WorkspaceSetClassificationResult };
  'session.open':              { params: SessionOpenParams; result: SessionOpenResult };
  'session.request':           { params: SessionRequestParams; result: SessionRequestResult };
  'workflow.get':              { params: WorkflowGetParams; result: WorkflowGetResult };
  'workflow.resolveGate':      { params: WorkflowResolveGateParams; result: WorkflowResolveGateResult };
  'approval.resolve':          { params: ApprovalResolveParams; result: ApprovalResolveResult };   // params include answer? (ID-05)
  'session.setPin':            { params: SessionSetPinParams; result: SessionSetPinResult };       // ID-04
  'workflow.deliver':          { params: WorkflowDeliverParams; result: WorkflowDeliverResult };   // ID-02
  // … one entry per method in core §6 except system.hello (bridge-only)
}
export type Method = keyof MethodMap;
export const WEBVIEW_METHODS = [/* same keys, used by the Rust allowlist generator too */] as const;
```

3. Emits `events.ts`: a discriminated union `WardenEvent = { [K in EventType]: Envelope & { type: K; payload: EventPayloadMap[K] } }[EventType]` with the envelope fields of core §5, and `StreamDelta` for the non-persisted notification.
4. Emits `src-tauri/src/bridge/allowlist_gen.rs` with `pub const WEBVIEW_METHODS: &[&str] = &[…]` so the Rust allowlist and the TS map cannot drift.
5. Copies the schemas to `src/rpc/generated/schemas/` for Ajv validation in dev and test builds (§4.3).

### 4.2 Client surface

```ts
// rpc/client.ts
export async function call<M extends Method>(method: M, params: MethodMap[M]['params'],
                                             opts?: { timeoutMs?: number }): Promise<MethodMap[M]['result']>;

export interface Subscription { localId: number; headSeq: number; close(): Promise<void>; }
export function subscribe(params: EventSubscribeParams,
                          onMessage: (m: BridgeMessage) => void): Promise<Subscription>;

export type BridgeMessage =
  | { kind: 'events'; events: WardenEvent[] }                     // batched, ordered by seq
  | { kind: 'delta'; delta: StreamDelta; recvMs: number }          // recvMs = bridge receive time (epoch ms)
  | { kind: 'resync'; reason: 'reconnected' | 'daemon_restarted'; afterSeq: number }
  | { kind: 'gap'; afterSeq: number }                                // daemon event.gap (ID-13)
  | { kind: 'closed'; reason: string };
```

Errors are thrown as `RpcError` with `{ code: number; dataCode: string /* e.g. 'invalid_state' */; message: string; data?: unknown }` for daemon errors (core §6 table), or `BridgeError` with `kind: 'disconnected' | 'timeout' | 'not_allowed' | 'bridge'`. `errors.ts` maps both to B07 keys (`error.rpc.<dataCode>`, `failure.daemon_disconnected`).

Default timeouts (enforced in Rust, overridable per call): 30 s; `audit.verify` and `audit.export` 300 s; `provider.test` 60 s; `session.cancel` 10 s (the daemon's own deadline is 5 s).

### 4.3 Validation

In development builds and in all tests, every incoming event and every result is validated with Ajv (draft 2020-12) against the generated schemas; a mismatch logs an error with the event seq and fails the test. Production builds skip validation (the daemon is the enforcement point; the UI only displays), except for `approval.requested` and `workflow.gate.presented`, which are validated always: a malformed prompt is rendered as a generic "Approval needed" card showing the raw `reason` and the ids, never as a blank or auto-dismissed card (WRD-11 §2.3).

## 5. Server state

### 5.1 Two mechanisms

- **TanStack Query** for request/response data that has an API method: lists, records, artifacts, provider data, doctor results, audit results, metrics.
- **Event store** for everything that is a consequence of events: the timeline, task states, pending approvals, gates, routing lines, costs, test results, chain status. The event store is the source of truth for anything live; queries that overlap with it are patched from events (§5.4) so the two never disagree for long.

### 5.2 Queries

Query keys are `['rpc', method, params]` built by `keys.ts`; the query function is `call(method, params)`.

| Query | Method | `staleTime` | Kept fresh by |
|---|---|---|---|
| Workspaces | `workspace.list` | 30 s | invalidated on `workspace.classification`, `session.open`, `session.close` (global subscription) |
| Sessions | `session.list {workspace_id}` | 30 s | invalidated on `session.open`, `session.close`, `workflow.end` |
| Run | `workflow.get {run_id}` | Infinity | patched on `task.state`, `workflow.gate.*`, `artifact.created`, `workflow.end` (§5.4); refetched on resync |
| Pending approvals | `approval.list {session_id, status: 'pending'}` | Infinity | patched on `approval.requested`, `approval.resolved`, `approval.revoked`; refetched on resync |
| Grants | `approval.list {session_id, status: 'granted'}` | 60 s | invalidated on `approval.resolved`, `approval.revoked` |
| Artifact record | `artifact.get {id}` | Infinity (content-addressed, immutable) | never refetched; `gcTime` 30 min |
| Artifact content | `artifact.read {id, range?, file?, side?}` | Infinity | same; fetched lazily per file for the diff viewer |
| Artifacts | `artifact.list {run_id}` | Infinity | invalidated on `artifact.created`, `artifact.edited` |
| Providers | `provider.list` | 30 s | invalidated on `provider.configured` |
| Models for picker | `provider.models {session_id, task_class}` | 30 s | invalidated on `provider.configured`, `workspace.classification`, `budget.changed`, and on `routing.decision` with a `circuit_open` candidate |
| Doctor | `system.doctor` | 60 s | manual "Run checks"; refetched after `provider.configured` |
| Policy | `policy.list` | 5 min | invalidated on `policy.reload` |
| Explain | `policy.explain {…}` | 0 (always fresh) | on demand in ExplainDrawer |
| Audit verify | `audit.verify {session_id, strict}` | Infinity per `(session, last_seq)` | triggered on demand and automatically at `session.close` and after delivery (core §13.17) |
| Metrics | `metrics.get {session_id?, since?}` | 60 s | manual (B09) |

Global query defaults: `retry` 2 for `BridgeError.kind === 'timeout'` only, never for `RpcError`; `refetchOnWindowFocus: false` (events keep data fresh); on `resync` all queries of that session are invalidated.

### 5.3 Mutations

| Mutation | Method | Optimistic? | Confirmed by |
|---|---|---|---|
| Approve / reject approval | `approval.resolve {approval_id, decision, scope}` | No. The card shows a pending state ("Approving…", buttons disabled) until the call returns; the card flips to resolved only when `approval.resolved` arrives | `approval.resolved` event |
| Answer a question (ID-05) | `approval.resolve {approval_id, decision: 'approve', answer}` (≤ 4,000 characters, checked in the UI before sending; the runtime redacts it before persistence); "Decline" sends `decision: 'reject'` without `answer` | No; same pattern as approvals. The answer text is kept in the field until `approval.resolved` arrives, then cleared from memory | `approval.resolved` event, then `tool.exec.end` of the `approval.request` call |
| Approve / reject / edit gate | `workflow.resolveGate {run_id, gate_id, decision, edited_artifact?, comment?}` | No; same pattern. G1 reject ends the run `cancelled(rejected)`; G2 approve ("Accept result") ends it `succeeded`; G2 reject ("Discard result") ends it `cancelled(rejected)` (core §15 ID-01) | `workflow.gate.resolved`, then `workflow.end` |
| Deliver (post-run, ID-02) | `workflow.deliver {run_id, action, branch_name?, message?, remote?, confirm}`. While G2 is still open the UI first sends `workflow.resolveGate(approve)` and waits for `workflow.gate.resolved` before `workflow.deliver` ("Accept and …", B05); if the gate call fails, deliver is not sent. Push is enabled only after a `workflow.delivered` with action `commit` or `apply_branch` in the session (ID-03) | No. The DeliveryBar shows "Delivering…" until the event; `status: 'approval_pending'` for push means the ApprovalCard arrives as an event | `workflow.delivered` event (the only confirmation of a completed delivery; the RPC result alone is not shown as done) |
| Set classification | `workspace.setClassification {workspace_id, classification, confirm}` | No; B07 `confirm.class_loosen` dialog first | `workspace.classification` event |
| Submit request | `session.request {session_id, text, pin_model?, kind?, interactive: true, client_request_id}` (`client_request_id` is a ULID generated when the user presses Start and reused on any retry of the same submission, so a retry after a bridge error cannot start a second run; ID-13) | Yes: the request entry is drawn immediately in a "Submitting" state and replaced by the `session.request` event (matched by `run_id` from the result) | `session.request` event |
| Cancel | `session.cancel {session_id, task_id?}` | Yes: the task card shows "Stopping…" immediately | `task.state → cancelled` |
| Pin model / Continue on another model | `session.setPin {session_id, pin_model | null}` (ID-04): sets or clears the session pin and immediately re-routes any task of the session in `waiting_for_input` with reason `no_admissible_model` or `provider`. Used by the ModelPicker and by "Continue on {model} ({tier})" (ST-3, BN-11, ID-16). The composer no longer sends `pin_model` for a pin change | No; button disabled until confirmed | RPC result, then `routing.decision` with `pin` set and `task.state → running` for the paused task; invalidates `provider.models` |
| Budget, provider add/remove/enable/test, policy reload, resume, revoke, audit export | respective methods | No | result, then events |

No mutation is retried automatically. If a mutation fails with `BridgeError.disconnected`, the UI shows "Connection lost; the action may not have been applied" and, after reconnect and replay, the event stream shows whether it was (`approval.resolved` present or not). A second attempt on an already-resolved item returns `invalid_state` (-32003), which the UI treats as "already resolved" and refreshes. This makes approvals safe across reconnects and against simultaneous resolution from the CLI.

### 5.4 Event store and projections

One `SessionEventStore` per open session (the app keeps at most the current session and the previously viewed one in memory; others are dropped and replayed on return).

```ts
interface SessionEventStore {
  sessionId: string;
  lastSeq: number;                              // highest seq applied
  chainHead: { hash: string; seq: number } | null; // for chain `ses_<ulid>`
  events: WardenEvent[];                        // ordered by seq, append-mostly
  index: { byId: Map<string, number>; byCall: Map<string, number[]>; byTask: Map<string, number[]> };
  projections: {
    timeline: TimelineModel;    // items for §8, derived per run
    tasks: Map<string, TaskView>;        // task_key, state, reason, attempt, step, max_steps, elapsed (paused while waiting, CF-38), cost
    approvals: Map<string, ApprovalView>;// kind action | gate | question (ID-05); pending, resolved, revoked; display fields
    deliveries: Map<string, DeliveryView>;// per run: actions done (from workflow.delivered: action, commit, branch, remote, patch_path, approval_id), pending push approval; drives DeliveryBar and Push enablement (ID-02, ID-03)
    gates: Map<string, GateView>;        // presented/resolved, artifacts
    routing: Map<string, RoutingView>;   // by routing_id and by task
    costs: CostView;                     // per task, per model, totals: tokens, quota units, usd with basis
    tests: Map<string, TestReportView>;  // by artifact id
    chain: ChainView;                    // unverified/verifying/verified/failed from audit.verify + linkage state
  };
  apply(batch: WardenEvent[]): void;     // idempotent by seq; runs reducers; notifies subscribers once per batch
  subscribe(listener: () => void): () => void;
  getSnapshot(): SessionSnapshot;        // immutable snapshot for useSyncExternalStore
}
```

Each projection is a pure reducer `(state, event) => state` registered per event type; replay is a fold over events in seq order, and live updates apply the same reducers incrementally. Components read via selector hooks (`useTask(taskKey)`, `usePendingApprovals()`, `useTimelineItems(runId)`) built on `useSyncExternalStore` with memoized selectors, so a `stream.delta` or a new tool row re-renders only the affected card.

`cache-sync.ts` maps events to Query cache updates:

| Event | Cache action |
|---|---|
| `task.state`, `workflow.gate.presented`, `workflow.gate.resolved`, `workflow.end` | `setQueryData(['rpc','workflow.get',{run_id}], patch)` |
| `artifact.created`, `artifact.edited` | patch `workflow.get.artifacts`; invalidate `artifact.list`; prefetch `artifact.get` for `plan`, `test-report`, `code-diff` |
| `approval.requested` / `approval.resolved` / `approval.revoked` | patch `approval.list {pending}`; invalidate `approval.list {granted}` |
| `routing.decision` with any `circuit_open` / `unhealthy` candidate | invalidate `provider.models` |
| `budget.changed` | invalidate `provider.models`, patch cost view limits |
| `workflow.delivered` | update the `deliveries` projection; patch `workflow.get` (delivery state); invalidate `session.list` (last activity) |
| `session.close`, `workflow.delivered` | trigger `audit.verify {strict: true}` (core §13.17) |
| `event.gap` notification (ID-13: the daemon dropped events for a slow subscriber) | not an event: the store runs the §7.3 backfill from `lastSeq` |

### 5.5 Global subscription

`global-store.ts` holds one subscription `event.subscribe {session_id: "*", types: [...]}` for the whole app lifetime with `types` = `runtime.start`, `runtime.stop`, `policy.reload`, `provider.configured`, `workspace.classification`, `session.open`, `session.close`, `session.purged`, `approval.requested`, `approval.resolved`, `workflow.gate.presented`, `workflow.gate.resolved`, `workflow.end`, `workflow.delivered`. It drives cross-session concerns: the PendingApprovalBadge count across sessions, OS notifications (B05) for sessions not in view, and invalidations of home and settings queries. Being type-filtered, it is not linkage-checked (§7.3); correctness comes from refetching `approval.list` for each session with pending items after a resync.

### 5.6 Streaming deltas

`stream.delta` notifications (`kind`: `model_text`, `model_tool_args`, `tool_output`) are not persisted and never enter the event store. `streams.ts` keeps one bounded buffer per `task_id` (last 64 KiB of text per kind). The live view component mounts a `<pre>` and appends text in a `requestAnimationFrame` callback by writing to a text node through a ref, bypassing React reconciliation; React only mounts and unmounts the component. At `model.call.end` for that task the buffer is frozen; on `task.state` leaving `running` it is discarded. After a reconnect deltas are not replayed: the card shows the persisted evidence (tool rows, artifacts) and resumes live text with the next delta. ASM: A10 does not persist assistant text in events; if it adds a blob reference to `model.call.end`, the card loads it on demand.

## 6. Tauri bridge

### 6.1 Decision: option (a), Rust owns socket and token

The webview never sees the token or the socket. It calls four Tauri commands; the Rust side speaks JSON-RPC to `wardend`.

| Criterion | (a) Rust bridge (chosen) | (b) Localhost WebSocket shim with token |
|---|---|---|
| Token exposure | Token only in Rust memory (`secrecy::SecretString`, zeroized on drop); never in the JS heap, DOM, devtools or `localStorage` | Token must be in the webview's JS memory; any script injection in the webview can read it and hand it to another process, which then has full API access without the UI |
| Transport exposure | Owner-only Unix socket `~/.warden/run/wardend.sock` (0600), the boundary WRD-02 §3, §5 specifies; the OS enforces the owner | A TCP listener on loopback is reachable by every local user and by web pages in any browser on the machine (cross-site WebSocket requests are not blocked by CORS), so it needs Origin checks and still exposes a port to port scans; it adds a second transport the daemon must secure |
| T-15 (UI bypass of policy) | Bridge exposes only an allowlist of methods (§6.5) and rejects `system.hello`; even a compromised webview can do only what the UI's buttons can do, and every effect still goes through the daemon's PDP | Whoever holds the token can call any method directly |
| BI-6 | The webview is just another JSON-RPC client through a relay; no privileged path | Same, but with a wider door |
| CSP | `connect-src` limited to Tauri IPC; the webview cannot open any network connection, so injected script cannot exfiltrate | `connect-src ws://127.0.0.1:<port>` must be allowed, and the port must be discovered and passed to the webview |
| Cost | ~600 lines of Rust (codec, connection, subscriptions, sidecar) | A WebSocket server in the daemon plus auth and origin handling in Go |

Residual risk with (a): script injection in the webview could call `approval.resolve` as if the user clicked. Mitigations: the webview never renders repository content, model output or command output as HTML (§6.7), CSP forbids inline and remote script, and navigation away from the app origin is blocked. This is recorded as an accepted residual risk (A16 should list it under T-03/T-15).

### 6.2 Daemon discovery and sidecar spawn (CF-14)

On app start (`setup` hook), `sidecar.rs` runs:

1. **Attach attempt.** `lstat(~/.warden/run/wardend.sock)`: must be a socket owned by the current uid with mode 0600. `token.rs` reads `~/.warden/run/token`, requiring a regular file owned by the current uid with mode 0600 and length ≤ 256 bytes; otherwise attach is refused (error `bridge`: "unsafe permissions", B07 `doctor.socket.fail`). Connect and call `system.hello {token, client: {name: "warden-desktop", version}, protocol: "warden.poc/1"}`. On success the bridge is `Attached { owned: false }` (the daemon was started by the CLI).
2. **Spawn.** Otherwise generate a token: 32 bytes from `getrandom`, base64url without padding. Spawn the sidecar with `tauri-plugin-shell`: `app.shell().sidecar("wardend")?.args(["--token-stdin"]).spawn()`, then `child.write(format!("{token}\n").as_bytes())`. The token never appears in argv or environment (both are visible to other processes via `ps` / `/proc`). The daemon reads the first stdin line, writes the token to `~/.warden/run/token` (0600) so the CLI can attach (CF-14), and creates the socket.
3. **Wait for readiness.** Poll for the socket every 50 ms up to 5 s (N-2 cold start target is 2 s), then connect and `system.hello`. `protocol_mismatch` (-32011) moves the bridge to `Incompatible` and the UI shows `error.rpc.protocol_mismatch`.
4. **Lifecycle.** Sidecar stdout/stderr are drained and dropped (the daemon writes `~/.warden/logs/wardend.log` itself). If an owned daemon exits (`CommandEvent::Terminated`), the bridge restarts it once after 1 s and then follows §7; the daemon's restart semantics (core §13.11) apply. On app quit: if the daemon is owned and tasks are running, `quit-guard.ts` shows B07 `confirm.shutdown`; on confirm the bridge calls `system.shutdown {confirm: true}` and then lets the sidecar exit. A CLI-started daemon (`owned: false`) is never stopped by quitting the app.

### 6.3 Connection, framing and request ids

- `connection.rs`: `tokio::net::UnixStream`, split into a reader task and a writer task. The writer receives frames from an `mpsc::Sender<Bytes>` (bounded, 1024).
- `codec.rs`: a `tokio_util::codec` `Decoder`/`Encoder` for LSP framing: ASCII header lines terminated by `\r\n`, required `Content-Length: <n>`, optional `Content-Type` ignored, blank line, then `n` bytes of UTF-8 JSON. Maximum frame 16 MiB (larger payloads are blobs per core §5); a malformed header closes the connection and triggers reconnect. Property tests split frames at every byte boundary.
- Request ids: the bridge assigns `u64` ids from an `AtomicU64`; the webview never chooses ids. `pending: DashMap<u64, oneshot::Sender<Result<Value, BridgeError>>>`. A response without a matching id is logged and dropped. On disconnect all pending senders resolve with `BridgeError::Disconnected`.
- Notifications: `event`, `stream.delta` and `event.gap` (ID-13; forwarded to the webview as `BridgeMessage {kind: 'gap', afterSeq}`, which triggers the §7.3 backfill) are routed by `params.subscription_id` to the owning local subscription (§6.4). Unknown subscription ids trigger `event.unsubscribe`.

```rust
pub struct Bridge {
    state: RwLock<BridgeState>,               // Starting | Attached{owned} | Reconnecting{attempt} | Incompatible | Failed
    conn: Mutex<Option<ConnHandle>>,          // writer tx + abort handles
    pending: DashMap<u64, oneshot::Sender<Result<Value, BridgeError>>>,
    subs: DashMap<u32, SubEntry>,             // local id -> entry
    next_id: AtomicU64,
    next_local_sub: AtomicU32,
    token: SecretString,
    child: Mutex<Option<CommandChild>>,       // Some if owned
}
struct SubEntry {
    params: SubscribeParams,                  // session_id or "*", types
    daemon_sub_id: Option<String>,            // sub_…
    last_seq: u64,                            // highest seq forwarded to the webview
    channel: tauri::ipc::Channel<BridgeMsg>,
    batch: Vec<Value>, batch_deadline: Option<Instant>,
}
#[derive(Serialize)] #[serde(tag = "kind", rename_all = "snake_case")]
enum BridgeMsg { Events { events: Vec<Value> }, Delta { delta: Value, recv_ms: u64 },
                 Resync { reason: &'static str, after_seq: u64 }, Closed { reason: String } }
```

### 6.4 Commands and subscription multiplexing

```rust
#[tauri::command] async fn rpc_call(bridge: State<'_, Arc<Bridge>>, method: String, params: Value,
                                    timeout_ms: Option<u64>) -> Result<Value, BridgeError>;
#[tauri::command] async fn rpc_subscribe(bridge: State<'_, Arc<Bridge>>, params: SubscribeParams,
                                         on_message: Channel<BridgeMsg>) -> Result<SubscribeResult, BridgeError>; // {local_id, head_seq}
#[tauri::command] async fn rpc_unsubscribe(bridge: State<'_, Arc<Bridge>>, local_id: u32) -> Result<(), BridgeError>;
#[tauri::command] fn bridge_status(bridge: State<'_, Arc<Bridge>>) -> BridgeStatus;   // state, daemon_version, protocol, mode, os
#[tauri::command] async fn daemon_restart(bridge: State<'_, Arc<Bridge>>) -> Result<(), BridgeError>;
```

- `rpc_subscribe` calls `event.subscribe` on the daemon, stores the mapping local id ↔ `subscription_id`, and returns `head_seq`. Each webview subscription gets its own daemon subscription (no sharing), which keeps replay cursors independent.
- Events are forwarded in order. To cut IPC overhead during replay, the bridge coalesces `event` notifications arriving within 4 ms into one `Events` message (max 256 events). `stream.delta` is never batched and carries `recv_ms` (bridge receive time, epoch ms) for the §12 measurement.
- The bridge updates `last_seq` for each forwarded event. If `Channel::send` fails (webview reloaded or closed), the entry is removed and `event.unsubscribe` is sent.
- Bridge state changes are emitted as the Tauri event `bridge://status` for the connection indicator in `AppShell`.

### 6.5 Method allowlist

`allowlist.rs` accepts exactly `WEBVIEW_METHODS` (generated, §4.1): every method in core §6 except `system.hello`, which only the bridge sends. `event.subscribe` and `event.unsubscribe` are accepted only through `rpc_subscribe`/`rpc_unsubscribe`, not through `rpc_call`, so subscription bookkeeping cannot be bypassed. Anything else returns `BridgeError { kind: 'not_allowed' }`. The daemon remains the enforcement point for confirmations (`confirmation_required`) and policy; the allowlist only narrows the relay.

### 6.6 Tauri configuration, capabilities and CSP

`tauri.conf.json` (relevant parts):

```json
{
  "app": {
    "withGlobalTauri": false,
    "security": {
      "freezePrototype": true,
      "csp": {
        "default-src": "'none'",
        "script-src": "'self'",
        "style-src": "'self' 'unsafe-inline'",
        "font-src": "'self'",
        "img-src": "'self' data:",
        "worker-src": "'self'",
        "connect-src": "ipc: http://ipc.localhost",
        "base-uri": "'none'",
        "form-action": "'none'",
        "frame-src": "'none'",
        "object-src": "'none'"
      }
    }
  },
  "bundle": { "externalBin": ["binaries/wardend", "binaries/warden-exec"] }
}
```

`'unsafe-inline'` for styles is required by Monaco, which creates `<style>` elements at runtime; scripts stay strictly `'self'` (Tauri adds hashes for its own bundled scripts). No remote origin is allowed anywhere: fonts, icons and Monaco are bundled (B06 §5.1). Navigation is restricted in Rust with `on_navigation` returning `false` for any URL whose origin is not the app's; external links are not rendered as anchors (§6.7).

`capabilities/main.json` (window `main` only):

```json
{
  "identifier": "main",
  "windows": ["main"],
  "permissions": [
    "core:event:allow-listen", "core:event:allow-unlisten",
    "allow-rpc-call", "allow-rpc-subscribe", "allow-rpc-unsubscribe", "allow-bridge-status", "allow-daemon-restart",
    "dialog:allow-open",
    "notification:allow-notify", "notification:allow-is-permission-granted", "notification:allow-request-permission",
    "clipboard-manager:allow-write-text",
    "opener:allow-reveal-item-in-dir"
  ]
}
```

The app's own commands are declared through an app manifest in `build.rs` (`tauri_build::Attributes::new().app_manifest(AppManifest::new().commands(&["rpc_call", "rpc_subscribe", "rpc_unsubscribe", "bridge_status", "daemon_restart"]))`) so that they are denied unless a capability grants them. No `shell`, `fs`, `http`, `process` or `os` permission is granted to the webview: the sidecar is spawned from Rust. `dialog:allow-open` is used for the directory picker (Open repository) with `directory: true`; the chosen path is passed to `session.open`, and the daemon validates it (F-WS-1). `opener:allow-reveal-item-in-dir` is scoped to `$HOME/.warden/exports/**` (export result "Show in folder").

### 6.7 Rendering untrusted content

Repository files, diffs, command output, harness output and model output (including plan summaries and failure analyses written by a model that read untrusted files) are rendered as text only: React text nodes, `<pre>`, or Monaco models. No Markdown-to-HTML, no `dangerouslySetInnerHTML`, no auto-linking. URLs in such content are shown as text with a "Copy" action. This keeps prompt-injected content (WRD-10 T-03) from becoming script or navigation in the webview, and it is the main mitigation for the residual risk in §6.1. Untrusted blocks carry the B06 untrusted styling and the B07 `provenance.untrusted_caption` (BI-4 made visible).

## 7. Reconnection, replay and gap detection

### 7.1 Initial load of a session

1. `event.query {session_id, after_seq: 0, limit: 1000}` in pages until `events` is shorter than `limit`; each page is applied to the store as one batch (the timeline renders after the first page with a "Loading earlier events" row if more remain; projections are complete only after the last page, and gates/approvals are not actionable until then).
2. `rpc_subscribe {session_id, after_seq: store.lastSeq}`: the daemon replays anything written between the last page and the subscription, then streams. `apply()` ignores events with `seq <= lastSeq` (idempotent), so the overlap is harmless.

### 7.2 Cursor storage decision

`lastSeq` and the chain head (`{hash, seq}`) are kept **in memory only**: in the webview's `SessionEventStore` and, per subscription, in the Rust bridge (`SubEntry.last_seq`). They are **not** written to `sessionStorage` or `localStorage`. Reason: a cursor without the events it refers to is useless after a webview reload (the store is empty and must be rebuilt anyway), and a replay of a PoC session (a few thousand events) takes well under the §12 budget. The Rust bridge survives webview reloads but drops the reloaded page's subscriptions (their channels fail), so there is no stale state to reconcile.

### 7.3 Gap detection by chain linkage

`seq` is store-global, so a session's events are not contiguous in `seq` and gaps cannot be detected by counting. Instead, the unfiltered session subscription gives every event of chain `ses_<ulid>` in order, and each such event's `prev_hash` must equal the `hash` of the previous event of that chain (the genesis `prev_hash` for the first, core §5). `linkage.ts` checks this on apply:

- Match: advance `chainHead`.
- Mismatch: the store marks a gap, stops applying live events to projections (they are buffered), and backfills with `event.query {session_id, after_seq: chainHead.seq, limit: 1000}` until linkage is restored; buffered events are then applied. A gap that cannot be closed after one backfill (the daemon returns the same mismatch) sets the chain view to "suspect" and triggers `audit.verify {session_id, strict: true}`; a failing verify shows ST-6. The UI check is a consistency check for display, not a substitute for `audit.verify`.
- Events on the `sys` chain that arrive on the session subscription (if A05 includes them) are applied without linkage checks.

The session subscription never uses a `types` filter, so linkage always holds for complete streams.

### 7.4 Reconnect

| Step | Behavior |
|---|---|
| Detect | Reader task sees EOF or an I/O error, or a call times out and a subsequent `system.version` ping within 2 s also fails |
| Pending calls | Resolve with `BridgeError::Disconnected`; mutations are not retried (§5.3) |
| State | `Reconnecting{attempt}`; `bridge://status` event; header shows B07 `failure.daemon_disconnected` after 3 s; the timeline stays readable; action buttons for gates and approvals are disabled with a tooltip ("Reconnecting to the runtime") |
| Backoff | 100 ms, then doubling to a 5 s cap, ±20 % jitter, unbounded while the app runs. After 30 s: banner `failure.daemon_unreachable` with "Restart runtime" (`daemon_restart`: spawns the sidecar if none is running, §6.2) |
| Re-attach | Re-read the token file if not owned (a CLI-restarted daemon has a new token); `system.hello`; then for every `SubEntry`, `event.subscribe {…params, after_seq: last_seq}` and send `Resync{reason, after_seq}` to its channel |
| Webview on resync | Continue applying from the replay (idempotent by seq, linkage-checked); invalidate the session's queries (§5.2); refetch `approval.list {pending}` for every session in the global store with pending items; show `failure.daemon_reconnected` in the polite live region |
| Daemon restarted | Detected by a `runtime.start` event on the global subscription or a changed `daemon_version`/pid in `system.hello`; the timeline shows `failure.daemon_restarted` from the `task.state(failed, interrupted)` events that the daemon writes (core §13.11) |

## 8. Timeline virtualization

- `@tanstack/react-virtual` `useVirtualizer` over the flattened items of the timeline projection. Item kinds and initial `estimateSize`: `entry` 28 px (compact) / 32 px (comfortable), `task-header` 32 / 36, `tool-row` 28 / 32, `routing-line` 24 / 28, `approval-card` 232, `gate-card` 280 (G1) / 200 (G2 summary), `tests` 64, `result` 120, `banner` 56.
- Dynamic measurement: every item renders with `ref={virtualizer.measureElement}` and `data-index`; ResizeObserver-based measuring handles streaming text growth, wrapped text and 200 % zoom. `getItemKey` returns a stable id (event id, task id, approval id) so measurements survive re-flattening when a task expands.
- Anchoring: `shouldAdjustScrollPositionOnItemSizeChange` returns true for items above the viewport, so growth of an earlier card never pushes the content the user is reading (B05 "no layout jumps").
- Follow mode: when the scroll position is within 32 px of the bottom, new items keep the view pinned to the end; otherwise a "{n} new entries" pill appears (B07 `live.coalesced` text) and nothing scrolls. Follow mode is suspended while focus is inside a card.
- Sticky task headers: a custom `rangeExtractor` always includes the index of the task header that owns the first visible item; that header renders with `position: sticky; top: 0` and `elevation-2`, so the step counter, state and Cancel stay visible while scrolling long tool lists.
- Sticky gate and approval access: open gates are also summarised in the `GateBar` (sticky bottom, outside the virtual list, B03), and pending approvals are counted in the `PendingApprovalBadge` in the header; `G` then `A` and the GateBar's "Review" scroll the virtualizer to the target index (`scrollToIndex(i, {align: 'center'})`, instant under reduced motion) and move focus to the card after the next measurement frame.
- Collapsed tool calls: a task shows one `tool-group` row by default ("24 tool calls · 1 denied"); rows with `deny` or pending approval are always expanded individually. Expanding inserts rows into the flattened list; the virtualizer re-measures only the new keys.
- Keyboard: roving tabindex over item keys (`J`/`K`), independent of which items are mounted; focusing an unmounted item first scrolls it into range (B05).
- Size: tested at 10,000 events in one session (§12).

## 9. Internationalization

- Library: **FormatJS** (`react-intl` for components, `@formatjs/intl` for non-React code such as notification text). ICU MessageFormat is native to it, matching B07.
- Catalog: `src/i18n/messages/en.json` with exactly the B07 keys; `@formatjs/cli compile --ast` produces `compiled/en.json` at build time (no runtime parsing). `ro.json` and `de.json` are added later (WRD-11 §6) by translating the same keys; missing keys fall back to `en` per key.
- Rich text: `<code>` in messages renders through `values={{ code: (c) => <code className="mono">{c}</code> }}` provided globally by a `FormattedRich` wrapper.
- Formatting: numbers, currencies, lists (`{x, list, conjunction}` uses `Intl.ListFormat`), plurals and ordinals come from the `Intl` APIs of the system webview (WKWebView and WebKitGTK versions targeted by Tauri 2 support them; ASM verified in week 6). Durations and bytes use helpers in `i18n/format.ts` (B07 §15).
- Locale: `navigator.language` by default, overridable in Settings → Appearance; stored in prefs.
- Daemon strings (`reason`, `detail`, `fix_hint`, `explanation`) are English; the UI prefers catalog keys derived from ids (rule id, rejection code, error code, check id) and falls back to the daemon string (B07 §1 fallback rule).
- Lint: `eslint-plugin-formatjs` (`enforce-id`, `no-literal-string-in-jsx`), and a CI script verifying that every key used in code exists in `en.json` and every `en.json` key is used. A pseudo-locale `en-XA` (accented, +35 % length) is available in dev and used by one Playwright pass to catch truncation.

## 10. Diff viewer

### 10.1 Decision: Monaco diff editor

| Criterion | Monaco diff editor (chosen) | `react-diff-viewer-continued` |
|---|---|---|
| Performance | Virtualized rendering; diff computed in a web worker; large files stay smooth | Renders every line as DOM table rows; no virtualization; slows down on files with thousands of lines |
| Unified and side-by-side | `renderSideBySide` toggle, same instance | Supported (`splitView`) |
| Hunk provenance decorations | View zones above hunk starts (a DOM node per hunk hosting the provenance chip), glyph-margin decorations, and `getLineChanges()` to map hunks to line ranges | Only per-line gutter render callbacks; no zone above a hunk; mapping hunks to rows must be reimplemented |
| Navigation and accessibility | Built-in next/previous change (F7 / Shift+F7) and the accessible diff viewer for screen readers | Plain table; screen-reader semantics must be added by hand |
| Theming | `defineTheme` with explicit hex colours: generated from B06 tokens at build time | CSS-in-JS style overrides with CSS variables |
| Syntax highlighting | Monarch tokenizers for TS, JS, JSON, Go, Python, YAML, Markdown (loaded selectively) | Needs a separate highlighter |
| Bundle | Large (~2 to 3 MB minified for the core plus selected languages); mitigated by lazy loading on the Result review and by prefetching during `verify` | Small (tens of KB plus the diff library and emotion) |
| CSP | Needs `style-src 'unsafe-inline'` and `worker-src 'self'` (§6.6) | Emotion also injects styles at runtime; same `style-src` need |

The bundle cost is paid once, lazily, in a desktop app loaded from disk; the virtualization, provenance zones and accessible navigation are what SCR-5 needs (WRD-16 §13: "a diff viewer that links a hunk to the task and step that produced it").

### 10.2 Integration

- Package: `monaco-editor` (ESM) imported directly, not the CDN loader; the React wrapper is a thin in-house component (`features/diff/MonacoDiff.tsx`) that creates `monaco.editor.createDiffEditor` once and swaps models per file. Workers: `self.MonacoEnvironment = { getWorker: () => new EditorWorker() }` with `import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'`; no language workers (highlighting only).
- Options: `readOnly: true`, `originalEditable: false`, `renderSideBySide` from the unified/side-by-side toggle (remembered in prefs), `renderIndicators: true` (the `+`/`−` gutter of B06 §3.6), `hideUnchangedRegions: { enabled: true, contextLineCount: 3 }`, `fontFamily` and size from B06 `mono-md`, `fontLigatures: false`, `minimap: { enabled: false }`, `scrollBeyondLastLine: false`, `automaticLayout: true`, `accessibilityVerbose: true`.
- Theme: `scripts/tokens-to-css.ts` also emits `src/features/diff/monaco-themes.ts` with `warden-light` and `warden-dark`: `editor.background` = `color-surface-1`, `editor.foreground` = `color-text`, `editorLineNumber.foreground` = `color-text-muted`, `diffEditor.insertedLineBackground` = `color-diff-add`, `diffEditor.removedLineBackground` = `color-diff-del`, `diffEditor.insertedTextBackground` = `color-diff-add-word`, `diffEditor.removedTextBackground` = `color-diff-del-word`, `focusBorder` = `color-focus`. The theme switches with the app theme. A CSS rule adds the changed-word underline (B06 §3.6).
- Data: the file list comes from the `code-diff` artifact record (`files[]`, `stats`, `base_commit`, `head_commit`). For the selected file the component needs the base and head contents: `artifact.read {id, file, side: 'base' | 'head'}` (core §15 ID-13). If a file's base or head is unavailable (for example a partial diff), the component reads the file's unified hunks with `artifact.read {id, file}` and builds hunk-only original and modified documents, using Monaco's `lineNumbers` function option to show the real line numbers from the hunk headers and a separator line between hunks.
- Provenance: for each hunk the component shows a chip in a view zone above the hunk start: "`implement` · step 14 · `fs.patch`" with the tier icon of the model that produced it. Data: `code-diff` metadata `files[].hunks[].provenance{task_key, task_id, execution_id, step, call_id, tool, model_call_id, attribution: call|task, contributors[]}` (core §15 ID-11); `attribution: task` shows `diff.provenance.taskOnly`, extra `contributors` show `diff.provenance.more`, and a chip is marked unverified when no `tool.exec.end` with that `call_id` exists in the session (B04 §5). Clicking the chip (or `Enter` on it in the hunk list) navigates to `?entry=<call_id>` in the timeline. If hunk-level data is absent, provenance falls back to file level: the file list shows the tasks whose `tool.exec.*` events wrote that path (from `args_redacted.path`).
- Keyboard and screen readers: view-zone chips are not in Monaco's tab order, so a `HunkList` beside the editor lists every hunk with its provenance as focusable rows (`J`/`K` inside the list, `Enter` jumps the editor to the hunk and `Shift+Enter` opens the provenance entry). F7 / Shift+F7 work inside the editor.
- Loading: `React.lazy(() => import('./MonacoDiff'))`, prefetched with `import()` on `requestIdleCallback` when the first `verify` task starts, so G2 opens without a load delay.

## 11. Testing

### 11.1 Unit (Vitest)

- Projections: one test file per projection; every event type in core §5 has at least one reducer test; replaying the recorded T1 fixture produces snapshot-tested timeline items, task states (including paused elapsed time while waiting, CF-38), costs and approvals.
- `linkage.ts`: gap detection with fixtures that drop, duplicate and reorder events.
- `cache-sync.ts`: event → query cache patch table (§5.4).
- `errors.ts`: every core §6 error code and every WRD-05 error code maps to an existing B07 key.
- Formatting helpers and the i18n key coverage check.
- Coverage gate: 90 % lines for `events/` and `rpc/`.

### 11.2 Component (Vitest + Testing Library + axe)

- Environment `jsdom`; `vitest-axe` runs `axe` on every rendered component story in both themes (`data-theme="light"` and `"dark"`); any violation fails the test. Contrast is not checked by axe in jsdom (no layout), so B06's script is the contrast gate.
- Key component tests: ApprovalCard (scope segments disabled above `scope_max`, `A`/`R`/`1`–`4`, never auto-dismissed, pending state until `approval.resolved`), PlanCard edit and validation, ModelPicker (inadmissible rows with reason, `aria-disabled`), RoutingLine text, DiffViewer HunkList navigation (Monaco mocked), TaskCard step counter and Cancel, StatusBadge variants (icon + text present for every value).
- The render tree is checked for `dangerouslySetInnerHTML` absence by lint, and a test feeds `<img src=x onerror=…>` and `javascript:` URLs as file content, command output and plan summary and asserts they render as text.

### 11.3 Rust bridge (cargo test, tokio)

- `codec`: property tests (proptest) for framing split at every byte boundary, oversize frames, bad headers.
- `connection` and `subscriptions` against the mock daemon's Unix-socket transport: request id mapping under concurrency, reconnect with re-subscribe from `last_seq`, no duplicate forwarding, pending calls failing with `Disconnected`.
- `allowlist`: `system.hello`, `event.subscribe` via `rpc_call` and unknown methods are rejected.
- `token`: refuses token files with wrong owner or mode; a log-capture test asserts the token string never appears in any log output or error message.

### 11.4 End-to-end (Playwright against a mocked daemon)

- **Mock daemon** (`e2e/mock-daemon/`, Node + TypeScript): a JSON-RPC server with two transports, LSP framing over a Unix socket (for the Tauri smoke tests and Rust tests) and WebSocket (for browser-mode Playwright). It implements every method in core §6 with scripted results and replays recorded fixtures.
- **Fixtures** (`e2e/fixtures/*.jsonl`) are recorded from real sessions with a recording relay (`mock-daemon record --upstream ~/.warden/run/wardend.sock --out t1.jsonl`) that sits between a client and a real `wardend` and records every request, response and notification (including `stream.delta`) with relative timing. Required fixtures: `t1-internal-local` (the canonical demo of core §12: plan on `local/qwen-coder-32b`, `apr_9` install approval, one repair round, 43 passed, commit, push rejected), `t1-confidential-company` (classification switch, greyed models), `t1-cancel-resume`, `s1-injection-lab`, `st2-sandbox-missing`, `st3-no-admissible`, `st4-budget`, `st6-chain-failed`, `reconnect-mid-stream`.
- **Interaction points**: the mock pauses the replay at each `approval.requested` and `workflow.gate.presented` and resumes only when the matching `approval.resolve` or `workflow.resolveGate` call arrives, answering with the recorded result; a different decision than recorded (for example reject instead of approve) switches to a scripted branch defined in the fixture header.
- **Deterministic clock**: the mock replays on a virtual clock (default 20× speed, or step mode driven by `POST /__control/advance`), rewrites `ts` to virtual time, and recomputes `hash`/`prev_hash` with JCS + SHA-256 after rewriting so linkage checks pass (a fixture flag deliberately breaks one link for gap and ST-6 tests). Playwright installs `page.clock` at the fixture's start time so elapsed timers, 24 h expiry displays and relative timestamps are deterministic.
- **Browser mode**: the app is served by `vite preview` with `VITE_BRIDGE=ws`, which swaps `rpc/bridge.ts` for `rpc/bridge.ws.ts` (WebSocket to the mock, same `BridgeMessage` shapes). This module exists only in test builds: `vite.config.ts` aliases it only when the flag is set, and a CI check greps the production bundle for the `bridge.ws` marker string and fails if found. Runs in Playwright's WebKit (closest to both Tauri webviews) and Chromium.
- **Tauri smoke**: on Linux CI only, `tauri-driver` with WebKitWebDriver drives the real app against the mock daemon over the Unix socket: attach, T1 happy path to G2, reconnect after the mock drops the connection. (macOS WKWebView has no WebDriver; macOS is covered by browser-mode WebKit plus the Rust tests.)
- **Scenarios**: T1 happy path by mouse and by keyboard only (A, R, 1 to 4, J/K, G then A, Mod+Enter, Mod+.); classification change greying `anthropic/claude-sonnet` and `copilot` with `tier_not_admitted`; cancel within implement then resume from G1; S1 deny and `proxy.denied` visible; ST-1 to ST-6; reconnect mid-stream with no duplicated rows and caught-up timeline; `invalid_state` when a gate is resolved from the "CLI" (mock control endpoint) while the card is open.
- **Accessibility**: `@axe-core/playwright` on every screen and state in both themes at 1024 × 768 and 1440 × 900; serious and critical violations fail the run.
- **Visual snapshots** (non-blocking): SCR-2, SCR-3, SCR-4, SCR-5 in both themes at 1024 and 1440 px.

### 11.5 Static checks

TypeScript `strict`, ESLint (rules in §2, §9), stylelint (`color-no-hex` outside `tokens.css`, no `outline: none`), `pnpm gen:rpc` diff check, B06 contrast script, i18n key coverage, bundle-size check (§12).

## 12. Performance budgets

| Budget | Target | Measured by |
|---|---|---|
| N-2 UI share: `stream.delta` arrival at the bridge → text painted | ≤ 50 ms p95 (bridge forward ≤ 5 ms, IPC channel ≤ 10 ms, rAF batch ≤ 16 ms, DOM write and paint ≤ 16 ms) | `recv_ms` from the bridge vs `performance.timeOrigin + performance.now()` in the rAF callback after the write; Playwright replays 50 deltas/s and asserts p95; the dev build shows a perf overlay |
| Event → timeline row visible | ≤ 100 ms p95 | same method with the event's bridge receive time |
| `approval.requested` → ApprovalCard visible and announced in the polite live region (focus never moves automatically, ID-15) | ≤ 150 ms p95 | Playwright |
| Session load (5,000 events) → first timeline paint | ≤ 1 s; full projection ≤ 2 s | Playwright with `t1-long` fixture |
| Timeline scroll with 10,000 events | ≥ 55 fps p95, no long task > 50 ms | Chromium tracing in Playwright |
| Memory with 10,000 events | ≤ 250 MB JS heap | `performance.memory` in Chromium |
| Cold start: daemon ready → SCR-1 interactive | ≤ 1.5 s | Tauri smoke test timing |
| Initial JS bundle (excluding the Monaco chunk and fonts) | ≤ 600 KB gzip | CI size check |
| Monaco chunk | ≤ 3.5 MB minified, loaded lazily | CI size check |

The whole path from the provider's first byte to the UI is budgeted at 300 ms (N-2); the daemon side (adapter parse, notification write) owns the remaining 250 ms (A10).

## Traceability

| Design element | WRD source (doc §) | Requirement / invariant satisfied |
|---|---|---|
| React 19 + Vite + TS, TanStack Query | WRD-16 §5.1 (Desktop row), brief §2 | Fixed stack decisions |
| No agent logic in the UI; all actions via RPC client | WRD-02 §2.1, §5; brief §2 | BI-6, T-15 |
| Generated `MethodMap`/`EventMap` from A05 schemas | WRD-02 §4 ("generated from a schema"), core §5, §6 | Contract fidelity; CLI and UI share one API |
| Event store with projections, events as source of truth | WRD-11 §1.2, WRD-16 §13 screen 2, WRD-09 | One request, one timeline; audit-backed UI |
| Mutations confirmed by events, no optimistic approvals | WRD-16 §12 example stream, core §13.1 | BI-1: the UI never shows "approved" before `approval.resolved` |
| Delivery confirmed only by `workflow.delivered`; accept-then-deliver composite; push after commit or apply | core §15 ID-01, ID-02, ID-03 | BI-1 (each delivery is a policy-checked action with `actor.kind: user`) |
| `session.setPin` for pin changes and Continue on a lower tier | core §15 ID-04, ID-16 | BI-7 (fallback never widens the tier on its own) |
| Question answers via `approval.resolve.answer` | core §15 ID-05 | BI-3, BI-4 (answer redacted, tagged as user input) |
| `client_request_id` on submit, `event.gap` backfill | core §15 ID-13 | No duplicate runs; no silent event loss |
| Always-validated prompts, never auto-dismissed | WRD-11 §2.3 | Approval prompts never auto-dismiss |
| Rust bridge owning socket and token (option a) | WRD-02 §3, §5; WRD-10 T-15, T-23; CF-14 | Owner-only socket; token never in the webview; BI-6 |
| Token via stdin to the sidecar, token file 0600 | WRD-02 §3, WRD-16 §5.1; CF-14 | Token handling |
| Method allowlist, `system.hello` bridge-only | core §6 | T-15 |
| CSP, no remote origins, navigation blocked, text-only rendering of untrusted content | WRD-10 T-03, WRD-16 §7.3 | BI-4 (untrusted data never becomes code in the UI) |
| Replay via `event.query` + `event.subscribe {after_seq}` | core §6, WRD-02 §11 ("clients reconnect; sessions resume") | Reconnect without loss |
| Gap detection by `prev_hash` linkage per session chain | core §5; CF-09 | Display consistency; ST-6 trigger via `audit.verify` |
| Reconnect with backoff, resync, daemon restart handling | WRD-02 §11, core §13.11 | Daemon crash behavior visible |
| Timeline virtualization, anchoring, sticky headers, GateBar | WRD-16 §13 screen 2, WRD-11 §2.2 | Live step counter, collapsed tool calls, no layout jumps |
| Monaco diff with hunk provenance zones and HunkList | WRD-16 §13 screen 5 and design constraints; WRD-09 §5 provenance | Provenance as a first-class UI concept; H5 visible |
| FormatJS ICU catalog, en first | WRD-11 §6 | Strings externalized; ro and de later |
| Vitest, Testing Library + axe, Playwright with mock daemon and recorded fixtures, deterministic clock | WRD-16 §15 items 9, 11; WRD-01 N-7 | Repeatable UI verification; accessibility |
| `stream.delta` ≤ 50 ms budget | WRD-01 N-2 | First token within 300 ms of provider first byte |
| Local-only preferences, no telemetry | WRD-16 §2.2, WRD-11 §7 | No telemetry beyond the local cost panel |

## Deviations and assumptions

- `artifact.read.side` (`base`/`head`) is part of the API per core §15 ID-13 (proposed by this file's first draft).
- Hunk-level provenance uses the `code-diff` metadata of core §15 ID-11 (`files[].hunks[].provenance`); the file-level fallback remains for hunks without metadata.
- `wardend --token-stdin` is official per core §15 ID-13 (CF-14).
- Integration pass: `session.setPin` (ID-04) replaces pin changes through `session.request`; `workflow.delivered` (ID-02) is the confirmation of every delivery and feeds the `deliveries` projection; `approval.resolve.answer` (ID-05) is sent for question approvals; `session.request.client_request_id` and `interactive` (ID-13) are sent on every submission; `event.gap` (ID-13) triggers backfill; incoming events never move focus (ID-15).
- NEW bridge-local identifiers: Tauri commands `rpc_call`, `rpc_subscribe`, `rpc_unsubscribe`, `bridge_status`, `daemon_restart`; Tauri event `bridge://status`; `BridgeMessage` kinds; `BridgeError` kinds `disconnected`, `timeout`, `not_allowed`, `bridge`. These are internal to the desktop and never reach the daemon.
- NEW files: `scripts/gen-rpc.ts`, `src-tauri/src/bridge/allowlist_gen.rs`, `e2e/mock-daemon` with `record` mode, fixtures listed in §11.4.
- ASM: A05 publishes schemas as `schemas/runtime-api/index.json` plus one schema file per method and event payload.
- ASM: `event.subscribe {session_id}` delivers every event with that `session_id` (all on chain `ses_<ulid>`), in `seq` order, and replays from `after_seq` exclusive; `event.query` returns events with `seq > after_seq` in order.
- ASM: `event.subscribe` accepts `types` together with `session_id: "*"` (core §6 lists both params).
- ASM: assistant text is not persisted in events (A10); streamed text is a live view only (§5.6).
- ASM: in `system.hello` results, `mode` distinguishes personal and shared builds, and `features[]` is informative only; the UI does not gate features on it except `claude-code` lock display.
- ASM: tauri-plugin-shell's `CommandChild::write` is available for writing the token to the sidecar's stdin (Tauri 2.x); verified in the week-6 UI start (A18).
- DEV: WRD-16 §5.1 leaves the bridge open ("thin Rust bridge … or a WebSocket shim"); this design removes the WebSocket option from production entirely. The WebSocket transport exists only in the mock daemon and test builds.
- DEV: `refetchOnWindowFocus` is off (events keep data fresh), which differs from TanStack Query defaults.
- Quitting the app stops an owned daemon after confirmation (running tasks are interrupted and resume per core §13.11); a CLI-started daemon keeps running. OQ candidate: whether the desktop should leave its own daemon running in the background after quit.
