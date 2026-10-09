//go:build windows

package sandbox

import "context"

// probeOS on Windows: there is no native sandbox (out of scope, CLAUDE.md
// §1); L2 through Docker Desktop is the only level, checked by l2Check.
func probeOS(_ context.Context, _ Options) []Check {
	return []Check{{ID: "sandbox.backend", Group: "sandbox", Status: "ok", Blocking: false,
		Title:  "Sandbox backend (Windows uses L2 only)",
		Detail: "no native Windows sandbox; every task runs in an L2 container (see sandbox.l2)"}}
}
