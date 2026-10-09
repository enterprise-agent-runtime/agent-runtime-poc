# CLAUDE.md — Warden PoC

You are the implementing engineer of the Warden Proof of Concept. This file is the contract for every session. Read it completely before doing anything. If anything here conflicts with a document in `docs/`, this file and `docs/WRD-16-PoC-Concept-and-Build-Plan.md` win, and you report the conflict in `docs/CONFLICTS.md`.

## 1. Mission

Warden is an enterprise AI agent runtime: a secure execution and governance layer that runs AI agents against the models an organization already has, inside a sandbox the agent cannot escape, with every action authorized by a policy engine and recorded in a hash-chained audit trail. This repository builds the **PoC** that proves six hypotheses (H1–H6 in WRD-16 §1):

- H1 model neutrality: the same agents and manifests complete the same task on Anthropic (API key, T3), a local model (Ollama/LM Studio, T0), GitHub Copilot through the Copilot SDK (subscription, T4) and a company-hosted model behind gateway auth (T1).
- H2 the runtime is the trust boundary: no tool executes without a prior allow decision; security scenarios S1–S3 are blocked.
- H3 the sandbox holds: the escape-check suite passes.
- H4 verification is execution: nothing reaches gate G2 without `verify` succeeding; a repair round is demonstrated.
- H5 auditability: `warden audit verify --strict` passes; artifacts carry provenance.
- H6 usability: another developer completes T1 in under 15 minutes through the desktop app.

**In scope** is exactly WRD-16 §2.1. **Out of scope**, never to be built here: control plane, SSO/RBAC, registries and signing, MCP client, browser/cloud/database tools, dynamic DAG planning, parallel worktrees, integrator agent, Agent SDK, cloud identity auth, native Gemini/Bedrock/Vertex adapters, Windows-native sandbox, full evaluation framework, telemetry upload, marketplace, chat-style UI. If a feature seems missing, check the simplifications register WRD-16 §2.3 first; it is probably a deliberate simplification with a planned upgrade.

## 2. Sources of truth and precedence

`docs/` contains three layers:

| Layer | Files | Role |
|---|---|---|
| Specification | `WRD-00` … `WRD-16` (+ `img/`) | What the product is; invariants; scope (WRD-16) |
| Design | the Claude Design output (system design A-series: A01–A18; UI/UX B-series: B01–B09; possibly other names) | How to build it: diagrams, schemas, DDL, API contracts, components, screens |
| Working files you own | `docs/INDEX.md`, `docs/CONFLICTS.md`, `docs/DECISIONS-poc.md`, `docs/PROGRESS.md`, `docs/POC-REPORT.md`, `docs/GLOSSARY.md`, `docs/WORKING-WITH-CLAUDE-CODE.md`, `docs/reports/` | Inventory, conflicts, decisions, progress; the concepts glossary, the way of working, dated session reports. Every document about this work lives in this repository (or, for the specification and design, in agent-platform-docs); claude.ai pages are only views of these files |

Precedence rules:
1. **Scope and invariants**: this file and WRD-16. No design document can add scope or weaken an invariant.
2. **Implementation detail** (API schemas, DDL, package structure, screens, components, microcopy): design documents > companion WRD for that subsystem (WRD-02 … WRD-11) > WRD-00.
3. A design document that contradicts an invariant or a fixed decision is a **defect**: record it in `docs/CONFLICTS.md` with both references and implement the invariant.
4. You never edit WRD or design documents. You only write the working files listed above.
5. Terminology and identifiers come from WRD-00 Appendix A: H1–H6, T1–T6, S1–S4, INV-1–INV-9, R0–R6, T0–T4, D-xx, P-xx. Use them in code comments, tests and commit messages.

## 3. Environment (fixed)

