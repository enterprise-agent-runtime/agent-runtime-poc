#!/usr/bin/env bash
# PreToolUse guard for Edit, Write and NotebookEdit. The specification (WRD),
# the design (A/B series) and the owner's prompts are read-only for every
# session and every agent (CLAUDE.md §2 rule 4, §10 "Never").
# Tests: .claude/hooks/test-hooks.sh.
set -uo pipefail
. "$(dirname "$0")/lib.sh"

input=$(cat)
path=$(json_field file_path "$input" || json_field notebook_path "$input") || exit 0
path=${path//\\//}
shopt -s nocasematch
case $path in
*/docs/docs/* | */docs/design/* | */docs/WRD-* | */docs/PROMPT-*)
	block "$path is a specification/design document and is never edited. Record the disagreement in docs/CONFLICTS.md."
	;;
esac
exit 0
