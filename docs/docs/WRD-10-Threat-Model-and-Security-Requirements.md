---
title: Threat Model and Security Requirements
subtitle: Assets, attackers, trust boundaries, threat catalog with mitigations, sandbox design per operating system, egress proxy, secrets, prompt-injection defenses, security requirements and tests
docid: WRD-10
version: 0.5
status: Working specification
date: September 25, 2026
owner: Security
audience: Security engineers, architects, engineers
---

# 1. Security objective

An agent may **read and modify only its worktree, run only what it was granted, reach the network only through the runtime's proxy to allowlisted hosts, and never see a credential**. Anything with effects beyond the sandbox requires an explicit, recorded human decision. The runtime remains the single decision point even when a vendor's agent engine is used.

Three mechanisms, all mandatory in the MVP:

| Mechanism | Guarantees | Where |
|---|---|---|
| Sandbox | Cannot get out (filesystem, process, network confinement) | §5 |
| Policy engine | May not do (authorization, approvals, obligations) | WRD-08 |
| Proxy and secrets broker | Cannot take anything with it (egress control, credential injection, redaction) | §6, §7 |

# 2. Assets

| Asset | Why it matters |
|---|---|
| Source code and repository history | Confidential IP; the primary data agents process |
| Developer credentials (SSH keys, cloud CLI tokens, package registry tokens, browser sessions) | Lateral movement and supply-chain attacks |
| Model provider credentials and harness logins | Financial loss, data exposure |
| The user's machine and network position | Pivot into the organization |
| Audit trail | Accountability; must be trustworthy |
| Policy bundles and agent packages | Integrity of the control layer |

# 3. Attackers

| Attacker | Capabilities |
|---|---|
| Malicious repository (or dependency) | Crafted files, git hooks, build scripts, `package.json` lifecycle scripts, `.warden` configuration, text designed to manipulate models |
| Prompt injection via tool output, web content, MCP tool descriptions or results | Instructions inside data |
| Compromised or malicious MCP server or harness | Arbitrary tool descriptions and outputs; attempts to escalate |
| Malicious or buggy agent package | Over-broad capability requests, deceptive instructions |
| Compromised model provider or intermediary | Poisoned outputs, exfiltration through responses is not possible but data exposure at the provider is |
| Local attacker with user privileges | Out of scope for confidentiality of local data; in scope for audit tamper evidence |
| Insider misuse | Attempting to exceed organization policy |

# 4. Trust boundaries

![Figure 1. Trust boundaries.](img/trust_boundaries.png)

Trusted computing base: `wardend`, `warden-exec` (as a second line), the OS sandbox primitives, the keychain, the proxy. Everything else, including the UI process, model output, repository content, tool results, agent packages and vendor harnesses, is untrusted.

# 5. Sandbox design (D-05)

![Figure 2. Sandbox levels.](img/sandbox_levels.png)

## 5.1 Contract common to all levels

| Property | Requirement |
|---|---|
| Filesystem | Worktree read-write; toolchain directories read-only (language runtimes, package caches in a per-workspace cache mounted rw); `/tmp` private; nothing else from the host. No home directory, no `~/.ssh`, `~/.aws`, `~/.config/gcloud`, `~/.npmrc`, `~/.docker`, keychain sockets, browser profiles |
| Process | No new privileges; capabilities dropped; no ptrace; PID namespace (Linux) so processes cannot see or signal host processes; resource limits (CPU, memory, pids, disk write quota); wall-clock timeout with kill |
| Network | No interfaces except loopback inside a private network namespace; the only exit is the proxy socket; DNS resolved by the proxy |
| Environment | Minimal, explicit variables; no inherited environment; `HOME` set to a scratch directory inside the sandbox |
| Git | `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_SYSTEM=/dev/null`, `core.hooksPath` pointing to an empty directory, `safe.directory` for the worktree; the `.git` directory of the main repository is not mounted; the worktree's `.git` file is rewritten to a private object store copy for L2 or bind-mounted read-only for L1 with commits performed by `warden-exec` through a controlled path |
| Executor | `warden-exec` is the only process launched by the runtime; every other process is its child |

