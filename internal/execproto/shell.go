package execproto

import "strings"

// IsShellString reports whether argv hands a shell a command string, which
// warden-exec refuses with ErrShellString and the policy rule
// platform.no-shell-strings denies (CF-19; WRD-16 §9 "argv array only, no
// shell string"). It implements A06 §6 step 2, widened by the owner on
// 2026-10-16 (stricter than A06; recorded in docs/CONFLICTS.md):
//
//   - argv[0]'s basename is a shell: sh, bash, zsh, dash, ksh, fish (A06),
//     ash, hush, rbash, csh, tcsh, mksh, yash, posh (owner); and any later
//     element is a command option. For these shells that is a single-dash
//     element whose leading letters reach a c, so -c, a cluster such as -lc
//     or -ec (A06) and an attached command such as -cid (owner); for fish
//     also C (-C, fish's --init-command); and --command, --init-command,
//     their unambiguous prefixes (--c…, --ini…), with or without =value
//     (owner).
//   - argv[0]'s basename is pwsh or powershell (owner) and a later element
//     is -Command, -EncodedCommand, -CommandWithArgs or one of their
//     case-insensitive prefixes or aliases (-c, -e, -ec, -enc, -cwa), with
//     one or two leading dashes.
//   - argv[0]'s basename is an exec wrapper: env, nice, nohup, timeout,
//     stdbuf, setsid, xargs, busybox, time, command (A06), exec, taskset,
//     chrt, ionice, prlimit, flock, script, watch (owner); and either the
//     first later element whose basename is a shell is followed by a
//     command option as above (A06), or an env among the wrappers is given
//     -S, an -S cluster or attachment, or --split-string[=…] or a prefix of
//     it (owner): env -S builds argv from a string and so can build any
//     shell string.
//   - among the wrappers, ones that hand their own argument to a shell, with
//     no shell in argv (owner): script or flock given -c / --command (any
//     getopt prefix, =value or attached, anywhere after it, since getopt
//     permutes); watch without -x / --exec among its own options, since
//     watch runs its command through sh -c. With -x the remaining argv is
//     checked as an ordinary command (watch -x sh -c … is still caught).
//
// Names are compared with strings.EqualFold (owner): on macOS the default
// file system is case-insensitive, so "BASH" and "ſh" (U+017F folds to s)
// run the shell. Options of POSIX shells stay case-sensitive (bash -C is
// noclobber, not -c).
//
// Interpreter strings (python -c, node -e, perl -e, ruby -e, awk programs)
// are deliberately not shell strings: they stay R3 commands that need an
// approval (owner decision 2026-10-16).
//
// The check sees names, not files. A repository can ship a symlink with an
// innocent name that points at a shell, so the executor's spawn path calls
// IsShellStringResolved again once argv[0] is resolved (A06 §6 step 3).
// Empty argv is not a shell string; the shape check rejects it first.
func IsShellString(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	b := baseName(argv[0])
	if isShell(b) {
		return hasCommandOption(b, argv[1:])
	}
	if !isExecWrapper(b) {
		return false
	}
	for i := 0; i < len(argv); i++ {
		bi := baseName(argv[i])
		if i > 0 && isShell(bi) {
			return hasCommandOption(bi, argv[i+1:])
		}
		switch {
		case strings.EqualFold(bi, "env"):
			if envSplitsString(argv[i+1:]) {
				return true
			}
		case foldIn(bi, "script", "flock"):
			if runsOwnCommand(argv[i+1:]) {
				return true
			}
		case strings.EqualFold(bi, "watch"):
			n, exec := watchOptions(argv[i+1:])
			if !exec {
				return true // watch runs its command through sh -c
			}
			i += n // with -x the rest is an ordinary command: keep checking it
		}
	}
	return false
}

// runsOwnCommand reports whether script or flock is given -c / --command
// (any getopt prefix, =value or attached), which they run through a shell.
// getopt permutes, so the option may come after the file operand.
func runsOwnCommand(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if name, ok := strings.CutPrefix(a, "--"); ok {
			name, _, _ = strings.Cut(name, "=")
			if isPrefixOf(name, "command", 1) {
				return true
			}
			continue
		}
		if isShortCommandOption(a, false) {
			return true
		}
	}
	return false
}

