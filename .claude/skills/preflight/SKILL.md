---
name: preflight
description: Check, before any code is written, that the machine and accounts have what a Warden milestone needs (toolchains, bwrap/userns, Docker, Ollama, Copilot CLI, provider keys, push access) and report what the owner must do. Use at the start of a milestone or session, or when the owner asks "are we ready for M<n>".
---

# /preflight [milestone]

Milestone defaults to the one at the top of `docs/PROGRESS.md`. M1 lost hours to prerequisites discovered mid-way (Ollama, Copilot CLI, Docker, push access); this check takes a minute.

## Needs per milestone (CLAUDE.md §3, §8, WRD-16 §16)

| Milestone | Needs |
|---|---|
| all | Go (version in `go.mod` or newer) on Windows and in WSL2; git; `gh auth status` with write access to the remote (`gh repo view --json viewerPermission`); `make check` green on the base branch; CI green on the last run of the branch |
| M2 | bwrap and unprivileged userns in WSL2 (`warden doctor`); Docker Desktop running with WSL2 integration (`docker version` on both sides); disk space for images |
| M3 | a model: Ollama with a coder model (`ollama list`), or a configured provider (`warden provider list`) |
| M4 | the fixtures build (`make fixtures`); a second model for routing/fallback |
| M5 | Copilot CLI installed and logged in (`copilot --version`, login state); Codex/Claude Code CLIs only if those optional harnesses are in scope |
| M6 | Node + npm, Rust (MSVC on Windows) and the Tauri prerequisites; Playwright browsers |
| M7 | a Mac |

## How

- Run each check read-only, on the OS it applies to. WSL2 checks go through a script file: `MSYS_NO_PATHCONV=1 wsl bash <script>`.
- Any `warden`/`wardend` run uses a temp `WARDEN_HOME`.
- Install nothing and change no system setting; that is the owner's call.

## Report

A table: need, status (ok / missing / not checked), evidence (version or error line), and for each missing item the exact action, whether it needs admin rights, and which planned tasks it blocks. End with a one-line verdict: ready, ready with gaps (name them), or blocked.
