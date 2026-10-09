---
title: Tool and Capability API Specification v0.1
subtitle: Tool descriptors, capability grammar, risk classes, executor protocol, built-in tools, MCP bridge, result handling
docid: WRD-04
version: 0.5
status: Working specification
date: September 25, 2026
owner: Architecture
audience: Engineers, agent authors, security
---

# 1. Concepts

- A **tool** is a named operation with typed input and output, a required capability, a risk class and an executor location.
- A **capability** is a grant: `(tool, operations[], resource scope, constraints)`. Capabilities are declared in manifests (what the agent may request) and narrowed by policy (what the environment allows).
- The **executor** performs the operation. Agent-requested operations execute inside the task sandbox through `warden-exec`. A small set of host-scoped operations (currently `git.push`, `orchestrator.delegate`, `approval.request`) execute in the daemon and always pass through approval or orchestration logic.
- A tool call flows: model proposal → agent loop → PDP → (approval) → executor → result tagging → context.

# 2. Tool descriptor

```
name: fs.write
version: 1
description: Write a UTF-8 file inside the task worktree, creating parent directories.
input_schema: schemas/tools/fs.write.input.json    # { path: string, content: string, mode?: "create"|"overwrite" }
output_schema: schemas/tools/fs.write.output.json  # { bytes_written: int, created: bool }
capability: { tool: fs, operation: write }
risk_class: R1                 # write inside worktree
side_effects: workspace        # none | workspace | host | external
idempotent: true
executor: sandbox              # sandbox | host
default_timeout_seconds: 30
untrusted_output: false        # fs.read and proc.exec set this to true
argument_redaction: [content]  # fields elided from events when policy requires
```

Descriptors are registered in `internal/tools/registry` and exposed to models in the provider's tool format with the description and input schema only.

# 3. Risk classes

| Class | Meaning | Default effect (platform policy) |
|---|---|---|
| R0 | Read within worktree or workspace | allow |
| R1 | Write within worktree | allow |
| R2 | Execute a command from an allowlisted profile inside the sandbox | allow |
| R3 | Execute an arbitrary command inside the sandbox | approval_required |
| R4 | Network egress to a domain outside the task allowlist | approval_required |
| R5 | Host effect: push, commit to protected branch, write outside worktree, send message, create ticket, secret access by name | approval_required (always; cannot be pre-approved at workspace scope) |
| R6 | Forbidden: secret paths, git hooks and config, package-manager auth files, escaping the worktree, destructive commands on the host | deny (non-overridable) |

# 4. Capability grammar

```
capability:
  tool: <tool-id>                      # fs | proc | git | test | web | mcp | orchestrator
  operations: [<op>, ...]
  # resource scope (tool-specific):
  paths: { allow: [glob...], deny: [glob...] }
  commands: { allow: [name...], profiles: [name...], deny: [name...] }
  hosts: { allow: ["host:port"...] }
  servers: { allow: [mcp-server-id...] }
  # constraints (all optional):
  cwd: <path-var>
  timeout_seconds: <int>
  max_output_bytes: <int>
  max_file_bytes: <int>
  egress: { allow: ["host:port"...] }  # for proc: domains the spawned processes may reach via proxy
  env: { allow: [VAR...] }             # environment variables passed into the process (never secrets)
  approval: { required: <bool>, scope_max: once|task|session|workspace }
```

Matching rules:

1. Globs are POSIX-like (`**` crosses directories); paths are canonicalized and symlinks resolved **inside the sandbox** before matching (protects against `../` and symlink escapes).
2. `deny` wins over `allow` inside a capability; the platform deny-list wins over everything.
3. `commands.allow` matches the executable basename after resolution inside the sandbox; profiles expand to sets maintained by policy (for example `node-test` = `npm test`, `pnpm test`, `npx vitest`, `npx jest`).
4. Effective capability = manifest capability ∩ organization ∩ user ∩ workspace(restrict-only) ∩ session grants.

# 5. Executor protocol (`warden-exec`)

`wardend` launches `warden-exec` as PID 1 (Linux L1 with PID namespace) or as the sandboxed child (macOS) and speaks JSON-RPC over a socketpair.

| Method | Purpose |
|---|---|
| `exec.hello` | Report executor version, platform, mounted paths |
| `exec.fs.read/list/search/write/patch/stat` | Filesystem operations, always canonicalized under allowed roots |
| `exec.proc.spawn` | Spawn a process with argv, cwd, env allowlist, resource limits; returns a handle |
| `exec.proc.io` | Stream stdout/stderr chunks (bounded by `max_output_bytes`, truncated with a marker) |
| `exec.proc.signal` | TERM/KILL a handle |
| `exec.git.*` | Git operations executed with `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_SYSTEM=/dev/null`, `core.hooksPath=/warden/empty` |
| `exec.shutdown` | Terminate all children and exit |