## 5.2 Linux L1 (default)

- `bubblewrap` with `--unshare-all`, `--new-session`, `--die-with-parent`, explicit `--ro-bind` and `--bind` lists, `--proc`, `--dev` minimal, `--tmpfs /tmp`, `--clearenv`.
- seccomp filter (default deny of dangerous syscalls: `ptrace`, `mount`, `keyctl`, `bpf`, `io_uring`, `userfaultfd`, `unshare` beyond initial, `kexec_load`, module loading).
- Landlock restrictions on top where the kernel supports it (5.13+).
- cgroup v2 limits via the daemon's user slice (systemd) when available; otherwise `prlimit`.
- Prerequisites: `bubblewrap` package; user namespaces enabled (documented fallback to L2 if unavailable).

## 5.3 macOS L1 (default)

- Seatbelt profiles (`sandbox-exec`) generated per task: deny default; allow file-read on toolchain paths; allow file-read/write on the worktree and scratch; deny all network except the proxy Unix socket; deny `process-info` outside the group; deny `mach-lookup` except a minimal set required by the toolchains.
- Resource limits via `setrlimit` and a watchdog; process group kill on timeout.
- Note: Seatbelt is a private-but-stable API used by current coding agents; the runtime ships tested profiles per macOS version and falls back to L2 if profile compilation fails.

## 5.4 L2 rootless OCI container (all platforms; default on Windows and for untrusted repositories)

- Docker or Podman in rootless mode; per-language images (`warden/sandbox-node`, `-go`, `-python`, `-java`) built from pinned digests; read-only root filesystem; `--cap-drop ALL`, `--security-opt no-new-privileges`, seccomp profile, `--pids-limit`, memory and CPU limits, `--network none` plus the proxy socket bind-mounted.
- Worktree bind-mounted at `/work`; package cache volume per workspace.
- `warden-exec` is the container entrypoint; the daemon communicates over the attached stdio or a bind-mounted socket.
- Windows: Docker Desktop or Podman Desktop (WSL2 backend); the worktree lives in the WSL2 filesystem for performance; native Windows sandboxing (restricted tokens, AppContainer) is a later phase.

## 5.5 L3 microVM (Phase 3, CI and remote workers)

gVisor (`runsc`) or Firecracker-backed containers; Apple containers on macOS 26+. Same executor and contract; stronger kernel isolation for multi-tenant workers and untrusted repositories.

## 5.6 Sandbox selection

`effective_level = max(policy.default, manifest.runtime.sandbox_min_level, workspace_trust_rule)` where untrusted repositories (first open, unknown remote, or flagged by policy) require L2. Harness tasks default to L2.

# 6. Secret deny-list (platform, non-overridable)

Paths (globs, evaluated inside and outside the sandbox): `**/.env`, `**/.env.*`, `**/*.pem`, `**/*.key`, `**/*.p12`, `**/*.pfx`, `**/id_rsa*`, `**/id_ed25519*`, `**/*.kdbx`, `**/.netrc`, `**/.npmrc`, `**/.pypirc`, `**/.git-credentials`, `**/credentials.json`, `**/service-account*.json`, `**/.aws/**`, `**/.ssh/**`, `**/.config/gcloud/**`, `**/.azure/**`, `**/.kube/**`, `**/.docker/config.json`, `**/.terraform.d/**`, `**/.warden/**` (runtime state). The list is versioned with the runtime; organizations may extend it, never shorten it.

Secret patterns for redaction: AWS access keys, GCP and Azure tokens, GitHub/GitLab tokens, OpenAI/Anthropic keys, JWTs, private key blocks, database connection strings with passwords, generic high-entropy `key=` assignments. The scanner runs on tool outputs, artifacts and context before persistence or model calls.

# 7. Egress proxy and credential injection

