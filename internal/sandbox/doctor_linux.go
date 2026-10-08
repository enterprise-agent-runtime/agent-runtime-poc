//go:build linux

package sandbox

// sandboxed: runs only the fixed probes "bwrap --version" and an empty
// bwrap sandbox ("/bin/true") for system.doctor; never agent input.
import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// usernsSysctl is one kernel setting reported by sandbox.userns.
type usernsSysctl struct{ path, name string }

// usernsSysctls lists the settings that decide whether bwrap may create
// user namespaces (CLAUDE.md §3), in report order.
func usernsSysctls() []usernsSysctl {
	return []usernsSysctl{
		{"/proc/sys/kernel/unprivileged_userns_clone", "kernel.unprivileged_userns_clone"},
		{"/proc/sys/kernel/apparmor_restrict_unprivileged_userns", "kernel.apparmor_restrict_unprivileged_userns"},
		{"/proc/sys/user/max_user_namespaces", "user.max_user_namespaces"},
	}
}

func readSysctl(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// l1Inputs is what probeOS observed on the machine; l1Checks turns it
// into a verdict without touching the system.
type l1Inputs struct {
	VersionOut   string            // stdout of "bwrap --version"
	VersionErr   error             // error running "bwrap --version"
	SandboxTried bool              // whether the empty sandbox was attempted
	SandboxErr   error             // error starting the empty sandbox
	Sysctls      map[string]string // sysctl name -> value; absent when not present
}

// bwrapUsable parses "bwrap --version" output and says whether the backend
// is present and recent enough to try an empty sandbox.
func bwrapUsable(out string, err error) (string, bool) {
	ver := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "bubblewrap"))
	return ver, err == nil && versionAtLeast(ver, MinBwrap)
}

// probeOS checks bubblewrap and unprivileged user namespaces (CLAUDE.md §3:
// bwrap --version, /proc/sys/kernel/unprivileged_userns_clone where
// present, the Ubuntu 24.04 AppArmor restriction). It only gathers inputs;
// l1Checks decides.
func probeOS(ctx context.Context, o Options) []Check {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	in := l1Inputs{Sysctls: map[string]string{}}
	out, err := exec.CommandContext(ctx, "bwrap", "--version").Output()
	in.VersionOut, in.VersionErr = string(out), err
	for _, s := range usernsSysctls() {
		if v, ok := readSysctl(s.path); ok {
			in.Sysctls[s.name] = v
		}
	}
	if _, ok := bwrapUsable(in.VersionOut, in.VersionErr); ok {
		in.SandboxTried = true
		in.SandboxErr = exec.CommandContext(ctx, "bwrap", "--unshare-all", "--die-with-parent", "--ro-bind", "/", "/", "true").Run()
	}
	return l1Checks(o, in)
}

// l1Checks is the pure L1 verdict (design A05 §8.1 sandbox.backend and
// sandbox.userns): a failure blocks when L1 is the default and is a
// warning when L2 is.
func l1Checks(o Options, in l1Inputs) []Check {
	l1Blocking := o.DefaultLevel != "L2"
	backend := Check{ID: "sandbox.backend", Group: "sandbox", Title: "L1 sandbox backend (bubblewrap)", Blocking: l1Blocking}
	ver, usable := bwrapUsable(in.VersionOut, in.VersionErr)
	switch {
	case in.VersionErr != nil:
		backend.Status, backend.Detail = "fail", "bwrap not found or not runnable: "+in.VersionErr.Error()
		backend.FixHint = hint("sudo apt install bubblewrap (Debian, Ubuntu) or sudo dnf install bubblewrap (Fedora), then run warden doctor again.")
	case !usable:
		backend.Status, backend.Detail = "fail", "bubblewrap "+ver+" is older than "+MinBwrap
		backend.FixHint = hint("Upgrade bubblewrap to " + MinBwrap + " or later.")
	default:
		backend.Status, backend.Detail = "ok", "bubblewrap "+ver
		backend.Data = map[string]any{"version": ver}
	}
	if backend.Status == "fail" && !l1Blocking {
		backend.Status = "warn"
	}

	userns := Check{ID: "sandbox.userns", Group: "sandbox", Title: "Unprivileged user namespaces", Blocking: l1Blocking, Data: map[string]any{}}
	notes := []string{}
	for _, s := range usernsSysctls() {
		if v, ok := in.Sysctls[s.name]; ok {
			userns.Data[s.name] = v
			notes = append(notes, s.name+"="+v)
		} else {
			notes = append(notes, s.name+" not present")
		}
	}
	switch {
	case !usable || !in.SandboxTried:
		userns.Status, userns.Detail = "fail", "not tested: bubblewrap is unavailable; "+strings.Join(notes, ", ")
		userns.FixHint = hint("Install bubblewrap " + MinBwrap + " or later first (see sandbox.backend), then run warden doctor again.")
	case in.SandboxErr != nil:
		userns.Status, userns.Detail = "fail", "an empty bwrap sandbox did not start ("+in.SandboxErr.Error()+"); "+strings.Join(notes, ", ")
		userns.FixHint = hint("On Ubuntu 23.10+ allow user namespaces for bwrap with an AppArmor profile or set kernel.apparmor_restrict_unprivileged_userns=0; on Debian set kernel.unprivileged_userns_clone=1; check user.max_user_namespaces > 0.")
	default:
		userns.Status, userns.Detail = "ok", "an empty bwrap sandbox started; "+strings.Join(notes, ", ")
	}
	if userns.Status == "fail" && !l1Blocking {
		userns.Status = "warn"
	}
	return []Check{backend, userns}
}
