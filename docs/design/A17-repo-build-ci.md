# A17 Repository layout, build and CI

Go module layout, the full directory tree, Makefile targets, the three-binary build with CGO disabled and version stamping, the `shared` build tag, Tauri 2 sidecar bundling, the GitHub Actions pipeline, release packaging, and versioning. Names from `00-DESIGN-CORE.md`; conflicts cite `CF-xx`. Packages from core §4 and WRD-16 §5.2 (extended by CF-34, CF-35).

## 1. Go module

**Decision: single module, path `warden.dev/warden`.** One module (not multi-module) keeps `cmd/`, `internal/` and the import-rule lint in one place; `warden.dev/warden` matches the `apiVersion: warden.dev/v1alpha1` and `schemas.warden.dev` conventions already in the specs and avoids tying the module identity to a git host. A vanity import path (`warden.dev/warden`) is served by a static `<meta name="go-import">` page later; for the PoC, builds are from the local checkout so the path need not resolve over the network. `go 1.23` (WRD-16 preparation). `apps/desktop` contains no Go and is not part of the module (core §4 import rule; enforced by `internal/archtest`).

`go.mod` (sketch):

```
module warden.dev/warden

go 1.23

require (
    github.com/creachadair/jrpc2 v1.x        // JSON-RPC 2.0 server/client (WRD-16 §5.1)
    github.com/google/cel-go v0.x            // policy CEL (A08)
    github.com/spf13/cobra v1.x              // CLI (A02)
    github.com/anthropics/anthropic-sdk-go v0.x
    github.com/github/copilot-sdk/go v0.x    // harness (A12); verify availability week 1 (spike)
    github.com/zalando/go-keyring v0.x       // keychain (A15)
    modernc.org/sqlite v1.x                  // pure-Go SQLite, CGO_ENABLED=0 (WRD-16 §5.1)
    github.com/oklog/ulid/v2 v2.x            // ULIDs
)
```

## 2. Directory tree

From WRD-16 §5.2 extended with core §4 packages (`execproto`, `audit`, `archtest`, `tools`), the sandbox sub-packages, `schemas/`, and the desktop split.

```
warden/
  go.mod  go.sum  Makefile  .golangci.yml  README.md  LICENSE  SBOM.spdx.json
  cmd/
    wardend/       main.go               # daemon; starts api, store, orchestrator…
    warden/        main.go               # CLI (cobra); starts daemon if absent
    warden-exec/   main.go               # in-sandbox executor + `proxy` subcommand
  internal/
    api/           # JSON-RPC server, system.hello handshake, token auth, notifications (A05)
      wire/        # NEW (A02): request/response/event wire types; stdlib only
      client/      # NEW (A02): JSON-RPC client used by cmd/warden (CLI imports only this + wire)
    session/       # sessions, session branch, resume (A02)
    orchestrator/  # workflow runner, state machine, gates, repair (A13)
      verify/      # verifier result parsers: vitest, gotest, pytest/junit (A13 §8)
    agentloop/     # context assembly, model call, proposals, compaction (A10)
    policy/        # PDP, CEL compile, approvals, explain, golden runner (A08)
    router/        # admission, ranking, health, fallback (A09)
    model/         # canonical ModelRequest/StreamEvent/error codes (A11)
    providers/
      anthropic/   # anthropic-messages adapter (A11)
      openaicompat/# openai-compatible adapter, 5 auth variants (A11)
    harness/
      copilot/     # copilot-sdk, split mode (A12)
      codex/       # codex app-server, colocated (A12)
      claudecode/  # claude-code cli, colocated, shared-mode lock (A12, CF-34)
    tools/         # DAEMON-SIDE executor client + Dispatcher target: talks to warden-exec over the socketpair (CF-35, A02 R7: only agentloop imports it)
      registry/    # descriptor table, name mapping .→__ (core §8)
    sandbox/
      darwin_seatbelt/  # Seatbelt profile template + launch (A06)
      linux_bwrap/      # bwrap argv + seccomp (A06)
      oci/              # optional L2 Docker (A06)
    exec/          # IN-SANDBOX executor implementation, linked only into cmd/warden-exec; imports only execproto + stdlib (core §4, A02 R3/R4); never linked into wardend
    execproto/     # NEW leaf: executor wire types shared by daemon + warden-exec (core §4)
    proxy/         # egress proxy, allowlist, per-task listener (A07)
    secrets/       # keychain, secret:// resolution, redaction (A15)
    worktree/      # session worktree, checkpoints, delivery git (A14)
    store/         # SQLite schema, events, hash chain, blobs, projections (A04)
    audit/         # NEW: export + verify (strict) on top of store (A04)
    buildinfo/     # NEW: version/commit/date set by -ldflags (§3)
    archtest/      # NEW: import-rule test (core §4)
  agents/
    coder/    { manifest.yaml, prompts/system.md, schemas/{input.json,output.json,plan.json} }
    verifier/ { manifest.yaml, prompts/system.md, schemas/{input.json,test-report.json} }
  workflows/
    poc-coding.yaml  poc-readonly.yaml            # (A13)
  policy/
    platform-defaults.yaml                         # L0/L1 invariants + profiles (A08); embedded
    golden/                                        # golden policy test corpus (A08)
      *.json
  schemas/
    events/*.json  artifacts/*.json  runtime-api/*.json  manifest.json  workflow.json  policy.json
  fixtures/
    ts-express-api.bundle  go-cli-tool.bundle  injection-lab.bundle
    py-fastapi-service.bundle                      # optional (WRD-16 §4.1)
  scripts/
    escape-check.sh  smoke-eval.sh  demo.sh
  apps/
    desktop/
      src-tauri/   { tauri.conf.json, Cargo.toml, capabilities/*.json, src/main.rs, binaries/ }
      src/         # React 19 + TypeScript + Vite UI (B08)
      package.json  vite.config.ts  playwright.config.ts
  .github/
    workflows/ci.yml  release.yml
  docs/            # WRD-00..WRD-16 + this design set
```