- **Three target operating systems from day one**: Linux, Windows, macOS. Differences are confined to `internal/platform` (transport: Unix socket on Linux/macOS, named pipe `\\.\pipe\warden-<user>` with owner-only DACL on Windows; secrets backend: Secret Service or encrypted file / Keychain / Credential Manager; config directory; default sandbox level: L1 / L1 / L2). Nothing above that package may contain `runtime.GOOS` checks.
- **Two checkouts, one remote.** The owner develops on a Windows machine. Checkout A: `D:\Projects\...\warden` on Windows (native Go, Rust/MSVC for Tauri, Docker Desktop for L2) for Windows-specific work and for running the real app daily. Checkout B: `~/src/warden` inside WSL2 (Ubuntu 22.04+/24.04) for Linux work and the L1 bubblewrap sandbox (hypothesis H3). You run in whichever checkout the current task needs; `docs/PROGRESS.md` and `docs/DECISIONS-poc.md` carry continuity through git. Never build from `/mnt/d/` inside WSL2 and never run Cargo over `\\wsl$`.
- **CI matrix** (GitHub Actions) runs `make check` on `ubuntu-latest`, `windows-latest` and `macos-latest` from M1 so that a break on any OS is caught regardless of where the work was done. Sandbox integration tests (`make escape-check`) run locally: L1 in WSL2, L2 in WSL2 and on Windows with Docker Desktop, L1 on macOS when a Mac is available.
- The daemon is never run inside Docker for desktop use; a `wardend` container image exists only for headless CI/server use. Windows-native (non-container) isolation is out of scope.
- **Docker** (Docker Desktop with WSL2 integration, or Docker Engine inside WSL2) is used for: the **first-class L2 sandbox backend** (per-task internal network + sandbox container + proxy sidecar container, executor over container stdio; WRD-16 §10.4), the per-language sandbox images and the proxy image under `images/`, CI parity. It is **not** the development environment and L1 is never run inside a container.
- **Sandbox L1 on Linux** uses bubblewrap + seccomp with unprivileged user namespaces. `warden doctor` must check `bwrap --version`, `/proc/sys/kernel/unprivileged_userns_clone` (where present) and AppArmor restrictions (Ubuntu 24.04 may need `sysctl kernel.apparmor_restrict_unprivileged_userns=0` or an AppArmor profile; document the chosen fix in `docs/DECISIONS-poc.md`).
- **macOS** (Seatbelt L1, `.dmg`) is a secondary target handled when a Mac is available (M7). Keep `internal/sandbox/darwin_seatbelt` compiling with build tags from the start.
- **Ollama** runs on Windows (GPU) or in WSL2. From WSL2, prefer WSL mirrored networking (`.wslconfig: networkingMode=mirrored`) so `http://127.0.0.1:11434/v1` works; otherwise use the Windows host IP and declare the provider as `tier: T0` with a note in `docs/DECISIONS-poc.md` (same physical machine).
- **Keychain**: `internal/secrets` uses Secret Service via `zalando/go-keyring` when available (gnome-keyring in WSL2) and otherwise an `age`-style encrypted file under `~/.warden/secrets.enc` unlocked with a passphrase in the session; which backend is active is reported by `warden doctor` and recorded in events as `secret.backend`.
- Credentials for development come from the OS keychain through `warden provider add …`, never from files in this repository. Environment variables are accepted only by the `warden provider add` command at setup time.

## 4. Fixed decisions (never reopen, never "improve")

