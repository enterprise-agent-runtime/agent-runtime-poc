#!/usr/bin/env bash
# PreToolUse guard for Bash and PowerShell commands (CLAUDE.md §15). Each rule
# enforces a line of CLAUDE.md that an instruction alone has already failed to
# hold at least once; the incident is named next to the rule. Exit 0 allows,
# exit 2 blocks and tells Claude why. Tests: .claude/hooks/test-hooks.sh, run
# by `make check`.
set -uo pipefail
. "$(dirname "$0")/lib.sh"

input=$(cat)
cmd=$(json_field command "$input") || exit 0
cwd=$(json_field cwd "$input" || pwd)

# The repository the command acts on: a leading `cd <dir> &&` wins over the
# session's working directory (the usual shape of commands here).
repo=$cwd
re_cd='^[[:space:]]*cd[[:space:]]+"?([^"&;]*[^"&;[:space:]])"?[[:space:]]*(&&|;)'
if [[ $cmd =~ $re_cd ]]; then repo=${BASH_REMATCH[1]}; fi
branch=$(git -C "$repo" branch --show-current 2>/dev/null || true)

# B: start of a command. G: `git` plus global options (-C dir, -c k=v, --x),
# so that only the real subcommand matches - not "push" inside a commit
# message. E: end of a word.
B='(^|[;&|(][[:space:]]*|[[:space:]]|^[[:space:]]*)'
G='git([[:space:]]+(-C[[:space:]]+[^[:space:]]+|-c[[:space:]]+[^[:space:]]+|--[^[:space:]]+))*[[:space:]]+'
E='([[:space:];&|)]|$)'
# bash cannot parse ; & | written literally inside [[ =~ ]], so every
# pattern lives in a variable.
re_push="${B}${G}push([^;&|]*)"
re_main="([[:space:]:+])(main|master)${E}"
re_force="(^|[[:space:]])(--force|-f)${E}"
re_noverify="${B}${G}(commit|push|merge|rebase|am|cherry-pick)[^;&|]*[[:space:]]--no-verify"
re_commit="${B}${G}commit${E}"
re_bin="${B}([^[:space:];&|]*/)?bin/wardend?(\.exe)?${E}"
re_gorun="${B}go[[:space:]]+run[[:space:]]+[^;&|]*cmd/wardend?${E}"
re_rundaemon="${B}make[[:space:]]+run-daemon"

# Rule 1: main/master change only through a pull request (CLAUDE.md §11, §13).
if [[ $cmd =~ $re_push ]]; then
	args=${BASH_REMATCH[${#BASH_REMATCH[@]}-1]}
	if [[ $args =~ $re_main ]]; then
		block "pushing to main/master. Push your branch and open a pull request."
	fi
	if [[ $args =~ $re_force ]]; then
		block "force push. Ask the owner; if agreed, use --force-with-lease on your own branch."
	fi
	# A bare `git push` (or `git push origin`) updates the current branch.
	positional=0
	for w in $args; do [[ $w == -* ]] || positional=$((positional + 1)); done
	if [[ $branch == main || $branch == master ]] && [ "$positional" -lt 2 ]; then
		block "the current branch is $branch; this push would update it. Create a branch first."
	fi
fi

# Rule 2: never skip hooks.
if [[ $cmd =~ $re_noverify ]]; then
	block "--no-verify. Fix what the hook reports instead."
fi

# Rule 3: never commit red (CLAUDE.md §8.1, §11). `make check` records the
# tree it passed on (scripts/tree-hash.sh); committing any other tree is
# refused. Changes that touch only Markdown are exempt.
# Incident: M1, a comment-only commit made without `make check`; and
# `make check` itself once ignored lint failures (D-025).
if [[ $cmd =~ $re_commit ]] && ! [[ $cmd =~ --dry-run ]]; then
	if [[ $branch == main || $branch == master ]]; then
		block "committing on $branch. Create a branch first: git checkout -b <topic>."
	fi
	if [ -f "$repo/scripts/tree-hash.sh" ]; then
		tree=$(bash "$repo/scripts/tree-hash.sh" "$repo" 2>/dev/null || echo none)
		stampfile=$(git -C "$repo" rev-parse --path-format=absolute --git-path warden-check-ok 2>/dev/null)
		stamp=$(cat "$stampfile" 2>/dev/null || true)
		if [ "$tree" != "$stamp" ]; then
			changed=$( (git -C "$repo" diff HEAD --name-only; git -C "$repo" ls-files --others --exclude-standard) 2>/dev/null)
			nonmd=$(printf '%s\n' "$changed" | grep -v -e '\.md$' -e '^$' || true)
			if [ -n "$nonmd" ]; then
				block "this working tree has not passed make check (changed: $(printf '%s' "$nonmd" | head -3 | tr '\n' ' ')). Run make check, then commit."
			fi
		fi
	fi
fi

# Rule 4: the CLI and daemon never run against the real ~/.warden or
# %LOCALAPPDATA%\Warden from a session (CLAUDE.md §8.4).
# Incident: M1, one CLI run without WARDEN_HOME created the owner's real home.
if [[ $cmd =~ $re_bin || $cmd =~ $re_gorun || $cmd =~ $re_rundaemon ]]; then
	if [ -z "${WARDEN_HOME:-}" ] && ! [[ $cmd =~ WARDEN_HOME= ]]; then
		block "warden/wardend without WARDEN_HOME. Set WARDEN_HOME to a temp dir in the same command."
	fi
fi

exit 0
