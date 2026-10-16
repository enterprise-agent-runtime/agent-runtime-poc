---
title: Enterprise Control Plane Specification
subtitle: Identity and RBAC, agent registry, model and provider registry, policy distribution, worker fleet, audit ingestion and analytics, offline behavior, deployment
docid: WRD-15
version: 0.5
status: Working specification (Phase 3 deliverable; contracts fixed now)
date: September 25, 2026
owner: Architecture
audience: Architects, platform teams, security, enterprise stakeholders
---

# 1. Role

The control plane manages **organizational configuration and governance**; the execution plane does the work. The MVP runs with no control plane; every contract below is designed so that a workstation or worker can be attached later without changing the runtime's behavior, only its inputs (bundles) and outputs (event sync).

![Figure 1. Control plane services and the execution plane.](img/control_plane.png)

# 2. Services

| Service | Responsibilities | Storage |
|---|---|---|
| Identity and RBAC | OIDC/SAML federation with the enterprise IdP; device and worker enrollment; roles | Postgres |
| Agent registry | OCI-based storage of agent packages, signatures, approval status, assignments | OCI registry (existing or bundled) + Postgres index |
| Model and provider registry | Catalog with tiers, capabilities, prices, data agreements; credential references (never values) | Postgres; secrets in the organization's secret manager |
| Policy service | Authoring, review, versioning, signing and distribution of policy bundles | Postgres; bundles as signed JSON |
| Fleet and jobs | Worker enrollment (mTLS), health, job queue, scheduling | Postgres; queue |
| Audit ingestion | Event stream ingestion with chain verification; retention; search | Append-only store (Postgres partitions or object storage) |
| Analytics | Usage, cost, success, violations, model performance | Warehouse or Postgres views |
| Evaluation management | Suites, runs on workers, leaderboards, priors distribution | Postgres; artifacts in object storage |

# 3. Identity and RBAC

| Role | Can |
|---|---|
| `org-admin` | Manage tenants, IdP, roles, retention |
| `security-admin` | Author and sign policies, approve agents for `restricted` workspaces, view all audit |
| `platform-admin` | Manage providers, model catalog, workers, sandbox images |
| `agent-author` | Publish agent versions to the registry (pending approval) |
| `developer` | Use assigned agents within policy |
| `auditor` | Read-only audit and analytics |

Workstation binding: the daemon obtains an identity token (device-bound where the IdP supports it) and includes the user identity in every event; workers use mTLS certificates issued at enrollment.

# 4. Agent registry

- Packages stored as OCI artifacts (`oci://registry.corp/warden/agents/security-reviewer:1.3.0`), signed with sigstore/cosign or organization Ed25519 keys; digests recorded.
- Lifecycle states: `submitted` → `evaluated` (suite results attached) → `security-reviewed` → `approved` → `published` → `deprecated`/`revoked`. Revocation propagates through bundles; a revoked digest cannot start new executions.
- Assignments: agents to teams, workspaces (by repository pattern) and classifications (`max_classification`).
- Capability review UI: shows the capability diff between versions and the policy intersection per assignment.

![Figure 2. Agent lifecycle.](img/registry_lifecycle.png)

# 5. Model and provider registry

The catalog of WRD-05 §2, centrally authored, with per-provider `data_agreement` metadata, tiers, prices and quotas; credential references resolve on the execution plane through the organization's secret manager (Vault, cloud secret managers) or gateway identity, so that keys never sit in the control plane database.

# 6. Policy distribution

- Bundles contain policy layers L1–L2 (WRD-08), routing admission, harness settings, budgets, deny-list extensions, pre-approvals for CI, trusted keys.
- Bundles are signed and carry `issued_at`, `expires_at` (TTL, default 24 h) and a `grace_period` (default 72 h).
- Runtimes fetch on start and periodically; they verify signatures and apply atomically; every application is an event (`policy.reload` with bundle digest).

# 7. Worker fleet and jobs

- Workers run `wardend --server` with L2 or L3 sandboxes; capacity advertised (languages, images, GPU/local models).
- Jobs are workflow runs with the same schema; a workstation may offload a task (for example a long test run) by submitting it with its artifacts; results and events return to the originating session.
- Scheduling respects classification: `restricted` jobs run only on workers labeled for it; worker labels are part of the signed fleet configuration.

# 8. Audit ingestion and analytics

- Runtimes stream events in order with their hashes; the ingestion service verifies the chain per store, co-signs checkpoints, and stores events immutably.
- Sync policy by classification: events always; artifact contents by classification; request text may be hashed only for `restricted`.
- Analytics answer the governance questions of the drafts: which agents ran, versions, models and providers, why chosen, tools called, data accessed, policies applied, approvals and denials, durations, tokens, costs, failures, artifacts, verification outcomes, intervention rates, model performance by task class.
- Exports: SIEM forwarding (JSON Lines, syslog/CEF), compliance reports per period.

# 9. Offline behavior (D-23)

| Condition | Behavior |
|---|---|
| Control plane reachable | Normal |
| Unreachable, bundle within TTL | Normal; events queued locally |
| Unreachable, within grace period | Normal with a visible warning; T3/T4 providers still allowed as per bundle |
| Beyond grace period | Only T0 models and read-only tools; approvals limited to `once`; no harness sessions |
| Bundle signature invalid | Refuse to run enterprise workspaces; local workspaces (if allowed) run under user policy only |

Queued events are synced in order; a workstation that was offline never loses its chain.

# 10. Deployment of the control plane

Container images for the services; Helm chart for Kubernetes; single-node compose for pilots; Postgres and an OCI registry as external dependencies; object storage optional. All services stateless except the databases. Private cloud and on-premises are first-class: no dependency on vendor-hosted components.

# 11. Security of the control plane

mTLS between services; least-privilege database roles; signing keys in an HSM or cloud KMS; audit of administrative actions in the same event format; regular external audits before customer pilots.