// watchOptions parses watch's own options (they stop at the command) and
// returns how many elements they take and whether -x / --exec is among
// them. -n/--interval and -q/--equexit take an argument.
func watchOptions(args []string) (n int, exec bool) {
	j := 0
	for ; j < len(args); j++ {
		a := args[j]
		if a == "--" {
			return j + 1, exec
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		if name, ok := strings.CutPrefix(a, "--"); ok {
			name, _, hasValue := strings.Cut(name, "=")
			switch {
			case isPrefixOf(name, "exec", 2):
				exec = true
			case !hasValue && (isPrefixOf(name, "interval", 1) || isPrefixOf(name, "equexit", 2)):
				j++
			}
			continue
		}
	short:
		for k := 1; k < len(a); k++ {
			switch a[k] {
			case 'x':
				exec = true
			case 'n', 'q':
				if k == len(a)-1 {
					j++ // the argument is the next element
				}
				break short // the rest of this element is the argument
			}
		}
	}
	return j, exec
}

// IsShellStringResolved is IsShellString applied twice: to argv as given,
// and to argv with argv[0] replaced by executable, the path the executor
// resolved it to with symlinks followed (A06 §6 step 3). It catches
// ./tools/xsh -> /bin/sh. The M2-8 spawn path calls it.
func IsShellStringResolved(executable string, argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if IsShellString(argv) {
		return true
	}
	resolved := append([]string{executable}, argv[1:]...)
	return IsShellString(resolved)
}

// IsShellName reports whether a path or basename names a shell under the
// rules of IsShellString.
func IsShellName(nameOrPath string) bool { return isShell(baseName(nameOrPath)) }

// baseName is the last slash-separated element. argv is in sandbox (Unix)
// coordinates, so only '/' separates.
func baseName(s string) string { return s[strings.LastIndexByte(s, '/')+1:] }

func foldIn(b string, names ...string) bool {
	for _, n := range names {
		if strings.EqualFold(b, n) {
			return true
		}
	}
	return false
}

func isShell(b string) bool {
	return foldIn(b, "sh", "bash", "zsh", "dash", "ksh", "fish",
		"ash", "hush", "rbash", "csh", "tcsh", "mksh", "yash", "posh") || isPowerShell(b)
}

func isPowerShell(b string) bool { return foldIn(b, "pwsh", "powershell") }

func isExecWrapper(b string) bool {
	return foldIn(b, "env", "nice", "nohup", "timeout", "stdbuf", "setsid", "xargs", "busybox", "time", "command",
		"exec", "taskset", "chrt", "ionice", "prlimit", "flock", "script", "watch")
}

func hasCommandOption(shell string, args []string) bool {
	pwsh, fish := isPowerShell(shell), strings.EqualFold(shell, "fish")
	for _, a := range args {
		if pwsh {
			if isPwshCommandOption(a) {
				return true
			}
			continue
		}
		if isLongCommandOption(a) || isShortCommandOption(a, fish) {
			return true
		}
	}
	return false
}

// isShortCommandOption: a single-dash element whose leading letters reach
// c (fish: also C). That covers -c, clusters (-lc, -ec) and an attached
// command (-cid, -c'id').
func isShortCommandOption(a string, fish bool) bool {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return false
	}
	for i := 1; i < len(a); i++ {
		ch := a[i]
		switch {
		case ch == 'c', fish && ch == 'C':
			return true
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z':
		default:
			return false
		}
	}
	return false
}

// isLongCommandOption: --command or --init-command, or a prefix getopt_long
// would accept as one of them (--c…, --ini…), optionally with =value.
func isLongCommandOption(a string) bool {
	name, ok := strings.CutPrefix(a, "--")
	if !ok {
		return false
	}
	name, _, _ = strings.Cut(name, "=")
	return isPrefixOf(name, "command", 1) || isPrefixOf(name, "init-command", 3)
}

// isPwshCommandOption: PowerShell accepts any unambiguous, case-insensitive
// prefix of a parameter name, with one or two dashes.
func isPwshCommandOption(a string) bool {
	name, ok := strings.CutPrefix(a, "-")
	if !ok {
		return false
	}
	name = strings.ToLower(strings.TrimPrefix(name, "-"))
	return isPrefixOf(name, "command", 1) || isPrefixOf(name, "encodedcommand", 1) ||
		isPrefixOf(name, "commandwithargs", 1) || name == "ec" || name == "cwa"
}

func isPrefixOf(s, full string, minLen int) bool {
	return len(s) >= minLen && strings.HasPrefix(full, s)
}

// envSplitsString parses env's own options (they come before the command)
// and reports whether one of them is -S or --split-string (any prefix, as
// getopt_long accepts). -u NAME and -C DIR take an argument, attached or
// separate, which is skipped.
func envSplitsString(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return false
		case strings.HasPrefix(a, "--"):
			name, _, hasValue := strings.Cut(a[2:], "=")
			if isPrefixOf(name, "split-string", 1) {
				return true
			}
			if !hasValue && (isPrefixOf(name, "unset", 1) || isPrefixOf(name, "chdir", 1)) {
				i++
			}
		case len(a) > 1 && a[0] == '-':
		short:
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'S':
					return true
				case 'u', 'C':
					if j == len(a)-1 {
						i++ // the argument is the next element
					}
					break short // the rest of this element is the argument
				}
			}
		case strings.Contains(a, "="):
			// NAME=VALUE assignment
		default:
			return false // the command
		}
	}
	return false
}
