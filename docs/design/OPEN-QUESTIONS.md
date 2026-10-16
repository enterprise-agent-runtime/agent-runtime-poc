# Open questions

Consolidated from the "Deviations and assumptions" sections of A01 to B09 and from the integration pass. Each question has a recommended answer; the design set already assumes the recommendation, so accepting it changes nothing, and rejecting it names the files to revise. Numbers OQ-02, OQ-05, OQ-06 and OQ-13 are cited from other files; the rest are new here.

Owner codes: **F** founder / product, **A** architecture, **S** security, **D** design.

## Scope and product

| Id | Question | Recommended answer | Affects | Owner |
|---|---|---|---|---|
| OQ-01 | Are Copilot, Claude Code or Codex part of the core PoC, or are they optional compatibility adapters? | They are optional compatibility adapters only. The core PoC proves a secure runtime for agent execution, model routing, policy enforcement and auditability. Vendor harnesses are not the product itself and are not required for the PoC. If used, they run behind the same policy and audit model, never as a bypass around it. | A06, A12, A18 | A |
| OQ-02 | What does `restricted` mean, and should the PoC accept it? | `restricted` means data subject to stricter controls than `confidential` (e.g. regulated or special-handling categories). The PoC does not accept it; it is rejected with `-32010 unsupported_in_poc` until the MVP. This keeps the PoC focused on `public`, `internal` and `confidential` only. | A05, A09, B03 | F |
| OQ-03 | How long do `workspace`-scope grants last? | 30 days is the default PoC value. It is long enough for practical use, short enough to avoid stale privileges, and is revocable at any time. | A04, A08, B04 | S |
| OQ-04 | Should all agent-initiated changes be audited? | Yes. Every mutating action by an agent must be recorded in the immutable event/audit trail, with hash chaining and provenance. Operational logs may be added for diagnostics, but the authoritative audit source is the event stream, not human-readable logs alone. | A04, A05, A08, A09 | S |
| OQ-05 | What is T6, and how is it started? | T6 is a read-only repo-summary / repo-map task, not a normal coding task. It is started as `session.request {kind: "readonly"}` (`warden run --readonly`, or the UI read-only toggle) and produces a `repo-map` artifact with no writes and no gate review. | A05, A10, A13, B03 | F |
| OQ-06 | Why do we need a test-framework parser in the PoC? | We do not need it as a mandatory core capability. Test-framework support is optional and relevant only if we include a language-specific fixture or verification path. When a language-specific runner is included, the verifier should support a native JSON report when available and otherwise fall back to a machine-readable XML or structured output format. | A13 | A |
| OQ-07 | Why do we need Copilot, Claude Code or Codex in the PoC at all? | We do not need them as required PoC functionality. They are optional compatibility adapters that demonstrate the runtime can integrate with common vendor harnesses under policy control. The main proof is a secure, policy-governed runtime for agents and models. The product is not a direct competitor to those vendor environments; it is the execution and governance layer behind them. | A06, A12, A18 | F |
| OQ-08 | What is the exact stop line for the PoC, and what starts the MVP? | The PoC stops when the core thesis is proven in a minimal CLI-first runtime: secure execution boundary, policy-enforced tool calls, model neutrality across at least local and hosted access modes, hash-chained auditability, and verification-before-delivery. The MVP starts only after that proof, with desktop UI hardening, stronger onboarding, richer provider configuration, and broader operational maturity. We do not add broad enterprise features or extra harness integrations before the PoC proof is complete. | A18 | F |
| OQ-09 | What does the budget question actually mean in the PoC? | The budget question is about a session-level spend guardrail, not a full billing or accounting architecture. In the PoC, the budget is a safety mechanism to prevent runaway model usage and to force explicit approval for spend increases; it does not define the final enterprise billing model. | A05, A08, B03 | F |

## Policy and security

