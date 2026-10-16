#!/usr/bin/env bash
# ci-require-tests.sh fails when a test that must run did not (CLAUDE.md §3,
# §12). The Linux integration tests skip cleanly when bwrap, openat2 or a
# separate mount is missing, which is right on a developer's machine and
# wrong in the CI job that exists to run them: there a skip is a red run.
#
#   scripts/ci-require-tests.sh <go-test.json> [--] TestName...
#
# <go-test.json> is the output of `go test -json` (test2json events, one
# JSON object per line). Each named top-level test must have a "pass" event,
# and neither it nor any of its subtests may have a "skip" or "fail" event,
# in any package. Names match exactly, never by prefix. Only the Action and
# Test fields of non-output events are read, so text a test prints (output
# events carry it JSON-escaped in "Output") cannot forge a pass.
#
# Exit 0: every named test ran and passed. 1: at least one did not; each is
# named. 2: usage error. Plain awk, so it runs in Git Bash, Ubuntu and
# macOS's bash 3.2 with BSD awk.
set -euo pipefail

if [ $# -lt 1 ]; then
	echo "usage: $0 <go-test.json> [--] TestName..." >&2
	exit 2
fi
file=$1
shift
[ "${1:-}" = "--" ] && shift
if [ $# -eq 0 ]; then
	echo "ci-require-tests: no test names given" >&2
	exit 2
fi
if [ ! -f "$file" ]; then
	echo "ci-require-tests: $file: no such file" >&2
	exit 2
fi
for name in "$@"; do
	case $name in
	Test*) ;;
	*)
		echo "ci-require-tests: $name: not a top-level test name" >&2
		exit 2
		;;
	esac
	case $name in
	*/* | *" "* | *'"'*)
		echo "ci-require-tests: $name: not a top-level test name" >&2
		exit 2
		;;
	esac
done

awk -v names="$*" '
BEGIN {
	n = split(names, req, " ")
	for (i = 1; i <= n; i++) want[req[i]] = 1
}
{
	# The first "Action" key belongs to the event: test2json writes Action
	# before Output, and inside Output every quote is escaped.
	if (!match($0, /"Action":"[a-z-]*"/)) next
	action = substr($0, RSTART + 10, RLENGTH - 11)
	if (action != "pass" && action != "skip" && action != "fail") next
	if (!match($0, /"Test":"[^"]*"/)) next
	test = substr($0, RSTART + 8, RLENGTH - 9)
	top = test
	sub(/\/.*/, "", top)
	if (!(top in want)) next
	if (action == "pass") {
		if (test == top) passed[top] = 1
	} else {
		bad[top] = bad[top] "    " toupper(action) " " test "\n"
	}
}
END {
	failed = 0
	for (i = 1; i <= n; i++) {
		t = req[i]
		if (t in bad) {
			printf "NOT OK  %s\n%s", t, bad[t]
			failed++
		} else if (!(t in passed)) {
			printf "NOT OK  %s\n    did not run (no pass event)\n", t
			failed++
		} else {
			printf "ok      %s\n", t
		}
	}
	if (failed > 0) {
		printf "ci-require-tests: %d of %d required test(s) did not run and pass\n", failed, n
		exit 1
	}
	printf "ci-require-tests: all %d required tests ran and passed\n", n
}
' "$file"
