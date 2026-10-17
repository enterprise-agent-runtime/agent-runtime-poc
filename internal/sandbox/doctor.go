// Package sandbox will build and launch the per-task sandboxes of WRD-10 §5
// and WRD-16 §10 (L1 bubblewrap/Seatbelt, L2 OCI; M2). In M1 it provides
// the sandbox prerequisite probes reported by system.doctor (A05 §8.1
// checks sandbox.*), which execute bwrap and talk to the Docker engine and
// therefore must live in a package allowed to start processes (INV-H).
package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"warden.dev/warden/internal/platform"
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
	Home         string // the effective Warden home (platform.Layout.Home)
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
	// Version and Os are chosen by whatever answers on the Docker endpoint,
	// a genuine engine or a process squatting on it. Neither is shown or
	// stored unless it has the shape a Docker engine reports (CWE-150, M2-3).
	if !engineVersion.MatchString(ver) {
		c.Status = unusable
		c.Detail = "Docker engine answered with a malformed version (not shown); the endpoint may not be a Docker engine"
		c.FixHint = hint("Check that the Docker socket or pipe (and DOCKER_HOST, if set) belongs to a genuine Docker engine, then run warden doctor again.")
		return c
	}
	if os != "linux" && os != "windows" {
		c.Status = unusable
		c.Detail = fmt.Sprintf("Docker engine %s reachable, OS type unrecognised (not shown); Linux containers are required", ver)
		c.Data = map[string]any{"version": ver}
		c.FixHint = hint("Switch Docker Desktop to Linux containers (WSL2 backend), and check that the Docker endpoint belongs to a genuine Docker engine.")
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

// engineVersion is the shape of a Docker engine version ("27.0.1",
// "27.0.1+dfsg-1ubuntu1", "28.0.0-rc.1"): nothing a terminal interprets, and
// short enough to print on one line.
var engineVersion = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,64}$`)

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

// ShortNamesFix is the remedy for a Warden home whose volume creates NTFS
// 8.3 names. The doctor check sandbox.short_names and the L2 spec
// builder's short_names_enabled refusal (internal/sandbox/oci) give the
// same text. The order follows the owner's decision of 2026-10-16: move
// the home first (on the owner's machine to D:), change the volume second.
const ShortNamesFix = "Put WARDEN_HOME on a volume without 8.3 name creation " +
	"(the sandbox.short_names check of warden doctor reports it per volume; `fsutil 8dot3name query <drive>:` needs elevation), " +
	"or have an administrator disable it for this volume with `fsutil 8dot3name set <drive>: 1`, " +
	"which affects new names only, then remove existing ones with `fsutil 8dot3name strip`."

// shortNamesCheck reports whether the volume holding the Warden home's
// sessions directory (where every worktree lives; the home itself until
// sessions/ exists) creates 8.3 short names. With creation on, a sandboxed
// process can open a masked .env as ENV~1, or rename a directory to give
// it a new alias, so the L2 spec builder refuses such a worktree
// (sandbox_unavailable, short_names_enabled). Doctor warns rather than
// fails: nothing is unsafe until an L2 sandbox is built, and the builder
// refuses then. The query needs no elevation (platform.HostFS).
//
// Whether the case-sensitivity flag can be set is not reported: HostFS
// reads the flag but cannot tell "unsupported" from "off" without trying
// to set it, and setting depends on the directory's ACL (full control),
// which only the attempt reveals.
func shortNamesCheck(home string, fs platform.HostFS) Check {
	c := shortNamesVerdict(home, fs)
	c.Detail = capRunes(c.Detail, maxDoctorText)
	return c
}

// maxDoctorText is the doctorCheck detail and fix_hint limit of
// api/types.json.
const maxDoctorText = 1000

// capRunes cuts s to at most n characters, ending in "..." when cut.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

func shortNamesVerdict(home string, fs platform.HostFS) Check {
	c := Check{ID: "sandbox.short_names", Group: "sandbox", Title: "NTFS 8.3 short names on the Warden home volume"}
	unknown := func(why string) Check {
		c.Status = "warn"
		c.Detail = "whether the volume of the Warden home creates 8.3 short names could not be determined: " + why +
			"; L2 sandboxes are refused until it can be"
		c.FixHint = hint("Check that the Warden home exists and is readable, then run warden doctor again. " + ShortNamesFix)
		return c
	}
	if home == "" {
		return unknown("the Warden home is not known")
	}
	dir := home
	if st, err := os.Stat(filepath.Join(home, "sessions")); err == nil && st.IsDir() {
		dir = filepath.Join(home, "sessions")
	}
	on, err := fs.ShortNameCreation(dir)
	if err != nil {
		return unknown(err.Error())
	}
	c.Data = map[string]any{"short_names": on, "path": dir}
	if on {
		c.Status = "warn"
		c.Detail = "the volume of " + dir + " creates 8.3 short names (ENV~1 for .env), which would bypass the worktree masks; L2 sandboxes are refused there"
		c.FixHint = hint(ShortNamesFix)
		return c
	}
	c.Status = "ok"
	c.Detail = "the volume of " + dir + " does not create 8.3 short names"
	return c
}