`policy/platform-defaults.yaml` and `workflows/*.yaml` are embedded into the binary via `go:embed` (core §10: "platform-defaults.yaml ships inside the binary and in repo policy/") and also present on disk for reference and golden tests.

## 3. Makefile targets

```make
BINARIES = wardend warden warden-exec
VERSION  ?= $(shell git describe --tags --always --dirty)
COMMIT   ?= $(shell git rev-parse --short HEAD)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  = -s -w \
  -X warden.dev/warden/internal/buildinfo.Version=$(VERSION) \
  -X warden.dev/warden/internal/buildinfo.Commit=$(COMMIT) \
  -X warden.dev/warden/internal/buildinfo.Date=$(DATE)
GOFLAGS  = -trimpath

.PHONY: build build-shared test lint policy-golden escape-check desktop clean sbom

build:            ## three binaries, CGO off, reproducible
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/wardend      ./cmd/wardend
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/warden       ./cmd/warden
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/warden-exec  ./cmd/warden-exec

build-shared:     ## shared-build variant (forces runtime.mode=shared; CF-21)
	CGO_ENABLED=0 go build $(GOFLAGS) -tags shared -ldflags '$(LDFLAGS)' -o dist/shared/wardend ./cmd/wardend

build-escapecheck: ## escape-check-instrumented daemon (A16 §3.1); never released
	CGO_ENABLED=0 go build $(GOFLAGS) -tags escapecheck -o dist/ec/wardend ./cmd/wardend
	CGO_ENABLED=0 go build $(GOFLAGS) -tags escapecheck -o dist/ec/warden-exec ./cmd/warden-exec

test:             go test -race ./...
lint:             golangci-lint run
policy-golden:    go run ./cmd/warden policy test policy/golden
escape-check:     build-escapecheck; WARDEN_ESCAPE_CHECK=1 scripts/escape-check.sh
desktop:          cd apps/desktop && npm ci && npm run tauri build
sbom:             syft dir:. -o spdx-json > SBOM.spdx.json
clean:            rm -rf dist
```

The daemon build stays `CGO_ENABLED=0` because `modernc.org/sqlite` is pure Go (WRD-16 §5.1), which makes cross-compilation for the macOS/Linux × amd64/arm64 matrix a plain `GOOS`/`GOARCH` change with no C toolchain. `-trimpath` and stamped `-ldflags` give reproducible, version-stamped binaries (S-11). `buildinfo.Version` is surfaced by `system.version` (core §6) and `warden version`.

