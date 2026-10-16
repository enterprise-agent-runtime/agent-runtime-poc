#!/usr/bin/env bash
# SessionStart hook (CLAUDE.md §11, §15). Orients every session before it
# does anything. What it prints goes into Claude's context:
#   - the branch and its uncommitted changes;
#   - whether the working tree still matches the last green `make check`;
#   - the open items of the current milestone in docs/PROGRESS.md;
#   - the §11 reading list.
# Offline and fast by design (no network, no gh), because it runs at every
# start, resume and compaction. It never blocks a session: it always exits 0.
# Tests: .claude/hooks/test-hooks.sh, run by `make check`.
set -uo pipefail
. "$(dirname "$0")/lib.sh"

input=$(cat 2>/dev/null || true)
dir=$(json_field cwd "$input" 2>/dev/null || true)
[ -n "$dir" ] || dir=${CLAUDE_PROJECT_DIR:-$(pwd)}
root=$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null) || exit 0

branch=$(git -C "$root" branch --show-current 2>/dev/null)
[ -n "$branch" ] || branch="(detached HEAD)"
changes=$(git -C "$root" status --porcelain 2>/dev/null | grep -c . || true)

echo "Session start (.claude/hooks/session-start.sh):"
line="- Branch: $branch, $changes uncommitted change(s)."
case $branch in
main | master) line="$line This is $branch: create a branch before changing anything." ;;
esac
echo "$line"

if [ -f "$root/scripts/tree-hash.sh" ]; then
	tree=$(bash "$root/scripts/tree-hash.sh" "$root" 2>/dev/null || echo none)
	stampfile=$(git -C "$root" rev-parse --path-format=absolute --git-path warden-check-ok 2>/dev/null)
	stamp=$(cat "$stampfile" 2>/dev/null || true)
	if [ -n "$stamp" ] && [ "$tree" = "$stamp" ]; then
		echo "- The working tree matches the last green make check."
	else
		echo "- The working tree has not passed make check in its current state; run it before committing code."
	fi
fi

progress="$root/docs/PROGRESS.md"
if [ -f "$progress" ]; then
	# Top-level open, limited and blocked items above "## Sessions": the
	# current milestone's checklist (CLAUDE.md §11).
	open=$(awk '/^## Sessions/ { exit } /^- \[( |~|!)\]/ { print }' "$progress" | cut -c1-200 | head -n 25)
	if [ -n "$open" ]; then
		echo "- Open items in docs/PROGRESS.md (- [ ] open, [~] limitation, [!] blocked):"
		printf '%s\n' "$open" | sed 's/^/  /'
	else
		echo "- docs/PROGRESS.md has no open items above \"## Sessions\"."
	fi
fi

cat <<'EOF'
- Read in layers (CLAUDE.md §11):
  - Always: docs/PROGRESS.md, docs/DECISIONS-poc.md, docs/CONFLICTS.md and the memory index (CLAUDE.md is already loaded).
  - Per task: the WRD and design sections it cites, through the reading table in docs/INDEX.md. Never the whole set at once.
  - The specification is the docs/ copy. Its source, agent-platform-docs, is re-copied only when the owner says it changed.
EOF
exit 0
