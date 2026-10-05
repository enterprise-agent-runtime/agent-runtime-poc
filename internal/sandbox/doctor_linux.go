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

func readSysctl(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// probeOS checks bubblewrap and unprivileged user namespaces (CLAUDE.md §3:
// bwrap --version, /proc/sys/kernel/unprivileged_userns_clone where
// present, the Ubuntu 24.04 AppArmor restriction).
func probeOS(ctx context.Context, o Options) []Check {
	l1Blocking := o.DefaultLevel != "L2"
	backend := Check{ID: "sandbox.backend", Group: "sandbox", Title: "L1 sandbox backend (bubblewrap)", Blocking: l1Blocking}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bwrap", "--version").Output()
	ver := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "bubblewrap"))
	switch {
	case err != nil:
		backend.Status, backend.Detail = "fail", "bwrap not found or not runnable: "+err.Error()
		backend.FixHint = hint("sudo apt install bubblewrap (Debian, Ubuntu) or sudo dnf install bubblewrap (Fedora), then run warden doctor again.")
	case !versionAtLeast(ver, MinBwrap):
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
	for _, s := range []struct{ path, name string }{
		{"/proc/sys/kernel/unprivileged_userns_clone", "kernel.unprivileged_userns_clone"},
		{"/proc/sys/kernel/apparmor_restrict_unprivileged_userns", "kernel.apparmor_restrict_unprivileged_userns"},
		{"/proc/sys/user/max_user_namespaces", "user.max_user_namespaces"},
	} {
		if v, ok := readSysctl(s.path); ok {
			userns.Data[s.name] = v
			notes = append(notes, s.name+"="+v)
		} else {
			notes = append(notes, s.name+" not present")
		}
	}
	if backend.Status != "ok" {
		userns.Status, userns.Detail = "fail", "not tested: bubblewrap is unavailable; "+strings.Join(notes, ", ")
	} else if err := exec.CommandContext(ctx, "bwrap", "--unshare-all", "--die-with-parent", "--ro-bind", "/", "/", "true").Run(); err != nil {
		userns.Status, userns.Detail = "fail", "an empty bwrap sandbox did not start ("+err.Error()+"); "+strings.Join(notes, ", ")
		userns.FixHint = hint("On Ubuntu 23.10+ allow user namespaces for bwrap with an AppArmor profile or set kernel.apparmor_restrict_unprivileged_userns=0; on Debian set kernel.unprivileged_userns_clone=1; check user.max_user_namespaces > 0.")
	} else {
		userns.Status, userns.Detail = "ok", "an empty bwrap sandbox started; "+strings.Join(notes, ", ")
	}
	if userns.Status == "fail" && !l1Blocking {
		userns.Status = "warn"
	}
	return []Check{backend, userns}
}