### 3.1 The `shared` build tag (core §13.15, CF-21)

`internal/config` has two files:

```go
// runtime_mode_default.go
//go:build !shared
package config
const forcedSharedMode = false

// runtime_mode_shared.go
//go:build shared
package config
const forcedSharedMode = true
```

When `forcedSharedMode` is true, `runtime.mode` is forced to `shared` regardless of `config.yaml`, and the `claude-code` harness with `billing: subscription_personal` is locked (`harness_locked_shared_mode`, `platform.personal-mode-lock`; A12). The public/demo build is `make build-shared`; the personal build (default) leaves `runtime.mode` to `config.yaml` (default `personal`). `runtime.start` event records `mode` (core §5).

## 4. Tauri 2 sidecar bundling

`wardend` and `warden-exec` are bundled as Tauri **externalBin** sidecars (WRD-16 §5.1: `tauri-plugin-shell` sidecar). The webview never links Go; it talks JSON-RPC through a thin Rust bridge (B08).

### 4.1 externalBin naming with target triple

Tauri resolves a sidecar named `<name>` to a file `<name>-<target-triple>` at bundle time. The build places, in `apps/desktop/src-tauri/binaries/`:

```
wardend-aarch64-apple-darwin
wardend-x86_64-apple-darwin
wardend-x86_64-unknown-linux-gnu
wardend-aarch64-unknown-linux-gnu
warden-exec-aarch64-apple-darwin
warden-exec-x86_64-apple-darwin
warden-exec-x86_64-unknown-linux-gnu
warden-exec-aarch64-unknown-linux-gnu
```

Each is the corresponding `CGO_ENABLED=0 GOOS/GOARCH go build` output copied to the triple-suffixed name (a CI step, §5). `warden-exec` is bundled too because the daemon launches it inside the sandbox and must find it at a known path next to `wardend` (WRD-16 §10.2 launch line).

### 4.2 `tauri.conf.json` snippet

```json
{
  "productName": "Warden",
  "version": "0.1.0",
  "identifier": "dev.warden.desktop",
  "build": { "frontendDist": "../dist", "devUrl": "http://localhost:1420" },
  "bundle": {
    "active": true,
    "targets": ["dmg", "app", "appimage", "deb"],
    "externalBin": ["binaries/wardend", "binaries/warden-exec"],
    "macOS": { "signingIdentity": "-", "entitlements": "entitlements.plist" },
    "linux": { "appimage": { "bundleMediaFramework": false } }
  },
  "app": {
    "windows": [{ "title": "Warden", "width": 1280, "height": 832, "minWidth": 1024 }],
    "security": { "csp": "default-src 'self'; connect-src 'self' ipc: http://ipc.localhost" }
  },
  "plugins": {
    "shell": { "open": false }
  }
}
```

### 4.3 Capabilities / permissions restricting shell to the sidecar

Tauri 2 uses a capabilities model. The webview gets **no** general shell access; the shell plugin is configured to allow only spawning the `wardend` sidecar. `apps/desktop/src-tauri/capabilities/default.json`:

```json
{
  "$schema": "../gen/schemas/desktop-schema.json",
  "identifier": "default",
  "description": "Warden desktop: talk to the wardend sidecar only",
  "windows": ["main"],
  "permissions": [
    "core:default",
    "core:event:default",
    {
      "identifier": "shell:allow-execute",
      "allow": [
        { "name": "binaries/wardend", "sidecar": true, "args": true }
      ]
    },
    "shell:allow-kill"
  ]
}
```

`shell:allow-open` is not granted (no arbitrary URL/file opening). The bridge is B08 option (a): Rust code in `src-tauri` owns the Unix socket and the token, spawns the sidecar as `wardend --token-stdin` when no daemon answers (CF-14, ID-13), and exposes `invoke('rpc_call')` plus a Tauri `Channel` for events to the webview, restricted to the generated method allowlist `allowlist_gen.rs` (B08 §4, §6). The token never reaches page JavaScript. No localhost WebSocket exists in production builds (`no-ws-bridge` job, §5.0). `warden-exec` is launched only by `wardend` inside the sandbox, never by the shell plugin, so it is not in the capabilities allow list.

