# Progress

The current milestone's checklist is at the top; below it, one entry per session (built, tested with the exact commands and a summary of their output, left, open questions). `CLAUDE.md` §11.

## Current milestone: M1 — skeleton, platform layer, store, providers, daemon, CLI, Copilot spike

Branch: `first-design-poc` (owner's instruction; DECISIONS-poc D-002). Acceptance restates WRD-16 §16 week 1 and the prompt's M1 block.

Legend: `[x]` done and tested · `[~]` done with a recorded limitation · `[ ]` open · `[!]` blocked on a prerequisite (stop condition)

### Session 0
- [x] `docs/INDEX.md` — every file in `docs/` with layer; design deliverables A01–A18/B01–B09 mapped; milestone reading table
- [x] Reading per prompt Part 2 step 2 (CLAUDE.md, WRD-16 in full, design core, conflicts, open questions; A04, A05, A11, A12, A15, A17 and the WRD sections through targeted extraction)
- [x] `docs/CONFLICTS.md` — 60 conflicts (C-01 … C-60), rule applied, resolution or "needs owner"
- [x] `docs/DECISIONS-poc.md` — environment facts (D-ENV-01 … 08) and implementation decisions
- [x] `docs/PROGRESS.md` — this checklist

### M1 build
- [x] Go module `warden.dev/warden`; `Makefile` + `make.ps1` dispatching to `scripts/make.sh` (`check`, `lint`, `test`, `build`, `run-daemon`, `cover`, `integration`; `escape-check`, `smoke`, `fixtures`, `images`, `e2e`, `desktop` report the milestone they land in)
- [x] `scripts/lint-imports.sh`, `scripts/lint-exec.sh` over `internal/archtest` (INV-H `TestExecutorOnlyViaSandbox`, INV-J `TestNoListenTCP`, import rules, OS confinement, with a seeded violation tree)
- [ ] GitHub Actions: `make check` on `ubuntu-latest`, `windows-latest`, `macos-latest`; Linux `-race` job
- [x] `internal/model` — canonical types, StreamEvent, error codes, `provider_options`, Provider contract, sequence validator, emulated tool codec
- [ ] `internal/model/wire` — SSE reader, auth transport, watchdog, retry-after
- [ ] `internal/providers/anthropic` — streaming, usage, errors, probe; request/stream/error goldens against `httptest`
- [ ] `internal/providers/openaicompat` — auth `none`, `api_key` (Bearer, Azure `api-key`), `gateway` (bearer, mTLS), quirks + shape retry, probe; goldens
- [ ] Neutrality test: one `ModelRequest` → tool proposal from both adapters (Anthropic, Ollama-style, vLLM-style fakes)
- [ ] `internal/platform` — transport (Unix socket / named pipe with owner-only DACL), config dirs, default sandbox level, process-group termination, daemon spawn, no-echo input; tests per OS
- [ ] `internal/secrets` — memory, keyring (Secret Service / Keychain / Credential Manager), encrypted file; `secret://` parsing and resolution; redaction regex set with corpus; deny-list helper
- [ ] `internal/ids`, `internal/store/jcs`
- [ ] `internal/store` — A04 DDL and migrations, event envelope with per-session and `sys` hash chains, `chain.checkpoint` signed with Ed25519, `audit export` (JSONL), `audit verify [--strict]` with tampered fixture, `TestAuditStrictOrdering`
- [ ] `internal/config` — `models.yaml` / `config.yaml` with unknown-field rejection and A11 §5.2 validation
- [ ] `internal/api` + schemas under `internal/api/schema` — JSON-RPC over the platform transport, `Content-Length` framing, `system.hello` token handshake, method registry, notifications; `system.*`, `provider.*`, `event.subscribe/unsubscribe/query`, `audit.*`, `secrets.unlock`; schema contract tests
- [ ] `cmd/wardend`
- [ ] `cmd/warden` — `doctor`, `provider add|test|list`, `models`, `audit export|verify`, `daemon start|stop|status`, `unlock`
- [ ] `spikes/copilot/` + `README.md`

### M1 acceptance (prompt Part 2, M1)
- [ ] `make check` green locally (Windows checkout A and WSL2 clone) and in CI on three OSes
- [!] `warden provider test ollama` succeeds against the local Ollama — **Ollama is not installed on this machine** (D-ENV-04)
- [ ] Anthropic adapter passes its golden tests against a fake server
- [ ] Unit test proves the same `ModelRequest` produces tool proposals from both adapters
- [ ] `audit verify` fails on a tampered fixture store and passes on a clean one
- [ ] `warden doctor` reports sandbox prerequisites honestly (bwrap present/absent, userns, Docker)
- [ ] Windows verification: `make check`, `wardend` listening on the named pipe, `warden doctor`, `warden provider test ollama` (the last blocked as above)
- [!] Copilot spike executed — **Copilot CLI not installed** (D-ENV-07); the spike is written and compiled, its findings come from the SDK source

## Sessions

### Session 1 — 2026-10-04 (Session 0 + M1, in progress)

Built so far: documentation copied into `docs/` (D-001); Go 1.27.1 installed user-local on Windows and WSL2 with the owner's consent (D-ENV-02); build skeleton and archtest; `internal/model`.

Tested:
- `bash scripts/make.sh check` (Windows, Git Bash): gofmt clean; `go vet` for linux/darwin/windows clean; staticcheck v0.8.1 clean; archtest `ok`; `go test ./...` ok. `.\make.ps1 check` runs the same script.
- `go test -cover ./internal/model`: ok, 92.0% of statements.

(continued below as the session proceeds)
