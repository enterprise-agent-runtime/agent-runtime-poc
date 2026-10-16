---
title: Policy Engine Specification
subtitle: Policy inputs, layers and precedence, rule syntax with CEL, effects and obligations, approvals and scopes, platform invariants, explainability, non-interactive mode, testing
docid: WRD-08
version: 0.5
status: Working specification
date: September 25, 2026
owner: Security / Architecture
audience: Security, platform teams, engineers
---

# 1. Role of the policy engine

The Policy Decision Point (PDP) is called before every tool execution, every delegation, every secret access, every harness session start and every model call (through the router's admission step). It answers with an **effect** and **obligations** and it explains itself. It is the only component that may say "yes"; every other component may only ask.

![Figure 1. Policy evaluation.](img/policy_flow.png)

# 2. Action request (input)

```
ActionRequest {
  actor:     { user, agent: {name, version, digest, trust}, task_id, execution_id, harness? }
  action:    { tool, operation, args (redacted copy), resource: {kind, path|command|host|server|model}, risk_class }
  context:   { workspace: {id, classification, root}, task_classification, taint: {untrusted_external: bool, sources[]},
               environment: interactive|non_interactive, sandbox_level, session_grants[], prior_approvals[] }
  time:      RFC3339
}
```

`args` are canonicalized (paths resolved inside the sandbox, command basenames resolved) before evaluation so that rules match reality, not the model's spelling.

# 3. Decision (output)

```
Decision {
  effect: allow | deny | approval_required
  reason: string                                  # human-readable, generated from matched rules
  matched_rules: [rule_id...]
  obligations: { sandbox_level?, timeout_seconds?, max_output_bytes?, redact_output?: bool,
                 restrict_models?: [glob], quota?: {kind, remaining}, log_full_args?: bool }
  approval?: { approvers: [session-owner|role:<r>], scope_max: once|task|session|workspace, ttl_seconds }
  ttl_seconds: int                                # how long the decision may be cached for identical requests
}
```

Effects are the only three listed. Routing fallback, scheduling deferral and quotas are not effects; they are obligations or belong to other components (fix of P-06).

# 4. Layers and precedence

| Layer | Source | May grant | May deny | May require approval |
|---|---|---|---|---|
| L0 Platform invariants | Built into the runtime | no | yes (non-overridable) | yes |
| L1 Platform defaults | Shipped policy | yes | yes | yes |
| L2 Organization bundle (Phase 3) | Control plane, signed | yes | yes | yes |
| L3 User policy | `~/.warden/policy/*.yaml` | yes (within L2) | yes | yes |
| L4 Workspace policy | `<repo>/.warden/policy.yaml` | **no** | yes | yes |
| L5 Session grants | Approvals with scope `session`/`workspace`, recorded | yes (only for actions that were approval_required) | no | no |

Combination:

1. Capability check: the action must be within the effective capability (manifest ∩ L1–L3 grants). Otherwise `deny` ("not granted").
2. L0 invariants: any violation is `deny`.
3. Collect matching rules from L1–L4. If any rule says `deny` → `deny`. Else if any says `approval_required` and no valid L5 grant covers the action → `approval_required`. Else `allow`.
4. Obligations are the union of obligations from all matching rules; conflicting numeric obligations take the most restrictive value.
5. Taint escalation: if `taint.untrusted_external` and `risk_class ≥ R5`, `allow` becomes `approval_required` and L5 grants of scope `session`/`workspace` are ignored for this action.

Workspace policy cannot grant (fix of P-20): a repository can make the runtime stricter for everyone who opens it, never more permissive.

# 5. Platform invariants (L0)

| Id | Invariant |
|---|---|
| INV-1 | No read or write of paths on the secret deny-list (WRD-10 §6), inside or outside the worktree |
| INV-2 | No write outside the task worktree from a sandboxed executor |
| INV-3 | No execution of git hooks; no modification of `.git/config`, `.git/hooks`, `.gitmodules` without R5 approval |
| INV-4 | No process may be spawned outside a sandbox by agent request |
| INV-5 | No network egress except through the proxy; no proxy rule may allow `*` in `confidential` or `restricted` workspaces |
| INV-6 | No secret value may be passed as a tool argument or environment variable to a sandbox |
| INV-7 | Harnesses with `vendor_terms: prohibited` cannot be enabled |
| INV-8 | Non-interactive mode cannot resolve `approval_required` to `allow` without a pre-recorded policy grant |
| INV-9 | A task cannot exceed manifest limits; policy may only lower them |

# 6. Rule syntax

Policy files are YAML; conditions are CEL expressions over the `ActionRequest` (`actor`, `action`, `context`, `time`).

```
apiVersion: warden.dev/v1alpha1
kind: Policy
metadata: { name: developer-local, layer: user, version: 3 }
spec:
  defaults:
    sandbox_level: L1
    approval_scope_max: session
  rules:
    - id: writes-in-worktree
      when: 'action.tool == "fs" && action.operation in ["write","patch"] && action.resource.path.startsWith(context.worktree)'
      effect: allow
    - id: test-commands
      when: 'action.tool == "proc" && action.resource.command_profile in ["node-test","go-test","python-test","lint","build"]'
      effect: allow
      obligations: { timeout_seconds: 900, max_output_bytes: 262144 }
    - id: other-commands
      when: 'action.tool == "proc" && action.resource.command_profile == ""'
      effect: approval_required
      approval: { scope_max: task }
      reason: "Command is outside the approved profiles."
    - id: package-install
      when: 'action.resource.command_profile == "install"'
      effect: approval_required
      approval: { scope_max: workspace }
      obligations: { egress_allow: ["registry.npmjs.org:443", "pypi.org:443", "files.pythonhosted.org:443", "proxy.golang.org:443"] }
    - id: egress-other
      when: 'action.tool == "proxy" && !(action.resource.host in context.task_egress_allow)'
      effect: approval_required
      approval: { scope_max: session }
    - id: push
      when: 'action.tool == "git" && action.operation == "push"'
      effect: approval_required
      approval: { scope_max: once }
    - id: protected-branches
      when: 'action.tool == "git" && action.operation == "commit" && action.resource.branch.matches("^(main|master|release/.*)$")'
      effect: deny
      reason: "Direct commits to protected branches are not allowed; use a session branch."
    - id: restricted-paths-model
      when: 'context.task_classification == "restricted"'
      effect: allow
      obligations: { restrict_models: ["local/*", "gateway/*"] }
  routing: { ... }            # see WRD-06 §9
  secrets:
    allow_refs: ["secret://providers/*"]
  harnesses:
    copilot: { enabled: true, max_classification: internal }
    codex:   { enabled: false }
  budgets: { session_usd: 25, daily_usd: 100 }
```

CEL environment: strings, lists, maps, `matches`, `startsWith`, `in`, timestamps; no network or filesystem functions; evaluation is bounded (cost limit) and side-effect free. Rule ids are stable and appear in events.

# 7. Approvals and scopes

| Scope | Meaning | Allowed for risk classes |
|---|---|---|
| `once` | This call only | all |
| `task` | Identical action pattern for the rest of the task | R3, R4 |
| `session` | Pattern for the rest of the session | R3, R4 |
| `workspace` | Pattern persisted for this workspace and user (stored under `~/.warden`, not in the repository) | R3, R4 (install and egress patterns); never R5 |

An approval record contains the canonical action pattern (tool, operation, resource pattern), scope, approver, timestamp, expiry, and the decision id it resolved. Approvals are events (WRD-09). Revocation: the user can revoke any grant from the session view; revocation is an event and takes effect on the next decision.

Approval prompt content (WRD-11): what will happen, why it needs approval (rule reason), which agent and task, the exact command or path, the risk class, and the scope selector limited by `scope_max`.

# 8. Explainability

`policy.explain(actionSpec)` returns the decision that *would* be made, with matched rules and the layer of each, without executing anything. The CLI exposes `warden policy explain --tool proc --argv "npm test"`; the desktop uses the same method in the settings screen ("What can agents do here?").

# 9. Non-interactive mode

In CI (`--non-interactive`) there is no approver. `approval_required` resolves to `deny`, unless the policy bundle contains a **pre-approval** with the same shape as an approval record (scope `workspace`, signed by an organization key in Phase 3). The run report lists every denied action so that the pipeline owner can add pre-approvals deliberately.

# 10. Caching and performance

Decisions are cached per `(execution, canonical action)` for `ttl_seconds` (default 60 s); any policy reload, approval, revocation or taint change flushes the cache. Target latency: under 2 ms per decision (CEL programs are compiled once per policy load).

# 11. Testing the policy engine

- Unit tests for combination logic with a table of (layers × effects).
- Golden tests: a corpus of `ActionRequest` JSON files with expected decisions, run in CI for the built-in policies.
- Property test: for any request, removing a workspace policy never turns `allow` into `deny` for a *stricter* result (monotonicity of restrict-only layers).
- Negative tests: every L0 invariant has a test that attempts a bypass through each entry point (tool, harness hook, MCP, delegation).
- `warden policy test <dir>` lets organizations run their own corpus against their bundle.
