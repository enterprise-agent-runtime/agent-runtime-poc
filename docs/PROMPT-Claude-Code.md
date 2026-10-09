# Prompt for Claude Code: build the Warden PoC

## Part 1. One-time setup (you do this, before the first session)

You will have two checkouts of the same repository: one on Windows (`D:\Projects\enterprize-agent-runtime\warden`) for Windows work and daily use of the app, one in WSL2 (`~/src/warden`) for Linux work and the L1 sandbox. Create the repository on GitHub (private) first; both checkouts push and pull from it. Claude Code runs natively on Windows (PowerShell or Git Bash) in checkout A and inside WSL2 in checkout B; which one to use is stated per milestone below.

In Windows, enable WSL2 mirrored networking so WSL2 can reach Ollama on `127.0.0.1` (optional but convenient): in `%UserProfile%\.wslconfig` add

```
[wsl2]
networkingMode=mirrored
```

then `wsl --shutdown` and reopen Ubuntu. Inside WSL2 (Ubuntu 22.04 or 24.04):

```
sudo apt update && sudo apt install -y git make build-essential bubblewrap gnome-keyring libwebkit2gtk-4.1-dev libssl-dev libayatana-appindicator3-dev librsvg2-dev
# Go 1.23+ (from go.dev/dl), Node 22 (nvm), Rust (rustup), Docker Desktop with WSL2 integration or Docker Engine
mkdir -p ~/src && cd ~/src
git clone <your-github-url> warden && cd warden
mkdir docs && cp -r /mnt/d/Projects/enterprize-agent-runtime/agent-platform-docs/. docs/
cp /mnt/d/Projects/enterprize-agent-runtime/agent-platform-docs/CLAUDE.md ./CLAUDE.md     # or from wherever you saved it
cp /mnt/d/Projects/enterprize-agent-runtime/agent-platform-docs/PROMPT-Claude-Code.md docs/
mkdir -p reference && unzip /mnt/d/path/to/lesson-01-agent-loop.zip -d reference/ && mv reference/01-agent-loop reference/agent-loop
git add -A && git commit -m "docs: specification, design and working rules"
```

Docker Desktop with WSL2 integration (or Docker Engine inside WSL2) is required from M2 on, because the L2 sandbox is first-class and is the Windows sandbox. Make sure the Copilot CLI is installed and logged in inside WSL2 (`copilot --version`), Ollama has a coder model (`ollama pull qwen2.5-coder:7b`, and a 30B-class model if the machine can run it), and you have an Anthropic API key at hand (you will enter it through `warden provider add anthropic --api-key`, never into a file).

On Windows, install Go 1.23+, Node 22, Rust (MSVC toolchain, via rustup), the Tauri 2 prerequisites (Visual Studio Build Tools with C++ and WebView2) and Docker Desktop with the WSL2 backend; clone the repository to `D:\Projects\enterprize-agent-runtime\warden`; install Claude Code natively.

Where each milestone runs: M1 in WSL2 then verified on Windows (`make check`, `warden doctor`); M2 L1 in WSL2, L2 in WSL2 **and** on Windows; M3–M5 in WSL2 with a Windows verification pass at the end of each; M6 desktop built on Windows (primary) and Linux; M7 macOS when a Mac is available plus hardening; M8 anywhere.

Then run `claude` in `~/src/warden` and paste Part 2. One milestone per session. For sessions after the first, paste only Part 4 with the milestone number.

---

## Part 2. Kickoff prompt (session 0 + M1)

You are the implementing engineer for the Warden PoC. The contract for your work is `CLAUDE.md` at the repository root: read it completely first. The specification and the design are in `docs/`. `docs/WRD-16-PoC-Concept-and-Build-Plan.md` defines the scope. The design documents produced by Claude Design (system design A01–A18 and UI/UX B01–B09, or whatever names they carry) define how to build it, but they can never add scope or weaken an invariant; where they conflict with `CLAUDE.md` or WRD-16, the conflict is a defect to record, not a decision to follow. `reference/agent-loop/` is a tested seed for the canonical model types and the two provider adapters; move it into the real package layout and extend it, do not keep two copies.

### Session 0: inventory and reconciliation (do this before writing any product code)

