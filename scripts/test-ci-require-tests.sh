#!/usr/bin/env bash
# Tests for ci-require-tests.sh, run by `make check` on all three CI runners.
# The CI linux-integration job relies on that script to turn a silently
# skipped kernel or bwrap test into a red run (CLAUDE.md §3, §12). A checker
# that stops noticing a skip protects nothing, and one that rejects a clean
# run blocks every pull request, so both directions are tested on fake
# `go test -json` output shaped like the real thing (Go 1.27, WSL2 run).
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
fails=0
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

# ev ACTION PKG TEST: one test2json event line.
ev() {
	printf '{"Time":"2026-10-16T22:11:56.954301074+03:00","Action":"%s","Package":"warden.dev/warden/internal/%s","Test":"%s"%s}\n' \
		"$1" "$2" "$3" "$([ "$1" = run ] || printf ',"Elapsed":0.01')"
}
# out PKG TEST TEXT: an output event (TEXT already JSON-escaped).
out() {
	printf '{"Time":"2026-10-16T22:11:56.954303297+03:00","Action":"output","Package":"warden.dev/warden/internal/%s","Test":"%s","Output":"%s\\n","OutputType":"frame"}\n' "$1" "$2" "$3"
}
pkg() { printf '{"Time":"2026-10-16T22:12:02.798289522+03:00","Action":"%s","Package":"warden.dev/warden/internal/%s","Elapsed":8.939}\n' "$1" "$2"; }

# A clean run: two top-level tests, one with subtests, plus a test whose
# name extends a required one.
clean() {
	ev run exec TestOpenCanonical_FallbackNoXdev
	out exec TestOpenCanonical_FallbackNoXdev '=== RUN   TestOpenCanonical_FallbackNoXdev'
	ev run exec TestOpenCanonical_FallbackNoXdev/shm/fallback
	ev pass exec TestOpenCanonical_FallbackNoXdev/shm/fallback
	ev run exec TestOpenCanonical_FallbackNoXdev/shm/openat2
	ev pass exec TestOpenCanonical_FallbackNoXdev/shm/openat2
	ev pass exec TestOpenCanonical_FallbackNoXdev
	ev run exec TestOpenCanonical_Fallback
	ev pass exec TestOpenCanonical_Fallback
	pkg pass exec
	ev run sandbox/linux_bwrap TestBwrap_RunsTheBuiltArgv
	out sandbox/linux_bwrap TestBwrap_RunsTheBuiltArgv '--- PASS: TestBwrap_RunsTheBuiltArgv (0.42s)'
	ev pass sandbox/linux_bwrap TestBwrap_RunsTheBuiltArgv
	pkg pass sandbox/linux_bwrap
}
required=(TestOpenCanonical_FallbackNoXdev TestBwrap_RunsTheBuiltArgv)

# expect CODE NAME FILE [MESSAGE] -- TEST...: run the checker on FILE with
# the required TESTs; compare the exit code and, if given, require MESSAGE in
# its output (so a red result names the test that caused it).
expect() {
	local want=$1 name=$2 file=$3 msg=$4 got output
	shift 5
	output=$(bash "$here/ci-require-tests.sh" "$file" "$@" 2>&1)
	got=$?
	if [ "$got" != "$want" ]; then
		echo "FAIL: $name: exit $got, want $want"
		printf '%s\n' "$output" | sed 's/^/    /'
		fails=$((fails + 1))
	elif [ -n "$msg" ] && ! printf '%s\n' "$output" | grep -qF -- "$msg"; then
		echo "FAIL: $name: output lacks [$msg]"
		printf '%s\n' "$output" | sed 's/^/    /'
		fails=$((fails + 1))
	fi
}

clean >"$dir/clean.json"
expect 0 clean-run "$dir/clean.json" '' -- "${required[@]}"

# A required test skipped at the top level (bwrap not on PATH).
{
	clean | grep -v 'TestBwrap_RunsTheBuiltArgv'
	ev run sandbox/linux_bwrap TestBwrap_RunsTheBuiltArgv
	out sandbox/linux_bwrap TestBwrap_RunsTheBuiltArgv '    bwrap_linux_test.go:180: bwrap is not installed'
	ev skip sandbox/linux_bwrap TestBwrap_RunsTheBuiltArgv
} >"$dir/skip.json"
expect 1 top-level-skip "$dir/skip.json" 'TestBwrap_RunsTheBuiltArgv' -- "${required[@]}"

