//go:build darwin

package sandbox

import (
	"context"
	"os"
)

// probeOS checks that sandbox-exec (Seatbelt) is present. The full
// Seatbelt probe sandbox lands with the darwin_seatbelt backend (M2/M7).
func probeOS(_ context.Context, o Options) []Check {
	c := Check{ID: "sandbox.backend", Group: "sandbox", Title: "L1 sandbox backend (Seatbelt)", Blocking: o.DefaultLevel != "L2"}
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		c.Status, c.Detail = "fail", "/usr/bin/sandbox-exec not found"
		c.FixHint = hint("sandbox-exec ships with macOS; check that it is not blocked by a management profile.")
		if !c.Blocking {
			c.Status = "warn"
		}
		return []Check{c}
	}
	c.Status, c.Detail = "ok", "/usr/bin/sandbox-exec present"
	return []Check{c}
}
