#!/usr/bin/env bash
# Shared helpers for the Claude Code guard hooks (CLAUDE.md §15). Plain bash,
# no jq/sed/python, so the hooks behave the same in Git Bash on Windows, WSL2
# and macOS's bash 3.2.

# json_field NAME JSON prints the unescaped value of the first string field
# called NAME. Enough for hook input, which is one JSON object per call.
json_field() {
	local re="\"$1\"[[:space:]]*:[[:space:]]*\"(([^\"\\\\]|\\\\.)*)\""
	[[ $2 =~ $re ]] || return 1
	local v=${BASH_REMATCH[1]}
	v=${v//\\\\/$'\x01'}  # protect escaped backslashes
	v=${v//\\\"/\"}
	v=${v//\\n/;}         # a newline separates commands like ';' does
	v=${v//\\t/ }
	v=${v//$'\x01'/\\}
	printf '%s' "$v"
}

# block MESSAGE refuses the tool call: exit 2 and stderr go back to Claude.
block() {
	printf 'Blocked by .claude/hooks (CLAUDE.md §15): %s\n' "$1" >&2
	exit 2
}
