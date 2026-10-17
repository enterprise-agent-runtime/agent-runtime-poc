//go:build windows

package sandbox

import (
	"context"

	"warden.dev/warden/internal/platform"
)

// probeOS on Windows: there is no native sandbox (out of scope, CLAUDE.md
// §1); L2 through Docker Desktop is the only level, checked by l2Check.
// The 8.3 short-name check exists only here: no other host OS creates
// them (platform.HostFS answers false there).
func probeOS(_ context.Context, o Options) []Check {
	return []Check{
		{ID: "sandbox.backend", Group: "sandbox", Status: "ok", Blocking: false,
			Title:  "Sandbox backend (Windows uses L2 only)",
			Detail: "no native Windows sandbox; every task runs in an L2 container (see sandbox.l2)"},
		shortNamesCheck(o.Home, platform.NewHostFS()),
	}
}
