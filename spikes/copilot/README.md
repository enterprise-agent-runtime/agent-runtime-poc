# Copilot SDK spike (M1)

A throwaway program that answers design A12's week-1 questions about the GitHub Copilot SDK before the harness adapter is built in M5 (WRD-16 §6.2, §16.1 risk "Copilot SDK server mode or hooks differ from expectations"). It is its own Go module, so the SDK and its transitive dependencies stay out of the product's `go.mod`.

**Status (2026-10-05):** compiled against `github.com/github/copilot-sdk/go` **v1.0.16** (commit `f8ae645902b7`, released 2026-09-30, built against Copilot CLI 1.0.90). **Not executed:** the Copilot CLI is not installed on the owner's machine (`docs/DECISIONS-poc.md` D-ENV-07). Every identifier below was checked against the SDK source at that commit; behaviours that only a run can show are marked *unverified*.

## What the program does

1. Starts the Copilot runtime as a child over **stdio** (`StdioConnection`), with `Mode: ModeEmpty` (no built-in tools, no environment context, no telemetry, no OS keychain) and an explicit minimal environment.
2. Registers one custom tool, `warden_echo`, and allowlists only that tool (`AvailableTools: NewToolSet().AddCustom("warden_echo")`).
3. Installs a permission handler that approves only `warden_echo` and rejects everything else with feedback that reaches the model; a `preToolUse` hook denies any non-runtime tool name.
4. Sends one prompt asking for the echo and for a shell command (`ls /`), prints every event, and shuts down.

```bash
cd spikes/copilot
go run . -cli <path to copilot runtime or warden-exec relay shim> -home <COPILOT_HOME with a login> -cwd <empty dir>
```

**Safety.** Run it only as written: `ModeEmpty`, the allowlist, and the rejecting permission handler. Never use `copilot.PermissionHandler.ApproveAll`, never leave `Connection` nil (the env var `COPILOT_SDK_DEFAULT_CONNECTION=inprocess` would load the vendor runtime into the calling process), never use `TCPConnection` or `URIConnection` (TCP listeners, INV-J). In the product the runtime never runs on the host: `Path` points at the `warden-exec relay` shim inside the harness sandbox (M5).

## What the SDK exposes (verified from source)

