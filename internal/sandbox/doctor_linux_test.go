//go:build linux

package sandbox

import (
	"errors"
	"strings"
	"testing"
)

// TestL1Checks_EveryNonOkCheckHasHint guards a CI failure: on runners
// without bubblewrap, sandbox.userns reported "not tested" with no fix
// hint, so a user was told something was wrong and not what to do
// (TestProbe_ReportsEveryCheckHonestly, doctor_test.go). Every non-ok L1
// check must name its fix, under both default levels.
func TestL1Checks_EveryNonOkCheckHasHint(t *testing.T) {
	sysctls := map[string]string{"user.max_user_namespaces": "63000"}
	cases := []struct {
		name    string
		in      l1Inputs
		backend string // expected status when L1 is the default; "ok" stays ok under L2
		userns  string
	}{
		{"bwrap missing", l1Inputs{VersionErr: errors.New(`exec: "bwrap": executable file not found in $PATH`), Sysctls: sysctls}, "fail", "fail"},
		{"bwrap older than 0.6.0", l1Inputs{VersionOut: "bubblewrap 0.5.0\n", Sysctls: sysctls}, "fail", "fail"},
		{"empty sandbox fails", l1Inputs{VersionOut: "bubblewrap 0.9.0\n", SandboxTried: true, SandboxErr: errors.New("exit status 1"), Sysctls: sysctls}, "ok", "fail"},
		{"all ok", l1Inputs{VersionOut: "bubblewrap 0.9.0\n", SandboxTried: true, Sysctls: sysctls}, "ok", "ok"},
	}
	for _, c := range cases {
		for _, level := range []string{"L1", "L2"} {
			checks := l1Checks(Options{DefaultLevel: level}, c.in)
			want := map[string]string{"sandbox.backend": c.backend, "sandbox.userns": c.userns}
			if len(checks) != 2 {
				t.Fatalf("%s/%s: %d checks", c.name, level, len(checks))
			}
			for _, ch := range checks {
				w := want[ch.ID]
				if w == "fail" && level == "L2" {
					w = "warn"
				}
				if ch.Status != w {
					t.Errorf("%s/%s: %s status %q, want %q", c.name, level, ch.ID, ch.Status, w)
				}
				if ch.Status != "ok" && ch.FixHint == nil {
					t.Errorf("%s/%s: %s is %s without a fix hint", c.name, level, ch.ID, ch.Status)
				}
				if ch.Blocking != (level == "L1") {
					t.Errorf("%s/%s: %s blocking = %v", c.name, level, ch.ID, ch.Blocking)
				}
				if c.name == "bwrap missing" && ch.ID == "sandbox.userns" {
					if !strings.Contains(ch.Detail, "not tested") {
						t.Errorf("%s/%s: userns detail %q does not say not tested", c.name, level, ch.Detail)
					}
					if ch.FixHint == nil || !strings.Contains(strings.ToLower(*ch.FixHint), "install bubblewrap") {
						t.Errorf("%s/%s: userns hint does not point to installing bubblewrap", c.name, level)
					}
				}
			}
		}
	}
}
