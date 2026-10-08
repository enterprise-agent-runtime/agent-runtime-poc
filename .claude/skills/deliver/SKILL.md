---
name: deliver
description: Take one planned Warden task (or a group of independent ones) through the full pipeline - implement test-first, independent code and security review, fixes, test audit, docs - ending in a green commit. Use when the owner says "do T3", "deliver", "next task", or after /plan.
---

# /deliver <task id(s)>

The main session orchestrates; agents do the work. Keep their reports short in the conversation and the details in the commits and `docs/PROGRESS.md`.

## 1. Set up

- Read the task block(s) from `docs/PROGRESS.md`.
- One branch per milestone or topic (never `main`). For tasks that can run in parallel, give each implementer its own worktree: `git worktree add ../<repo>-<task> -b <branch>-<task> <branch>`, and merge back in task order. Checkouts are shared; never let two agents commit in the same one.

## 2. Implement

Delegate to `implementer` with the task block, the branch, and the worktree path. It returns a commit, the fail-before/pass-after evidence, and decisions taken. If it stopped instead (question, three failed attempts, scope), bring that to the owner.

## 3. Review, in parallel, with fresh context

Launch in one message:

- `reviewer` on the commit (always);
- `security-reviewer` on the commit when it touches sandbox, exec, proxy, policy, secrets, store/audit, api transport, harnesses, model output handling, or CI secrets, which is most of Warden.

Give them the commit range and the task's acceptance criterion, **not** the implementer's reasoning: the point is an independent look.

## 4. Resolve findings

- Blockers, majors, critical/high: back to `implementer` with the findings (it amends with a new commit, not a rewrite). Re-review only the fix.
- PLAUSIBLE findings: verify them yourself, or have the reviewer confirm, before acting.
- A finding that is really a disagreement with a design document is a `docs/CONFLICTS.md` entry and possibly an owner question, not a code change.
- Minor findings: fix if cheap; otherwise list them in `docs/PROGRESS.md` "left".

## 5. Test audit

For tasks with non-trivial logic, or at least once per group: `test-engineer` on the package(s), to run mutation checks and close gaps. Its commits go through `reviewer` like any other.

## 6. Record

`docs-writer` ticks the task in `docs/PROGRESS.md` with the evidence and appends decisions. Then report to the owner in a few lines: what changed, what was found in review and how it was settled, and the concept worth knowing behind the trickiest part.

Do not push or open a PR here unless the owner asked; that is `/milestone-close` or an explicit request.
