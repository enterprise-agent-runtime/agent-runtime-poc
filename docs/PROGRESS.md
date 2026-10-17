# Progress

The current milestone's checklist is at the top; below it, one entry per session (built, tested with the exact commands and a summary of their output, left, open questions). `CLAUDE.md` §11.

## Current milestone: M2 — sandbox, executor, proxy

Branch `m2-sandbox-executor-proxy` (off `first-design-poc`), one PR per task into it, one milestone PR at the end. Plan by the `planner` agent, 2026-10-16. The owner answered its four questions on 2026-10-16:

- **D-041:** `golang.org/x/sys` is allowed as a direct dependency.
- **C-61:** OS build tags are allowed in the executor.
- **Windows hardening (WH-1..3):** runs before the L2 tasks, in parallel with the executor work.
- **C-62:** escape-check egress goes to a local test upstream; one online row is opt-in for the recorded acceptance run.

Acceptance: WRD-16 §16 week 2, as restated in `docs/PROMPT-Claude-Code.md` M2. **[Docker]** marks a task that needs Docker Desktop running with WSL2 integration.

**Preflight, 2026-10-16:**

| Check | Result |
| --- | --- |
| Toolchains and access | Go 1.27.1 on both sides, push access, CI green |
| L1 sandbox prerequisites | bwrap 0.9.0, userns ok, libseccomp present |
| Docker | **Not running** on Windows or in WSL2 |
| cgroups | WSL2 has v1 only, so L1 limits use rlimits |
| Disk | D: has 30 GB free; images live on C:, so this is fine |

- [ ] **M2-0** Bookkeeping: this plan; WSL checkout fast-forward; L1 escape-check tools in WSL (curl, netcat-openbsd, dnsutils, strace, util-linux). The tools need the owner's OK to install.

**Group A** (platform and guard rails; no Docker):

- [x] **M2-1** `warden daemon stop` waits for wardend to exit (D-029). Commits `1ff879a`, `5f4a238`, `0894d6d`, `89dbdac`, `4812dae`. The review also found that test daemons shared the owner's keychain, so each home now has its own keychain namespace (D-042, C-63) and the integration test cleans up after itself (D-043). Evidence: on Windows the new integration test failed 3/3 before the fix and passed after, leaving no new Credential Manager entry. Two reviews and two re-reviews.
- [x] **M2-2** archtest guards for the executor.
  - **What landed:** OS tags allowed in the executor (C-61); one file-level forwarder listener exemption (D-030); the A02 import rules R3/R4/R10; a ban on importing `cmd/`; a fail-closed scanner; complete INV-H/INV-J API lists; exact-file allowlists for raw syscalls and DLLs (D-044).
  - **Commits:** `6bdf845`, `fb26514`, `9f196a5`, `dc6cb2e`, `0323023`, `b952d49`, `baa9d11`, `13e7bca`, `9df8fec`, `3cfdfb6`, `d56d865`, `9be7467`, plus the CLAUDE.md alignments `788c9e2`, `93b19af`, `3cb2b58`, `a1e0e83`, `8d51530`.
  - **Review:** six rounds, each closing a bypass class: the daemon linking the forwarder, intermediate packages, skipped directories, links, imports of ignored paths, execabs. The last round approved under an explicit convergence rule.
- [x] **M2-3** CLI output is terminal-safe (CWE-150, D-045). Human output is sanitized, all `--json` output and event payloads are escaped, a Docker engine error keeps only its status code, and Docker Version/Os are validated. Commits `bd2fe0d`, `d92f362`, `35ebc68`, `6d89542`, `5e98358`, `df0724e`. The review found that `--json` passed C1 controls raw. The re-review approved, after fuzzing both functions for 40 s each (about 2M inputs) with no failure.

**WH, Windows hardening** (before L2, in parallel with the executor work):

- [x] **WH-1** The token file is owner-only from creation: random exclusive temp name, descriptor set by CreateFile, verified after writing; existing homes and subdirectories are checked and tightened (D-051, D-052).
- [x] **WH-2** Owner-only also requires owner == current user (CWE-283); the client reads the token once on a no-follow handle before dialling; bind before token, autostart only on `ErrNotRunning` (D-051, D-053, C-66, C-67).
  - **Commits:** `c8f81ca`, `4e452d5`, `19b0043`, `eebe8d7`, `330a5ed`, `9730d41`, `6e0f286`, `160d10e`, `5533efd`.
  - **Review:** three rounds, code and security together. An earlier round reproduced a second daemon overwriting the live token; round 3 approved after 40 crash-and-race trials with no anomaly and every earlier probe (symlink, FIFO, junction, loose marker, planted temp name) refused.