`warden-exec` re-checks path roots and command allowlists it was launched with; it is a second line of defense, not the PDP.

# 6. Built-in tools (MVP)

| Tool | Operations | Risk | Notes |
|---|---|---|---|
| `fs` | `read`, `list`, `search`, `write`, `patch`, `stat` | R0/R1 | `patch` applies unified diffs; `search` is ripgrep-style with result caps |
| `proc` | `exec` | R2/R3 | Output truncated at `max_output_bytes` (default 256 KiB); stdin closed |
| `git` | `status`, `diff`, `log`, `commit`, `worktree` (internal), `push` (host, R5) | R0–R5 | `commit` inside worktree is R1 on session branches, R5 on protected branches |
| `test` | `run` | R2 | Alias of `proc.exec` with the `test` profile and structured result parsing (JUnit, Go test JSON, pytest JSON) |
| `orchestrator` | `delegate`, `report_progress` | host | Governed by `spec.delegation` |
| `approval` | `request` | host | Lets an agent ask the user a question or request a scope; produces `waiting_for_input` |

Phase 2 additions: `web.fetch` (through proxy, allowlist, content sanitized), `mcp.<server>.<tool>`, `browser.*`, `db.query`.

# 7. Result handling

1. Every result is wrapped as `{tool, call_id, ok, output, truncated, provenance:{source, path|command, sandbox_id}, trust: "untrusted"}` before it reaches the model.
2. Outputs pass the secret scanner; matches are replaced by `[REDACTED:<type>]` and counted in a `redaction` event.
3. Outputs from `web`, `mcp` and package contents set the task **taint** flag (`untrusted_external: true`), which escalates R5 actions to approval for the remainder of the task even if a session-scope approval exists.
4. Denied calls return `{ok:false, error:{code:"policy_denied", reason, rule_ids}}`; the model may adapt. Repeated identical denials (3) end the step with a `blocked_by_policy` note in the transcript and prompt the user.

# 8. MCP bridge (Phase 2)

- MCP servers are declared in policy or workspace configuration with `id`, `transport` (`stdio` or `http`), `trust` (`internal`, `vendor`, `community`), and a per-tool grant list.
- `stdio` servers run **inside the task sandbox**; `http` servers are reached only through the proxy allowlist.
- Tool descriptions and schemas from a server are captured at first use, hashed, and shown to the user; a change in description or schema requires re-approval (protects against tool-description poisoning).
- MCP tool outputs are always `untrusted_output: true` and set the taint flag.
- An MCP tool is presented to the model as `mcp.<server>.<tool>` and requires a capability `{tool: mcp, servers: {allow: [id]}, operations: [<tool>]}`.

# 9. Command profiles (platform-maintained)

| Profile | Commands |
|---|---|
| `node-build` | `npm run build`, `pnpm build`, `yarn build`, `npx tsc`, `npx vite build` |
| `node-test` | `npm test`, `pnpm test`, `npx vitest`, `npx jest`, `npx playwright test` |
| `go-build` / `go-test` | `go build ./...`, `go vet ./...`, `go test ./...` |
| `python-test` | `pytest`, `python -m pytest`, `python -m unittest` |
| `java-build` / `java-test` | `mvn -q -B compile`, `mvn -q -B test`, `./gradlew build`, `./gradlew test` |
| `lint` | `npx eslint .`, `golangci-lint run`, `ruff check .`, `semgrep --config auto` |

Package installation (`npm install`, `pip install`, `go mod download`) is a separate profile `install` that is R4 (egress) and requires one approval per workspace; subsequent installs in the same workspace reuse the approval and the allowlisted registry hosts.

# 10. Tool call examples

Model proposal (provider-neutral, as seen by the agent loop):

```
{ "type": "tool_use", "id": "call_17", "name": "proc.exec",
  "input": { "argv": ["npm", "test", "--", "auth"], "cwd": "${worktree}" } }
```

PDP decision event (abridged):

```
{ "type": "policy.decision", "call_id": "call_17", "effect": "allow",
  "reason": "npm test matches profile node-test; cwd inside worktree",
  "matched_rules": ["platform.commands.profiles", "workspace.profiles.node"],
  "obligations": { "timeout_seconds": 600, "max_output_bytes": 262144 } }
```

Result as appended to context:

```
{ "type": "tool_result", "call_id": "call_17", "ok": true, "trust": "untrusted",
  "output": { "exit_code": 1, "stdout": "... 3 failed, 41 passed ...", "stderr": "" , "truncated": false },
  "provenance": { "sandbox_id": "sb-9f2", "command": "npm test -- auth" } }
```
