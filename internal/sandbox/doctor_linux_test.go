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
		// Mutation audit of FX-1: the oldest supported version is usable
		// (kills a strict comparison), and userns is never "ok" unless an
		// empty sandbox actually ran on a usable bwrap, whatever the other
		// inputs say (kills dropping either half of !usable || !SandboxTried).
		{"bwrap exactly 0.6.0", l1Inputs{VersionOut: "bubblewrap 0.6.0\n", SandboxTried: true, Sysctls: sysctls}, "ok", "ok"},
		{"sandbox not tried", l1Inputs{VersionOut: "bubblewrap 0.9.0\n", Sysctls: sysctls}, "ok", "fail"},
		{"tried although bwrap missing", l1Inputs{VersionErr: errors.New("exit status 1"), SandboxTried: true, Sysctls: sysctls}, "fail", "fail"},
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

// TestBwrapUsable: probeOS tries the empty sandbox only when this says yes,
// so it decides whether sandbox.userns is tested at all. "bwrap --version"
// can exit non-zero after printing a version (Output returns stdout and an
// ExitError together); that is not a usable backend (mutation audit of
// FX-1: "err == nil &&" could be dropped with every other test green).
func TestBwrapUsable(t *testing.T) {
	cases := []struct {
		out  string
		err  error
		ver  string
		want bool
	}{
		{"bubblewrap 0.9.0\n", nil, "0.9.0", true},
		{"bubblewrap 0.6.0\n", nil, "0.6.0", true},
		{"bubblewrap 0.5.9\n", nil, "0.5.9", false},
		{"  bubblewrap 0.11.0  \n", nil, "0.11.0", true},
		// When bwrap --version fails only usability is checked: no caller
		// reads the version then (review of 2fab6a3), so ver is not pinned.
		{"bubblewrap 0.9.0\n", errors.New("exit status 1"), "", false},
		{"", errors.New("not found"), "", false},
		{"", nil, "", false},
	}
	for _, c := range cases {
		ver, ok := bwrapUsable(c.out, c.err)
		if ok != c.want || (c.err == nil && ver != c.ver) {
			t.Errorf("bwrapUsable(%q, %v) = %q, %v; want %q, %v", c.out, c.err, ver, ok, c.ver, c.want)
		}
	}
}

// TestL1Checks_ReportsWhatItSaw: the checks carry what was observed, so a
// user (and the doctor JSON) can tell why a check failed. Mutation audit of
// FX-1: dropping the version from Data, a sysctl from Data or Detail, or
// the sandbox error from Detail left every other test green.
func TestL1Checks_ReportsWhatItSaw(t *testing.T) {
	sysctls := map[string]string{"user.max_user_namespaces": "63000"}
	checks := l1Checks(Options{DefaultLevel: "L1"}, l1Inputs{VersionOut: "bubblewrap 0.9.0\n", SandboxTried: true, Sysctls: sysctls})
	backend, userns := checks[0], checks[1]
	if backend.ID != "sandbox.backend" || userns.ID != "sandbox.userns" {
		t.Fatalf("order: %s, %s", backend.ID, userns.ID)
	}
	if backend.Data["version"] != "0.9.0" || !strings.Contains(backend.Detail, "0.9.0") {
		t.Errorf("backend data %v detail %q", backend.Data, backend.Detail)
	}
	if userns.Data["user.max_user_namespaces"] != "63000" || len(userns.Data) != 1 {
		t.Errorf("userns data %v", userns.Data)
	}
	for _, want := range []string{"user.max_user_namespaces=63000", "kernel.unprivileged_userns_clone not present", "kernel.apparmor_restrict_unprivileged_userns not present"} {
		if !strings.Contains(userns.Detail, want) {
			t.Errorf("userns detail %q lacks %q", userns.Detail, want)
		}
	}

	checks = l1Checks(Options{DefaultLevel: "L1"}, l1Inputs{VersionOut: "bubblewrap 0.9.0\n", SandboxTried: true, SandboxErr: errors.New("setting up uid map: Permission denied"), Sysctls: sysctls})
	if d := checks[1].Detail; !strings.Contains(d, "Permission denied") || !strings.Contains(d, "user.max_user_namespaces=63000") {
		t.Errorf("userns detail %q lacks the sandbox error or the sysctls", d)
	}
	checks = l1Checks(Options{DefaultLevel: "L1"}, l1Inputs{VersionOut: "bubblewrap 0.5.0\n", Sysctls: sysctls})
	if d := checks[0].Detail; !strings.Contains(d, "0.5.0") || !strings.Contains(d, MinBwrap) {
		t.Errorf("backend detail %q lacks the found and the required version", d)
	}
}
