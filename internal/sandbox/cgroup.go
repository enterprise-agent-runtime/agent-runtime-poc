package sandbox

// The cgroup prerequisite of design A06 §11.6 as a pure verdict: whether a
// systemd user scope can hold an L1 sandbox with memory, pids and cpu
// limits. The Linux probe (cgroup_linux.go) gathers a CgroupStatus; the
// doctor check sandbox.cgroup and the L1 launcher both decide from it, so
// doctor reports exactly what the launch will do.

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// CgroupControllers are the controllers the user manager must delegate
// for the A06 §7.3 cgroup column (memory.max, pids.max, cpu.max).
var CgroupControllers = []string{"memory", "pids", "cpu"}

// CgroupStatus is what the cgroup probe observed.
type CgroupStatus struct {
	SystemdRun     string   // absolute path of systemd-run; "" when none was found
	Env            []string // ScopeEnv: the only variables systemd-run receives
	EnvErr         error    // why ScopeEnv failed
	ScopeTried     bool     // whether a probe scope was started
	ScopeErr       error    // nil when "systemd-run --user --scope /bin/true" succeeded
	Unified        bool     // cgroup v2 is the only hierarchy (/sys/fs/cgroup/cgroup.controllers exists)
	ServiceDir     string   // the user manager's cgroup directory, user@<uid>.service
	Controllers    []string // its cgroup.controllers
	ControllersErr error    // why they could not be read
}

// Missing lists the CgroupControllers the user manager does not have.
func (c CgroupStatus) Missing() []string {
	var out []string
	for _, k := range CgroupControllers {
		if !slices.Contains(c.Controllers, k) {
			out = append(out, k)
		}
	}
	return out
}

// Usable reports whether the launch can run in a systemd user scope that
// enforces the A06 §7.3 cgroup limits. When false the launch falls back to
// rlimits and sandbox.create.limits.cgroup is false (A06 §11.6).
func (c CgroupStatus) Usable() bool {
	return c.SystemdRun != "" && c.EnvErr == nil && len(c.Env) > 0 && c.ScopeTried && c.ScopeErr == nil &&
		c.Unified && c.ControllersErr == nil && len(c.Missing()) == 0
}

const cgroupFallback = "; L1 sandboxes fall back to per-process rlimits (A06 §7.3) and sandbox.create records cgroup false"

// CgroupCheck is the sandbox.cgroup doctor check (design A05 §8.1 group
// sandbox, A06 §11.6): ok when the cgroup limits will be enforced, a
// warning otherwise. Never blocking and never a failure: the rlimit
// fallback still confines the sandbox. Detail and FixHint are capped here
// and sanitized at the CLI (D-045).
func CgroupCheck(c CgroupStatus) Check {
	ch := Check{ID: "sandbox.cgroup", Group: "sandbox", Title: "cgroup limits for L1 sandboxes (systemd user scope, cgroup v2)",
		Data: map[string]any{
			"cgroup": c.Usable(), "systemd_run": c.SystemdRun != "", "scope": c.ScopeTried && c.ScopeErr == nil,
			"unified": c.Unified, "controllers": append([]string{}, c.Controllers...),
		}}
	warn := func(detail, fix string) Check {
		ch.Status = "warn"
		ch.Detail = capRunes(detail+cgroupFallback, maxDoctorText)
		ch.FixHint = hint(capRunes(fix, maxDoctorText))
		return ch
	}
	switch {
	case c.SystemdRun == "":
		return warn("systemd-run not found", "Install systemd (the systemd-run command) and run the session under a systemd user manager, then run warden doctor again.")
	case c.EnvErr != nil:
		return warn("the systemd user manager is not reachable: "+c.EnvErr.Error(),
			"Log in through a systemd session so XDG_RUNTIME_DIR is set, or run `loginctl enable-linger` for this user, then restart wardend.")
	case !c.ScopeTried || c.ScopeErr != nil:
		why := "not tried"
		if c.ScopeErr != nil {
			why = c.ScopeErr.Error()
		}
		return warn("a systemd user scope did not start ("+why+")",
			"Check that `systemctl --user status` works for this user (WSL: set systemd=true under [boot] in /etc/wsl.conf), then run warden doctor again.")
	case !c.Unified:
		return warn("cgroup v2 is not the unified hierarchy (hybrid or v1 mount)",
			"Boot with the unified cgroup v2 hierarchy (systemd.unified_cgroup_hierarchy=1; on WSL add kernelCommandLine = cgroup_no_v1=all under [wsl2] in .wslconfig and restart WSL).")
	case c.ControllersErr != nil:
		return warn("the user manager's cgroup.controllers is not readable: "+c.ControllersErr.Error(),
			"Delegate controllers to the user manager: `sudo systemctl edit user@.service` with [Service] Delegate=memory pids cpu, then log in again.")
	case len(c.Missing()) > 0:
		return warn("the user manager does not delegate "+strings.Join(c.Missing(), ", ")+" (has: "+strings.Join(c.Controllers, " ")+")",
			"Delegate controllers to the user manager: `sudo systemctl edit user@.service` with [Service] Delegate=memory pids cpu, then log in again.")
	}
	ch.Status = "ok"
	ch.Detail = "systemd user scopes start and the user manager delegates memory, pids and cpu on cgroup v2"
	return ch
}

// maxControllersLen bounds a cgroup.controllers read: the kernel has a
// few dozen controllers, each a short word.
const maxControllersLen = 1024

// ParseControllers reads a cgroup.controllers file: one line of
// space-separated lower-case controller names.
func ParseControllers(s string) ([]string, error) {
	if len(s) > maxControllersLen {
		return nil, fmt.Errorf("cgroup.controllers is %d bytes", len(s))
	}
	s = strings.TrimSuffix(s, "\n")
	if strings.Contains(s, "\n") {
		return nil, fmt.Errorf("cgroup.controllers has more than one line")
	}
	out := []string{}
	for _, f := range strings.Fields(s) {
		if strings.Trim(f, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
			return nil, fmt.Errorf("cgroup.controllers names an unexpected controller")
		}
		out = append(out, f)
	}
	return out, nil
}

// UserServiceCgroup is the cgroup directory of uid's systemd user manager
// under the cgroup v2 root (A06 §11.6): every scope it creates lies below
// it, which bounds the orphan sweep (A06 §13.5) to this user's scopes.
func UserServiceCgroup(root string, uid int) string {
	return filepath.Join(root, "user.slice", fmt.Sprintf("user-%d.slice", uid), fmt.Sprintf("user@%d.service", uid))
}