| Topic | Fact |
|---|---|
| Module | `github.com/github/copilot-sdk/go`, package `copilot`; Go 1.24; requires runtime protocol version exactly 3 |
| Transport | `ClientOptions.Connection` is a sealed interface with four implementations: `StdioConnection` (default; spawns `Path` and speaks JSON-RPC on stdin/stdout), `TCPConnection` (spawns a TCP listener), `URIConnection` (dials a running server), `InProcessConnection` (loads the runtime into the process via FFI, build tag `copilot_inprocess`) |
| Framing | LSP `Content-Length` headers (same as Warden's API), handshake request `connect`, fallback `ping` |
| Server-mode flags | the SDK appends `--headless --no-auto-update [--log-level L] --stdio [--auth-token-env COPILOT_SDK_AUTH_TOKEN] [--no-auto-login] [--session-idle-timeout N]` to `Path` + `Args` |
| Environment | `ClientOptions.Env` nil passes the whole `os.Environ()` of the caller to the child; `BaseDirectory` becomes `COPILOT_HOME`; `ModeEmpty` adds `COPILOT_DISABLE_KEYTAR=1` |
| Executable | `StdioConnection.Path` as given (no `PATH` lookup, despite the README), else `COPILOT_CLI_PATH`, else an embedded bundle (native `copilot-runtime` + `runtime.node`) |
| Client | `NewClient(*ClientOptions)` (panics on invalid combinations), `Start`, `Stop` (waits up to 10 s), `ForceStop`, `CreateSession`, `GetAuthStatus`, `ListModels`, `Ping` |
| Built-ins | `Mode: ModeEmpty` + `SessionConfig.AvailableTools` (mandatory under ModeEmpty) with source-qualified names `custom:x`, `builtin:y`, `mcp:z` |
| Custom tools | `Tool{Name, Description, Parameters, OverridesBuiltInTool, SkipPermission, IsTerminal, Handler}`; `DefineTool[T,U](name, desc, func(T, ToolInvocation) (U, error))`; `ToolInvocation` carries `ToolCallID`; no SDK-side handler timeout |
| Permissions | `OnPermissionRequest func(PermissionRequest, PermissionInvocation) (rpc.PermissionDecision, error)`; kinds `custom-tool`, `shell`, `write`, `read`, `url`, `mcp`, `memory`, `hook`, `workflow`, `extension-*`; decisions `ApproveOnce`, `Reject{Feedback}`, `UserNotAvailable`, `NoResult` (plus session/location/permanent approvals Warden must never use). A nil handler leaves requests pending; an error or panic answers `UserNotAvailable` |
| Hooks | `SessionHooks{OnPreToolUse, OnPostToolUse, OnPostToolUseFailure, OnPreMCPToolCall, OnUserPromptSubmitted, OnSessionStart, OnSessionEnd, OnErrorOccurred, OnAgentStop, …}`; `PreToolUseHookInput{ToolName, ToolArgs, cwd}` has **no tool call id**; output `PermissionDecision allow|deny|ask` |
| Events | `session.On` / `SessionConfig.OnEvent`, type switch on `ev.Data`: `assistant.message_delta` (needs `Streaming: Bool(true)`), `assistant.message`, `assistant.usage`, `tool.execution_start/complete`, `external_tool.requested/completed`, `permission.requested/completed`, `hook.start/end`, `session.idle`, `session.error` (`ErrorType` authentication, authorization, quota, rate_limit, context_limit, query), `session.shutdown` |
| Sending | `Session.Send`, `SendAndWait` (silently 60 s timeout without a ctx deadline), `Abort`, `Disconnect` |
| Usage | `AssistantUsageData{InputTokens, OutputTokens, Cost (experimental model multiplier), CopilotUsage.TotalNanoAiu}`; `SessionShutdownData.TotalPremiumRequests` is marked internal |
| Extra seam | `ClientOptions.RequestHandler` (an `http.RoundTripper`) routes the runtime's model HTTP traffic through the host; if it carries Copilot auth, the harness sandbox could need no egress at all (*unverified*) |

## Differences from design A12 / WRD-16 §6.2 / A18 §4

Recorded as `docs/CONFLICTS.md` C-51. The architecture does not change: Copilot still runs in split mode inside a sandbox, built-in tools are excluded, permission and tool hooks are routed to the policy engine, and egress is vendor-only.

1. The A18 §4 sketch API (`copilot.NewSession`, `Options{ServerMode, ExcludeBuiltinTools}`, `RegisterTool`, `OnPermission`, `turn.Events()`) does not exist; the real flow is `NewClient` → `Start` → `CreateSession(SessionConfig{…})` → `SendAndWait`.
2. A12 §5.2 option A (hand the SDK an fd-4 connection) is impossible: the connection type is sealed and the JSON-RPC client is internal. Option B needs a loopback TCP listener in the daemon and is forbidden (INV-J, CONFLICTS C-08). **Option C works directly:** `StdioConnection{Path: warden-exec, Args: [relay, …]}`; the shim receives the server flags and `COPILOT_*` variables and splices stdio to the sandboxed runtime. Option D (speak the protocol directly) is possible but large. M5 implements option C.
3. Two more connection modes exist that A12 does not mention and must be locked out: `TCPConnection` and `InProcessConnection` (also selectable by environment variable when `Connection` is nil).
4. Excluding built-ins is simpler than designed: `ModeEmpty` + an allowlist; A12 §14 `ExcludeBuiltins bool` becomes `Mode` + `AvailableTools`.
5. "Deny every permission request" (A12 §5.5) would block Warden's own tools: custom tools raise `custom-tool` permission requests. Approve runtime tool names, reject the rest.
6. The pre-tool-use hook has no call id (A12 §5.4 assumes `ProviderCallID`): authorize inside the tool handler (`ToolInvocation.ToolCallID`) and use `OnPreToolUse` only to deny non-runtime names.
7. Quota: no public per-turn premium-request field; sum `assistant.usage.Cost` per turn or fall back to `1 × multiplier`, reported as unverified until observed (A12 §5.8 `premium_requests`).
8. The engine is a native `copilot-runtime` plus `runtime.node`, not a Node script; the Node toolchain mount may be unnecessary (*unverified*). `--no-auto-update` is always passed.
9. `Stop()` can take 10 s, over the 5 s cancel budget: use `ForceStop()` and let `warden-exec` TERM/KILL the engine.
10. `ModeEmpty` disables keytar, so on macOS a keychain-stored Copilot login is invisible: a file login under `COPILOT_HOME` (HX-1a) or a token is needed (CONFLICTS C-13 if it turns out keychain-only).

## Open questions for the first real run

| Question | How the program answers it |
|---|---|
| Does `Start` succeed with the real runtime, and through the relay shim? | `-cli copilot`, then `-cli warden-exec relay …` (M5) |
| Order of `preToolUse`, `permission.requested` and the tool handler | the `HOOK`, `PERMISSION`, `TOOL` log lines |
| Does the model see a shell tool at all under ModeEmpty? | no `tool.execution_start` for shell; a `PERMISSION shell DENY` line if it tries |
| Does the installed CLI accept the `custom:` allowlist prefix? | `CreateSession` succeeds |
| Does the runtime time out a slow tool handler? | add a sleep to the handler |
| How is usage reported? | `[assistant.usage] … cost=` and `[session.shutdown] premiumRequests=` |
| Does `RequestHandler` see the model calls with auth? | wrap a logging RoundTripper (follow-up) |
