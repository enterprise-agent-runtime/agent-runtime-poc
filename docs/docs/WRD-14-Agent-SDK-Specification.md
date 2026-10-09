---
title: Agent SDK Specification
subtitle: Declarative agents first, the Agent Protocol for coded agents, SDK surface in TypeScript, Go and Python, packaging, testing, compatibility
docid: WRD-14
version: 0.5
status: Working specification (Phase 4 deliverable; contracts fixed now)
date: September 25, 2026
owner: Architecture
audience: Engineers, agent authors, partners
---

# 1. Two ways to build an agent

| Kind | When | What you write | Where it runs |
|---|---|---|---|
| Declarative (MVP) | Most agents | Manifest, prompts, schemas, evals | No code; the runtime's agent loop executes it |
| Coded (Phase 4) | Deterministic logic, custom state machines, domain adapters | The same package plus an implementation speaking the Agent Protocol | **Inside the task sandbox**; the implementation calls back into the runtime for models and tools |

The second answers the open question of the drafts ("where does agent code run?"): never inside the runtime process. A coded agent is just another sandboxed process; the runtime remains the Policy Decision Point and the only holder of credentials.

# 2. Agent Protocol

JSON-RPC 2.0 over stdio between `warden-exec` (proxying to the daemon) and the agent process. Methods are namespaced.

## 2.1 Runtime → agent

| Method | Purpose |
|---|---|
| `agent.initialize` | Manifest, task input, artifact summaries, limits, protocol version |
| `agent.run` | Start; the agent returns when done with `{output, artifacts[]}` |
| `agent.cancel` | Cooperative cancellation |
| `agent.input` | Answer to a question raised with `runtime.ask` |

## 2.2 Agent → runtime

| Method | Purpose |
|---|---|
| `runtime.model.generate` | Canonical Model API request (WRD-05); the runtime routes, calls, streams back events; tools in the request are limited to the agent's grants |
| `runtime.tool.call` | Tool call → PDP → executor; returns the tagged result |
| `runtime.artifact.write/read` | Store and fetch artifacts with provenance filled by the runtime |
| `runtime.ask` | Ask the user (yields `waiting_for_input`) |
| `runtime.delegate` | Propose a child task (subject to `spec.delegation`) |
| `runtime.log` | Structured log lines attached to the execution |
| `runtime.progress` | Progress for the timeline |

Everything the agent does is still an event; budgets are enforced by the runtime regardless of what the agent process does; exceeding them terminates the process.

# 3. SDK surface (TypeScript shown; Go and Python mirror it)

```
import { defineAgent, type AgentContext } from "@warden/sdk";

export default defineAgent({
  async run(ctx: AgentContext) {
    const plan = await ctx.artifacts.read("plan");
    const files = await ctx.tools.call("fs.search", { pattern: "TODO", paths: ["src/**"] });
    const res = await ctx.model.generate({
      messages: [{ role: "user", content: [{ type: "text", text: `Summarize: ${files.output.matches.length} TODOs` }] }],
      response_format: { type: "json_schema", schema: ctx.schemas.output },
    });
    await ctx.artifacts.write("todo-report", res.json);
    return { summary: `Found ${files.output.matches.length} TODOs` };
  },
});
```

Helpers: typed tool wrappers generated from descriptors, schema validation, retry helpers for retryable model errors (bounded by runtime budgets), test harness (`@warden/sdk/testing`) that runs an agent against a fixture with a fake model or a recorded model transcript.

# 4. Packaging coded agents

```
manifest.yaml
  spec.implementation:
    kind: process
    runtime: node@22 | python@3.12 | go-binary
    entry: dist/agent.js
    image: ghcr.io/acme/todo-agent@sha256:...   # optional L2 image with dependencies
```

The implementation's dependencies are part of the package digest; for Node and Python the runtime uses a locked install in a package cache built inside a sandbox; for Go a static binary. Packages are signed like declarative ones.

# 5. Testing

- `warden agent test ./my-agent` runs the package's evals with a fake model (deterministic transcripts) and with real models when configured.
- Contract tests verify that the agent never attempts a tool outside its manifest (any such attempt is logged and fails the test).
- Golden transcripts for regression.

# 6. Compatibility

`protocol_version` negotiated at `agent.initialize`; the runtime supports the current and previous major version; SDKs pin to a protocol version and warn on mismatch.

# 7. Relationship to external harnesses

External harness adapters (WRD-05 §9) are implemented as built-in coded agents speaking the Agent Protocol on the runtime side and the vendor's SDK on the other side, inside the sandbox. This keeps vendor engines under the same contract as any other coded agent.
