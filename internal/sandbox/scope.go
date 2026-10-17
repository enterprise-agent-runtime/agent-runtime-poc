package sandbox

// The systemd user scope that holds an L1 sandbox when cgroup v2
// delegation is available (design A06 §11.6): its unit name and the
// environment systemd-run is given. Both are decided here, once, for the
// launcher (linux_bwrap.Plan.ScopedCommand), the doctor probe and the
// orphan sweep (A06 §13.5).

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"warden.dev/warden/internal/platform"
)

// scopeEnvKeys are the only daemon variables systemd-run --user receives:
// it needs the user's runtime directory to find the user manager's bus,
// and the bus address when the session sets one.
var scopeEnvKeys = []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}

// ScopeEnv returns the environment for systemd-run --user, read through
// getenv (os.Getenv in production): XDG_RUNTIME_DIR, which must be an
// absolute path, and DBUS_SESSION_BUS_ADDRESS when set. Nothing else of the
// daemon's environment is ever passed. With --scope, systemd-run execs the
// command itself, so this is the environment the launched process starts
// with; the launcher therefore runs bwrap under "env -i" as well (A06
// §11.6, M2-13 "must hold"; INV-C, CWE-214/526). A missing or malformed
// value is ErrUnavailable: no scope, the launch falls back to rlimits.
func ScopeEnv(getenv func(string) string) ([]string, error) {
	var out []string
	for _, k := range scopeEnvKeys {
		v := getenv(k)
		if v == "" {
			if k == "XDG_RUNTIME_DIR" {
				return nil, fmt.Errorf("%w: XDG_RUNTIME_DIR is not set; systemd-run --user cannot reach the user manager", ErrUnavailable)
			}
			continue
		}
		if strings.ContainsAny(v, "\x00\n\r") || len(v) > 4096 {
			return nil, fmt.Errorf("%w: %s is malformed", ErrUnavailable, k)
		}
		if k == "XDG_RUNTIME_DIR" && !path.IsAbs(v) {
			return nil, fmt.Errorf("%w: XDG_RUNTIME_DIR is not an absolute path", ErrUnavailable)
		}
		out = append(out, k+"="+v)
	}
	return out, nil
}

// ScopeUnitPrefix is the unit name prefix of every sandbox scope of one
// Warden home: "warden-" and the D-042 home hash. A06 §11.6 names the unit
// warden-<sb_id>; the hash is added so that the orphan sweep of one home
// never matches the scopes of a second daemon with another WARDEN_HOME
// under the same user manager.
func ScopeUnitPrefix(wardenHome string) string {
	return "warden-" + platform.HomeHash(wardenHome) + "-"
}

// ScopeUnit is the scope unit of one sandbox:
// warden-<home hash>-<sb_id>.scope.
func ScopeUnit(wardenHome, sandboxID string) string {
	return ScopeUnitPrefix(wardenHome) + sandboxID + ".scope"
}

// ScopeSandbox reports whether unit is a sandbox scope of wardenHome and
// returns its sandbox id. Only names ScopeUnit can produce are accepted.
func ScopeSandbox(wardenHome, unit string) (string, bool) {
	rest, ok := strings.CutPrefix(unit, ScopeUnitPrefix(wardenHome))
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, ".scope")
	if !ok || !reSandboxID.MatchString(id) {
		return "", false
	}
	return id, true
}

// reScopeUnit is the shape of any home's sandbox scope unit.
var reScopeUnit = regexp.MustCompile(`^warden-[0-9a-f]{8}-sb_[0-9A-Za-z]{1,64}\.scope$`)

// ValidScopeUnit reports whether unit has the shape ScopeUnit produces.
func ValidScopeUnit(unit string) bool { return reScopeUnit.MatchString(unit) }

// ValidID reports whether id has the sandbox id shape sb_<ulid> that
// Validate requires.
func ValidID(id string) bool { return reSandboxID.MatchString(id) }
