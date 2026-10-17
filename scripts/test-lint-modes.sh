#!/usr/bin/env bash
# Tests for lint-modes.sh, run by `make check` on all three CI runners.
# Shell scripts committed from Windows lose their exec bit (mode 100644 in
# the index), and CI then fails with "Permission denied": tree-hash.sh in
# M1, ci-require-tests.sh and its test in M2-23 (fixed in 56c540c). The lint
# reads the index, not the file system, because a Windows checkout has no
# exec bits to look at. Fed fake `git ls-files -s` output.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
fails=0
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

# expect CODE NAME FILE [MESSAGE]: run the lint on FILE (fake index), compare
# the exit code and, if given, require MESSAGE in its output.
expect() {
	local want=$1 name=$2 file=$3 msg=${4:-} got output
	output=$(bash "$here/lint-modes.sh" "$file" 2>&1)
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

T=$(printf '\t')
cat >"$dir/ok.txt" <<EOF
100755 d7c198754757249babcb9e3f46f1b0b3a7db6590 0${T}.claude/hooks/guard-bash.sh
100644 4a5c1e0d1b2c3d4e5f60718293a4b5c6d7e8f901 0${T}README.md
100755 1ac563bfe285b73ba3563c413f2a45df1763e68c 0${T}scripts/lint-exec.sh
100644 2b5c1e0d1b2c3d4e5f60718293a4b5c6d7e8f902 0${T}scripts/notes.sh.md
100755 6f1d48b474aea0f65178e0cded320cdf782be28d 0${T}docs/tools/build.sh
EOF
expect 0 all-executable "$dir/ok.txt"

# The state before 56c540c: both new scripts committed as 100644.
{
	cat "$dir/ok.txt"
	echo "100644 78f735bb5a1c64a61fe3b092c79437bea06e40eb 0${T}scripts/ci-require-tests.sh"
	echo "100644 695aa2eec6977523b98d4395a835e2115e14e5a8 0${T}scripts/test-ci-require-tests.sh"
} >"$dir/bad.txt"
expect 1 two-without-exec-bit "$dir/bad.txt" 'scripts/ci-require-tests.sh'
expect 1 two-without-exec-bit-both-named "$dir/bad.txt" 'scripts/test-ci-require-tests.sh'

# Any directory, not only scripts/; and a path with a space.
{
	cat "$dir/ok.txt"
	echo "100644 6f1d48b474aea0f65178e0cded320cdf782be28d 0${T}internal/x/my build.sh"
} >"$dir/space.txt"
expect 1 other-directory-and-space "$dir/space.txt" 'internal/x/my build.sh'

# docs/ mirrors agent-platform-docs and is never edited here: its scripts
# keep the upstream mode (100644) and are not checked.
{
	grep -v "docs/tools/build.sh" "$dir/ok.txt"
	echo "100644 6f1d48b474aea0f65178e0cded320cdf782be28d 0${T}docs/tools/build.sh"
} >"$dir/mirror.txt"
expect 0 docs-mirror-not-checked "$dir/mirror.txt"

# A file named like a script only in its middle is not a script.
grep -v '\.sh$' "$dir/ok.txt" >"$dir/none.txt"
expect 0 no-shell-scripts "$dir/none.txt"
: >"$dir/empty.txt"
expect 0 empty-index "$dir/empty.txt"

expect 2 missing-file "$dir/nope.txt"

# Without an argument it reads the real index: this repository must pass.
output=$(bash "$here/lint-modes.sh" 2>&1)
code=$?
[ "$code" = 0 ] || { echo "FAIL: this repository's index: exit $code"; printf '%s\n' "$output" | sed 's/^/    /'; fails=$((fails + 1)); }

if [ "$fails" -gt 0 ]; then
	echo "lint-modes: $fails failing case(s)"
	exit 1
fi
echo "lint-modes: ok"