1. Build `docs/INDEX.md`: every file in `docs/` with one line describing it and its layer (specification, design, working). Identify which design documents correspond to which deliverables (A01…A18, B01…B09 per `docs/PROMPT-Claude-Design.md` §4–§5) even if they are named differently.
2. Read, in this order: `CLAUDE.md`; WRD-16 in full; the design documents A01–A05, A08, A10, A12, A13 and B01, B03, B04 (skim the rest, read fully when their milestone comes); WRD-02, WRD-03, WRD-04, WRD-05, WRD-06, WRD-07, WRD-08, WRD-09, WRD-10, WRD-11 sections referenced by WRD-16; WRD-00 Part III (decision log) and Appendix A (terminology).
3. Write `docs/CONFLICTS.md`: every place where the design output contradicts `CLAUDE.md`, WRD-16 or an invariant (examples to look for: a TCP port or WebSocket in the daemon, agent processes outside a sandbox, secrets in environment variables, an extra effect in the policy engine, tier-widening fallback, scope beyond WRD-16 §2.1, Windows-native sandboxing, chat-style UI). For each: both references, the precedence rule applied, the resolution or "needs owner".
4. Write the initial `docs/DECISIONS-poc.md` with the environment decisions of `CLAUDE.md` §3 as recorded facts (WSL2 primary, Docker for L2 only, Ollama reachability, keychain backend).
5. Write `docs/PROGRESS.md` with the M1 checklist, then proceed to M1 in the same session if time allows; otherwise stop and report.

### How to work in every session
Follow `CLAUDE.md` §10–§11 exactly: checklist first; small compiling steps with `make check` after each; tests with the code (table-driven, golden files, fakes; no real providers, keys, keychain or sandbox in unit tests); invariants enforced in code and tests; decide-and-record for open details, ask only when the "ask" criterion applies; finish by updating `docs/PROGRESS.md` with the acceptance commands you ran and their results; then stop.

### Milestones

Acceptance criteria restate WRD-16 §15–§16; the design documents to apply are listed per milestone.

**M1. Skeleton, platform layer, store, providers, daemon, CLI, Copilot spike** (WRD-16 §16 week 1; design A01, A02, A04, A05, A11, A17)
- Go module; `Makefile` (`check`, `build`, `run-daemon`, `desktop`, `escape-check`, `smoke`, `fixtures`, `images`) that works with GNU make on Linux/macOS and with `make` from Git for Windows or a `make.ps1` wrapper on Windows; `scripts/lint-imports.sh`, `scripts/lint-exec.sh`; GitHub Actions running `make check` on `ubuntu-latest`, `windows-latest` and `macos-latest`.
- `internal/platform`: transport (Unix socket on Linux/macOS, named pipe with owner-only DACL on Windows via `go-winio`), secrets backend selection, config directories (`~/.warden` or `%LOCALAPPDATA%\Warden`), default sandbox level per OS, process-group termination per OS. Unit tests per OS run in the CI matrix.
- `internal/model` from the reference code plus streaming `StreamEvent`, normalized error codes, `provider_options` (WRD-05 §3–§4).
- `internal/providers/anthropic` and `internal/providers/openaicompat` with streaming, usage, error normalization, auth modes `none`, `api_key` (Bearer and Azure `api-key` header), `gateway` (bearer, mTLS), and the capability probe (`provider.test`). Golden files for both wire formats.
- `internal/store`: DDL from design A04 (or WRD-16 §11 if A04 is missing), migrations, event envelope with hash chain, `chain.checkpoint` signed with a local Ed25519 key, `audit export` (JSON Lines) and `audit verify` (chain) with `--strict` (INV-A ordering).
- `internal/secrets` with the in-memory, Secret Service and encrypted-file backends; `secret://` resolution; redaction regex set with a test corpus.
- `internal/api` + `cmd/wardend`: JSON-RPC server on the Unix socket with token auth, method registry, notifications; methods `system.doctor`, `provider.*`, `event.subscribe`, `audit.*`; API schemas under `internal/api/schema` (from design A05).
- `cmd/warden`: `doctor`, `provider add|test|list`, `models`, `audit export|verify`, `daemon start|stop|status`. `warden provider add ollama` discovers models; `warden provider add anthropic --api-key` stores the key via `internal/secrets` (Secret Service or encrypted file on Linux, Keychain on macOS, Credential Manager on Windows).
- Windows verification at the end of M1 (checkout A): `make check`, `wardend` starts and listens on the named pipe, `warden doctor` and `warden provider test ollama` work on Windows.
- Spike: `spikes/copilot/` — a small Go program that starts the Copilot CLI in server mode with one custom tool and one permission hook, plus `spikes/copilot/README.md` recording exactly what the SDK exposes (flags, tool override, hook names, event shapes). If reality differs from WRD-16 §6.2 or design A12, record the differences in `docs/CONFLICTS.md`; do not change the architecture.
- Acceptance: `make check` green locally and in CI; `warden provider test ollama` succeeds against the local Ollama; the Anthropic adapter passes its golden tests against a fake server; a unit test proves the same `ModelRequest` produces tool proposals from both adapters; `audit verify` fails on a tampered fixture store and passes on a clean one; `warden doctor` reports sandbox prerequisites honestly (bwrap present/absent, userns, Docker).

