---
title: Artifact, Event, and Audit Schema
subtitle: Event envelope and taxonomy, hash chain and checkpoints, artifact records and types, provenance model, cost and usage records, storage layout, retention and export
docid: WRD-09
version: 0.5
status: Working specification
date: September 25, 2026
owner: Architecture
audience: Engineers, security, compliance
---

# 1. Principles

1. Everything important is an event; events are append-only and hash-chained.
2. Every output worth keeping is an artifact; artifacts are content-addressed and carry provenance.
3. Secrets never enter events or artifacts; redaction happens before persistence and is itself recorded.
4. Schemas are versioned and published as JSON Schema under `schemas/events/` and `schemas/artifacts/`.

![Figure 1. Core data model.](img/data_model.png)

# 2. Event envelope

```
{
  "v": 1,
  "seq": 4412,                            # monotonically increasing per store
  "id": "evt_01J8Y...",                   # ULID
  "ts": "2026-09-25T10:14:03.214Z",
  "type": "tool.exec.end",
  "session_id": "ses_...", "workflow_run_id": "wfr_...", "task_id": "tsk_...", "execution_id": "exe_...",
  "actor": { "kind": "agent", "name": "coder", "version": "1.0.0", "digest": "sha256:..." },
  "user": "local:robert",
  "workspace_id": "wsp_...",
  "classification": "confidential",
  "payload": { ... },                     # type-specific, schema-validated
  "redactions": { "count": 0, "types": [] },
  "prev_hash": "sha256:7a0...",
  "hash": "sha256:9c1..."                 # sha256(canonical_json(envelope without hash))
}
```

# 3. Event taxonomy

| Family | Types | Payload highlights |
|---|---|---|
| session | `session.open`, `session.resume`, `session.close`, `session.request` | workspace, request text (classification-aware) |
| workflow | `workflow.start`, `workflow.end`, `workflow.gate.presented`, `workflow.gate.resolved` | template, plan artifact id, decision |
| task | `task.state` | from, to, reason, attempt |
| routing | `routing.decision`, `routing.fallback` | candidates with statuses, chosen, strategy, estimated cost |
| model | `model.call.start`, `model.call.end` | provider, model, request hash, usage, latency, stop reason, provider request id |
| policy | `policy.decision`, `policy.reload` | ActionRequest (redacted), effect, matched rules, obligations |
| approval | `approval.requested`, `approval.resolved`, `approval.revoked` | pattern, scope, approver, expiry |
| tool | `tool.exec.start`, `tool.exec.end` | tool, args (redacted per descriptor), exit code, bytes, truncated, duration |
| sandbox | `sandbox.create`, `sandbox.destroy`, `sandbox.violation` | level, backend, mounts summary, violation kind |
| proxy | `proxy.connect`, `proxy.denied` | host:port, task, rule |
| secret | `secret.access`, `secret.injected` | ref (never value), consumer (adapter or proxy) |
| context | `context.assembled`, `context.compacted`, `redaction` | sources with provenance, token counts, redaction counts |
| artifact | `artifact.created`, `artifact.edited` | id, type, hash, size, provenance summary |
| worktree | `worktree.create`, `worktree.checkpoint`, `worktree.merge`, `worktree.conflict`, `worktree.remove` | branch, commit ids |
| harness | `harness.session.start`, `harness.hook`, `harness.session.end` | harness id, billing mode, quota units |
| eval | `eval.run.start`, `eval.case.result`, `eval.run.end` | suite, case, graders, metrics |
| system | `runtime.start`, `runtime.stop`, `chain.checkpoint`, `store.integrity` | version, checkpoint signature |

The `payload` schema for each type is in `schemas/events/<type>.json`; unknown types are rejected.

# 4. Hash chain and checkpoints

![Figure 2. Hash chain and provenance.](img/provenance_chain.png)

- `hash = SHA-256(canonical JSON of the envelope without hash)`; `prev_hash` is the previous event's hash in the same store (genesis uses a fixed constant).
- Every 1,000 events and at session close, a `chain.checkpoint` event records the last hash signed with the runtime's local key (generated at first start, stored in the keychain). In Phase 3 the organization key co-signs checkpoints during sync.
- `warden audit verify` recomputes the chain and validates checkpoints; the desktop shows chain status in the session view.
- Tamper evidence, not tamper proofing: a local attacker with the keychain can rewrite history. Central sync (Phase 3) removes that limitation.

# 5. Artifact record

