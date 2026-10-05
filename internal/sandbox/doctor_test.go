package sandbox

import (
	"context"
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
// unreachable engine is a failure exactly when L2 is the default.
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
			t.Errorf("level %s: unreachable engine status %s", level, l2.Status)
		}
	}
}