**M2. Sandbox, executor, proxy** (week 2; WRD-16 §9–§10; WRD-10 §5–§7; design A06, A07)
- `cmd/warden-exec` + `internal/exec`: JSON-RPC over the inherited socketpair (fd 3); `exec.hello`, `exec.fs.read|list|search|write|patch|stat`, `exec.proc.spawn|io|signal`, `exec.git.run`, `exec.shutdown`; path canonicalization and root checks; command allowlist; output caps with truncation markers; the proxy forwarder (`warden-exec proxy`).
- `internal/sandbox/linux_bwrap`: spec → `bwrap` argv (from WRD-16 §10.3 and design A06), seccomp filter, rlimits, environment allowlist, cgroup limits where available.
- `internal/sandbox/oci` **first-class** (WRD-16 §10.4): per-task `--internal` Docker network, sandbox container from a pinned image with the worktree bind-mounted and a cache volume, proxy sidecar container as the only member with external connectivity, `warden-exec` as PID 1 over container stdio; Docker Engine API via `docker/docker/client`; path mapping layer for Windows paths; `images/` with Dockerfiles for `warden/proxy` and `warden/sandbox-{node,go,python}` and a `make images` target.
- `cmd/warden-proxy`: the proxy as a sidecar binary (L2) and as an in-process component (L1), sharing `internal/proxy`.
- `internal/sandbox/darwin_seatbelt` compiling behind build tags with the profile template from WRD-16 §10.2 (tested in M7 on a Mac if available).
- `internal/proxy`: per-task Unix socket listener, HTTP CONNECT and plain HTTP, allowlist, `proxy.connect`/`proxy.denied` events, DNS by the daemon, credential-injection hook present but unused.
- `scripts/escape-check.sh` implementing every row of WRD-16 §10.7, runnable as `make escape-check` (integration tag).
- Windows: `internal/sandbox/oci` with Docker Desktop from the native Windows daemon, including `D:\...` path mapping for worktree bind mounts and named volumes for caches.
- Acceptance: `make escape-check` passes in WSL2 with L1 **and** with L2 (`sandbox.default_level: L2`), and on Windows with L2 through Docker Desktop; inside every sandbox `~/.ssh` is absent, writes outside the worktree fail, direct network fails (L2: the internal network has no route out), proxied allowlisted traffic works through the sidecar, `env` shows only the allowlist; the same executor protocol is used over socketpair (L1) and container stdio (L2).