```
{
  "id": "art_01J8Y...",
  "type": "code-diff",                       # registered type
  "schema": "schemas/artifacts/code-diff.v1.json",
  "content_hash": "sha256:1a2b...",
  "size_bytes": 18422,
  "media_type": "text/x-diff",
  "uri": "blob://sha256/1a2b...",            # content-addressed store
  "summary": "14 files changed, +612 -38; adds OAuth provider abstraction ...",
  "partial": false,
  "version": 1, "supersedes": null,
  "classification": "confidential",
  "provenance": {
    "session_id": "...", "workflow_run_id": "...", "task_id": "...", "execution_id": "...",
    "agent": { "name": "coder", "version": "1.0.0", "digest": "sha256:..." },
    "runtime_version": "0.4.0",
    "model": { "id": "anthropic/claude-sonnet", "provider": "anthropic", "tier": "T3", "routing_event": "evt_..." },
    "inputs": ["art_plan_...", "art_repomap_..."],
    "tool_calls": 37, "policy_decisions": 41, "approvals": ["apr_..."],
    "worktree": { "branch": "warden/ses_.../implement-backend", "base": "c0ffee..", "head": "beef.." },
    "created_at": "2026-09-25T10:31:00Z", "created_by": "agent"
  }
}
```

# 6. Registered artifact types (MVP)

| Type | Media | Schema highlights |
|---|---|---|
| `repo-map` | JSON | languages, build/test commands detected, key directories, conventions |
| `plan` | JSON | WRD-07 §9 |
| `security-notes`, `security-report` | JSON | findings: id, severity, confidence, location, evidence, remediation, blocking |
| `code-diff`, `integrated-diff` | unified diff + JSON metadata | files, stats, base and head commits |
| `build-report`, `test-report` | JSON | profile, exit code, counts, failures with messages, duration |
| `failure-analysis` | JSON | root cause, affected files, proposed fix tasks |
| `merge-conflict` | JSON | files, hunks, resolution options |
| `checkpoint` | JSON | reason (budget, cancel), state summary, next steps |
| `execution-trace` | JSON Lines | filtered events for a task, generated on demand |
| `cost-report` | JSON | per task and model: tokens, quota units, estimated cost |
| `final-result` | JSON | summary, artifacts, verification status, cost, chain checkpoint |

Custom types are registered by agent packages under a namespace (`acme.design-spec`).

# 7. Usage and cost records

`model.call.end.payload.usage`:

```
{ "input_tokens": 12873, "output_tokens": 1450, "cached_input_tokens": 9800, "reasoning_tokens": 0,
  "billing_mode": "api_key", "estimated_cost": { "amount": 0.061, "currency": "USD", "basis": "catalog-2026-09" } }
```

For harnesses: `{ "billing_mode": "harness_subscription", "quota": { "kind": "premium_requests", "units": 1 } }` and `estimated_cost: null`. Session and daily aggregates are computed from events, not stored separately, so that the chain remains the single source of truth.

# 8. Storage layout

```
~/.warden/
  run/                 # socket, token, pid
  config.yaml          # daemon settings
  models.yaml          # provider and model catalog
  policy/              # user policy files
  agents/<name>/<version>/   # local agent packages
  db/warden.sqlite     # sessions, workflows, tasks, executions, events, artifacts index, approvals
  blobs/sha256/<aa>/<hash>   # artifact contents (content-addressed)
  worktrees/<workspace>/<task>/
  logs/                # daemon logs (redacted)
  keys/                # local checkpoint key reference (value in keychain)
```

SQLite runs in WAL mode; `events` has an index on `(session_id, seq)` and `(type, ts)`; large payloads (over 64 KiB) are stored as blobs and referenced.

# 9. Retention and export

- Default retention: sessions, events and artifacts kept for 90 days locally (configurable); worktrees removed at session close after a 24-hour grace period.
- `warden audit export --session <id> --format jsonl` writes events and artifact metadata; `--with-blobs` includes contents in a tarball; the export ends with a checkpoint record so that receivers can verify the chain.
- Deletion: `warden session purge <id>` deletes blobs and events but leaves a `session.purged` tombstone event with the last hash, keeping the chain verifiable.

# 10. Privacy and classification

Events inherit the task's classification. In Phase 3, sync policies decide which classifications leave the workstation (for example events yes, artifact contents only for `internal` and below). Request text and tool outputs are redacted of secrets before persistence; redaction counts are recorded.
