#!/usr/bin/env bash
# tree-hash.sh prints the git tree id of the working tree as it is on disk:
# tracked files with their current content plus untracked files that are not
# ignored. The real index is left alone (a copy is used).
#
# `make check` records this id when it passes; the commit guard in
# .claude/hooks/guard-bash.sh refuses a commit whose working tree has a
# different id, i.e. one that `make check` has not seen (CLAUDE.md §11
# "never commit red", §15).
#
#   scripts/tree-hash.sh [repo-dir]
set -euo pipefail
cd "${1:-.}"
idx=$(mktemp)
trap 'rm -f "$idx"' EXIT
real=$(git rev-parse --git-path index)
if [ -f "$real" ]; then cp "$real" "$idx"; else rm -f "$idx"; fi
GIT_INDEX_FILE="$idx" git add -A . >/dev/null 2>&1
GIT_INDEX_FILE="$idx" git write-tree