# One subtest of a required test skipped (the parent still reports pass).
clean | sed 's#"pass","Package":"warden.dev/warden/internal/exec","Test":"TestOpenCanonical_FallbackNoXdev/shm/openat2"#"skip","Package":"warden.dev/warden/internal/exec","Test":"TestOpenCanonical_FallbackNoXdev/shm/openat2"#' >"$dir/subskip.json"
grep -q '"skip".*shm/openat2' "$dir/subskip.json" || { echo "FAIL: fixture subskip has no skip"; fails=$((fails + 1)); }
expect 1 subtest-skip "$dir/subskip.json" 'TestOpenCanonical_FallbackNoXdev/shm/openat2' -- "${required[@]}"

# A required test that never ran (renamed, deleted, or build-tagged away).
clean | grep -v 'TestBwrap_RunsTheBuiltArgv' >"$dir/missing.json"
expect 1 missing-test "$dir/missing.json" 'TestBwrap_RunsTheBuiltArgv' -- "${required[@]}"

# Only a test whose name extends the required one ran: no match by prefix.
clean | grep -v '"Test":"TestOpenCanonical_FallbackNoXdev' >"$dir/prefix.json"
grep -q '"Test":"TestOpenCanonical_Fallback"' "$dir/prefix.json" || { echo "FAIL: fixture prefix lost TestOpenCanonical_Fallback"; fails=$((fails + 1)); }
expect 1 prefix-is-not-a-match "$dir/prefix.json" 'TestOpenCanonical_FallbackNoXdev' -- "${required[@]}"
# ... and the other way round: a required name that is a prefix of one that ran.
clean | grep -v '"Test":"TestOpenCanonical_Fallback"' >"$dir/prefix2.json"
expect 1 required-prefix-of-another "$dir/prefix2.json" 'TestOpenCanonical_Fallback' -- TestOpenCanonical_Fallback

# Subtests passed but the parent never finished (a panic or the -timeout
# kill after them): no pass event for the test itself.
clean | grep -v '"Action":"pass","Package":"warden.dev/warden/internal/exec","Test":"TestOpenCanonical_FallbackNoXdev",' >"$dir/noparent.json"
grep -q 'FallbackNoXdev/shm/openat2","Elapsed' "$dir/noparent.json" || { echo "FAIL: fixture noparent lost the subtests"; fails=$((fails + 1)); }
expect 1 subtests-without-parent-pass "$dir/noparent.json" 'TestOpenCanonical_FallbackNoXdev' -- "${required[@]}"

# A required test that failed (go test exits non-zero too; the summary
# must still not call it fine).
clean | sed 's#"pass","Package":"warden.dev/warden/internal/sandbox/linux_bwrap","Test":"TestBwrap_RunsTheBuiltArgv"#"fail","Package":"warden.dev/warden/internal/sandbox/linux_bwrap","Test":"TestBwrap_RunsTheBuiltArgv"#' >"$dir/fail.json"
expect 1 failed-test "$dir/fail.json" 'TestBwrap_RunsTheBuiltArgv' -- "${required[@]}"

# A pass printed by a test as output text is not a pass event.
{
	clean | grep -v 'TestBwrap_RunsTheBuiltArgv'
	out sandbox/linux_bwrap TestOther '{\"Action\":\"pass\",\"Test\":\"TestBwrap_RunsTheBuiltArgv\"}'
	out sandbox/linux_bwrap TestOther '--- PASS: TestBwrap_RunsTheBuiltArgv (0.42s)'
} >"$dir/forged.json"
expect 1 output-text-is-not-a-pass "$dir/forged.json" 'TestBwrap_RunsTheBuiltArgv' -- "${required[@]}"

# The same name in two packages: a skip in one is not hidden by a pass in
# the other.
{
	clean
	ev run sandbox TestBwrap_RunsTheBuiltArgv
	ev skip sandbox TestBwrap_RunsTheBuiltArgv
} >"$dir/twopkg.json"
expect 1 skip-in-second-package "$dir/twopkg.json" 'TestBwrap_RunsTheBuiltArgv' -- "${required[@]}"

# Usage errors are exit 2, never a pass.
expect 2 no-tests "$dir/clean.json" '' --
expect 2 missing-file "$dir/nope.json" '' -- TestBwrap_RunsTheBuiltArgv
: >"$dir/empty.json"
expect 1 empty-output "$dir/empty.json" 'TestBwrap_RunsTheBuiltArgv' -- TestBwrap_RunsTheBuiltArgv

if [ "$fails" -gt 0 ]; then
	echo "ci-require-tests: $fails failing case(s)"
	exit 1
fi
echo "ci-require-tests: ok"
