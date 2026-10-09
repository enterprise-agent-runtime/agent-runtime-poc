---
name: milestone-close
description: Close a Warden milestone - run its acceptance commands, final test and security audits, update the working docs, push, open the PR to main and read CI. Use when the owner says the milestone is done, "close M<n>", or "open the PR".
---

# /milestone-close [milestone]

CLAUDE.md §13 is the definition of done. Nothing is ticked that was not run.

1. **Acceptance.** Run every acceptance command for the milestone (WRD-16 §15–§16, restated in `docs/PROMPT-Claude-Code.md` and the `docs/PROGRESS.md` checklist) on Linux (WSL2) and on Windows where the behaviour is visible there. Temp `WARDEN_HOME` for every binary run. Capture command, exit code and a one-line summary each.
2. **Audits, in parallel:** `test-engineer` (the §8.7 tests for this milestone exist and bite; coverage per §8.6) and `security-reviewer` on the whole milestone diff (`git diff main...HEAD`). Blockers go back through `/deliver`.
3. **Docs and glossary.** Add the milestone's new concepts (the "concept behind the choice" sentences from plans, reviews and reports) to `docs/GLOSSARY.md`, each with where it showed up, and write the milestone's report in `docs/reports/<date>-<milestone>.md`. `docs-writer` updates `docs/PROGRESS.md` (checklist, session entry with the exact commands and results, left, open questions), `DECISIONS-poc.md`, `CONFLICTS.md`, `INDEX.md`, `README.md`.
4. **`make check`** green, then push the branch (`git push -u origin <branch>`; never `main`).
5. **PR to `main`** with `gh pr create --base main`. Body: what was built (by package), what was verified and where (table), what is not done or blocked and why, the owner questions. End with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
6. **CI.** Bind the PR to the session and read the result when it arrives. A red run goes to `platform-engineer` for diagnosis; never merge red, never retry to green.
7. **Report** to the owner: PR link, CI state, the verdict per acceptance item, and what needs them.

The owner merges. Do not enable auto-merge unless they ask.
