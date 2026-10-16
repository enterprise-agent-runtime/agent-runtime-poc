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
//     chrt, ionice, prlimit, flock, script, watch (owner). Wrappers are
//     recognised only in command position, as A08 §2.4 step 4 does: the
//     walk starts at argv[0], skips each wrapper's own options, their
//     arguments, NAME=VALUE pairs and the wrapper's operands (timeout's
//     duration, taskset's mask, chrt's priority, flock's lock file) and
//     applies the rules above to the first command that is not a wrapper.
//     `timeout 600 npm run watch` is therefore an ordinary command.
//   - a wrapper's own options build a shell string (owner): env -S, an -S
//     cluster or attachment, --split-string[=…] or a prefix (env -S builds
//     argv from a string); script with -c / --command anywhere (getopt
//     permutes; otherwise the walk continues at the command operand that
//     BSD and macOS script take after the file); flock with -c / --command
//     right after the lock file (util-linux's "+" optstring: options come
//     first, and the command option is only special there); watch without
//     -x / --exec, since watch runs its command through sh -c (with -x the
//     walk continues at watch's command).
//   - fail closed: the wrapper tables know the common GNU, util-linux and
//     BSD options, not every one, and an unknown option argument stops the
//     walk early. So whenever argv[0] is a wrapper, A06's own rule also
//     applies to every later element: one whose basename is a shell,
//     followed by a shell command option, makes a shell string. The price
//     is that a wrapped command whose arguments contain such a pair is
//     refused too (timeout 600 go test -run sh -count=1 ./...).
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
	if commandPositionShell(argv) {
		return true
	}
	// Fail closed where a wrapper table is incomplete: once argv[0] is a
	// wrapper, A06's own rule applies to every later element.
	if _, _, isWrapper := wrapperArgs(baseName(argv[0]), argv[1:]); isWrapper {
		return laterShell(argv[1:])
	}
	return false
}

// laterShell is A06 §6 step 2's wrapper rule: some element whose basename
// is a shell is followed by a shell command option.
func laterShell(args []string) bool {
	for i, a := range args {
		if b := baseName(a); isShell(b) && hasCommandOption(b, args[i+1:]) {
			return true
		}
	}
	return false
}

// commandPositionShell walks the wrappers from argv[0] to the command they
// run (A08 §2.4 step 4) and applies the shell rules there.
func commandPositionShell(argv []string) bool {
	for i := 0; i < len(argv); {
		b := baseName(argv[i])
		rest := argv[i+1:]
		if isShell(b) {
			return hasCommandOption(b, rest)
		}
		n, shellString, isWrapper := wrapperArgs(b, rest)
		n = min(max(n, 0), len(rest)) // every parser clamps too; this one guards the slice below
		switch {
		case !isWrapper:
			return false // the command, and not a shell
		case shellString:
			return true
		}
		i += 1 + n
	}
	return false
}

// wrapperArgs reports whether b is an exec wrapper and, if so, how many of
// the following elements are its own (options, their arguments,
// assignments, operands; the command starts after them) and whether those
// already make a shell string.
func wrapperArgs(b string, rest []string) (n int, shellString, isWrapper bool) {
	switch {
	case strings.EqualFold(b, "env"):
		split, n := envOptions(rest)
		return n, split, true
	case strings.EqualFold(b, "script"):
		// -c runs a command through $SHELL -c (GNU and BSD); BSD and macOS
		// script also run a command operand after the file.
		if scriptRunsCommand(rest) {
			return len(rest), true, true
		}
		return skipArgs(rest, wrapperSpec{shortArg: "EIOBTmt", longArg: []string{"echo", "log-in", "log-out", "log-io", "log-timing", "logging-format", "output-limit"}, operands: 1}), false, true
	case strings.EqualFold(b, "flock"):
		n, sh := flockArgs(rest)
		return n, sh, true
	case strings.EqualFold(b, "watch"):
		n, exec := watchOptions(rest)
		return n, !exec, true // without -x, watch runs its command through sh -c
	case strings.EqualFold(b, "busybox"):
		return 0, false, true // the next element is the applet
	}
	spec, ok := wrapperSpecFor(b)
	if !ok {
		return 0, false, false
	}
	return skipArgs(rest, spec), false, true
}

// wrapperSpec describes a wrapper's command line: the short options that
// take an argument (attached or as the next element), the long options
// that take one (as =value or the next element), and how many operands
// come before the command.
type wrapperSpec struct {
	shortArg string
	longArg  []string
	operands int
}

