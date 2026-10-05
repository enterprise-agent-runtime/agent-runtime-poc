# Progress

The current milestone's checklist is at the top; below it, one entry per session (built, tested with the exact commands and a summary of their output, left, open questions). `CLAUDE.md` §11.

## Current milestone: M1 — skeleton, platform layer, store, providers, daemon, CLI, Copilot spike

Branch: `first-design-poc` (owner's instruction; DECISIONS-poc D-002). Acceptance restates WRD-16 §16 week 1 and the M1 block of `docs/PROMPT-Claude-Code.md`.

Legend: `[x]` done and tested · `[~]` done with a recorded limitation · `[ ]` open · `[!]` blocked on a missing prerequisite (stop condition, CLAUDE.md §10)

### Session 0
- [x] `docs/INDEX.md` — every file in `docs/` with its layer; design deliverables A01–A18/B01–B09 mapped; milestone reading table
- [x] Reading per prompt Part 2 step 2 (CLAUDE.md, WRD-16 in full, design core, conflicts, open questions; A04, A05, A11, A12, A15, A17 and the WRD sections through targeted extraction)
- [x] `docs/CONFLICTS.md` — 60 conflicts (C-01 … C-60), rule applied, resolution or "needs owner"
- [x] `docs/DECISIONS-poc.md` — environment facts (D-ENV-01 … 08) and implementation decisions (D-001 … D-025)
- [x] `docs/PROGRESS.md` — this file

### M1 build
- [x] Go module `warden.dev/warden` (Go 1.26 directive, built with 1.27.1); `Makefile` + `make.ps1` dispatching to `scripts/make.sh` (`check`, `lint`, `test`, `build`, `run-daemon`, `cover`, `integration`; `escape-check`, `smoke`, `fixtures`, `images`, `e2e`, `desktop` report the milestone they land in)
- [x] `scripts/lint-imports.sh`, `scripts/lint-exec.sh` over `internal/archtest` (INV-H `TestExecutorOnlyViaSandbox`, INV-J `TestNoListenTCP`, import rules, OS confinement; each rule proven on a seeded violation tree)
- [~] GitHub Actions `.github/workflows/ci.yml`: `make check` on `ubuntu-latest`, `windows-latest` (through `make.ps1`), `macos-latest`; `go test -race` on Linux. **Not run yet: the branch could not be pushed** (the GitHub account `IosuaPallo` has read-only access to `enterprise-agent-runtime/agent-runtime-poc`)
- [x] `internal/model` — canonical types, StreamEvent, ten error codes, `provider_options`, Provider contract, sequence validator, stream assembly, effective capabilities, `warden-emu@1` codec
- [x] `internal/model/wire` — SSE reader, credential-injecting transport, TLS/mTLS client, first-byte/idle watchdog, retry-after parsing
- [x] `internal/providers/anthropic` — hand-rolled Messages adapter (D-005): streaming, usage, error normalization, response_format by tool forcing, probe; request/stream/error goldens against `httptest`
- [x] `internal/providers/openaicompat` — auth `none`, `api_key` (Bearer and Azure `api-key`), `gateway` bearer and mTLS; quirks with the single shape retry; non-streaming fallback; probe with vLLM/LM Studio/Ollama context metadata; goldens
- [x] Neutrality: `TestNeutrality_SameRequestYieldsToolProposals` — one `ModelRequest` → the same canonical tool proposal from Anthropic (T3), Ollama-style (T0) and a gateway vLLM (T1)
- [x] `internal/platform` — home (`~/.warden`, `%LOCALAPPDATA%\Warden`, `WARDEN_HOME`), Unix socket (0600, SO_PEERCRED on Linux) / named pipe with owner-only DACL, owner-only files (Windows ACL), default sandbox level, detached daemon spawn, process-group termination, no-echo input, free space
- [x] `internal/secrets` — keyring (Secret Service / Keychain / Credential Manager), encrypted file (PBKDF2 + AES-256-GCM, D-010), memory; strict `secret://` parsing and consumer binding; broker with `secret.access` events; redaction (16 types + known values) with `TestRedactionCorpus`; redacting log handler; Ed25519 checkpoint signer
- [x] `internal/exec/denylist` — WRD-10 §6 deny-list, CF-17 matcher, argv heuristic (D-014)
- [x] `internal/ids` (ULID), `internal/store/jcs` (RFC 8785 with the RFC vectors)
- [x] `internal/store` — A04 DDL as migration 0001, append-only triggers, per-session + `sys` hash chains (CF-09), signed `chain.checkpoint` with session-close anchors, blob spill, `audit export` (JSON Lines) and `audit verify [--strict]` in store and file mode; `TestAuditStrictOrdering`; tampered-store tests; deterministic audit fixtures in `internal/store/testdata/audit/`
- [x] `internal/config` — `models.yaml` with unknown-field rejection and the A11 §5.2 rules; file-backed probe cache
- [x] `internal/api` + `internal/api/schema` — JSON-RPC 2.0 with Content-Length framing over the platform endpoint, `system.hello` token gating, batch rejection, strict params against the A05 schemas (extracted verbatim, 53 files), ordered subscriptions with exact replay/live handoff, methods `system.hello/version/doctor/shutdown`, `secrets.unlock` (NEW), `provider.list/add/remove/enable/test/models`, `event.subscribe/unsubscribe/query`, `audit.export/verify`; every result validated against its schema in the tests
- [x] `internal/sandbox` (doctor probes: bwrap version, userns probe, Docker engine over its socket/pipe), `internal/worktree` (host git version)
- [x] `cmd/wardend` — token file before bind, `runtime.start/stop`, SDK credential variables scrubbed, logs through the redactor, exit codes 0/1/2/3
- [x] `cmd/warden` — `doctor`, `daemon start|stop|status`, `unlock`, `provider add|test|list|remove|enable`, `harness enable`, `models`, `audit export|verify`, `events`, `version`; starts wardend when absent
- [~] `spikes/copilot/` + `README.md` — compiled against SDK v1.0.16; **not executed** (Copilot CLI not installed, D-ENV-07); differences recorded (CONFLICTS C-51)

### M1 acceptance (prompt Part 2, M1)
- [~] `make check` green locally — **yes on Windows (checkout A) and in WSL2 Ubuntu 24.04 (`~/src/warden`)**; CI on three OSes not run (push blocked, see above)
- [!] `warden provider test ollama` succeeds against the local Ollama — **Ollama is not installed** (D-ENV-04). The command runs and reports `provider_unavailable: provider unreachable` honestly on Windows; the success path is covered by `TestProbe_*` against fake servers
- [x] Anthropic adapter passes its golden tests against a fake server
- [x] A unit test proves the same `ModelRequest` produces tool proposals from both adapters
- [x] `audit verify` fails on a tampered fixture store and passes on a clean one (`TestVerify_TamperedStoreFails`, `TestGolden_AuditFixtures`; CLI on the committed fixtures on Windows and Linux)
- [x] `warden doctor` reports sandbox prerequisites honestly (bwrap present + version, userns probe with the sysctls, Docker reachable or not, keychain backend and lock state, executor pending M2)
- [~] Windows verification — `make check` green; `wardend` starts and listens on `\\.\pipe\warden-<user>`; `warden doctor` works; `warden provider test ollama` runs but fails because Ollama is absent
- [!] Copilot spike executed — Copilot CLI not installed

## Sessions

### Session 1 — 2026-10-04 / 2026-10-05 (Session 0 + M1)

**Built:** everything ticked above, in 25 commits on `first-design-poc`. Go 1.27.1 was installed user-local on Windows and in WSL2 with the owner's consent (D-ENV-02).

**Tested (exact commands, summarised output):**

| Where | Command | Result |
|---|---|---|
| Windows 11 (Git Bash) | `bash scripts/make.sh check` (= `.\make.ps1 check`) | gofmt clean; `go vet` for linux/darwin/windows clean; staticcheck v0.8.1 for linux/darwin/windows clean; archtest ok; 19 test packages ok; exit 0 |
| Windows | planted `var   Bad = 1` (gofmt violation), `bash scripts/make.sh check` | exit 1 (the lint failure now fails the check, D-025); file removed |
| WSL2 Ubuntu 24.04 | `make check` in `~/src/warden` | identical, exit 0 |
| Windows | `go test -count=1 -cover ./...` | all ok; total statement coverage 73.8% (see coverage table) |
| Windows | `warden daemon status` → `warden daemon start` → `warden daemon status` | "not running" exit 1 → "started wardend (pid …)" → "running on `\\.\pipe\warden-pallo-48099ec6`, features [event.gap cancel_request]" exit 0 |
| Windows | `warden doctor` | sandbox.backend ok (L2 only), **sandbox.l2 FAIL blocking** (Docker Desktop not running), exec.binary warn (M2), keychain ok (wincred round trip), checkpoint.key ok (key loaded, checkpoints signed), store ok, disk ok, catalog ok, providers warn (none), providers.local warn (no Ollama/LM Studio), git ok (2.45); overall fail, exit 1 |
| Windows | `warden provider add ollama` / `warden provider test ollama` | added; probe failed `provider_unavailable: provider unreachable`; test exit 1; `provider.configured` events on the sys chain |
| Windows | `warden daemon stop` | `runtime.stop(shutdown_request)`; token and pid files removed |
| Windows + WSL2 | `warden audit verify --strict --file internal/store/testdata/audit/clean.jsonl` | 12 events, chain ok, checkpoints ok, strict ok (2 tool calls), anchored, VERIFIED, exit 0 |
| Windows + WSL2 | same on `tampered-edited.jsonl` / `tampered-dropped.jsonl` | NOT VERIFIED, exit 1: `hash_mismatch` + `decision_not_allow` / `prev_hash_mismatch`, `anchor_mismatch`, `missing_decision` |
| WSL2 | `warden daemon start`, `warden doctor` | sandbox.backend ok (bubblewrap 0.9.0), sandbox.userns ok (empty bwrap sandbox started; userns_clone and AppArmor restriction absent, max_user_namespaces 63358), sandbox.l2 warn (Docker not running), keychain warn (encrypted file, locked), checkpoint.key warn (unsigned until unlock), git ok (2.43); overall warn, exit 0. Socket `srw-------` in a `drwx------` run dir |
| WSL2 | `warden unlock` (passphrase on stdin), `FAKE_TOKEN=… warden provider add company-vllm --base-url https://llm.example.internal/v1 --tier T1 --auth bearer --secret-env FAKE_TOKEN --model … --no-test` | secrets file created (0600, no plaintext), checkpoint key created and loaded; provider added; the token appears in no file under the home; events: `secret.access` (checkpoint, adapter:company-vllm) and `provider.configured` |

**Coverage (statements, `go test -cover`):** model 92.2% · model/wire 83.6% · model/probe 95.4% · providers/anthropic 89.6% · providers/openaicompat 89.3% · store 86.6% · store/jcs 92.1% · secrets 79.9% · exec/denylist 96.2% · ids 98.3% · config 85.2% · api 80.1% · api/schema 82.6% · platform 58.8% · sandbox 68.0% · archtest 91.2% · worktree 89.5% · total 73.8%. Below the CLAUDE.md §8.6 floors: `internal/store` (90%: the uncovered lines are SQLite failure branches), `internal/secrets` (90%: the OS keyring backend is exercised only by integration tests, by rule). The CI coverage gate is not wired yet.

**Bugs found and fixed while testing:** jrpc2's client reorders notifications (D-016); `Serve` hung on shutdown with open client connections; probe P4t derived a context size from Warden's own estimate (D-023); ULID generator at millisecond 0; trailer counts decoded as floats in export verification; `make check` ignored lint failures (D-025); POSIX-only path patterns in the A05 schemas (C-47).

**Side effects on the owner's machine:** Go 1.27.1 in `%LOCALAPPDATA%\Programs\go` and `~/.local/go` (WSL); the checkpoint signing key in Windows Credential Manager (`warden:keys/checkpoint/ed25519`, normal product behaviour on first start); a Warden home at `%LOCALAPPDATA%\Warden` created by one CLI run without `WARDEN_HOME` (its daemon was stopped; the directory can be deleted); a WSL clone in `~/src/warden`.

**Left for M1 (needs the owner or a prerequisite):**
1. Push the branch and open the PR (write access to `enterprise-agent-runtime/agent-runtime-poc`, or a fork), then read the CI result on the three runners.
2. Install Ollama (and a coder model) and run `warden provider add ollama` + `warden provider test ollama` on Windows and in WSL2.
3. Install and log in to the Copilot CLI, run `spikes/copilot` (`-cli copilot`), and record the observed behaviour in `spikes/copilot/README.md`.
4. Start Docker Desktop before M2 (L2 is the only level on Windows).
5. Anthropic key and a company-hosted endpoint for `warden provider test anthropic` / `company-vllm` (WRD-16 §16 week 1 "Ollama or vLLM deployed on a VPS").
6. Coverage gate in CI and the two packages below their floors; macOS peer-credential check (`getpeereid`, stdlib gap) recorded as deferred.

**Open questions for the owner (from CONFLICTS "needs owner"):** C-01 (WRD-16 says Windows is not a target), C-05 (definition of "untrusted repository"), C-12/C-33 (dependencies outside §4, if any are wanted later), C-14 (Claude Code outside personal mode), C-19 (T6 read-only template), C-23 (Ollama via host IP as T1), C-24 (sandbox mounts beyond INV-B), C-25/C-27 (macOS Seatbelt loopback, orphan processes), C-35 (UI dependencies), C-41 (CLAUDE.md cites WRD-16 §10.8 and escape-check row 9), C-42 (acceptance on Linux + Windows instead of macOS + Linux), C-45 (the one process-start exception for `warden daemon start`), C-59 (missing WRD-16 figures), C-60 (agent-loop seed).
