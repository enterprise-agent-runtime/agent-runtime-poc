#!/usr/bin/env bash
# Tests for the guard hooks (CLAUDE.md §15), run by `make check` on all three
# CI runners. A guard that silently stops matching protects nothing, and one
# that matches too much blocks honest work; both directions are tested.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
fails=0

esc() { local s=${1//\\/\\\\}; printf '%s' "${s//\"/\\\"}"; }

# expect CODE TOOL TEXT [CWD]: run the guard for TOOL with TEXT (a command,
# or a file path for edits) and compare the exit code.
expect() {
	local want=$1 tool=$2 text=$3 cwd=${4:-$repo} json guard got
	case $tool in
	Bash | PowerShell)
		guard=guard-bash.sh
		json="{\"tool_name\":\"$tool\",\"cwd\":\"$(esc "$cwd")\",\"tool_input\":{\"command\":\"$(esc "$text")\",\"description\":\"push to main\"}}"
		;;
	*)
		guard=guard-edit.sh
		json="{\"tool_name\":\"$tool\",\"cwd\":\"$(esc "$cwd")\",\"tool_input\":{\"file_path\":\"$(esc "$text")\",\"content\":\"x\"}}"
		;;
	esac
	printf '%s' "$json" | env -u WARDEN_HOME bash "$here/$guard" >/dev/null 2>&1
	got=$?
	if [ "$got" != "$want" ]; then
		echo "FAIL: $tool [$text] on ${branch:-?}: exit $got, want $want"
		fails=$((fails + 1))
	fi
}

# A scratch repository with a feature branch and a copy of tree-hash.sh.
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT
git -C "$repo" init -q
git -C "$repo" config user.email t@example.invalid
git -C "$repo" config user.name test
git -C "$repo" config commit.gpgsign false
git -C "$repo" checkout -q -b main
mkdir -p "$repo/scripts" && cp "$root/scripts/tree-hash.sh" "$repo/scripts/"
echo a >"$repo/a.go" && echo r >"$repo/README.md"
git -C "$repo" add -A && git -C "$repo" commit -qm init
stamp() { bash "$repo/scripts/tree-hash.sh" "$repo" >"$(git -C "$repo" rev-parse --path-format=absolute --git-path warden-check-ok)"; }

# --- on main
branch=main
expect 2 Bash 'git push'
expect 2 Bash 'git push origin'
expect 2 Bash 'git push -u origin main'
expect 0 Bash 'git push -u origin feature'
expect 2 Bash 'git commit -m x'
expect 0 Bash 'git status'

git -C "$repo" checkout -q -b feature
branch=feature
# Rule 1: pushes
expect 0 Bash 'git push -u origin feature'
expect 0 Bash 'git push'
expect 2 Bash 'git push origin main'
expect 2 Bash 'git push origin HEAD:master'
expect 2 Bash 'git push origin +main'
expect 2 Bash "git -C \"$repo\" push origin main"
expect 2 Bash 'git fetch && git push origin main'
expect 2 PowerShell 'git push origin main'
expect 2 Bash 'git push --force origin feature'
expect 2 Bash 'git push -f'
expect 0 Bash 'git push --force-with-lease origin feature'
expect 0 Bash 'git push origin maintenance'
# Rule 2
expect 2 Bash 'git commit --no-verify -m x'
expect 2 Bash 'git push --no-verify'
# Rule 3: commits need a green make check on exactly this tree
stamp
expect 0 Bash 'git commit -m x'
expect 0 Bash 'git commit -m "do not push to main; force -f"'
echo b >>"$repo/a.go"
expect 2 Bash 'git commit -am x'
expect 2 Bash "cd \"$repo\" && git commit -am x" "$(mktemp -d)"
expect 0 Bash 'git commit --dry-run -am x'
stamp
expect 0 Bash 'git add -A && git commit -m x'
echo new >"$repo/new.go"
expect 2 Bash 'git add -A && git commit -m x'
rm "$repo/new.go"
git -C "$repo" add -A && git -C "$repo" commit -qm b
echo more >>"$repo/README.md"
expect 0 Bash 'git commit -am "docs only"'
expect 0 Bash 'git log --oneline -3'
# Rule 4: binaries need WARDEN_HOME
expect 2 Bash 'bin/warden doctor'
expect 2 Bash './bin/wardend --foreground'
expect 2 Bash '/d/x/bin/warden.exe daemon status'
expect 2 Bash 'make build && bin/warden provider list'
expect 2 Bash 'go run ./cmd/warden doctor'
expect 2 Bash 'make run-daemon'
expect 0 Bash 'WARDEN_HOME=/tmp/w bin/warden doctor'
expect 0 Bash 'export WARDEN_HOME=$(mktemp -d); bin/warden doctor'
expect 0 Bash 'ls cmd/warden internal'
expect 0 Bash 'go build -o bin/ ./cmd/...'
expect 0 Bash 'go test ./cmd/warden/...'
# Edits: specification and design are read-only
expect 2 Edit "$root/docs/design/A05-runtime-api.md"
expect 2 Write "$root/docs/docs/WRD-00-Master-Specification-v0.5.md"
expect 2 Edit "$root/docs/WRD-16-PoC-Concept-and-Build-Plan.md"
expect 2 Edit "$root/docs/PROMPT-Claude-Code.md"
expect 2 Edit 'D:\Projects\x\docs\Design\A08-policy-engine.md'
expect 0 Edit "$root/docs/PROGRESS.md"
expect 0 Write "$root/internal/design/x.go"