// wrapperSpecFor gives the GNU coreutils / util-linux / bash-builtin
// command lines of the generic wrappers.
func wrapperSpecFor(b string) (wrapperSpec, bool) {
	switch strings.ToLower(b) {
	case "nice":
		return wrapperSpec{shortArg: "n", longArg: []string{"adjustment"}}, true
	case "nohup", "setsid", "command":
		return wrapperSpec{}, true
	case "timeout":
		return wrapperSpec{shortArg: "ks", longArg: []string{"kill-after", "signal"}, operands: 1}, true
	case "stdbuf":
		return wrapperSpec{shortArg: "ioe", longArg: []string{"input", "output", "error"}}, true
	case "xargs":
		return wrapperSpec{shortArg: "aEIdLnPsJR", longArg: []string{"arg-file", "delimiter", "max-args", "max-procs", "max-chars", "process-slot-var"}}, true
	case "time":
		return wrapperSpec{shortArg: "fo", longArg: []string{"format", "output"}}, true
	case "exec":
		return wrapperSpec{shortArg: "a"}, true
	case "taskset":
		return wrapperSpec{operands: 1}, true
	case "chrt":
		return wrapperSpec{shortArg: "TPD", longArg: []string{"sched-runtime", "sched-period", "sched-deadline"}, operands: 1}, true
	case "ionice":
		return wrapperSpec{shortArg: "cnpPu", longArg: []string{"class", "classdata", "pid", "pgid", "uid"}}, true
	case "prlimit":
		return wrapperSpec{shortArg: "po", longArg: []string{"pid", "output"}}, true
	}
	return wrapperSpec{}, false
}

// skipArgs counts a wrapper's own options (up to "--" or the first
// non-option, as the "+" optstrings of these tools do) and its operands.
func skipArgs(args []string, spec wrapperSpec) int {
	j := 0
	for j < len(args) {
		a := args[j]
		if a == "--" {
			j++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break
		}
		if name, ok := strings.CutPrefix(a, "--"); ok {
			name, _, hasValue := strings.Cut(name, "=")
			if !hasValue && takesArg(name, spec.longArg) {
				j++
			}
			j++
			continue
		}
		for k := 1; k < len(a); k++ {
			if strings.IndexByte(spec.shortArg, a[k]) >= 0 {
				if k == len(a)-1 {
					j++ // the argument is the next element
				}
				break // or the rest of this element
			}
		}
		j++
	}
	j += spec.operands
	return min(j, len(args))
}

func takesArg(name string, long []string) bool {
	for _, l := range long {
		if isPrefixOf(name, l, 1) {
			return true
		}
	}
	return false
}

// flockArgs parses util-linux flock: options (its optstring starts with
// "+", so they stop at the first non-option), the lock file, then either
// -c / --command, which runs the next element through sh -c, or the
// command. n counts the options and the lock file.
func flockArgs(args []string) (n int, shellString bool) {
	j := skipArgs(args, wrapperSpec{shortArg: "wE", longArg: []string{"timeout", "wait", "conflict-exit-code"}})
	if j >= len(args) {
		return len(args), false
	}
	j++ // the lock file
	if j < len(args) && isFlockCommandOption(args[j]) {
		return j, true
	}
	return j, false
}

func isFlockCommandOption(a string) bool {
	if name, ok := strings.CutPrefix(a, "--"); ok {
		name, _, _ = strings.Cut(name, "=")
		return isPrefixOf(name, "command", 1)
	}
	return isShortCommandOption(a, false)
}

// scriptRunsCommand reports whether script is given -c / --command (any
// getopt prefix, =value or attached), which it runs through $SHELL -c.
// getopt permutes, so the option may come after the file operand.
func scriptRunsCommand(args []string) bool {
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
	return min(j, len(args)), exec
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

// envOptions parses env's own options and NAME=VALUE assignments (they come
// before the command), reports whether one of the options is -S or
// --split-string (any prefix, as getopt_long accepts), and returns how many
// elements precede the command. -u NAME and -C DIR take an argument,
// attached or separate, which is skipped.
func envOptions(args []string) (split bool, n int) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return false, i + 1
		case a == "-":
			// a lone "-" is -i
		case strings.HasPrefix(a, "--"):
			name, _, hasValue := strings.Cut(a[2:], "=")
			if isPrefixOf(name, "split-string", 1) {
				return true, i
			}
			if !hasValue && (isPrefixOf(name, "unset", 1) || isPrefixOf(name, "chdir", 1) || isPrefixOf(name, "argv0", 1)) {
				i++
			}
		case len(a) > 1 && a[0] == '-':
		short:
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'S':
					return true, i
				case 'u', 'C', 'P', 'a':
					if j == len(a)-1 {
						i++ // the argument is the next element
					}
					break short // the rest of this element is the argument
				}
			}
		case strings.Contains(a, "="):
			// NAME=VALUE assignment
		default:
			return false, i // the command
		}
	}
	return false, len(args)
}