**M3. Policy engine, agent loop, first end-to-end run** (week 3; WRD-16 §7, §10.6; WRD-08; WRD-03; design A08, A10)
- `internal/policy`: ActionRequest normalization, YAML loading with unknown-field rejection, CEL compilation, combination rules (WRD-08 §4), obligations merge, approvals (request, resolve, scope matching, expiry, revoke), `policy.explain`, invariants INV-1–INV-9 in `invariants.go`, golden corpus in `testdata/`, monotonicity property test for the restrict-only layer.
- `policy/platform-defaults.yaml` (L0/L1: invariants, command profiles of WRD-16 §9, deny-list) and the generated user policy of WRD-16 §10.6.
- `internal/agentloop`: context assembly with redaction and provenance tags, budgets (`failed(budget)` + checkpoint artifact), tool rendering from capabilities, proposal handling with structured denials, emulated tool calling for models without native support, output schema validation with one repair turn, compaction, streaming to clients, events for every step.
- `agents/coder` manifest, prompts, schemas (WRD-16 §7); manifest loader and validator (WRD-03 §5) including the `when` condition on write capabilities.
- `warden open`, `warden run "<request>"` executing a single `coder` task on a workspace (no workflow yet) with terminal approval prompts; `warden policy explain`.
- Acceptance: T1 on the `ts-express-api` fixture produces a diff on the session branch with Anthropic, and with Ollama if the local model manages it, in WSL2 (L1) and on Windows (L2); `warden audit verify --strict` passes on those sessions; policy golden tests pass; S2 (`.env` read) is denied with `policy.decision`, executor refusal and absence of the file in the sandbox, each visible in events.

**M4. Workflow runner, verifier, repair, router, artifacts, fixtures** (week 4; WRD-16 §6.3, §8, §11; WRD-07 §3–§8; WRD-06; WRD-09 §5; design A09, A13, A14)
- `internal/orchestrator`: template loading from `workflows/poc-coding.yaml`, task state machine with transition table and reason codes persisted as `task.state` events, gates G1/G2 via `workflow.resolveGate`, repair round (max 1, `rerun: [verify]`), resume on restart, cancellation (SIGTERM, 3 s, SIGKILL; partial artifacts flagged).
- `internal/worktree`: session branch, worktree creation, hook neutralization, checkpoints, cleanup.
- `agents/verifier` with result parsers for Vitest JSON, `go test -json`, pytest JSON; artifacts `plan`, `code-diff`, `test-report`, `final-result` with provenance.
- `internal/router`: admission matrix (`confidential` → T0–T2), capability filter, ranking per strategy with static priors from `models.yaml`, circuit breaker, tier-bounded fallback, per-session pin, `routing.decision` with per-candidate reasons.
- `fixtures/`: build scripts producing the three git bundles; T1's intentionally failing 404 test; `scripts/smoke-eval.sh` (`make smoke`) running T1 and T2 per configured model and printing pass/fail and cost.
- Acceptance: T1 through the full workflow on Anthropic and on Ollama, including one repair round, in WSL2 and on Windows; `audit verify --strict` passes; setting the workspace to `confidential` makes T3/T4 providers inadmissible with a visible reason while T1 or T2 completes on a T0/T1 model; `warden cancel` during implement stops all sandbox processes within 5 s on both OSes and the session resumes from G1.

**M5. Harnesses and the company-hosted endpoint** (week 5; WRD-16 §6; WRD-05 §9; design A12)
- `internal/harness/copilot` from the M1 spike: Copilot CLI in server mode inside the sandbox, runtime tools overriding built-ins, permission and tool hooks routed to `internal/policy`, vendor-endpoint-only egress, premium-request accounting in `usage` events.
- Optional, same adapter shape: `internal/harness/codex` (app-server; ChatGPT login opt-in with notice or API key) and `internal/harness/claudecode` (`claude -p --output-format stream-json`, PreToolUse/PostToolUse hooks; personal mode only; `api_key` lock when `runtime.mode != personal`).
- `company-vllm` provider against a T1 endpoint (Ollama or vLLM on a server behind TLS and a bearer token) using the `gateway` auth mode; T1 run documented in `docs/PROGRESS.md`.
- Acceptance: T1 completes on Copilot and on the company-hosted endpoint with manifests and workflow identical to M4; H1 met on the CLI; `vendor_terms` shown by `warden harness list`.

