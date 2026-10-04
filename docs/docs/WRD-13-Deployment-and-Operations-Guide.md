---
title: Deployment and Operations Guide
subtitle: Installation per operating system, configuration, directories, provider setup, sandbox prerequisites, operations, updates, backup, troubleshooting, and later deployment models
docid: WRD-13
version: 0.5
status: Working specification
date: September 25, 2026
owner: Engineering / Operations
audience: Developers, platform teams, operations
---

# 1. Deployment models

| Model | Phase | Components |
|---|---|---|
| Desktop, local-first | MVP | Desktop app with bundled `wardend`, or CLI alone |
| Desktop plus control plane | 3 | Adds signed policy and registry bundles, identity, central audit |
| CI worker | 2 | `wardend` headless in a runner with L2/L3 sandboxes |
| Remote worker fleet | 4 | `wardend` server mode, job queue, mTLS enrollment |
| Private cloud / on-premises control plane | 3 | Control plane services (WRD-15) |

![Figure 1. Local deployment.](img/deployment_local.png)

# 2. Installation

## 2.1 macOS (14+)

- Desktop: signed and notarized `.dmg`; the app bundles `wardend` and `warden-exec` as sidecars; first launch runs `warden doctor`.
- CLI: `brew install warden` (tap) or a signed tarball.
- Sandbox L1 uses Seatbelt; no additional install. L2 requires Docker Desktop, Podman Desktop or Apple container tooling.

## 2.2 Linux (Ubuntu 22.04+, Fedora 39+, Debian 12+)

- Packages: `.deb`, `.rpm`, and an AppImage for the desktop; CLI tarball with checksums and signatures.
- L1 prerequisites: `bubblewrap`; unprivileged user namespaces enabled (`kernel.unprivileged_userns_clone=1` on older Debian/Ubuntu); `doctor` reports and links to fixes.
- L2: rootless Docker or Podman.

## 2.3 Windows 11

- MSIX installer for the desktop; `winget install warden`.
- L2 required: Docker Desktop or Podman Desktop with the WSL2 backend; the runtime stores worktrees inside the WSL2 filesystem (`\\wsl$\...`) for performance and mounts them into containers; `doctor` verifies.
- Native L1 is not available in the MVP; the UI states this.

# 3. Directories and files

See WRD-09 §8 for `~/.warden`. Permissions are owner-only. On Windows `%LOCALAPPDATA%\Warden` is used with an equivalent layout.

`config.yaml`:

```
runtime:
  socket: ~/.warden/run/wardend.sock
  log_level: info
  telemetry: off
sandbox:
  default_level: L1            # L2 on Windows
  backend: auto                # bwrap | seatbelt | docker | podman
  images: { node: ghcr.io/warden/sandbox-node@sha256:..., go: ..., python: ..., java: ... }
  package_cache: ~/.warden/cache
proxy:
  dns_over_proxy: true
worktrees:
  root: ~/.warden/worktrees
  retention_hours: 24
store:
  retention_days: 90
budgets: { session_usd: 25, daily_usd: 100 }
```

# 4. Provider setup

| Path | Command | Notes |
|---|---|---|
| API key | `warden provider add anthropic --api-key` | Prompts for the key; stores in the keychain; tests the endpoint |
| Local model | `warden provider add ollama` | Discovers models; probes tool-calling support |
| OpenAI-compatible server | `warden provider add openai-compatible --url https://vllm.corp.internal/v1 --auth mtls` | |
| Gateway | `warden provider add gateway --url ... --auth sso-oidc` | Browser login flow; token stored in keychain |
| Cloud identity (Phase 2) | `warden provider add azure-foundry --auth entra` | Uses the platform credential chain |
| Harness | `warden harness enable copilot` | Requires Copilot CLI installed and logged in; shows vendor terms notice |

`warden provider test <id>` validates connectivity, capabilities and prices; `warden models` lists the catalog with tiers.

# 5. Operating the daemon

- Lifecycle: started by the desktop app or on demand by the CLI; `warden daemon status|stop|restart`.
- Logs: `~/.warden/logs/wardend.log` (rotated, redacted); `warden logs -f`.
- Health: `warden doctor` checks sandbox backend, namespaces, proxy socket, keychain access, disk space, catalog validity, policy compilation.
- Resource footprint: idle daemon under 60 MB; each L1 sandbox adds the toolchain's usage only; L2 images pinned by digest and pulled on first use (documented size per image).

# 6. Updates

- Desktop auto-update with signature verification (Tauri updater) and a release notes prompt; CLI via package manager.
- Schema migrations for SQLite run on daemon start with a backup of the database file; downgrade is not supported across migrations.
- Sandbox images are updated independently, pinned in `config.yaml` by digest.

# 7. Backup and restore

`~/.warden` is self-contained: backing up the directory (excluding `run/` and `cache/`) preserves sessions, artifacts, worktrees and policies; secrets live in the keychain and must be re-entered on a new machine. `warden audit export` produces portable session archives.

# 8. Troubleshooting

| Symptom | Likely cause | Action |
|---|---|---|
| "Sandbox backend unavailable" | bubblewrap missing or user namespaces disabled; Docker not running | `warden doctor`; install or enable; or switch to L2 |
| Tasks never start on Windows | Docker Desktop without WSL2 backend | Enable WSL2 integration |
| "No admissible model" | Classification excludes configured providers | Add a T0–T2 provider or adjust admission via policy |
| Approval prompts for every command | Toolchain commands not in profiles | Add commands to a workspace profile via a `once`/`workspace` approval |
| Slow file operations on macOS L2 | Bind mounts through Docker Desktop | Prefer L1 on macOS or use VirtioFS |
| Keychain prompts repeatedly | Application signature changed | Reinstall signed build |

# 9. CI usage (Phase 2)

`warden run --non-interactive --policy ci-policy.yaml --workspace . "Fix flaky test in auth module"` runs with a pre-approval bundle; exit codes drive the pipeline; artifacts and reports are uploaded as job outputs; events can be shipped to central audit when the control plane exists.

# 10. Hybrid deployment (Phase 3+)

![Figure 2. Hybrid deployment with control plane.](img/deployment_hybrid.png)

Workstations and workers enroll with the control plane, pull signed bundles with TTLs, and stream events. Offline behavior is defined in WRD-15 §9.
