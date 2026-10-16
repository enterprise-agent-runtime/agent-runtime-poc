package exec

// Per-child resource limits (WRD-10 §5.1, design A06 §3.1 --child-limits,
// §7.3). The package doc is in server.go.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ChildLimits are the per-child rlimits of design A06 §7.3, given to the
// executor as --child-limits (§3.1) and applied by the child shim before
// execve (§7.1 step 4). Data and Nproc are optional: without a cgroup the
// daemon passes data (RLIMIT_DATA, the memory row) and nproc (the user's
// process count + 512); with a cgroup those limits are the sandbox's.
// RLIMIT_AS is deliberately absent (§7.3: V8 and Go reserve large ranges).
type ChildLimits struct {
	CPU    uint64  `json:"cpu"`
	Fsize  uint64  `json:"fsize"`
	Nofile uint64  `json:"nofile"`
	Core   uint64  `json:"core"`
	Data   *uint64 `json:"data,omitempty"`
	Nproc  *uint64 `json:"nproc,omitempty"`
}

// ParseChildLimits parses the --child-limits value,
// "cpu=<s>,fsize=<bytes>,nofile=<n>,core=<bytes>[,data=<bytes>][,nproc=<n>]"
// in any order. It fails closed: an unknown or repeated key, a missing
// required key, anything but a plain decimal, and a cpu, nofile or nproc of
// 0 (which would not be a limit but a ban on running anything) are errors.
// The flag is the daemon's, so its text may be quoted in the error.
func ParseChildLimits(s string) (ChildLimits, error) {
	var l ChildLimits
	seen := map[string]bool{}
	for _, entry := range strings.Split(s, ",") {
		key, val, ok := strings.Cut(entry, "=")
		if !ok {
			return ChildLimits{}, fmt.Errorf("exec: --child-limits entry %q is not key=value", entry)
		}
		if seen[key] {
			return ChildLimits{}, fmt.Errorf("exec: --child-limits repeats %q", key)
		}
		seen[key] = true
		n, err := plainUint(val)
		if err != nil {
			return ChildLimits{}, fmt.Errorf("exec: --child-limits %s: %w", key, err)
		}
		switch key {
		case "cpu":
			l.CPU = n
		case "fsize":
			l.Fsize = n
		case "nofile":
			l.Nofile = n
		case "core":
			l.Core = n
		case "data":
			l.Data = &n
		case "nproc":
			l.Nproc = &n
		default:
			return ChildLimits{}, fmt.Errorf("exec: --child-limits has unknown key %q", key)
		}
	}
	for _, k := range []string{"cpu", "fsize", "nofile", "core"} {
		if !seen[k] {
			return ChildLimits{}, fmt.Errorf("exec: --child-limits lacks %s", k)
		}
	}
	if l.CPU == 0 || l.Nofile == 0 || (l.Nproc != nil && *l.Nproc == 0) {
		return ChildLimits{}, errors.New("exec: --child-limits cpu, nofile and nproc must be at least 1")
	}
	return l, nil
}

// plainUint accepts only canonical decimal: no sign, no leading zero, no
// space, no base prefix.
func plainUint(s string) (uint64, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, errors.New("not a plain decimal")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("not a plain decimal")
		}
	}
	return strconv.ParseUint(s, 10, 64)
}

// ForCall returns the limits for one call with the given timeout: A06
// §7.3's CPU row is "4 × call timeout, runaway guard only", and the launch
// flag's cpu is the ceiling, so the child gets the smaller of the two, in
// whole seconds rounded up. The other limits are the launch values.
func (l ChildLimits) ForCall(timeout time.Duration) ChildLimits {
	secs := uint64((timeout + time.Second - 1) / time.Second)
	if c := 4 * secs; c < l.CPU {
		l.CPU = c
	}
	return l
}