### 4.4 macOS signing / notarization

The `.app`/`.dmg` are signed with a Developer ID Application certificate and notarized via `notarytool`; the bundled sidecars (`wardend`, `warden-exec`) are signed with the same identity and the hardened runtime, with entitlements permitting `sandbox-exec` invocation (`com.apple.security.cs.allow-jit` not needed; the daemon calls `sandbox-exec`, which is a system binary). A re-signature of the sidecars is required whenever they change, otherwise the keychain re-prompts (WRD-13 §8 "Application signature changed"). In CI, signing runs only on the release workflow with secrets available (§6); PR builds use ad-hoc signing (`signingIdentity: "-"`).

### 4.5 Linux AppImage / deb

`targets: ["appimage", "deb"]`. The AppImage bundles the webview runtime and both sidecars; the `.deb` declares a dependency on `bubblewrap` (L1 prerequisite, WRD-13 §2.2) and recommends nothing else (Docker only for optional L2). CLI ships separately as a signed tarball with checksums (WRD-13 §2.2). Neither package requires root to run; the daemon runs as the user.

## 5. GitHub Actions pipeline (`.github/workflows/ci.yml`)

Runs on PR and push to `main`. Jobs: lint, unit tests, policy golden tests, escape-check matrix, desktop build, Playwright e2e. Release packaging is a separate workflow (§6).

```yaml
name: ci
on: { pull_request: {}, push: { branches: [main] } }
permissions: { contents: read }

jobs:
  lint:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - uses: golangci/golangci-lint-action@v8      # v2 config with depguard rules from A02 §6.2 (§5.1)
        with: { version: v2.1 }
      - name: JS lint + typecheck
        working-directory: apps/desktop
        run: npm ci && npm run lint && npm run typecheck   # eslint + tsc --noEmit

  unit:
    runs-on: ${{ matrix.os }}
    strategy: { matrix: { os: [ubuntu-24.04, macos-15] } }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - run: CGO_ENABLED=0 go test -race ./...          # includes internal/archtest import-rule test

  policy-golden:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - run: go run ./cmd/warden policy test policy/golden   # must be 100% (WRD-12 §6)

  escape-check:
    runs-on: ${{ matrix.os }}
    strategy:
      fail-fast: false
      matrix:
        include:
          - { os: macos-14 }
          - { os: macos-15 }
          - { os: ubuntu-22.04 }
          - { os: ubuntu-24.04 }
          - { os: ubuntu-22.04-arm }
          - { os: ubuntu-24.04-arm }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - name: Linux sandbox prerequisites
        if: startsWith(matrix.os, 'ubuntu')
        run: |
          sudo apt-get update && sudo apt-get install -y bubblewrap
          # Ubuntu 24.04 restricts unprivileged userns via AppArmor (§5.2)
          if sysctl -n kernel.apparmor_restrict_unprivileged_userns 2>/dev/null | grep -q 1; then
            sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
          fi
      - name: Build escape-check daemon
        run: make build-escapecheck
      - name: Run escape check
        run: WARDEN_ESCAPE_CHECK=1 scripts/escape-check.sh --format junit > escape-$RUNNER_OS-$RUNNER_ARCH.xml
      - uses: actions/upload-artifact@v4
        with: { name: escape-${{ matrix.os }}, path: 'escape-*.xml' }

  desktop:
    runs-on: ${{ matrix.os }}
    strategy: { matrix: { os: [ubuntu-24.04, macos-15] } }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - uses: dtolnay/rust-toolchain@stable
      - uses: actions/setup-node@v4
        with: { node-version: '22' }
      - name: Build sidecars for host triple
        run: |
          TRIPLE=$(rustc -vV | sed -n 's/host: //p')
          CGO_ENABLED=0 go build -trimpath -o apps/desktop/src-tauri/binaries/wardend-$TRIPLE ./cmd/wardend
          CGO_ENABLED=0 go build -trimpath -o apps/desktop/src-tauri/binaries/warden-exec-$TRIPLE ./cmd/warden-exec
      - name: Tauri build
        working-directory: apps/desktop
        run: npm ci && npm run tauri build

  e2e:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: '22' }
      - name: Playwright against mocked daemon
        working-directory: apps/desktop
        run: |
          npm ci
          npx playwright install --with-deps chromium
          npm run test:e2e            # runs UI against a mock JSON-RPC daemon (B08)
```