# SessionStart: orients the session and never blocks it (CLAUDE.md §11, §15).
# Why: a session that starts without reading PROGRESS.md repeats finished work
# and misses blocked prerequisites; the reading list keeps it from loading the
# whole document set at once.
session() { # session CWD: run the hook as Claude Code would, capture stdout
	printf '{"hook_event_name":"SessionStart","source":"startup","cwd":"%s"}' "$(esc "$1")" |
		bash "$here/session-start.sh" 2>/dev/null
}
want_out() { # want_out yes|no PATTERN OUTPUT LABEL
	if printf '%s' "$3" | grep -q -e "$2"; then has=yes; else has=no; fi
	if [ "$has" != "$1" ]; then
		echo "FAIL: session-start ($4): pattern [$2] present=$has, want $1"
		fails=$((fails + 1))
	fi
}
mkdir -p "$repo/docs"
cat >"$repo/docs/PROGRESS.md" <<'EOF'
# Progress
## Current milestone: M9 test
- [x] finished thing
- [ ] open thing
  - [ ] nested detail line
- [!] blocked thing
- [~] limited thing
## Sessions
- [ ] old session item
EOF
git -C "$repo" add -A && git -C "$repo" commit -qm progress
out=$(session "$repo")
code=$?
[ "$code" = 0 ] || { echo "FAIL: session-start exit $code, want 0"; fails=$((fails + 1)); }
want_out yes 'Branch: feature' "$out" branch
want_out yes 'open thing' "$out" open
want_out yes 'blocked thing' "$out" blocked
want_out yes 'limited thing' "$out" limited
want_out no 'finished thing' "$out" done-excluded
want_out no 'nested detail' "$out" nested-excluded
want_out no 'old session item' "$out" sessions-excluded
want_out yes 'docs/INDEX.md' "$out" reading-list
want_out yes 'agent-platform-docs' "$out" spec-source
want_out yes 'not passed make check' "$out" stamp-missing
stamp
want_out yes 'matches the last green make check' "$(session "$repo")" stamp-matches
echo c >>"$repo/a.go"
want_out yes 'not passed make check' "$(session "$repo")" stamp-stale
git -C "$repo" stash -q
git -C "$repo" checkout -q main
want_out yes 'create a branch' "$(session "$repo")" on-main
git -C "$repo" checkout -q feature
rm "$repo/docs/PROGRESS.md"
out=$(session "$repo")
code=$?
[ "$code" = 0 ] || { echo "FAIL: session-start without PROGRESS.md exit $code, want 0"; fails=$((fails + 1)); }
want_out yes 'Branch: feature' "$out" no-progress-still-orients
notrepo=$(mktemp -d)
out=$(session "$notrepo")
code=$?
[ "$code" = 0 ] || { echo "FAIL: session-start outside a repo exit $code, want 0"; fails=$((fails + 1)); }
want_out no 'Branch:' "$out" outside-repo-silent
rm -rf "$notrepo"

if [ "$fails" -gt 0 ]; then
	echo "guard hooks: $fails failing case(s)"
	exit 1
fi
echo "guard hooks: ok"