| Id | Question | Recommended answer | Affects | Owner |
|---|---|---|---|---|
| OQ-10 | WRD-16 rule `user.egress-other` compares a bare host with `host:port` entries, so it matches every host. | Fix the generated `user.yaml` now: `!((action.resource.host + ":" + string(action.resource.port)) in context.task_egress_allow)`. Behaviour is unchanged today because the proxy only asks about hosts outside the allowlist, but the rule should be correct for `policy.explain`. | A08 | S |
| OQ-11 | WRD-16 ships a subset of the WRD-10 §6 deny-list. | Adopt the full WRD-10 list; it is stricter and has no demo impact. | A06, A08, A15 | S |
| OQ-12 | `startsWith(context.worktree)` is a string-prefix test. | Keep it with the trailing-slash convention of A08 §2.1 for the PoC; move to a `globMatch` function in the MVP. | A08 | S |
| OQ-13 | Figures `poc_scope`, `poc_components` and `poc_access_modes` disagree with WRD-16 text (CF-33; unlabeled node `b2`). | Regenerate the three figures from the corrected `.dot` sources before handing WRD-16 to anyone else. | WRD-16 | A |
| OQ-14 | `audit verify --strict` on a single-session export cannot prove a `workspace` grant created in another session. | Report `external_grant_unverified` as a warning, not a violation, in file mode; verify both chains in store mode. | A03, A04, A08 | S |
| OQ-15 | Non-interactive runs always stop at G1 (exit 2) because gates need a human. | Accept for the PoC; `warden eval smoke` resolves gates as the user through the API. CI pre-approvals of gates are MVP. | A05, A08, A13 | A |
| OQ-16 | Should co-located harnesses (Codex, Claude Code) be forced to L2? | On Linux, require L2 when a co-located harness is enabled. On macOS, where L2 cannot carry the proxy socket (OQ-18), allow L1 in personal mode only, with a warning on the provider card. | A06, A12, A16 | S |
| OQ-17 | Codex or Claude Code with API-key billing would put the key inside the sandbox. | Reject in the PoC (`unsupported_in_poc`). Only the vendor's own login file (HX-1) is mounted. | A12, A15 | S |
| OQ-18 | What is the role of Docker in the PoC? | Docker is not core to the product thesis. It is an optional L2 execution backend for Linux only, useful for stronger isolation when running untrusted repositories or for future portability. It is not required for the main PoC proof and is not a baseline requirement on macOS because host Unix sockets do not cross Docker Desktop cleanly. | A06, A17 | A |
| OQ-19 | Committed `.env.*` files (e.g. `.env.test`) are masked, which may break fixture tests. | Keep masking; list masked paths in the sandbox details panel so the user understands a failing test. | A06, B04 | S |
| OQ-20 | On macOS a process that double-forks and calls `setsid` can outlive teardown (still Seatbelt-confined). | Accept for the PoC and document; recommend L2 on Linux for untrusted repositories. | A06, A16 | S |
| OQ-21 | Codex with ChatGPT login may rotate refresh tokens and log the user out of their own CLI. | Verify in the week-5 spike; if confirmed, disable Codex ChatGPT-login mode in the PoC. | A12 | A |
| OQ-22 | Plain `http://` to a LAN model endpoint with a bearer token. | Reject authenticated modes over plain HTTP except loopback; use TLS with a private CA (`ca_file`). | A11, A15 | S |
| OQ-23 | Landlock (WRD-10 §5.2). | Defer to the MVP. | A06 | S |
| OQ-24 | Harness vendor egress allowlists. | Confirm in week 5 from observed `proxy.denied` events; keep vendor telemetry hosts denied. | A07, A12 | S |

## Sandbox tooling

| Id | Question | Recommended answer | Affects | Owner |
|---|---|---|---|---|
| OQ-25 | Set `NO_PROXY=127.0.0.1,localhost,::1` in the sandbox? Some HTTP client libraries otherwise send loopback test traffic to the proxy. | Yes, once the week-2 escape check confirms loopback cannot reach host services (A06 denies host ports already listening). | A06, A07 | A |
| OQ-26 | Set `GOTOOLCHAIN=local` in the sandbox? | Yes; toolchain downloads are egress. | A06 | A |
| OQ-27 | Per-task egress byte cap? | Not in the PoC; `bytes_up` is recorded on `proxy.connect`. | A07 | S |
| OQ-28 | Native Keychain access via cgo instead of the `security` CLI (go-keyring)? | MVP, together with code signing. A06 already blocks `security`, `launchctl`, `osascript` and SecurityServer lookups inside Seatbelt. | A06, A15 | S |
| OQ-29 | Should user-level global git hooks run on Warden pushes from the host? | No in the PoC (hooks disabled for all host git calls); opt-in setting in the MVP. | A14 | S |

## UX

| Id | Question | Recommended answer | Affects | Owner |
|---|---|---|---|---|
| OQ-30 | Does the push approval count toward H6's "at most three prompts beyond the gates"? | Report it separately and do not count it: push is a delivery decision after the run ends (ID-01). The expected T1 path has 1 counted prompt. | B09 | F |
| OQ-31 | Should delivery stay available after chain verification fails (ST-6)? | Yes, with a note in the Commit and Push dialogs; the chain is evidence, not a lock. | B03, B05, B07 | S |
| OQ-32 | Should the pending-approval badge also count open gates? | No; gates and "needs input" are separate badge segments. | B03, B04, B05 | D |
| OQ-33 | Quitting the desktop app: stop the daemon it started? | Yes after a confirmation when runs are active; a CLI-started daemon keeps running. | B08 | D |
| OQ-34 | Language of policy denial texts. | The UI shows its catalog text keyed by rule id (B07) and falls back to the daemon's `reason` string. | B07, B08 | D |
| OQ-35 | Hunk attribution for edits made by `proc.exec` (formatters, codegen) rather than `fs.write`/`fs.patch`. | Attribute at task level (`attribution: task`) and say so in the provenance chip. | A04, B04 | D |
| OQ-36 | Dirty working tree at session open. | Start from `HEAD`, never touch the user's working tree, report `uncommitted` in `session.open` and show a notice. | A05, A14, B03 | D |

## Traceability

| Design element | WRD source (doc §) | Requirement / invariant satisfied |
|---|---|---|
| Consolidated open questions with recommendations | Brief §6 ("collect open questions in a final OPEN-QUESTIONS.md with your recommended answer") | Brief §7 step 3 |
| OQ-01, OQ-16 to OQ-18, OQ-21 | WRD-05 §9, WRD-10 §5, WRD-16 §6.2 | BI-2, BI-3 |
| OQ-10 to OQ-15 | WRD-08 §4 to §9, WRD-16 §10.6, §11 | BI-1, BI-5, H2 |
| OQ-30 | WRD-16 §1 H6, §15 item 9 | H6 |

## Deviations and assumptions

- ASM: every recommendation above is already reflected in the design files; the "Affects" column lists the files to revise if a recommendation is rejected.
- ASM: unnumbered "OQ" bullets inside the deliverables are covered by the numbered entries here.
