---
name: plan
description: Break a Warden problem (a milestone, feature or bug) into ordered, testable tasks with the planner agent and record them in docs/PROGRESS.md. Use when the owner says "plan", starts a milestone, or describes a problem bigger than one commit.
---

# /plan <problem>

1. **Preflight first** if this starts a milestone: run the `preflight` skill for it. Missing prerequisites change the plan.
2. **Delegate to the `planner` agent** with: the problem in the owner's words, the milestone, the relevant WRD-16 section, and anything the owner has already decided in this conversation. Ask for the plan format in its instructions.
3. **Check the plan yourself** before showing it:
   - every task has a test that would fail today;
   - no task leaves `make check` red;
   - nothing is outside WRD-16 §2.1;
   - "Ask before" items (CLAUDE.md §10) are listed as questions, not tasks.
4. **Show the owner** the task list (ids, titles, one-line acceptance, parallel groups), the questions, and the prerequisites. Do not start implementing until the owner agrees or says to proceed.
5. **Record**: once agreed, add the tasks under the current milestone in `docs/PROGRESS.md` as `[ ]` items, and candidate conflicts/decisions to `docs/CONFLICTS.md` / `docs/DECISIONS-poc.md`. Commit as `docs: plan <milestone or topic>`.

Then deliver tasks with the `deliver` skill, one at a time or in parallel groups.