- One listener per task on a Unix socket bind-mounted into the sandbox; the in-sandbox forwarder exposes it as `127.0.0.1:<port>` inside the network namespace.
- Modes: HTTP CONNECT (TLS passthrough) for allowlisted `host:port`; optional TLS termination with an injected per-task CA for hosts where credential injection is configured (git over HTTPS to the organization's host, package registries with private scopes). Injection replaces a placeholder header or adds the `Authorization` header host-side; processes in the sandbox never see the token.
- Deny by default; per-task allowlist = manifest `egress.allow` ∩ policy ∩ session approvals; every connection is an event; denials are surfaced as approval prompts where the rule permits.
- Model provider traffic never traverses the sandbox proxy; it originates in the daemon.
- Harness tasks: allowlist limited to the vendor's endpoints; no injection.

# 8. Threat catalog and mitigations

| Id | Threat (STRIDE) | Mitigation | Verified by |
|---|---|---|---|
| T-01 | Repository hook execution on checkout or commit (E) | Hooks path neutralized; global config disabled; `.git` not mounted | Test: repo with malicious `pre-commit` and `core.hooksPath` |
| T-02 | Package lifecycle scripts run arbitrary code (E) | Scripts run only inside the sandbox; install is R4 with approval; no credentials in the sandbox | Test: `postinstall` attempting to read `~/.ssh` and to reach the network |
| T-03 | Prompt injection in files or tool output (T) | Untrusted tagging; instructions in data never elevate; taint escalation; approvals show exact actions | Eval cases SEC-1..3 (WRD-12) |
| T-04 | Exfiltration via network from the sandbox (I) | Network namespace; proxy allowlist; DNS via proxy | Test: raw socket, DNS tunneling attempt, HTTP to unlisted host |
| T-05 | Exfiltration via model prompt (I) | Data classification and tiers; redaction; no secrets in context | Router tests; redaction tests |
| T-06 | Reading secrets on disk (I) | Deny-list at PDP, executor and sandbox mount level (defense in depth) | Test: `fs.read ~/.aws/credentials`, symlink to `.env`, `../` escape |
| T-07 | Writing outside worktree (T) | Only worktree mounted rw; executor path canonicalization; INV-2 | Test: absolute path write, symlink escape, `git worktree add` to home |
| T-08 | Privilege escalation in sandbox (E) | no-new-privileges, cap-drop, seccomp, rootless | Test: setuid binaries, `unshare`, `ptrace` |
| T-09 | Sandbox denial of service (D) | CPU, memory, pids, disk quotas, timeouts | Test: fork bomb, memory balloon, disk fill |
| T-10 | Tool impersonation by model (S) | Tools are runtime-defined; names validated; unknown tools rejected | Unit tests |
| T-11 | Malicious agent package requests broad capabilities (E) | Manifest ∩ policy; review UI shows capability diff on install; signing | Lint and install tests |
| T-12 | MCP tool description poisoning (T) | Descriptions hashed and re-approved on change; outputs untrusted | Phase 2 tests |
| T-13 | Compromised harness exceeds policy (E) | Harness runs in sandbox; hooks to PDP; L2 default; allowlist to vendor only | Harness tests |
| T-14 | Harness credential theft by repository code (I) | Minimal mount of harness token; L2; taint; policy may forbid harness in confidential workspaces | Test: repo script reading harness config |
| T-15 | UI bypass of policy (E) | UI has no privileged path; daemon enforces; socket owner-only | API tests |
| T-16 | Workspace policy widening permissions (E) | Restrict-only layer; INV | Policy property tests |
| T-17 | Audit tampering (R) | Hash chain; signed checkpoints; central sync later | `audit verify` tests |
| T-18 | Secret leakage into logs or events (I) | Redaction before persistence; descriptor `argument_redaction`; N-4 | Log scanning tests |
| T-19 | Cross-session data leakage (I) | Fresh sandbox per task; artifacts scoped by session; no shared temp | Tests |
| T-20 | Runaway autonomous execution (D) | Limits; budgets; cancellation in 5 s; step cap | Budget tests |
| T-21 | Supply chain of the runtime itself (T) | Pinned dependencies, reproducible builds, signed releases, SBOM | Release pipeline |
| T-22 | Provider outage forcing insecure fallback (I) | Tier-bounded fallback | Router tests |
| T-23 | Local attacker reads `~/.warden` (I) | Owner-only permissions; secrets in keychain; artifacts classification | Out of scope beyond OS controls; documented |
| T-24 | Approval fatigue leading to careless grants (E) | Scoped approvals; clear reasons; R5 never persistable; metrics on prompt frequency | UX research and metrics |

# 9. Prompt-injection defenses (detail)

1. **Structure**: instructions come only from manifests and the user; every observation is wrapped as data with provenance and a trust marker; system prompts state this explicitly.
2. **Taint**: reading external content (web, MCP, third-party packages, harness output) sets `untrusted_external`; R5 actions then require approval regardless of session grants; the approval prompt shows the taint sources.
3. **Capability minimization**: analysis and review agents are read-only by manifest; implementation agents have no network beyond installs; only the integrator and the user can push.
4. **Content hygiene**: tool outputs are truncated, secret-scanned and, for HTML or Markdown from the web, stripped to text.
5. **Visibility**: the UI shows every tool call and its arguments; approvals show the exact effect.
6. **Evaluation**: security cases test that injected instructions do not lead to denied actions or to attempts outside scope; regressions block releases.

# 10. Security requirements (testable)

| Id | Requirement |
|---|---|
| S-1 | The runtime MUST refuse to execute agent-requested processes outside a sandbox. |
| S-2 | The sandbox MUST NOT have any network path except the proxy socket. |
| S-3 | No credential value MUST be present in any sandbox mount, environment or argument. |
| S-4 | The deny-list MUST be enforced at PDP, executor and mount level. |
| S-5 | Every event MUST be hash-chained; `audit verify` MUST detect any modification. |
| S-6 | Workspace policy MUST NOT be able to grant. |
| S-7 | Approvals of risk class R5 MUST NOT be persistable beyond `once`. |
| S-8 | Cancellation MUST terminate all sandbox processes within 5 s. |
| S-9 | Redaction MUST run before persistence and before any model call. |
| S-10 | Harnesses marked `prohibited` MUST NOT be enableable. |
| S-11 | Release artifacts MUST be signed and reproducible; dependencies pinned with an SBOM. |
| S-12 | An external penetration test of the sandbox and proxy MUST precede the first enterprise pilot. |

# 11. Sandbox escape test suite (run in CI on each OS)

| Test | Expectation |
|---|---|
| Read `$HOME/.ssh/id_ed25519` via absolute path, symlink, and `..` traversal | denied at all three layers, events recorded |
| Write to `$HOME/escape.txt` and to the repository outside the worktree | denied |
| `curl https://example.com` without proxy; raw TCP connect; UDP DNS to 8.8.8.8 | fails; `proxy.denied` or no route |
| `curl` through proxy to an unlisted host | approval prompt (interactive) or denied (non-interactive) |
| Fork bomb; allocate 8 GiB; fill disk | limits enforced; task `failed(resource)` |
| Execute `sudo`, setuid binary, `unshare -r`, `ptrace` a sibling | fails |
| Commit with `pre-commit` hook present; repository sets `core.hooksPath` | hook never runs |
| `npm install` with `postinstall` reading `~/.npmrc` and calling home | file absent; network denied |
| Environment dump | no secrets, no host variables |
| Kill test: cancel during `sleep 1000` chain | all processes gone within 5 s |

# 12. Operational security

Signed releases; auto-update with signature verification; vulnerability disclosure policy; secure defaults (L1/L2 only, deny-all egress); telemetry off by default; logs redacted; `warden doctor` reports sandbox prerequisites and misconfigurations.
