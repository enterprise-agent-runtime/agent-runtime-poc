package execproto

import "strings"

// IsShellString reports whether argv hands a shell a command string, which
// warden-exec refuses with ErrShellString and the policy rule
// platform.no-shell-strings denies (CF-19; WRD-16 §9 "argv array only, no
// shell string"). It is A06 §6 step 2:
//
//   - argv[0]'s basename is a shell (sh, bash, zsh, dash, ksh, fish) and any
//     later element is -c, --command, or a short-option cluster containing c
//     (^-[A-Za-z]*c[A-Za-z]*$, e.g. -lc, -ec);
//   - or argv[0]'s basename is an exec wrapper (env, nice, nohup, timeout,
//     stdbuf, setsid, xargs, busybox, time, command) and the first later
//     element whose basename is a shell is followed by such an element.
//
// Basenames are compared without regard to case, a choice A06 leaves open:
// on macOS the default file system is case-insensitive, so "BASH" would run
// /bin/bash. Options stay case-sensitive (bash -C is noclobber, not -c).
// Empty argv is not a shell string; the shape check rejects it first.
func IsShellString(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	b := baseName(argv[0])
	if isShell(b) {
		return hasCommandFlag(argv[1:])
	}
	if isExecWrapper(b) {
		for i := 1; i < len(argv); i++ {
			if isShell(baseName(argv[i])) {
				return hasCommandFlag(argv[i+1:])
			}
		}
	}
	return false
}

// baseName is the last slash-separated element, lower-cased. argv is in
// sandbox (Unix) coordinates, so only '/' separates.
func baseName(s string) string {
	return strings.ToLower(s[strings.LastIndexByte(s, '/')+1:])
}

func isShell(b string) bool {
	switch b {
	case "sh", "bash", "zsh", "dash", "ksh", "fish":
		return true
	}
	return false
}

func isExecWrapper(b string) bool {
	switch b {
	case "env", "nice", "nohup", "timeout", "stdbuf", "setsid", "xargs", "busybox", "time", "command":
		return true
	}
	return false
}

func hasCommandFlag(args []string) bool {
	for _, a := range args {
		if a == "--command" || isCommandCluster(a) {
			return true
		}
	}
	return false
}

// isCommandCluster matches ^-[A-Za-z]*c[A-Za-z]*$, which includes "-c".
func isCommandCluster(a string) bool {
	if len(a) < 2 || a[0] != '-' {
		return false
	}
	hasC := false
	for i := 1; i < len(a); i++ {
		ch := a[i]
		switch {
		case ch == 'c':
			hasC = true
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z':
		default:
			return false
		}
	}
	return hasC
}
