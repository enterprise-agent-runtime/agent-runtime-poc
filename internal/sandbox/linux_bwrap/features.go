package linux_bwrap

// The bwrap feature probe of design A06 §11.1 ("Feature probing"): parse
// "bwrap --version" and "bwrap --help" for the optional flags, refuse
// anything below the minimum. Pure functions over the captured text, so the
// probe is tested on real outputs without running bwrap.

import (
	"fmt"
	"strconv"
	"strings"

	"warden.dev/warden/internal/sandbox"
)

// Optional flags A06 §11.1 uses when bwrap has them.
const (
	FlagAddSeccompFD  = "--add-seccomp-fd" // bwrap >= 0.5; without it, the deprecated --seccomp
	FlagSize          = "--size"           // bwrap >= 0.7; without it, /tmp and /dev/shm have the kernel default size
	FlagDisableUserns = "--disable-userns" // bwrap >= 0.8; without it, only the seccomp unshare/clone rows stop nested namespaces
)

// Features is what the probe found: the bwrap version and which optional
// flags it has. The zero value of each flag is the safe fallback.
type Features struct {
	Version       string
	AddSeccompFD  bool
	Size          bool
	DisableUserns bool
}

// Missing lists the optional flags this bwrap lacks, for
// sandbox.create.limits (A06 §11.1: "Missing optional flags are dropped and
// recorded").
func (f Features) Missing() []string {
	var out []string
	for _, x := range []struct {
		flag string
		ok   bool
	}{{FlagAddSeccompFD, f.AddSeccompFD}, {FlagSize, f.Size}, {FlagDisableUserns, f.DisableUserns}} {
		if !x.ok {
			out = append(out, x.flag)
		}
	}
	return out
}

// ParseFeatures reads the output of "bwrap --version" and "bwrap --help".
// The version must be "bubblewrap X.Y.Z" with X.Y.Z at least
// sandbox.MinBwrap; anything else is sandbox.ErrUnavailable, so an
// unrecognised or too old bwrap never gets an argv. A flag counts as present
// only when a help line starts with it; the 0.6.x help names
// --add-seccomp-fd "--add-seccomp" (its parser accepts only the long name),
// so that spelling counts too.
func ParseFeatures(versionOut, helpOut string) (Features, error) {
	fields := strings.Fields(versionOut)
	if len(fields) != 2 || fields[0] != "bubblewrap" {
		return Features{}, fmt.Errorf("%w: bwrap --version printed %q, not \"bubblewrap X.Y.Z\" (A06 §11.1)", sandbox.ErrUnavailable, versionOut)
	}
	v, ok := parseVersion(fields[1])
	if !ok {
		return Features{}, fmt.Errorf("%w: bwrap version %q is not X.Y.Z (A06 §11.1)", sandbox.ErrUnavailable, fields[1])
	}
	floor, ok := parseVersion(sandbox.MinBwrap)
	if !ok || less(v, floor) {
		return Features{}, fmt.Errorf("%w: bubblewrap %s is older than %s (A06 §11.1)", sandbox.ErrUnavailable, fields[1], sandbox.MinBwrap)
	}
	flags := helpFlags(helpOut)
	return Features{
		Version:       fields[1],
		AddSeccompFD:  flags[FlagAddSeccompFD] || flags["--add-seccomp"],
		Size:          flags[FlagSize],
		DisableUserns: flags[FlagDisableUserns],
	}, nil
}

// helpFlags returns the flag that starts each help line.
func helpFlags(help string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(help, "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(f[0], "--") {
			out[f[0]] = true
		}
	}
	return out
}

type version [3]int

func parseVersion(s string) (version, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" || len(p) > 6 {
			return version{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		v[i] = n
	}
	return v, true
}

func less(a, b version) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