**M6. Desktop app** (week 6; WRD-16 §13; WRD-11; design B01–B08)
- `apps/desktop`: Tauri 2 project with `wardend` as sidecar; Rust commands `rpc_call`, `rpc_subscribe`, `daemon_status` proxying JSON-RPC over the Unix socket or the Windows named pipe with the token (no TCP, no WebSocket); built and run natively on Windows (checkout A) and on Linux; React + TypeScript UI implementing design B03/B04 for screens 1–5: workspace home, session view (timeline + context panel, live events), plan review (G1), approval prompt (inline + OS notification; keys A/R/1–4), result review (G2) with diff viewer, test report, cost panel, chain status; the six states of WRD-16 §13 as designed in B01/B03.
- Playwright end-to-end tests against a mocked daemon; the UI records the H6 metrics locally (prompts per task, time to first approval, plan edit rate, cancel rate).
- Acceptance: T1 end to end through the UI on Windows and on Linux, including an inline approval and both gates; cancel from the UI stops the sandbox within 5 s; `TestCLIParity` passes; the Windows installer (MSIX or NSIS) installs and launches the app with the sidecar.

**M7. macOS, settings and doctor screens, security scenarios, documentation** (week 7; WRD-16 §10.2)
- macOS: `internal/sandbox/darwin_seatbelt` tested on a Mac (escape-check), Keychain backend verified, `.dmg` packaging via Tauri, notarization steps documented; if no Mac is available, run the macOS CI job for unit tests and record Seatbelt verification as deferred in `docs/PROGRESS.md`.
- Windows and Linux hardening from the escape-check findings of M2–M6; `warden doctor` final checks per OS (bwrap/userns on Linux, Docker Desktop and WSL2 backend on Windows, Seatbelt availability on macOS).
- Settings screen (providers, harnesses with vendor-terms notice, models, tiers) and doctor/audit screen (design B03 screens 6–7); S1–S3 polished to produce exactly the events of WRD-16 §4.3; `README.md`, setup guides for WSL2, Windows and macOS, `scripts/demo.sh` following WRD-16 §3.
- Acceptance: WRD-16 §15 items 1, 6, 7, 8 on Linux (L1) and Windows (L2); on macOS (L1) if a Mac is available.

**M8. Hardening and PoC report** (week 8)
- H6 test with a second developer (screen capture), demo rehearsal twice, fixes, `docs/POC-REPORT.md` with pass/fail and numbers per hypothesis H1–H6 and the list of simplifications to upgrade for the MVP (WRD-16 §2.3, §18).
- Acceptance: WRD-16 §15 items 9–12.

### Rules that override everything else (repeat of CLAUDE.md §9–§10)
Never run an agent-requested process outside a sandbox, including in tests. Never put a credential in a file, test, log, event, artifact or sandbox. Never implement token replay, header spoofing or vendor-client impersonation. Never open a TCP port in the daemon. Never add scope beyond WRD-16 §2.1. Never weaken a test to make it pass. Never edit WRD or design documents. Stop and report when a stop condition in `CLAUDE.md` §10 is met.

### Start now
Do Session 0 (inventory, reading, `docs/INDEX.md`, `docs/CONFLICTS.md`, initial `docs/DECISIONS-poc.md`, M1 checklist in `docs/PROGRESS.md`). Then begin M1 on branch `m1-skeleton-providers`: Go module and Makefile, lint scripts and CI, `internal/model` from the reference code, the two adapters with golden tests, `internal/store` with the hash chain and `audit verify`, `internal/secrets`, `internal/api` and `cmd/wardend`, `cmd/warden` commands, the Copilot spike. Run `make check` after every step. Stop when M1's acceptance criteria pass or a stop condition is met, and update `docs/PROGRESS.md`.

---

## Part 3. If Claude Code asks a question

Answer only the question. Then say: "Record the answer in `docs/DECISIONS-poc.md` and continue the current milestone." If the question reveals a conflict between the design output and the invariants, the answer is always the invariant.

---

## Part 4. Session start block (sessions for M2 … M8)

Read `CLAUDE.md`, `docs/PROGRESS.md`, `docs/DECISIONS-poc.md` and `docs/CONFLICTS.md`. We are on milestone **M<N>** of the Warden PoC as defined in `docs/PROMPT-Claude-Code.md` (section "Milestones"), on branch `m<N>-…`. Confirm what the previous session left incomplete, write this session's checklist at the top of `docs/PROGRESS.md`, read the design documents listed for M<N>, then implement following `CLAUDE.md` §10–§11. Run `make check` after each step. Stop when the acceptance criteria of M<N> pass or a stop condition is met, and finish by updating `docs/PROGRESS.md` with the acceptance commands and their results.