- [x] **WH-4** Follow-ups from the WH-1+2 review (D-063, D-064, C-72, C-73).
  - **What landed:** the spawned wardend is reaped (a failed start reports its exit status in about 3 s); a lifetime lock `run/wardend.lock` taken before any write and released last; the token removed at shutdown only if it is still ours; the elevation hint for Administrators-owned directories; checkpoint key files trusted for integrity (owner, no foreign writer), a refused one moved aside; the link message covers every directory.
  - **Commits:** `c8af1c6`, `cdee732`, `f943167`, `df340eb`, `d1e409c`, `05dcff2`.
  - **Review:** two rounds. Round 1 found the first key-file check refused every 0644 key an older build wrote, stopping an upgraded headless home (reproduced), and the lock taken after keys and migrations. Round 2 approved; each test fails on revert.
  - **Low, left:** `writePublicKey` moves a key aside on any read error, including a transient Windows sharing violation (move only on the integrity refusal or a parse failure); the "store closes before unlock" order is checked by reading, not by a test; on Windows a scanner holding `wardend.lock` open reports "already running" (retry briefly); the CLI sends a malformed token and gets -32602 instead of -32001 (validate the pattern first); a sudo-owned directory on Unix could get a hint like the elevation one.
  - **For M6 (desktop):** with `--token-stdin` a losing sidecar exits 3 without reading stdin, so the desktop's write may fail with EPIPE; attach on exit 3 as D-053 says.
- [x] **WH-3** The client verifies that the endpoint's server is the current user before sending the token, on Windows and on Unix (D-049).
  - **Daemon side:** the socket directory must be owned by the uid; the macOS peer check is on.
  - **New test:** the Windows pipe DACL is owner-only, which was a missing M1 transport test.
  - **Commits:** `963cc2b`, `f04a5d0`, `0365c39`.
  - **Review:** a combined code and security review approved it after a real cross-user squat in WSL2. The squatter got 0 bytes, and the daemon refused the squatted directory.

**Group B** (executor; no Docker):

- [x] **M2-4** Executor wire types in `internal/execproto` (design A02 layout, D-046).
  - **Scope:** error codes, command profiles and matching (A08 limits), and shell-string detection stricter than A06 (C-64, D-047).
  - **Commits:** `38efd07`, `eb427b2`, `0b7ef4e`, `5d1c6de`, `223b804`, `0fee0cc`, `3ddbd15`, `8272194`, `8e49d97`.
  - **Review:** four rounds. They found secret leaks, a parser differential, two quadratic slow inputs (measured at 9.7 GiB and 84 s), a panic and over-blocking. The final review fuzzed it for 70 s per target with no failure, and package coverage is 96%.
- [x] **M2-4b** The deny-list moved to `internal/execproto/denylist`, a pure move with its tests unchanged (D-014 note). archtest enforces A02 R6: non-test `internal/execproto/...` imports only the standard library, and `import "C"` is not counted as standard library, which also tightens R3. Commits `a99c8e9`, `26c4f4d`. Reviewed once and approved.
- [x] **M2-5** Ordered JSON-RPC transport for the executor (D-050).
  - **Pieces:** the Content-Length codec in `internal/execproto`, the server in `internal/exec`, the client in `internal/tools`; it runs over a socketpair or stdio.
  - **Commits:** `bcfa1fc`, `c37e6d7`, `8bcce6f`, `fdbde48`.
  - **Review:** three rounds. They found a write that ignored its deadline (CWE-1088), unsanitized executor text, silently ignored unknown ids, and a cancel-while-waiting teardown. Fuzzing ran 5.3M + 1.5M execs with no failure; `-race -count=20` passes; coverage 94–98%.
  - **Left for M3:** check `WaitResult.State == exited && ExitCode != nil` before trusting that all output is in.
