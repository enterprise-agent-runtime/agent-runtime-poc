package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		v, min string
		ok     bool
	}{
		{"0.9.0", MinBwrap, true}, {"0.6.1", MinBwrap, true}, {"0.6.0", MinBwrap, true},
		{"0.5.9", MinBwrap, false}, {"1.0", MinBwrap, true}, {"0.10.0", "0.9.0", true}, {"", MinBwrap, false},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.v, c.min); got != c.ok {
			t.Errorf("versionAtLeast(%q, %q) = %v", c.v, c.min, got)
		}
	}
}

// TestProbe_ReportsEveryCheckHonestly: whatever the machine has, Probe
// returns the backend and L2 checks with a definite status, and an
// unusable engine (unreachable, or not serving Linux containers) is a
// failure exactly when L2 is the default.
func TestProbe_ReportsEveryCheckHonestly(t *testing.T) {
	for _, level := range []string{"L1", "L2"} {
		checks := Probe(context.Background(), Options{DefaultLevel: level})
		ids := map[string]Check{}
		for _, c := range checks {
			ids[c.ID] = c
			if c.Status != "ok" && c.Status != "warn" && c.Status != "fail" {
				t.Fatalf("%s: status %q", c.ID, c.Status)
			}
			if c.Status != "ok" && c.FixHint == nil && c.ID != "sandbox.backend" {
				t.Errorf("%s fails without a fix hint", c.ID)
			}
		}
		l2, ok := ids["sandbox.l2"]
		if !ok || ids["sandbox.backend"].ID == "" {
			t.Fatalf("checks = %+v", checks)
		}
		if l2.Status != "ok" && (l2.Status == "fail") != (level == "L2") {
			t.Errorf("level %s: engine not usable, status %s", level, l2.Status)
		}
	}
}

// TestL2Check_StatusFollowsBlocking guards the bug CI hit on windows-latest:
// the runner's Docker engine answered with Windows containers, and l2Check
// reported "fail" at L1 although L2 was not blocking, while an unreachable
// engine was correctly downgraded to "warn". Any unusable engine must be
// "fail" exactly when L2 blocks (default_level L2, always so on Windows) and
// "warn" otherwise (CONFLICTS C-02, design A05 §8.1).
func TestL2Check_StatusFollowsBlocking(t *testing.T) {
	engines := []struct {
		name   string
		ping   func(context.Context) (string, string, error)
		usable bool
		hint   string
	}{
		{"reachable linux", func(context.Context) (string, string, error) { return "27.0.1", "linux", nil }, true, ""},
		{"reachable windows", func(context.Context) (string, string, error) { return "27.0.1", "windows", nil }, false, "Switch Docker Desktop to Linux containers"},
		{"unreachable", func(context.Context) (string, string, error) { return "", "", errors.New("connection refused") }, false, "Start Docker Desktop"},
	}
	for _, e := range engines {
		for _, level := range []string{"L1", "L2"} {
			t.Run(e.name+"/"+level, func(t *testing.T) {
				c := l2CheckWith(context.Background(), Options{DefaultLevel: level}, e.ping)
				if c.ID != "sandbox.l2" {
					t.Fatalf("id %q", c.ID)
				}
				if c.Blocking != (level == "L2") {
					t.Errorf("blocking = %v", c.Blocking)
				}
				want := "ok"
				if !e.usable {
					want = "warn"
					if level == "L2" {
						want = "fail"
					}
				}
				if c.Status != want {
					t.Errorf("status = %s, want %s (detail %q)", c.Status, want, c.Detail)
				}
				if e.usable {
					if c.FixHint != nil {
						t.Errorf("unexpected hint %q", *c.FixHint)
					}
					if c.Data["os_type"] != "linux" {
						t.Errorf("data = %v", c.Data)
					}
					return
				}
				if c.FixHint == nil || !strings.Contains(*c.FixHint, e.hint) {
					t.Errorf("hint = %v, want containing %q", c.FixHint, e.hint)
				}
			})
		}
	}
}