| Area | Decision |
|---|---|
| Languages | Go 1.23+ for `cmd/wardend`, `cmd/warden`, `cmd/warden-exec`; Rust only for the Tauri bridge in `apps/desktop/src-tauri`; TypeScript + React 19 + Vite for the UI. Nothing else. |
| IPC | JSON-RPC 2.0, `Content-Length` framing (LSP style); Unix socket `~/.warden/run/wardend.sock` (0600) on Linux/macOS, named pipe `\\.\pipe\warden-<user>` (owner-only DACL) on Windows; per-session token in the user profile (owner-only). **The daemon never listens on a TCP port on the host.** The webview reaches the daemon only through Tauri Rust commands that proxy JSON-RPC over the socket or pipe. |
| Store | SQLite via `modernc.org/sqlite` (no cgo), WAL, migrations in `internal/store/migrations`; events append-only, hash-chained; artifacts content-addressed under `~/.warden/blobs/sha256/`. |
| Policy | YAML rules with CEL (`google/cel-go`); effects exactly `allow | deny | approval_required`; obligations separate; layers in the PoC: platform invariants/defaults and user; approval scopes `once | task | session | workspace`; R5 never persistable beyond `once`; repository content can only restrict. |
| Sandbox | L1 native default on Linux (bubblewrap+seccomp) and macOS (Seatbelt); **L2 container first-class** on every OS and the only level on Windows: per-task `--internal` Docker network, sandbox container (read-only image, `cap-drop ALL`, `no-new-privileges`, pids/memory/cpu limits, worktree bind mount, cache volume), proxy sidecar container as the only member with external connectivity, `warden-exec` as PID 1 speaking the executor protocol over container stdio; selected with `sandbox.default_level`; default for harness tasks and untrusted repositories; no agent-requested process ever runs outside a sandbox. |
| Network | Sandbox egress only through the daemon-controlled per-task proxy: L1 = Unix socket + in-sandbox forwarder on 127.0.0.1:3128 inside the netns; L2 = proxy sidecar container reachable only on the task's internal network; deny-by-default allowlist; DNS by the proxy; model calls host-side from the daemon. |
| Models | Provider = protocol × auth mode × trust tier. Adapters: `anthropic-messages`, `openai-compatible` (auth `none`, `api_key` Bearer, `api_key` Azure header, `gateway` bearer/mTLS). Admission: `confidential` → T0, T1, T2; `internal`/`public` → T0–T4. Fallback never widens the tier. Strategy default `prefer-internal`. |
| Harnesses | Run the vendor's **unmodified official CLI inside the sandbox**; tool calls and permission requests routed to the policy engine; vendor-endpoint-only egress. Copilot SDK required; Codex app-server optional; Claude Code CLI optional in **personal mode only** (subscription login allowed only when `runtime.mode: personal`; forced to `api_key` billing otherwise). No token replay, no header spoofing, no impersonation, ever. |
| Agents | Two declarative agents: `coder` (modes plan/implement/repair) and `verifier`. Manifests per WRD-03 and WRD-16 §7. |
| Workflow | `workflows/poc-coding.yaml`: plan → G1 → implement → verify → repair (max 1) → G2 → deliver; sequential; one worktree per session. |
| Targets | Linux (L1 default), Windows (L2 only, Docker Desktop), macOS (L1 default) are all first-class from M1; Windows and Linux are verified continuously on the owner's machine; macOS packaging and Seatbelt tests happen in M7 when a Mac is available. |
| Dependencies | Only: `spf13/cobra`, `creachadair/jrpc2`, `modernc.org/sqlite`, `google/cel-go`, `zalando/go-keyring`, `santhosh-tekuri/jsonschema/v6`, `gopkg.in/yaml.v3`, `github/copilot-sdk/go`, `github.com/docker/docker/client` (L2 backend), `github.com/Microsoft/go-winio` (named pipes); UI: React, Vite, TanStack Query, one diff-viewer component; Tauri 2 with `tauri-plugin-shell`. Anything else needs an entry in `docs/DECISIONS-poc.md` with the reason. |

## 5. Invariants and the tests that prove them

Every invariant has a named test that must exist and pass from the milestone where the component lands. A change that breaks one of these tests is wrong; the test is right.

