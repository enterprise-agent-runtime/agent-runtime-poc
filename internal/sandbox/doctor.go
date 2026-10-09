// Package sandbox will build and launch the per-task sandboxes of WRD-10 §5
// and WRD-16 §10 (L1 bubblewrap/Seatbelt, L2 OCI; M2). In M1 it provides
// the sandbox prerequisite probes reported by system.doctor (A05 §8.1
// checks sandbox.*), which execute bwrap and talk to the Docker engine and
// therefore must live in a package allowed to start processes (INV-H).
package sandbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Check is one doctor result (A05 types.json doctorCheck).
type Check struct {
	ID       string         `json:"id"`
	Group    string         `json:"group"`
	Status   string         `json:"status"` // ok | warn | fail
	Title    string         `json:"title"`
	Detail   string         `json:"detail"`
	FixHint  *string        `json:"fix_hint"`
	Blocking bool           `json:"blocking"`
	Data     map[string]any `json:"data,omitempty"`
}

func hint(s string) *string { return &s }

// Options tune the probes.
type Options struct {
	DefaultLevel string // L1 | L2 (sandbox.default_level)
}

// MinBwrap is the oldest supported bubblewrap (A06 §11.2; Ubuntu 22.04
// ships 0.6.1; CONFLICTS C-56).
const MinBwrap = "0.6.0"

// Probe runs every sandbox prerequisite check for this OS.
func Probe(ctx context.Context, o Options) []Check {
	checks := probeOS(ctx, o)
	checks = append(checks, l2Check(ctx, o))
	return checks
}

// l2Check pings the Docker engine. L2 is first-class on every OS. It is
// blocking exactly when L2 is the configured default; on Windows that is
// always the case, because the platform default level there is L2 and L2 is
// the only level offered (CONFLICTS C-02).
func l2Check(ctx context.Context, o Options) Check {
	return l2CheckWith(ctx, o, dockerPing)
}

// l2CheckWith is l2Check with the engine ping injected. An unusable engine
// (unreachable, or not serving Linux containers) is "fail" when L2 is
// blocking and "warn" otherwise (design A05 §8.1).
func l2CheckWith(ctx context.Context, o Options, ping func(context.Context) (ver, osType string, err error)) Check {
	blocking := o.DefaultLevel == "L2"
	c := Check{ID: "sandbox.l2", Group: "sandbox", Title: "L2 container sandbox (Docker engine)", Blocking: blocking}
	ver, os, err := ping(ctx)
	unusable := "warn"
	if blocking {
		unusable = "fail"
	}
	if err != nil {
		c.Status = unusable
		c.Detail = "Docker engine not reachable: " + err.Error()
		c.FixHint = hint("Start Docker Desktop (Windows, with the WSL2 backend) or Docker Engine (Linux), then run warden doctor again.")
		return c
	}
	c.Status, c.Detail = "ok", fmt.Sprintf("Docker engine %s reachable (%s containers)", ver, os)
	c.Data = map[string]any{"version": ver, "os_type": os}
	if os != "linux" {
		c.Status = unusable
		c.Detail += "; Linux containers are required"
		c.FixHint = hint("Switch Docker Desktop to Linux containers (WSL2 backend).")
	}
	return c
}

// versionAtLeast compares dotted versions.
func versionAtLeast(v, min string) bool {
	pa, pb := strings.Split(v, "."), strings.Split(min, ".")
	for i := 0; i < len(pb); i++ {
		var a, b int
		if i < len(pa) {
			a, _ = strconv.Atoi(strings.TrimFunc(pa[i], func(r rune) bool { return r < '0' || r > '9' }))
		}
		b, _ = strconv.Atoi(pb[i])
		if a != b {
			return a > b
		}
	}
	return true
}
