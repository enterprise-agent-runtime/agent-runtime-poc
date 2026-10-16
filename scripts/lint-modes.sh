#!/usr/bin/env bash
# lint-modes.sh fails when a tracked shell script is not executable in the
# git index (CLAUDE.md §12). Scripts committed from Windows land as mode
# 100644 because a Windows checkout has no exec bits, and CI then fails with
# "Permission denied" (tree-hash.sh in M1; ci-require-tests.sh and its test
# in M2-23, fixed in 56c540c). It reads the index, never the file system,
# so it gives the same answer on Windows, Linux and macOS.
#
#   scripts/lint-modes.sh             check this repository's index
#   scripts/lint-modes.sh <file>      check <file>, saved `git ls-files -s` output
#
# Every tracked *.sh, in any directory, must be mode 100755. Fix one with
#   git update-index --chmod=+x <path>
# Exit 0: all are. 1: some are not, each named. 2: usage error.
set -euo pipefail

if [ $# -gt 1 ]; then
	echo "usage: $0 [git-ls-files-s-output]" >&2
	exit 2
fi
if [ $# -eq 1 ]; then
	if [ ! -f "$1" ]; then
		echo "lint-modes: $1: no such file" >&2
		exit 2
	fi
	index=$(cat "$1")
else
	cd "$(dirname "$0")/.."
	index=$(git -c core.quotePath=false ls-files -s)
fi

# "<mode> <object> <stage>\t<path>"; a quoted path (control characters
# even with quotePath off) ends in .sh" and is still checked.
printf '%s\n' "$index" | awk -F '\t' '
NF >= 2 && $2 ~ /\.sh"?$/ {
	split($1, meta, " ")
	if (meta[1] != "100755") {
		printf "not executable in the index (mode %s): %s\n", meta[1], $2
		bad++
	}
}
END {
	if (bad > 0) {
		printf "lint-modes: %d shell script(s) without the exec bit; fix with git update-index --chmod=+x <path>\n", bad
		exit 1
	}
}'