### 5.0 Additional CI jobs requested by other deliverables

These jobs are part of `ci.yml` (same triggers). Each row gives the job id, the runner, the command and the pass condition; the owning deliverable specifies the test content.

| Job id | Runs on | Command | Passes when | Owner |
|---|---|---|---|---|
| `secrets-canary` | `macos-15`, `ubuntu-24.04` | `go test -tags e2e ./internal/secrets/e2e -run TestCanary -v` (scripted demo session against loopback mock model servers, then byte search for the per-run nonce `N` in all its encodings) | zero hits outside the mock servers' auth headers; required `secret.access`, `redaction`, `policy.decision(deny, invariant.INV-1)` and `sandbox.violation(deny_list)` events present; locked-keychain rerun yields `auth_failed` | A15 §8.2 |
| `redactor-bench` | `ubuntu-24.04`, `macos-15` | `go test -run '^$' -bench 'BenchmarkRedact' -benchtime 3x ./internal/secrets/redact` piped to `scripts/check-bench.go` | ≥ 100 MB/s clean text, ≥ 20 MB/s adversarial text, 256 KiB tool output < 10 ms, 2 MiB file read < 50 ms | A15 (budgets) |
| `seatbelt-compile` | matrix `macos-14`, `macos-15` (and every newer hosted macOS image as it appears) | `go test -tags darwin ./internal/sandbox/darwin_seatbelt -run TestProfileCompiles` which renders the template with test roots and runs `sandbox-exec -f <profile> <params> /usr/bin/true` | exit 0 on every macOS version in the matrix; the rendered profile hash is logged per OS version | A06 (profile template tested per macOS version) |
| `jcs-vectors` | `ubuntu-24.04` | `go test ./internal/store/jcs -run TestRFC8785Vectors` against the RFC 8785 test vectors vendored in `internal/store/testdata/jcs/` (including number serialization and UTF-16 key ordering cases) | byte-identical output for every vector; also run by the `unit` job | A04 §10 |
| `migrations` | `ubuntu-24.04` | `go test ./internal/store -run TestMigrations` which applies every migration from 0001 on an empty store and on the fixture store of the previous release (`internal/store/testdata/fixtures/v<prev>.sqlite`), then `PRAGMA integrity_check`, `PRAGMA foreign_key_check` and `audit verify --strict` on every fixture session | all checks clean; `audit verify` identical before and after | A04 §5 item 8 |
| `contrast` | `ubuntu-24.04` | `python3 apps/desktop/scripts/check-contrast.py apps/desktop/src/styles/tokens.json` | every listed pair meets its minimum (4.5:1 text, 3:1 UI boundaries and focus) in light and dark | B06 §4 |
| `rpc-codegen` | `ubuntu-24.04` | `cd apps/desktop && npm ci && npm run gen:rpc && git diff --exit-code src/rpc/generated src-tauri/src/bridge/allowlist_gen.rs` | no diff: generated TS types, method/event maps and the Rust allowlist match the A05 schemas in `schemas/runtime-api/` | B08 §4, A05 |
| `no-ws-bridge` | `ubuntu-24.04` (after `desktop` build) | `! grep -r --binary-files=text 'bridge.ws' apps/desktop/dist` and `! grep -rn 'tokio-tungstenite\|tungstenite' apps/desktop/src-tauri/Cargo.lock` | the production bundle contains no WebSocket bridge module marker and the Rust shell links no WebSocket library; the daemon exposes no TCP listener (checked by `go test ./internal/api -run TestNoTCPListener`) | B08 §6 (WebSocket only in test builds) |

`escape-check`, `secrets-canary` and `seatbelt-compile` are required status checks on `main`; `redactor-bench` is required but retried once on failure to absorb runner noise.

### 5.1 Import rules (golangci-lint v2 `depguard` + archtest)

The `.golangci.yml` is the golangci-lint **v2** configuration given verbatim in A02 §6.2 (`version: "2"`, rules under `linters.settings.depguard.rules`, `list-mode: strict` for leaves and adapters). A02 is authoritative; this file does not restate the rule bodies, only the wiring:

```yaml
version: "2"
linters:
  default: standard
  enable: [depguard, gosec, errorlint, bodyclose, misspell]
  settings:
    depguard:
      rules:
        # leaves-stdlib-only (R6), executor-isolation (R3), provider-anthropic,
        # provider-openaicompat, harness-copilot, harness-codex-claudecode (R1),
        # cli-api-only (R5), adapters-wired-only-in-root (R2), exec-not-in-daemon (R4),
        # executor-client-only-agentloop (R7): copy verbatim from A02 §6.2.
formatters:
  enable: [gofmt, goimports]
```

The CI `lint` job pins `golangci/golangci-lint-action@v8` with `version: v2.x` (v1 does not accept `version: "2"` files). `depguard` gives fast feedback in editors; the authoritative check is the `internal/archtest` test (A02), which covers the full matrix including R8 (no Go under `apps/`) and runs in the `unit` job.

### 5.2 Ubuntu 24.04 unprivileged userns

Ubuntu 24.04 ships an AppArmor policy that restricts unprivileged user namespaces (`kernel.apparmor_restrict_unprivileged_userns=1`), which bwrap needs (WRD-16 §10.3, A06). The CI step (above) checks the sysctl and sets it to `0` on the runner so the escape-check can run. In the product, `warden doctor` detects this and offers three documented fixes (WRD-13 §8): set the sysctl, install an AppArmor profile for `wardend`/`warden-exec` that permits userns, or fall back to L2. The escape-check leg reports exit code 2 (unusable env, A16 §3.4) rather than a false pass if the sysctl cannot be relaxed on a locked-down runner.

## 6. Release packaging (`.github/workflows/release.yml`)

Triggered on a `v*` tag. Builds the matrix, signs, generates checksums, an SBOM and signatures, and attaches everything to the GitHub release.

- **Binaries**: cross-compiled for `darwin/{arm64,amd64}` and `linux/{amd64,arm64}` with `CGO_ENABLED=0 -trimpath` and stamped `-ldflags` (reproducible; S-11). CLI tarballs per platform.
- **Checksums**: `sha256sum` over every artifact → `SHA256SUMS`.
- **SBOM**: `syft dir:. -o spdx-json > SBOM.spdx.json` (S-11); attached to the release and embedded reference in `system.version`.
- **Signatures**: **cosign** (keyless via GitHub OIDC) signs the `SHA256SUMS` file and each release binary; `minisign` is provided as a simpler fallback for users who do not run cosign. Both are documented in the release notes. (Chosen: cosign primary because it needs no long-lived key and integrates with GitHub OIDC; minisign fallback for offline verification.)
- **Desktop**: signed/notarized `.dmg` + `.app` (macOS), `.AppImage` + `.deb` (Linux), produced with the release signing secrets available.
- **Reproducibility**: `-trimpath`, pinned `go.sum`, pinned action SHAs, `SOURCE_DATE_EPOCH` from the tag; a `verify-reproducible` job rebuilds and diffs the Go binaries to confirm byte-identical output.

Signing secrets (Apple cert, notarization key) live in GitHub Actions encrypted secrets and are referenced only in the release workflow; PR/CI builds never see them.

## 7. Versioning

Three independent version numbers:

| Version | Scheme | Where | Bumps when |
|---|---|---|---|
| Product / runtime | semver `0.x` (PoC starts `0.1.0`) | `buildinfo.Version`, `system.version`, `tauri.conf.json`, agent manifests `runtime.min_version` | any release |
| Protocol | `warden.poc/1` (integer, separate from product) | `system.hello.protocol` (core §6), `-32011 protocol_mismatch` | any breaking change to the JSON-RPC contract or event envelope `v` |
| Schema (DB) | integer migration number | `store` migrations table; SQLite `PRAGMA user_version` | any SQLite schema change (A04) |

Semver `0.x` signals pre-1.0 instability (WRD-16 is a PoC). The protocol version is separate so a UI and daemon of different product versions can still negotiate (`system.hello` returns `protocol` and `features[]`; a mismatch is `-32011`). Schema/migration versions run on daemon start with a DB backup (WRD-13 §6); downgrade across a migration is unsupported. The event envelope carries its own `v: 1` (core §5); a bump there is a protocol-version bump.

