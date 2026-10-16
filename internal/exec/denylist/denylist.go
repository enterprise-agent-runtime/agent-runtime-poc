// Package denylist is the platform deny-list of WRD-10 §6 (full list,
// OQ-11) and its matcher (design A15 §4.2, CF-17). It is enforced at three
// layers (INV-I): the policy engine (INV-1), the executor and the sandbox
// mounts. It lives under internal/exec so the in-sandbox executor, which
// imports nothing from the daemon (CLAUDE.md §6), can use the same
// implementation as the policy engine and the secrets package
// (docs/DECISIONS-poc.md D-014).
package denylist

import (
	"path"
	"strings"
)

// Version identifies the list; it appears in sandbox.create limits.
const Version = "denylist/v2"

// Patterns is WRD-10 §6, in order. It is compiled into the binary and can
// be extended by organizations but never shortened.
var Patterns = []string{
	"**/.env", "**/.env.*", "**/*.pem", "**/*.key", "**/*.p12", "**/*.pfx", "**/id_rsa*", "**/id_ed25519*",
	"**/*.kdbx", "**/.netrc", "**/.npmrc", "**/.pypirc", "**/.git-credentials", "**/credentials.json",
	"**/service-account*.json", "**/.aws/**", "**/.ssh/**", "**/.config/gcloud/**", "**/.azure/**", "**/.kube/**",
	"**/.docker/config.json", "**/.terraform.d/**", "**/.warden/**",
}

// Normalize turns a host or sandbox path into the slash form the globs are
// matched against: forward slashes, lower-case drive letter, no trailing
// slash, cleaned. Backslashes count as separators on every OS: that can
// only deny more, and keeps Windows paths matching identically when the
// policy engine runs on Linux.
func Normalize(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "//?/")
	if len(p) >= 2 && p[1] == ':' {
		p = strings.ToLower(p[:1]) + p[1:]
	}
	if p == "" {
		return p
	}
	return path.Clean(p)
}

// Match reports the first pattern matching p. Matching is ASCII
// case-insensitive. p is a path relative to a mounted root (CF-17) or an
// absolute path; both use the same globs.
func Match(p string) (string, bool) {
	p = strings.ToLower(Normalize(p))
	p = strings.TrimPrefix(p, "./")
	segs := split(p)
	for _, pat := range Patterns {
		if matchSegs(split(strings.ToLower(pat)), segs) {
			return pat, true
		}
	}
	return "", false
}

// MatchUnder additionally denies anything under one of the given absolute
// roots (the effective Warden home, CONFLICTS C-53), except paths inside an
// allowed root (the task's own mounted roots, CF-17).
func MatchUnder(abs string, denyRoots, allowRoots []string) (string, bool) {
	n := strings.ToLower(Normalize(abs))
	for _, a := range allowRoots {
		if within(n, strings.ToLower(Normalize(a))) {
			rel := strings.TrimPrefix(strings.TrimPrefix(n, strings.ToLower(Normalize(a))), "/")
			return Match(rel)
		}
	}
	for _, d := range denyRoots {
		if within(n, strings.ToLower(Normalize(d))) {
			return "warden_home", true
		}
	}
	return Match(abs)
}

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

func split(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}

// matchSegs matches glob segments: "**" matches zero or more segments,
// "*" and "?" match within one segment.
func matchSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegs(pat[1:], segs[1:])
}

// Argv applies the layer-1 argv heuristic (A15 §4.3): any argument after
// argv[0] (or the value of --flag=value) without whitespace, at most 4096
// bytes, that names a deny-listed path relative to cwd or absolutely,
// denies the call. ["cat", ".env"] is denied before approval is asked.
func Argv(argv []string, cwdRel string) (string, bool) {
	for _, a := range argv[min(1, len(argv)):] {
		if _, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "-") {
			a = v
		}
		if a == "" || len(a) > 4096 || strings.ContainsAny(a, " \t\n") {
			continue
		}
		if pat, ok := Match(a); ok {
			return pat, true
		}
		if cwdRel != "" {
			if pat, ok := Match(path.Join(cwdRel, strings.ReplaceAll(a, "\\", "/"))); ok {
				return pat, true
			}
		}
	}
	return "", false
}