- [x] **M2-6** Resolve and race-free open under the roots (D-054, D-055, C-68).
  - **Pieces:** pure `Resolve` in `internal/exec` (every OS), the fd-stack walk and `OpenCanonical` (openat2, or the O_NOFOLLOW walk) on Linux and macOS.
  - **Commits:** `ab63720`, `6e260c3`, `deeea82`.
  - **Review:** three rounds. They found `/work//../..` clamped instead of refused (CWE-22), NTFS short names past every deny-list layer (CWE-41), an untested st_dev check, an open that trusted a forged `Resolved`, and the 8.3 rule over-blocking `git diff HEAD~1`. FuzzResolve ran 1.38M execs clean; `-race` passes in WSL2.
  - **Left for M2-7:** refuse reads of regular files with `st_nlink > 1` (hard links, CWE-59); pass only `Tree.Resolve` output to `OpenCanonical`; temp names that cannot match the deny-list or the 8.3 rule (e.g. `.warden-tmp-<rand>`); reject `Resolved.Root != worktree` for model-originated calls.
  - **Left for L2 (owner sign-off):** a spawned process in Windows L2 can still open `ENV~1` past the `/dev/null` mask; disable 8.3 names on the sessions volume or have doctor refuse.
  - **Low:** `strings.ToLower` does not fold U+017F (matters on case-insensitive APFS).
- [x] **M2-7** fs operations: read, list, search, write, patch, stat (D-066, D-067; the 4096-file patch cap of D-048).
  - **Pieces:** `internal/exec` `RegisterFS` and the six methods on M2-6's `Tree.Resolve`/`OpenCanonical`; the patch parser; `execproto` limits pinned to the fs schema.
  - **Commits:** `81383d6`, `c4dfd8d`, `c61eac0`, `4266b30`, `5500a26`.
  - **Review:** two rounds plus a small fix. No escape, deny-list bypass or content leak was found; round 1 found three bounds failures (a patch result over the frame after the files were written, unbounded search, a quadratic parse) and a search that could read scratch through a swapped link. 62 + 21 mutants (survivors layered or equivalent); FuzzPatch 47M execs; `-race` in WSL2; coverage 91%%.
  - **Left for M2-10:** `warden-exec` calls `Tree.SweepTemps` at start-up; a concurrency cap on fs calls (N searches cost N x 31 s of CPU).
  - **Left for M3:** the daemon rejects `Resolved.Root != worktree` for model-originated calls (an absolute link in the worktree resolves read and stat into scratch).
  - **Low, left:** list and search fail `too_large` instead of truncating when hostile long names push the result over the frame; reads under a directory named `.env` are allowed while writes refuse such prefixes; U+017F and HFS+ ignorable code points (carried from M2-6).
- [ ] **M2-8** proc operations, rlimits and output cap
- [ ] **M2-9** git.run
- [ ] **M2-10** the `cmd/warden-exec` binary and the forwarder

**Group C** (proxy; no Docker):