## Traceability

| Design element | WRD source (doc §) | Requirement / invariant satisfied |
|---|---|---|
| Single module `warden.dev/warden` | WRD-02 §9; WRD-16 §5.2; core §4 | repository structure; import rules |
| Directory tree | WRD-16 §5.2; WRD-02 §9; core §4 | PoC package set incl. execproto/audit/archtest/tools |
| CGO_ENABLED=0 + modernc sqlite | WRD-16 §5.1 | pure-Go cross-compile |
| Version stamping `-ldflags`, `-trimpath` | WRD-10 S-11 | reproducible, versioned builds |
| `shared` build tag | WRD-16 §6.1; CF-21; core §13.15 | personal-mode harness locked in shared builds (INV-7 adjacent) |
| Tauri sidecar externalBin + triples | WRD-16 §5.1; WRD-13 §2 | desktop bundles daemon; no Go in webview |
| Capabilities restricting shell to sidecar | WRD-02 §1 (clients no privileged path); BI-6 | UI reaches runtime only via API |
| macOS signing/notarization; Linux AppImage/deb | WRD-13 §2; WRD-10 S-11 | signed releases; L1 prereqs declared |
| CI: lint (golangci v2 depguard per A02 §6.2), unit + archtest, policy golden, escape matrix, desktop, e2e | WRD-16 §16; WRD-12 §6; WRD-10 §11; A02 §6 | import rules; escape check both OSes; golden 100% |
| CI jobs secrets-canary, redactor-bench | A15 §8; WRD-10 S-3, S-9 | BI-3 |
| CI jobs seatbelt-compile, jcs-vectors, migrations | A06; A04 §5, §10; WRD-09 §4 | H3 on each macOS version; S-5 chain reproducibility; WRD-13 §6 migrations |
| CI jobs contrast, rpc-codegen, no-ws-bridge | B06 §4; B08 §4, §6; A05 | WCAG 2.2 AA; API/UI contract drift; BI-6 (no extra transport) |
| Ubuntu 24.04 userns handling | WRD-16 §10.3; WRD-13 §8 | bwrap prerequisite |
| Release packaging: checksums, SBOM, cosign/minisign | WRD-10 S-11 | signed, reproducible, SBOM |
| Versioning: semver / protocol / schema | WRD-16; WRD-13 §6; core §6 | protocol negotiation; DB migrations |

## Deviations and assumptions

- DEV:module-path: chose `warden.dev/warden` over a `github.com/...` path (§1); either is valid per the brief. Vanity path resolution deferred (local builds in the PoC).
- DEV:cosign-primary: cosign (keyless, GitHub OIDC) is the primary signature with minisign as fallback (§6); WRD-16/WRD-10 name "cosign or minisign".
- ASM:tauri-bridge: the bridge is the Rust command pair over the Unix socket chosen by B08 (option a); no WebSocket transport ships, enforced by the `no-ws-bridge` job (§5.0).
- DEV:tree-labels: corrected in the integration pass: `internal/exec` is the in-sandbox executor linked only into `cmd/warden-exec`; the daemon-side executor client is `internal/tools` (A02 is authoritative).
- DEV:sidecar-list: A17 bundles both `wardend` and `warden-exec` as externalBin (the daemon needs `warden-exec` next to itself to bind it into sandboxes, A06 mount table); B08's `tauri.conf.json` excerpt lists only `wardend` and should add `binaries/warden-exec`.
- ASM:arm64-runners: Ubuntu arm64 legs assume GitHub-hosted arm runners (`ubuntu-*-arm`) or self-hosted equivalents (§5); if unavailable, arm64 escape-check runs on a self-hosted node.
- ASM:copilot-sdk-dep: `github.com/github/copilot-sdk/go` is pinned pending the week-1 spike (A18); if the SDK path differs, `go.mod` is corrected then.
- NEW `internal/buildinfo` package for version stamping (referenced by `-ldflags`).
- OQ (A17); whether to serve the `warden.dev/warden` vanity import path during the PoC; recommended answer: no, build from checkout. See OPEN-QUESTIONS.
