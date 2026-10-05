# Agent Runtime PoC (Warden)

Warden is an enterprise AI agent runtime. It runs AI agents against the models an organization already has, inside a sandbox the agent cannot escape. A policy engine authorizes every action, and every action is recorded in a hash-chained audit trail. This repository is the **Proof of Concept** defined in [`docs/WRD-16-PoC-Concept-and-Build-Plan.md`](docs/WRD-16-PoC-Concept-and-Build-Plan.md). [`CLAUDE.md`](CLAUDE.md) is the working contract.

The PoC proves end-to-end feasibility of a secure coding workflow: plan, implement, verify, summarize. It does so with policy enforcement, model abstraction and auditable events.

## Status

Milestone **M1** is implemented: the skeleton, platform layer, store, providers, daemon, CLI and the Copilot spike. [`docs/PROGRESS.md`](docs/PROGRESS.md) records what was verified on Windows and in WSL2 Linux. It also lists what is blocked on prerequisites: Ollama, the Copilot CLI and Docker Desktop.

No agent can run tasks yet. The sandbox executor arrives in M2 and the agent loop in M3.

## Build and test

You need Go 1.26 or later (developed with 1.27.1), git, and bash (Git for Windows on Windows).

```bash
make check
```

On Windows without GNU make:

```bash
powershell -File make.ps1 check
```

`make check` runs:

- gofmt
- `go vet` and staticcheck for Linux, macOS and Windows
- the architecture rules:
  - INV-H: no process creation outside the sandbox packages
  - INV-J: no TCP listener
  - the import rules
- the schema checks
- all unit, golden and contract tests

`make build` writes `bin/wardend` and `bin/warden`.

## Try it

Set a throwaway home first, so nothing touches `~/.warden` or `%LOCALAPPDATA%\Warden`:

```bash
export WARDEN_HOME=/tmp/warden-demo
```

```bash
bin/warden doctor
```

```bash
bin/warden provider add ollama
```

```bash
bin/warden audit verify --strict --file internal/store/testdata/audit/clean.jsonl
```

```bash
bin/warden daemon stop
```

`warden` starts `wardend` when it is needed. The daemon listens only on a Unix socket (`$WARDEN_HOME/run/wardend.sock`, mode 0600), or on Windows a named pipe with an owner-only DACL. Every connection authenticates with the token in `run/token`.

Provider secrets go to the OS keychain, or to an encrypted file unlocked with `warden unlock`. They never go into `models.yaml`.

## Layout

| Path | What |
|---|---|
| `cmd/wardend`, `cmd/warden` | daemon and CLI |
| `internal/api` (+ `schema`) | JSON-RPC runtime API (design A05) and its JSON Schemas |
| `internal/model` (+ `wire`, `probe`) | canonical model API (WRD-05), shared HTTP/SSE plumbing, capability probe |
| `internal/providers/{anthropic,openaicompat}` | provider adapters (design A11) |
| `internal/store` (+ `jcs`) | SQLite event ledger, hash chains, checkpoints, audit export/verify (design A04) |
| `internal/secrets` | keychain and encrypted-file backends, `secret://` broker, redaction, checkpoint signer (design A15) |
| `internal/platform` | every OS difference: home, socket/pipe, files, spawn, terminal |
| `internal/config` | `models.yaml` loading and validation, probe cache |
| `internal/exec/denylist` | the platform deny-list shared by policy, executor and sandbox |
| `internal/sandbox`, `internal/worktree` | M1: doctor probes. M2/M4: sandboxes and worktrees |
| `internal/archtest` | static architecture checks |
| `spikes/copilot` | Copilot SDK spike (its own module) |
| `docs/` | specification (WRD), design (A/B series), and the working files `INDEX.md`, `CONFLICTS.md`, `DECISIONS-poc.md`, `PROGRESS.md` |