- [x] **M2-11** Egress proxy core: normalize, range check, allowlist, CONNECT and plain HTTP, events (D-058, D-059, D-060, C-69, C-70).
  - **Pieces:** `internal/proxy`: `Normalize`, the range and floor checks, exact-match `Allowlist`, the `Decider` hand-off with `StaticDecider`, CONNECT with the SNI check, plain HTTP, limits, `proxy.connect`/`proxy.denied` events. No listener (INV-J): `Serve` takes one the caller owns.
  - **Commits:** `7ad63b0`, `eac0d47`, `f713670`, `41a09ae`, `bb6ef92`, `7b189e2`, `71a358f`.
  - **Review:** three rounds. They found events lost at shutdown (CWE-778), an unbounded flood before evaluation (CWE-770), an operator entry acting as a standalone allow (CWE-863), operator entries in transition ranges (CWE-918), misleading shutdown reasons and an uncapped method. The SSRF matrix (every literal encoding, zone ids, mapped/NAT64/6to4/Teredo, DNS rebinding) was refused throughout. 47 + 7 + 7 mutants caught; `-race -count=20` in WSL2; coverage 92.6%.
  - **Left for M3:** the PDP behind `Decider`, the 120 s approval hold, the deny cache and per-host rate-limit summaries.
  - **Minor, left:** the burst summary is flushed only at the next accept or at shutdown (delayed, not lost; a 10 s timer would make it timely), and the flush runs in the accept loop (a slow store delays only the task's own accepts).
  - **Accepted residual:** no tunnel inspection after a valid ClientHello, so domain fronting through the inner Host is possible.
- [x] **M2-12a** Per-task Unix listener for L1 (D-065, C-74).
  - **Pieces:** `internal/platform` `ListenTaskSocket`, `TaskSocket`, `SweepTaskSockets` (Linux and macOS; unsupported elsewhere); `internal/sandbox` `OpenProxy`, `ProxyListener`, `SweepProxySockets`.
  - **Commits:** `2aa3972`, `2cc6c6c`.
  - **Review:** one round, approved; its one low finding (a swapped directory during bind could make the refusal delete another session's live socket) was fixed before merge. 33 mutants caught; `-race -count=20` in WSL2. Cross-user cases use seams: no second uid is available without sudo.
  - **Left for M2-15 (wiring):** call `SweepProxySockets(home)` once in wardend after `platform.Listen` succeeds and before any sandbox; `Manager.Create` calls `OpenProxy` before launch, binds `pl.Path()` at `/run/warden/proxy.sock`, and closes it on every failure path; `Destroy` closes it before `sandbox.destroy`.
  - **Left for session creation:** make `<home>/sessions` and S owner-only (D-052's `Ensure` does not manage `sessions/`); otherwise `OpenProxy` fails closed with `sandbox_unavailable`.
  - **Low, left:** check ancestors above the home, or do every operation relative to an `os.OpenRoot` of `S/scratch`; a `hook-<n>.sock` (A12 §7.3) needs its own sweep pattern; archtest could list `net.ListenUnix` for consistency (it only accepts unix networks, so not a gap).

**Group D** (L1 sandbox, WSL2):

- [x] **M2-12** Spec, mounts and env model, with `TestSandboxSpecMounts`, `TestSandboxEnvAllowlist` and `TestDenyListThreeLayers` (D-056, D-057).
  - **Pieces:** `Spec`, `Validate`, `Mounts`, `Env`, `WorktreeMasks`, the `Manager` interface, and `sandboxtest.FakeSandbox` built from the same code. The shared three-layer corpus is in `internal/execproto/denylist/testdata/three-layers.json`; the executor and policy layers load it too.
  - **Commits:** `4a530f3`, `d9d381d`, `abd1dc5`, `fa795a5`, `c8fce80`.
  - **Review:** two rounds. Four checks had no test (the sidecar host, the walk entry cap, the toolchain-root overlap, the main objects deny-list); a custom WARDEN_HOME let `$HOME` be a toolchain root; the proxy socket and the session directory were not pinned. After the fixes 28 of 29 mutants are caught; coverage 94%%.
  - **Low, left:** the `ids.Valid` check on the session id has no isolating test row (the ulid match still guards it).
  - **Left for M2-13:** EvalSymlinks each toolchain root and re-check (`/var/run` is a link to `/run`, exposing docker.sock); consider reserving `/sys`; Lstat the proxy socket for S_ISSOCK before binding; pin the backend-added mounts to A06 §11.1 in the bwrap argv test; a real-disk walk test with `os.DirFS`; `Manager.RecoverOrphans` with the A06 §13.5 sweep.
  - **Must fix before the L2 backend merges (high, CWE-178):** bind masks are keyed on the on-disk case, so on a case-insensitive host file system (Windows L2 over NTFS through Docker Desktop, macOS L2) `.ENV` reads the masked `.env`; reproduced on WSL drvfs. Make S/worktree case-sensitive before checkout and fail closed if it is not, or mask host-side; add an escape-check row. NTFS 8.3 names (`ENV~1`) bypass the masks the same way for spawned processes (D-055).
  - **Before a real Spec is built (platform layer):** expand host paths to their long form (GetLongPathName) before they reach the Spec; host-path checks skip the 8.3 rule (D-055 amendment), so a short-form host path would otherwise dodge it.
  - **For A14 / the policy engine:** masks hide only the working tree; `git show HEAD:.env` through /git reads a committed secret, and `denylist.Argv` does not match `HEAD:.env`. A deny-listed pathspec rule for git object reads is needed.
- [x] **M2-13** bwrap argv (D-068, D-069, C-75, C-76).
  - **Pieces:** `internal/sandbox/linux_bwrap`: `ParseFeatures`, `Prepare`, the pure builder, the fd plan and `Plan.Command`, `EtcFiles`, the sealed seccomp memfd, `Limits()` for `sandbox.create.limits`, `SeccompKill`; `Spec.ResolveToolchains`; `/sys`, `/var/run` and `/nix/var` reserved.
  - **Commits:** `d1ceda6`, `64094b2`.
  - **Review:** two rounds; round 2 approved, with the leak confirmed fixed by the reviewer's own probe. The argv, mounts, masks, read-only root, /proc, capabilities, seccomp and the memfd seals checked clean against the real bwrap 0.9.0; round 1 found the daemon environment readable at `/proc/1/environ` (high), resolved toolchain roots compared with unresolved homes, `/etc/ssl/private` inside a bind, and `/nix/var` unreserved.
  - **Left for M2-15:** launch through `Plan.Command` (with the `systemd-run --user --scope` prefix), rlimits and cgroups, write `EtcFiles` into `S/sandbox/<id>/etc`, `state.json`/`argv.json` and `profile_digest`, read the info pipe, put `Limits()` in `sandbox.create`, `Manager.RecoverOrphans` (A06 §13.5), register the backend.
  - **Must hold in M2-15:** `Command` must own the `systemd-run --user --scope` wrapper too. systemd-run needs `XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS`, and with `--scope` bwrap inherits its environment, so the `/proc/1/environ` leak returns unless bwrap runs under it through `/usr/bin/env -i`; repeat the integration assertion for the wrapped path. `sandbox.create.env_keys` must list `PWD`, which bwrap sets after `--chdir`.
  - **Low, left:** a toolchain configured as `/nix/var/nix/profiles/...` (the standard multi-user Nix profile) is refused although it resolves into `/nix/store` (apply the store-only rule to the resolved root); mask `/etc/pki/tls/private` (Fedora) like `/etc/ssl/private`; `Plan.Argv` is exported, so a hand-written launcher could bypass `Command` (the `//sandboxed` gate reviews that); a home that does not exist fails closed when toolchains are configured.
  - **Left for M2-16:** escape-check rows for `/proc/1/environ` and the masks.
  - **Later:** `--ro-bind-fd` with O_PATH descriptors on bwrap ≥ 0.9 against the EvalSymlinks-to-bind window (only same-uid host processes can use it); `doctor_linux.go` should use `ParseFeatures` and report missing flags; a doctor line for Arch's `/sbin -> usr/bin` layout, which fails closed today.
- [x] **M2-13b** Inherited descriptors never reach a sandbox (D-070). Found by the M2-23 CI job on ubuntu-24.04; reproduced in WSL by opening a descriptor without close-on-exec before the launch. Commit `6ae5cc5`; 8 mutants caught. Left for M2-10: `warden-exec` flags every descriptor above its planned ones at start (it cannot import `internal/platform`, A02 R3).
- [x] **M2-14** seccomp in pure Go (D-061, D-062, C-71).
  - **Pieces:** `internal/sandbox/seccomp`: the A06 §11.5 table as data, the cBPF assembler and `Render(arch)`, `DockerProfile()`, `Version = "v1"`.
  - **Commits:** `1d0e81b`, `dd19245`, `961e265`.
  - **Review:** one round plus fixes. It confirmed every row on both arches, the fail-closed paths, the x32 check, the upper-32-bit behaviour in the kernel and 6.4M Docker-vs-program probes; it found the aarch64 numbers unchecked in CI and `pidfd_getfd` past the ptrace row. 30 mutants caught; the kernel test in WSL2 shows each filtered call refused and each control allowed.
  - **Left for M2-13:** pick the arch at run time; write `Render(arch)` to fd 5 (memfd or pipe) and pass `--add-seccomp-fd 5` (fallback `--seccomp 5`); write `DockerProfile()` to `S/sandbox/<sb_id>/seccomp.json` and pass `--security-opt`; report `Version` in `sandbox.create.limits`; map a SIGSYS exit to `seccomp_kill`.
  - **Left for M2-10:** `warden-exec` sets `PR_SET_DUMPABLE 0`; escape-check row "steal an executor descriptor".
  - **Left for L2:** runc silently skips syscall names its libseccomp does not know (`mount_setattr` needs 2.5.2); have doctor check the version or verify in the L2 escape check; verify runc's bad-arch action.
- [ ] **M2-15** launch, rlimits, `sandbox.cgroup` doctor check
- [ ] **M2-16** `make escape-check` L1

**Group E** (L2 sandbox):

- [ ] **M2-17** [Docker] engine gate at creation (CWE-636), plus `sandbox.default_level` normalisation (CWE-178)
- [ ] **M2-18** container spec and Windows path mapping
- [ ] **M2-19** images and `make images`
- [ ] **M2-20** [Docker] proxy sidecar
- [ ] **M2-21** [Docker] launch over container stdio, orphan cleanup
- [ ] **M2-22** [Docker] escape-check L2 in WSL2 and on Windows
- [ ] **M2-23** CI Linux integration job, pinned to ubuntu-24.04
- [~] **INV-I** `TestDenyListThreeLayers` covers the exec and mount layers in M2; the policy layer arrives in M3

## Milestone M1 — skeleton, platform layer, store, providers, daemon, CLI, Copilot spike (open items carried)

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
- [x] GitHub Actions `.github/workflows/ci.yml`: `make check` on `ubuntu-latest`, `windows-latest` (through `make.ps1`), `macos-latest`; `go test -race` on Linux. First run red (FX-1..FX-3); green on all four jobs since run 37825777402 (2026-10-08, see FX-4)
- [x] `internal/model` — canonical types, StreamEvent, ten error codes, `provider_options`, Provider contract, sequence validator, stream assembly, effective capabilities, `warden-emu@1` codec
- [x] `internal/model/wire` — SSE reader, credential-injecting transport, TLS/mTLS client, first-byte/idle watchdog, retry-after parsing
- [x] `internal/providers/anthropic` — hand-rolled Messages adapter (D-005): streaming, usage, error normalization, response_format by tool forcing, probe; request/stream/error goldens against `httptest`
- [x] `internal/providers/openaicompat` — auth `none`, `api_key` (Bearer and Azure `api-key`), `gateway` bearer and mTLS; quirks with the single shape retry; non-streaming fallback; probe with vLLM/LM Studio/Ollama context metadata; goldens
- [x] Neutrality: `TestNeutrality_SameRequestYieldsToolProposals` — one `ModelRequest` → the same canonical tool proposal from Anthropic (T3), Ollama-style (T0) and a gateway vLLM (T1)
- [x] `internal/platform` — home (`~/.warden`, `%LOCALAPPDATA%\Warden`, `WARDEN_HOME`), Unix socket (0600, SO_PEERCRED on Linux) / named pipe with owner-only DACL, owner-only files (Windows ACL), default sandbox level, detached daemon spawn, process-group termination, no-echo input, free space
- [x] `internal/secrets` — keyring (Secret Service / Keychain / Credential Manager), encrypted file (PBKDF2 + AES-256-GCM, D-010), memory; strict `secret://` parsing and consumer binding; broker with `secret.access` events; redaction (16 types + known values) with `TestRedactionCorpus`; redacting log handler; Ed25519 checkpoint signer
- [x] `internal/execproto/denylist` — WRD-10 §6 deny-list, CF-17 matcher, argv heuristic (D-014)
- [x] `internal/ids` (ULID), `internal/store/jcs` (RFC 8785 with the RFC vectors)
- [x] `internal/store` — A04 DDL as migration 0001, append-only triggers, per-session + `sys` hash chains (CF-09), signed `chain.checkpoint` with session-close anchors, blob spill, `audit export` (JSON Lines) and `audit verify [--strict]` in store and file mode; `TestAuditStrictOrdering`; tampered-store tests; deterministic audit fixtures in `internal/store/testdata/audit/`
- [x] `internal/config` — `models.yaml` with unknown-field rejection and the A11 §5.2 rules; file-backed probe cache
- [x] `internal/api` + `internal/api/schema` — JSON-RPC 2.0 with Content-Length framing over the platform endpoint, `system.hello` token gating, batch rejection, strict params against the A05 schemas (extracted verbatim, 53 files), ordered subscriptions with exact replay/live handoff, methods `system.hello/version/doctor/shutdown`, `secrets.unlock` (NEW), `provider.list/add/remove/enable/test/models`, `event.subscribe/unsubscribe/query`, `audit.export/verify`; every result validated against its schema in the tests
- [x] `internal/sandbox` (doctor probes: bwrap version, userns probe, Docker engine over its socket/pipe), `internal/worktree` (host git version)
- [x] `cmd/wardend` — token file before bind, `runtime.start/stop`, SDK credential variables scrubbed, logs through the redactor, exit codes 0/1/2/3
- [x] `cmd/warden` — `doctor`, `daemon start|stop|status`, `unlock`, `provider add|test|list|remove|enable`, `harness enable`, `models`, `audit export|verify`, `events`, `version`; starts wardend when absent
- [~] `spikes/copilot/` + `README.md` — compiled against SDK v1.0.16; **not executed** (Copilot CLI not installed, D-ENV-07); differences recorded (CONFLICTS C-51)

### M1 acceptance (prompt Part 2, M1)
- [x] `make check` green locally — **yes on Windows (checkout A) and in WSL2 Ubuntu 24.04 (`~/src/warden`)** — and on the three CI runners (run 37825777402, FX-4)
- [!] `warden provider test ollama` succeeds against the local Ollama — **Ollama is not installed** (D-ENV-04). The command runs and reports `provider_unavailable: provider unreachable` honestly on Windows; the success path is covered by `TestProbe_*` against fake servers
- [x] Anthropic adapter passes its golden tests against a fake server
- [x] A unit test proves the same `ModelRequest` produces tool proposals from both adapters
- [x] `audit verify` fails on a tampered fixture store and passes on a clean one (`TestVerify_TamperedStoreFails`, `TestGolden_AuditFixtures`; CLI on the committed fixtures on Windows and Linux)
- [x] `warden doctor` reports sandbox prerequisites honestly (bwrap present + version, userns probe with the sysctls, Docker reachable or not, keychain backend and lock state, executor pending M2)
- [~] Windows verification — `make check` green; `wardend` starts and listens on `\\.\pipe\warden-<user>`; `warden doctor` works; `warden provider test ollama` runs but fails because Ollama is absent
- [!] Copilot spike executed — Copilot CLI not installed

### CI fixes for PR #1 (first CI run 37365637550, 2026-10-05)

The first CI run on PR #1 had three failing tests, and none was a race, a panic or a compile error. The runs:

| Job | Result |
|---|---|
| `go test -race (linux)` | failed: bug 1 |
| `make check (windows-latest)` | failed: bugs 2 and 3 |
| `make check (ubuntu-latest)` | never ran ("job was not acquired by Runner"); would hit bug 1 |
| `make check (macos-latest)` | passed |

Planned by the `planner` agent. Each task is one commit, test first, delivered through `/deliver` (CLAUDE.md §15). Ids are `FX-n` so they don't collide with the demo tasks T1–T6.

- [x] **FX-1** `sandbox: the userns check names its fix when bubblewrap is unavailable` (design A05 §8.1) - `4c69f91`, merged `26d312d`
  - Evidence: the test failed before the fix with `sandbox.userns is fail without a fix hint`; reverting the hint reproduces the CI error `doctor_test.go:36`. WSL2 `go test -race ./internal/sandbox/...` passes with bwrap present and hidden. Reviewer approve, security reviewer no findings.
  - Lift the L1 verdict into a pure `l1Checks(Options, l1Inputs)`.
  - Test first: `TestL1Checks_EveryNonOkCheckHasHint` (linux, L1). It covers {bwrap missing, too old, empty sandbox fails, ok} × {L1, L2}.
  - Records decision D-028.
- [x] **FX-2** `sandbox: the sandbox.l2 status follows blocking for every unusable engine` (CLAUDE.md §4, C-02) - `c2b3f62` + `a7d0d1b`, merged `9fa4315`
  - Evidence: `TestL2Check_StatusFollowsBlocking` failed first with `reachable_windows/L1: status = fail, want warn`. Reviewer approve with one minor (stale test comment, fixed in `a7d0d1b`); security reviewer no blocking findings.
  - Inject the ping into `l2CheckWith`.
  - Test first: `TestL2Check_StatusFollowsBlocking` (all OSes, L1). It covers {reachable linux, reachable windows-containers, unreachable} × {L1, L2}.
  - Records decision D-026.
- [x] **FX-3** `platform: the owner-only check compares the DACL structurally, not as SDDL text` (CLAUDE.md §4 IPC, D-011) - `0c98a32`, `f0f4670`, `fe4ebe9`, merged `be4fd4b`
  - Evidence: `TestOwnerOnly_AbbreviatedSID` fails with the old string compare (`DACL read back D:P(A;;FA;;;BA)`). Reviewer major finding (the "DACL absent" row did not isolate SE_DACL_PRESENT) fixed in `f0f4670` with a "present bit clear, valid ACL" row, proved to bite. Security reviewer: parser sound, fuzzed ~5.9M execs without a panic.
  - Add a pure, bounds-checked descriptor parser `ownerOnlyDACL`.
  - Test first: `TestOwnerOnlyDACL` (all OSes, hostile inputs) and `TestOwnerOnly_AbbreviatedSID` (windows, BUILTIN\Administrators renders as `BA`).
  - Adds no new dependency (`syscall` only). Records decision D-027.
- [x] **Test audit of FX-1..FX-3** (`2fab6a3`, then `1f737a7` + `f61cc96` after review, merged `821aa4d`)
  - 66 mutants: 38 killed by existing tests, 28 survived; 23 now killed, 5 equivalent (documented).
  - Added `FuzzOwnerOnlyDACL`, `TestValidSID`, `TestBwrapUsable`, `TestL1Checks_ReportsWhatItSaw`, `TestOwnerOnly_MissingFileIsError` and more table rows.
  - Review found the fuzz property wrong for a DACL offset inside the 20-byte header, so `sd.go` now rejects `off < 20` (`1f737a7`); `TestBwrapUsable` no longer pins the version on error rows (`f61cc96`). Re-review approve; fuzz 15 s, ~4M execs, clean.
  - Coverage: `internal/sandbox` Windows 82.7%, Linux 90.2% (unchanged; changed functions 100%); `internal/platform` Windows 65.4% -> 66.8%, Linux 58.9% -> 59.9%.
  - Windows `bash scripts/make.sh check`: exit 0, 23 packages ok after the FX-3 merge; for `821aa4d`: see FX-4.
- [x] **FX-4** CI run 37825777402 on `395b623` (PR #1), 2026-10-08: `go test -race (linux)` pass, `make check (ubuntu-latest)` pass (the job that never got a runner on the first run), `make check (macos-latest)` pass, `make check (windows-latest)` pass. PR #1 is green on all four jobs.
- Review follow-ups (proposed, not scheduled; owner decides):
  - M2: L2 sandbox creation must check the engine itself (ping, `Os == linux`) and refuse; doctor is not the gate (CWE-636).
  - The CLI prints doctor `Detail` raw: strip control characters and validate the Docker `Version`/`Os` shapes (CWE-150).
  - Config loading (C-06): normalise `sandbox.default_level` against `platform.SandboxLevels()` (CWE-178).
  - Windows owner-only: also verify the file OWNER equals the token user, and write the owner (`O:<sid>`) (CWE-283).
  - Windows `WriteOwnerOnly`: the token bytes exist before the DACL is set; create with the DACL at `CreateFile` time, use a random `O_EXCL` temp name, and set protected owner-only DACLs on the home and run directories in `Ensure` (CWE-367/377/276).
  - Production callers of `OwnerOnly`: wardend after writing the token; the client before sending it.
  - Named pipe: the client does not verify who serves `\.\pipe\warden-<user>` before sending the token (pipe squatting); needs its own security review.
  - A Windows test that the named pipe's DACL is owner-only (CLAUDE.md §8.7 M1 "platform transport tests on 3 OSes"); depends on FX-3.
  - `ubuntu-latest` moves to 26 on 2026-10-19: decide pinning with the M2 integration job.
- Owner questions:
  - Keep `TestProbe_ReportsEveryCheckHonestly` in `make check` (planner recommends yes for M1), or move it to `integration`?
  - Pin the CI image to `ubuntu-24.04` before ubuntu-latest moves to 26 on 2026-10-19? This belongs with the M2 integration job.

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
