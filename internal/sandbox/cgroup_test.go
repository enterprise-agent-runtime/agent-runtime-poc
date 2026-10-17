package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func usableCgroup() CgroupStatus {
	return CgroupStatus{
		SystemdRun: "/usr/bin/systemd-run", Env: []string{"XDG_RUNTIME_DIR=/run/user/1000"}, ScopeTried: true,
		Unified: true, ServiceDir: "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service",
		Controllers: []string{"cpuset", "cpu", "io", "memory", "pids"},
	}
}

// TestCgroupCheck is the sandbox.cgroup doctor verdict (A06 §11.6): ok
// only when a user scope starts and the user manager's cgroup delegates
// memory, pids and cpu on the unified (v2) hierarchy; every other case is
// a warning, never a failure and never blocking, because the launch falls
// back to the rlimits of A06 §7.3 and records cgroup false. Each warning
// names what is missing and how to fix it.
func TestCgroupCheck(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(*CgroupStatus)
		status string
		usable bool
		detail string // substring of Detail
		hint   string // substring of FixHint
	}{
		{"usable", func(*CgroupStatus) {}, "ok", true, "memory, pids and cpu", ""},
		{"no systemd-run", func(c *CgroupStatus) { c.SystemdRun, c.ScopeTried = "", false }, "warn", false, "systemd-run not found", "systemd"},
		{"no bus", func(c *CgroupStatus) {
			c.Env, c.EnvErr, c.ScopeTried = nil, errors.New("XDG_RUNTIME_DIR is not set"), false
		}, "warn", false, "XDG_RUNTIME_DIR", "loginctl enable-linger"},
		{"scope fails", func(c *CgroupStatus) { c.ScopeErr = errors.New("exit status 1") }, "warn", false, "did not start", "systemctl --user"},
		{"hybrid hierarchy", func(c *CgroupStatus) { c.Unified = false }, "warn", false, "unified", "cgroup_no_v1=all"},
		{"no controllers file", func(c *CgroupStatus) { c.Controllers, c.ControllersErr = nil, errors.New("no such file") }, "warn", false, "not readable", "Delegate="},
		{"memory not delegated", func(c *CgroupStatus) { c.Controllers = []string{"cpu", "pids"} }, "warn", false, "memory", "Delegate="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := usableCgroup()
			tc.mod(&s)
			if s.Usable() != tc.usable {
				t.Errorf("Usable() = %v, want %v", s.Usable(), tc.usable)
			}
			c := CgroupCheck(s)
			if c.ID != "sandbox.cgroup" || c.Group != "sandbox" || c.Blocking {
				t.Errorf("check = %+v, want sandbox.cgroup in group sandbox, never blocking", c)
			}
			if c.Status != tc.status {
				t.Errorf("Status = %q, want %q (%s)", c.Status, tc.status, c.Detail)
			}
			if !strings.Contains(c.Detail, tc.detail) {
				t.Errorf("Detail = %q, want it to mention %q", c.Detail, tc.detail)
			}
			if tc.hint == "" {
				if c.FixHint != nil {
					t.Errorf("FixHint = %q on ok", *c.FixHint)
				}
			} else if c.FixHint == nil || !strings.Contains(*c.FixHint, tc.hint) {
				t.Errorf("FixHint = %v, want it to mention %q", c.FixHint, tc.hint)
			}
			if c.Data["cgroup"] != tc.usable {
				t.Errorf("Data[cgroup] = %v, want %v", c.Data["cgroup"], tc.usable)
			}
			if len([]rune(c.Detail)) > maxDoctorText {
				t.Errorf("Detail is %d characters, over the doctorCheck limit", len([]rune(c.Detail)))
			}
		})
	}
}

// TestCgroupCheck_CapsForeignText: the scope error carries text systemd-run
// printed; it is capped to the doctorCheck limit here and sanitized at the
// CLI (D-045).
func TestCgroupCheck_CapsForeignText(t *testing.T) {
	s := usableCgroup()
	s.ScopeErr = errors.New(strings.Repeat("x", 5000))
	if c := CgroupCheck(s); len([]rune(c.Detail)) > maxDoctorText {
		t.Errorf("Detail is %d characters", len([]rune(c.Detail)))
	}
}

// TestParseControllers reads cgroup.controllers (one line, space
// separated) and refuses anything else.
func TestParseControllers(t *testing.T) {
	got, err := ParseControllers("cpuset cpu io memory pids\n")
	if err != nil || strings.Join(got, ",") != "cpuset,cpu,io,memory,pids" {
		t.Errorf("ParseControllers = %q, %v", got, err)
	}
	if got, err := ParseControllers(""); err != nil || len(got) != 0 {
		t.Errorf("empty file: %q, %v; want no controllers", got, err)
	}
	for _, bad := range []string{"cpu\nmemory\n", "cpu \x1b[2J", strings.Repeat("a", 5000)} {
		if _, err := ParseControllers(bad); err == nil {
			t.Errorf("ParseControllers(%q) accepted", bad)
		}
	}
}