| Id | Invariant | Test (name, location) |
|---|---|---|
| INV-A | No `tool.exec.start` without a preceding `policy.decision{effect: allow}` for the same `call_id` | `TestAuditStrictOrdering` in `internal/store`; `warden audit verify --strict` |
| INV-B | Sandbox mounts only worktree (rw), read-only toolchain, scratch, proxy socket; environment cleared; no `$HOME` | `scripts/escape-check.sh` rows 1, 2, 9; `TestSandboxSpecMounts` in `internal/sandbox` |
| INV-C | No secret value in events, artifacts, logs, sandbox env, args or model context | `TestNoSecretsInEvents`, `TestRedactionCorpus` in `internal/secrets`; `TestSandboxEnvAllowlist` |
| INV-D | Tool results enter model context wrapped as untrusted data with provenance | `TestUntrustedWrapping` in `internal/agentloop` |
| INV-E | Repository content (including `.warden/` in a repo) cannot widen permissions | `TestWorkspaceLayerRestrictOnly` (monotonicity property test) in `internal/policy` |
| INV-F | Clients use only the JSON-RPC API; CLI parity | `TestCLIParity` (every desktop command has a CLI method mapping) in `internal/api` |
| INV-G | `confidential` routes only to T0–T2; fallback never widens tier | `TestAdmissionMatrix`, `TestFallbackNeverWidens` in `internal/router` |
| INV-H | No agent-requested process outside a sandbox, including tests | `TestExecutorOnlyViaSandbox` (static check: `os/exec` allowed only in `internal/sandbox`, `internal/exec`, `internal/worktree`, `internal/harness`, each with a `//sandboxed` justification comment) via `scripts/lint-exec.sh` |
| INV-I | Deny-list enforced at policy, executor and mount level | `TestDenyListThreeLayers` spanning policy, exec and sandbox spec |
| INV-J | Daemon listens on no host TCP port (the L2 proxy sidecar listens on TCP only inside the task's internal Docker network) | `TestNoListenTCP` (static grep for `net.Listen("tcp"` outside `cmd/warden-proxy` and tests) |

Platform invariants INV-1–INV-9 of WRD-08 §5 are implemented as code checks in `internal/policy/invariants.go` with one golden test each.

## 6. Architecture map

```
cmd/wardend                 daemon entry: config, socket, token, lifecycle                       WRD-02 §3, WRD-16 §5
cmd/warden                  CLI (cobra): doctor, provider, models, open, run, approve, reject,  WRD-16 §14
                            status, cancel, diff, report, policy explain, audit export|verify, eval smoke
cmd/warden-exec             in-sandbox executor: fs/proc/git over JSON-RPC on fd 3; proxy forwarder   WRD-04 §5, WRD-16 §9
internal/api                JSON-RPC server, token auth, method registry, notifications            WRD-02 §4, WRD-16 §12, design A05
internal/session            sessions, session branch, resume                                       WRD-09 §8
internal/orchestrator       template loading, task state machine, gates, repair, cancellation, resume   WRD-07, WRD-16 §8, design A13
internal/agentloop          context assembly, budgets, tool rendering, proposals, compaction, streaming   WRD-16 §7, design A10
internal/policy             ActionRequest normalization, CEL rules, combination, obligations, approvals, explain, invariants   WRD-08, design A08
internal/router             admission, filter, rank, health, fallback, pin, routing.decision        WRD-06, WRD-16 §6.3, design A09
internal/platform           OS differences only: transport (socket/pipe), secrets backend, config dirs, default sandbox level   WRD-02 §3
internal/model              canonical types, StreamEvent, error codes, provider_options            WRD-05 §3–§4
internal/providers/{anthropic,openaicompat}   adapters, auth modes, probes                         WRD-05 §5–§8, design A11
internal/harness/{copilot,codex,claudecode}   vendor CLIs in sandbox, hooks → policy               WRD-05 §9, WRD-16 §6.2, design A12
internal/sandbox/{linux_bwrap,darwin_seatbelt,oci}   spec → launch; limits; env; oci = internal network + sandbox + proxy sidecar   WRD-10 §5, WRD-16 §10.4, design A06
cmd/warden-proxy            proxy sidecar binary (L2) and in-process proxy (L1); allowlist; events over stdio          WRD-16 §10.4–§10.5
images/                     Dockerfiles: warden/proxy, warden/sandbox-{node,go,python}; pinned digests
internal/exec               executor protocol client (daemon side) and server (warden-exec side)   WRD-04 §5
internal/proxy              per-task CONNECT proxy, allowlist, events                              WRD-10 §7, design A07
internal/secrets            keychain backends, secret:// refs, deny-list helper, redaction         WRD-10 §6, design A15
internal/worktree           session branch, worktree, hook neutralization, checkpoints, cleanup    WRD-07 §10, design A14
internal/store              SQLite, migrations, events + hash chain, artifacts, approvals, export/verify   WRD-09, WRD-16 §11, design A04
agents/{coder,verifier}     manifest.yaml, prompts/, schemas/                                      WRD-03, WRD-16 §7
workflows/poc-coding.yaml   the fixed template                                                     WRD-16 §8
policy/platform-defaults.yaml   L0/L1 invariants, command profiles, deny-list                     WRD-16 §9–§10
fixtures/                   ts-express-api, go-cli-tool, injection-lab (git bundles) + build scripts   WRD-16 §4
scripts/                    lint-imports.sh, lint-exec.sh, escape-check.sh, smoke-eval.sh, demo.sh
apps/desktop                Tauri 2 (src-tauri: sidecar + socket bridge) + React UI                WRD-16 §13, WRD-11, design B-series
docs/                       specification, design, working files
```

Import rules enforced by `scripts/lint-imports.sh` (run in `make check`): `internal/providers/*` and `internal/harness/*` import only `internal/model` (and stdlib); `internal/exec` imports nothing from the daemon; `internal/policy` imports no provider or harness package; `apps/desktop` contains no Go; `cmd/*` contain only wiring; `runtime.GOOS` and build tags for OS selection appear only in `internal/platform`, `internal/sandbox/*` and `internal/secrets` backends.

## 7. Coding standards

Go:
- `gofmt`, `go vet`, `staticcheck` clean. Errors wrapped with `%w` and classified with the WRD-05 §4 codes where they cross a boundary (`model.ErrRateLimited` etc.). No `panic` outside `main` initialization. Every goroutine has a `context.Context` and a way to stop.
- Every package starts with a doc comment naming the WRD/design sections it implements (`// Package policy implements WRD-08 §2–§8 and design A08.`).
- Structured logging (`log/slog`) with redaction applied before logging; never log request bodies at `info`.
- Public functions have table-driven tests; wire formats and policy decisions have golden files under `testdata/`; fakes for providers (`httptest`), executor (`FakeExecutor`), sandbox (`FakeSandbox`), clock.
- No global mutable state except the registered tool descriptors. Configuration is explicit structs loaded from YAML with unknown fields rejected.
- Concurrency: the scheduler is the only component that starts task goroutines; one execution owns one sandbox; cancellation flows through contexts and `warden-exec` signals.

TypeScript/React:
- Strict TypeScript, no `any`; API types generated from the JSON Schemas in `internal/api/schema` (one generation script, committed output).
- Server state only through the JSON-RPC subscription (TanStack Query + event stream reducer); no component talks to the bridge directly except the `api/` module.
- Every UI element that shows a decision, a routing choice or a cost names its data source (event type or method) in a code comment.
- Keyboard handling for approvals (A, R, 1–4) implemented centrally; approval prompts never auto-dismiss.

Rust (bridge only): a handful of Tauri commands (`rpc_call`, `rpc_subscribe`, `daemon_status`) that open the Unix socket with the token; no business logic; no network.

## 8. Testing strategy

Tests are how this PoC proves its hypotheses; they are not optional and they are not written after the fact. Every component lands with its tests in the same commit. The strategy has seven layers; each layer has a location, a command, a trigger and a rule about what it may touch.

### 8.1 The layers

| Layer | What it proves | Location and naming | Command | Runs | May touch |
|---|---|---|---|---|---|
| L0 Static checks | Formatting, vet, staticcheck, import rules, exec rules, no host TCP listener, schema validity of all YAML examples, generated API types up to date | `scripts/lint-*.sh`, `make lint` | `make lint` (part of `make check`) | every commit, CI on 3 OSes | nothing external |
| L1 Unit tests | One package's logic in isolation: policy combination, router ranking, hash chain, adapters' mapping, state machine transitions, path canonicalization, redaction | `*_test.go` next to the code; `Test<Unit>_<Behavior>` | `go test ./...` (part of `make check`) | every commit, CI on 3 OSes | in-memory fakes only: `FakeProvider`, `FakeExecutor`, `FakeSandbox`, `FakeClock`, in-memory secrets backend, temp SQLite |
| L2 Golden and contract tests | Wire formats and decisions do not drift: adapter request/response pairs per protocol; policy corpus (ActionRequest → Decision); event envelopes with known hashes; API method params/results validate against `internal/api/schema`; manifest and workflow examples validate against their JSON Schemas | `testdata/golden/**` next to the package; `TestGolden_*`; update with `UPDATE_GOLDEN=1 go test` and review the diff | part of `make check` | every commit, CI | files only |
| L3 Integration tests | Real OS primitives: bubblewrap/Seatbelt launch, Docker L2 with the sidecar, proxy CONNECT path, worktree and hook neutralization, Credential Manager/Secret Service/Keychain backends, named pipe and Unix socket transports | `*_integration_test.go` with `//go:build integration`; `scripts/escape-check.sh` | `make integration`, `make escape-check` | locally on the OS under test (L1 in WSL2, L2 in WSL2 and Windows, Seatbelt on macOS); CI: Linux L1 + L2 on `ubuntu-latest` (Docker available); Windows and macOS integration runs are manual and recorded in `docs/PROGRESS.md` | the real sandbox backends, Docker, local filesystem under a temp dir; never the network beyond `127.0.0.1` and the proxy test server |
| L4 End-to-end (headless) | The whole daemon on a real fixture: `warden run` through the full workflow, gates and approvals via the CLI, audit export and `verify --strict` | `e2e/` package with `//go:build e2e`; one test per demo task T1–T6 and per security scenario S1–S4 | `make e2e` | CI on `ubuntu-latest` with a `FakeProvider` driven by **recorded transcripts**; locally also against real models when configured | real daemon, real sandbox (L1 or L2), fixtures from `fixtures/*.bundle`; model calls answered from recorded transcripts unless `WARDEN_E2E_LIVE=1` |
| L5 UI tests | Components render every state; the app completes T1 against a mocked daemon; keyboard approval flow; CLI parity | `apps/desktop/src/**/*.test.tsx` (Vitest + Testing Library) for components; `apps/desktop/e2e/*.spec.ts` (Playwright) against a mock daemon that replays an event log | `make ui-test` | every commit, CI on `ubuntu-latest` and `windows-latest` | mock daemon only |
| L6 Smoke evaluation | Real models actually complete the demo tasks; costs and durations are recorded | `scripts/smoke-eval.sh` → `warden eval smoke` | `make smoke` | manual, after M4, and before every demo; results pasted into `docs/PROGRESS.md` | real providers configured on the machine, real sandbox |

`make check` = L0 + L1 + L2 + L5 component tests. It must pass before every commit and is what CI runs on all three operating systems. `make integration`, `make escape-check`, `make e2e` and `make smoke` are explicit.

### 8.2 Recorded transcripts (how real-model behavior is tested without flakiness)

- `internal/model/record` provides a `RecordingProvider` that wraps a real provider and writes every request/response pair (redacted, content-hashed) to `testdata/transcripts/<case>/<provider>.jsonl`, and a `ReplayProvider` that answers from those files by matching the request hash (with a tolerant fallback on the step index).
- E2E tests for T1–T6 and S1–S4 run against `ReplayProvider` by default, so they are deterministic and need no key. `WARDEN_E2E_LIVE=1` switches to the real provider and re-records when `WARDEN_E2E_RECORD=1`.
- Transcripts are committed; they contain no secrets (the redaction pipeline runs before recording and a test asserts it). Re-record when a prompt or tool definition changes; the diff is reviewed like code.

### 8.3 Security tests (mandatory, not "when there is time")

- `scripts/escape-check.sh`: every row of WRD-16 §10.8, for L1 and L2; fails on the first violation; produces a table in its output that goes into `docs/PROGRESS.md`.
- Policy corpus: `internal/policy/testdata/corpus/*.json`, at least one case per rule in `policy/platform-defaults.yaml` and the user policy, plus every invariant INV-1–INV-9, plus the monotonicity property test for the restrict-only layer.
- Redaction corpus: `internal/secrets/testdata/redaction/*.txt` with expected counts per secret type; includes real-looking AWS, GitHub, OpenAI, Anthropic, JWT, private-key and connection-string samples (synthetic, never real).
- Injection scenarios S1–S4 as E2E tests with recorded transcripts; each asserts the exact events of WRD-16 §4.3 (`policy.decision{deny}`, `proxy.denied`, absence of the file in the sandbox, no hook execution).
- `TestNoSecretsInEvents`: runs a full T1 e2e with a known fake key and asserts it appears nowhere in the store, logs or artifacts.

### 8.4 Fakes, fixtures and test data

- Fakes live in `internal/<pkg>/<pkg>test` packages (`policytest`, `sandboxtest`, `providertest`) and are the only doubles allowed; no ad-hoc mocking of exec or network in unit tests.
- Fixtures: `fixtures/<name>/src` is the checked-in source; `make fixtures` builds `fixtures/<name>.bundle` deterministically (fixed commit date and author). T1's intentionally failing 404 test lives in `fixtures/ts-express-api/src/test/users.hidden.test.ts` and is injected by the e2e grader, not visible to the agent.
- Temp SQLite stores per test; never the real `~/.warden`. Tests set `WARDEN_HOME` to a temp dir and fail if it is unset in integration/e2e.

### 8.5 Performance budgets (asserted in integration tests, reported in PROGRESS.md)

Daemon cold start < 2 s; L1 sandbox creation < 1.5 s; L2 sandbox creation < 6 s with a warm image; policy decision < 2 ms p50 (benchmark `BenchmarkDecide`); event append < 1 ms p50; cancellation to all-processes-gone < 5 s; UI first paint of the session view < 500 ms with 1,000 events (Playwright trace).

### 8.6 Coverage and quality rules

- Statement coverage targets enforced in CI: `internal/policy`, `internal/router`, `internal/store`, `internal/secrets`, `internal/exec` ≥ 90%; `internal/agentloop`, `internal/orchestrator`, `internal/providers/*` ≥ 80%; repository overall ≥ 70%. Coverage is a floor, not a goal; a test without an assertion does not count as a test.
- One behavior per test; table-driven where cases vary; names say what is asserted (`TestRouter_FallbackNeverWidensTier`, not `TestRouter2`).
- Flaky tests are bugs: a test that fails intermittently is fixed or quarantined with a `//flaky: <issue>` comment and an entry in `docs/PROGRESS.md` within the same session; never retried in CI to hide the flake.
- A test is never weakened, skipped or deleted to make the build pass. If a test and a document disagree, write it in `docs/CONFLICTS.md` and stop at the end of the step.
- Tests run on all three OS runners; OS-specific tests use build tags (`//go:build linux`, `windows`, `darwin`) and each OS-specific code path has at least one test on its OS.

### 8.7 What each milestone must add (minimum)

| Milestone | Tests that must exist when it is done |
|---|---|
| M1 | Platform transport tests on 3 OSes; adapter goldens for both protocols; store hash-chain and `verify --strict` tests incl. tampered fixture; secrets redaction corpus; API schema contract tests; CI matrix green |
| M2 | Executor protocol tests; sandbox spec tests (`TestSandboxSpecMounts`, env allowlist); proxy CONNECT/deny tests; `escape-check` L1 (WSL2) and L2 (WSL2 + Windows) |
| M3 | Policy corpus and invariants; monotonicity property test; agent-loop tests (budgets, untrusted wrapping, denial feedback, emulated tool calling); first e2e T1 with recorded transcript; S2 e2e |
| M4 | State-machine transition table test; resume-from-events test; cancellation timing test; router admission and fallback tests; verifier parser goldens; e2e T1 with repair round, T2, T6; classification switch e2e |
| M5 | Harness adapter tests against a fake Copilot server (from the spike); e2e T1 on `ReplayProvider` for the harness; T1 gateway-auth adapter test |
| M6 | Component tests for every component in design B04 and every state in B01; Playwright T1 against the mock daemon on Linux and Windows; `TestCLIParity` |
| M7 | macOS integration (Seatbelt, Keychain) when available; hardening regressions from escape-check findings as new rows |
| M8 | H6 usability session recorded; full `make check && make integration && make e2e` green on Linux and Windows; smoke results for all configured models in `docs/POC-REPORT.md` |

## 9. Security rules for the code you write

1. Agent-requested processes run only via `internal/sandbox` → `warden-exec`. `os/exec` elsewhere needs a `//sandboxed: <reason>` comment and is limited to the packages listed in INV-H.
2. Paths from the model are canonicalized inside the sandbox before matching (`..`, symlinks); the platform deny-list is applied at policy, executor and mount level.
3. Secrets are `secret://` references until the moment of injection into an adapter header or the proxy; they are never returned by any API method, never serialized, never logged.
4. All tool outputs, artifacts and context pass the redaction pipeline before persistence or model calls; counts are recorded, values never.
5. Harness processes get read-only mounts of only the files they need (their config/token directory), L2 by default, vendor-endpoint allowlists, and their output is tainted.
6. Git inside sandboxes runs with `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_SYSTEM=/dev/null`, `-c core.hooksPath=/warden/empty`; the main repository's `.git` is not mounted into the sandbox.
7. Approval scopes are enforced by the policy engine, not by the UI; R5 actions (push, protected-branch commit, writes outside worktree, message/ticket tools) are `once` only.
8. Non-interactive mode turns `approval_required` into `deny` unless a pre-approval exists in the policy bundle.

## 10. Rules for how you work

Allowed without asking: creating and editing code, tests, scripts, Makefile, CI, fixtures, and the working files in `docs/`; choosing implementation details not fixed by the documents (record them); installing the dependencies listed in §4; running `make check`, `make build`, unit tests; running `make escape-check` and `make smoke` when prerequisites exist.

Ask before: adding a dependency not in §4; changing anything in §3–§5; implementing a design-document instruction that conflicts with an invariant; anything that would send data to a provider during tests; deleting or weakening a test; touching files outside the repository.

Never: edit WRD or design documents; write a credential into any file, test, log or event; run an agent-requested process outside a sandbox; implement token replay, header spoofing or vendor-client impersonation; open a TCP port in the daemon; add out-of-scope features; mark a milestone done without its acceptance commands having been run and summarized in `docs/PROGRESS.md`.

Stop conditions (finish the step, write `docs/PROGRESS.md`, then stop and report): the milestone's acceptance criteria pass; a question meets the "ask" criterion; a conflict between layers blocks progress; a prerequisite is missing on the machine (bwrap, Docker, Ollama, Copilot CLI login) and cannot be installed by you; three consecutive attempts to fix a failing test have not worked.

When the specification leaves a detail open: choose the simplest option consistent with §5, implement it, and append to `docs/DECISIONS-poc.md`: date, question, choice, reason, documents consulted. Decide-and-record is the default; asking is the exception.

## 11. Git, files you maintain, session protocol

- Branch per milestone: `m1-skeleton-providers`, `m2-sandbox-executor-proxy`, … Merge to `main` only when the milestone's acceptance criteria pass.
- Commit messages: `<package>: <what> (WRD-xx §y / design Axx)`. Small, compiling, tested commits; never commit red.
- `docs/INDEX.md`: inventory of every document in `docs/` with one line each and its layer (specification / design / working); built in session 0 and updated when files are added.
- `docs/CONFLICTS.md`: each conflict with both references, the rule applied (precedence §2) and the resolution or "needs owner".
- `docs/DECISIONS-poc.md`: dated decision log (see §10).
- `docs/PROGRESS.md`: at the top, the current milestone's checklist; below, per session: built, tested (exact commands and a summary of their output), left, open questions.
- Session protocol: (1) read this file, `docs/PROGRESS.md`, `docs/DECISIONS-poc.md`, `docs/CONFLICTS.md`; (2) write the session checklist; (3) implement in small steps with `make check`; (4) run the milestone's acceptance commands; (5) update `docs/PROGRESS.md`; (6) stop.

## 12. Commands

`make check` · `make build` · `make run-daemon` · `make desktop` · `make escape-check` (integration; needs bwrap or Docker) · `make smoke` (runs T1 and T2 with configured providers) · `make fixtures` · `warden doctor`.

## 13. Definition of done (milestone)

The milestone's acceptance criteria from WRD-16 §15–§16 (restated in `docs/PROMPT-Claude-Code.md`) pass on Linux (WSL2) and on Windows (where the milestone has Windows-visible behavior); `make check` is green on all three CI runners; new behavior is covered by tests including the invariant tests that apply; `docs/DECISIONS-poc.md` and `docs/CONFLICTS.md` are current; `docs/PROGRESS.md` reports the acceptance commands and their results; the branch is merged to `main`.

## 14. Quick reference

Hypotheses H1–H6 (WRD-16 §1) · demo tasks T1–T6 and security scenarios S1–S4 (WRD-16 §4) · risk classes R0–R6 (WRD-04 §3) · tiers T0 local, T1 private-hosted, T2 enterprise cloud, T3 vendor API, T4 subscription harness (WRD-06 §3) · classifications public/internal/confidential/restricted (WRD-06 §2) · effects allow/deny/approval_required (WRD-08 §3) · task states created/queued/running/waiting_for_approval/waiting_for_input/succeeded/failed/cancelled/timed_out/skipped/blocked (WRD-07 §4) · event types (WRD-09 §3) · storage layout `~/.warden/` (WRD-09 §8).

## 15. Team and process

The main session orchestrates. Specialised agents in `.claude/agents/` do the work, each with a fresh context, a narrow job and only the tools that job needs. Repeatable procedures are skills in `.claude/skills/`.

| Agent | Job | Writes |
|---|---|---|
| `planner` | Problem → ordered tasks, each with acceptance criteria, the test that proves it, references, risks | nothing (returns a plan) |
| `implementer` | One task: test first, prove it fails, code, prove the test bites, `make check`, one commit | code + tests |
| `reviewer` | Independent correctness and contract review of a commit range | nothing (findings) |
| `security-reviewer` | Attacker's review against §5, §9 and WRD-10 | nothing (findings) |
| `test-engineer` | Mutation checks, edge cases, coverage floors, flaky tests, §8.7 milestone tests | tests only |
| `platform-engineer` | CI on three OSes, scripts, Docker/WSL2/Windows, prerequisites | build, CI, scripts |
| `docs-writer` | Working files (§11) kept true to what was run | Markdown only |
| `ui-designer` | B-series design → specs; review of the running app (from M6) | nothing (findings) |

The flow:

1. `/preflight`
2. `/plan`, then wait for the owner to agree
3. `/deliver` for each task:
   1. `implementer`
   2. `reviewer` and `security-reviewer`, in parallel
   3. fixes
   4. `test-engineer`
   5. `docs-writer`
4. `/milestone-close`: acceptance, audits, docs, PR, CI

Rules that make this work:

- **Reviews are independent.** Reviewers get the diff and the acceptance criteria, not the implementer's reasoning. A finding needs a concrete failure scenario. Unconfirmed findings are verified before anyone acts on them.
- **One checkout per writer.** Parallel tasks run in separate `git worktree`s, and two agents never commit in the same checkout. Run `git branch --show-current` before every commit.
- **The owner decides** three things: the questions §10 says to ask, conflicts marked "needs owner", and merges. Agents stop and report rather than guess.
- **Explain the why.** The owner is building depth in architecture, security, testing and operations through this project. When a choice is non-obvious, plans, reviews and reports name the concept behind it in a sentence or two.
- **Guards, not reminders.** `.claude/settings.json` runs `.claude/hooks/guard-bash.sh` and `guard-edit.sh` before every command and edit. Each rule names the incident that motivated it. The guards refuse:
  - pushes to `main`/`master`, bare pushes from them, and force pushes;
  - `--no-verify`;
  - commits on `main`/`master`;
  - commits whose working tree has not passed `make check` (`make check` records the tree it passed on; Markdown-only changes are exempt);
  - `warden`/`wardend` runs without `WARDEN_HOME`;
  - edits to `docs/docs/`, `docs/design/`, `docs/WRD-*` and `docs/PROMPT-*`.

  `.claude/hooks/test-hooks.sh` tests every rule in both directions and runs in `make check`. A blocked command means the work is not done yet; fix the cause rather than working around the guard.
