// Package denylist is the platform deny-list of WRD-10 §6 (full list,
// OQ-11) and its matcher (design A15 §4.2, CF-17). It is enforced at three
// layers (INV-I): the policy engine (INV-1), the executor and the sandbox
// mounts. All three must use this one implementation
// (docs/DECISIONS-poc.md D-014), so it lives in internal/execproto, the
// leaf both sides of the executor protocol import, where design A06 §3.1
// and §17 compile the deny-list matcher. Under internal/exec it was out of
// reach: design A02 §5 R4 lets only the executor import internal/exec, so
// internal/sandbox and internal/policy could not use it. Like the rest of
// internal/execproto it imports only the standard library (A02 §5 R6,
// enforced by internal/archtest).
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
// absolute path; both use the same globs. A path with an NTFS 8.3
// short-name segment matches too (ShortNameMatch, see IsShortName).
//
// Because of the 8.3 rule, Match and MatchUnder are not suitable for
// absolute Windows host paths in their short form: C:\Users\RUNNER~1\...
// (the TEMP of GitHub's Windows runners) is denied as a whole. Callers pass
// root-relative paths, or expand host paths to their long form first
// (GetLongPathName). For absolute host paths the daemon built, use
// MatchHost, which skips only the 8.3 rule.
func Match(p string) (string, bool) {
	return match(p, true)
}

// match is Match, with the 8.3 rule only when shortNames is set.
func match(p string, shortNames bool) (string, bool) {
	p = strings.ToLower(Normalize(p))
	p = strings.TrimPrefix(p, "./")
	segs := split(p)
	for _, pat := range Patterns {
		if matchSegs(split(strings.ToLower(pat)), segs) {
			return pat, true
		}
	}
	if shortNames {
		for _, s := range segs {
			if IsShortName(s) {
				return ShortNameMatch, true
			}
		}
	}
	return "", false
}

// ShortNameMatch is what Match reports for a path with an NTFS 8.3
// short-name segment (see IsShortName).
const ShortNameMatch = "8.3-short-name"

// IsShortName reports whether one path segment has the form of an NTFS 8.3
// short name: a base of at most 8 characters made of a non-empty prefix
// without "." or "~", then "~" and 1-6 digits, and optionally "." and an
// extension of at most 3 characters without "." (an empty one too: Windows
// drops a trailing dot). Case does not matter; the shape has no letters.
//
// NTFS creates such an alias for long names by default on the system drive,
// and opening the alias opens the file: ENV~1 is .env, GIT~1 is .git,
// KUBE~1 is .kube, CREDEN~1.JSO is credentials.json (CWE-41; git refuses
// git~1 for the same reason, CVE-2014-9390, CVE-2019-1353). On Windows L2
// the worktree is a bind mount from NTFS. Which long name an alias stands
// for depends on the volume, so it cannot be mapped back here: every
// segment of this shape is denied, at all three INV-I layers through Match.
// Real files named like this ("draft~1.txt") are denied too; that is the
// accepted false positive.
func IsShortName(seg string) bool {
	base, ext, hasExt := strings.Cut(seg, ".")
	if hasExt && (len(ext) > 3 || strings.Contains(ext, ".")) {
		return false
	}
	if len(base) > 8 {
		return false
	}
	prefix, digits, ok := strings.Cut(base, "~")
	if !ok || prefix == "" || len(digits) == 0 || len(digits) > 6 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// MatchUnder additionally denies anything under one of the given absolute
// roots (the effective Warden home, CONFLICTS C-53), except paths inside an
// allowed root (the task's own mounted roots, CF-17). It applies the 8.3
// short-name rule like Match, so the same caveat holds: an absolute Windows
// host path in short form (C:\Users\RUNNER~1\...) outside the allowed roots
// is denied. Pass long-form paths (GetLongPathName).
func MatchUnder(abs string, denyRoots, allowRoots []string) (string, bool) {
	return matchUnder(abs, denyRoots, allowRoots, true)
}

// MatchHost is Match without the 8.3 short-name rule, for absolute host
// paths the daemon built itself (the sources of a sandbox Spec). The 8.3
// rule defends against aliases a model chooses; a daemon-built path in
// short form, such as the TEMP of GitHub's Windows runners
// (C:\Users\RUNNER~1\...), is not one, and Match would deny it as a whole.
// Every glob still applies. Callers must pass long-form paths (the platform
// layer expands them). Never use it for a path a model proposed.
func MatchHost(abs string) (string, bool) {
	return match(abs, false)
}

// MatchUnderHost is MatchUnder without the 8.3 short-name rule, under the
// same conditions as MatchHost: absolute host paths the daemon built, in
// long form, never a model-proposed path. Deny roots, allowed roots and
// every glob still apply.
func MatchUnderHost(abs string, denyRoots, allowRoots []string) (string, bool) {
	return matchUnder(abs, denyRoots, allowRoots, false)
}

// matchUnder is MatchUnder, with the 8.3 rule only when shortNames is set.
func matchUnder(abs string, denyRoots, allowRoots []string, shortNames bool) (string, bool) {
	n := strings.ToLower(Normalize(abs))
	for _, a := range allowRoots {
		if within(n, strings.ToLower(Normalize(a))) {
			rel := strings.TrimPrefix(strings.TrimPrefix(n, strings.ToLower(Normalize(a))), "/")
			return match(rel, shortNames)
		}
	}
	for _, d := range denyRoots {
		if within(n, strings.ToLower(Normalize(d))) {
			return "warden_home", true
		}
	}
	return match(abs, shortNames)
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
//
// Argv leaves out the 8.3 short-name rule: git revision syntax has that
// shape (HEAD~1, origin/main~3), and routine git commands would be denied
// before approval. Nothing is lost by it. An argv check is a heuristic that
// a spawned process defeats anyway (it can build any path at run time); the
// defence against 8.3 aliases of paths is Match and MatchUnder, which the
// executor applies to every path it opens and the mask layer to every mount.
func Argv(argv []string, cwdRel string) (string, bool) {
	for _, a := range argv[min(1, len(argv)):] {
		if _, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "-") {
			a = v
		}
		if a == "" || len(a) > 4096 || strings.ContainsAny(a, " \t\n") {
			continue
		}
		if pat, ok := match(a, false); ok {
			return pat, true
		}
		if cwdRel != "" {
			if pat, ok := match(path.Join(cwdRel, strings.ReplaceAll(a, "\\", "/")), false); ok {
				return pat, true
			}
		}
	}
	return "", false
}
