//go:build windows

package platform

import "testing"

// TestDefaultSandboxLevel_Windows: L2 is the default and the only level on
// Windows (CLAUDE.md §3–§4).
func TestDefaultSandboxLevel_Windows(t *testing.T) {
	if DefaultSandboxLevel() != "L2" || len(SandboxLevels()) != 1 || SandboxLevels()[0] != "L2" {
		t.Fatalf("level %s %v", DefaultSandboxLevel(), SandboxLevels())
	}
	if e := Endpoint(`C:\x`); e[:len(`\\.\pipe\warden-`)] != `\\.\pipe\warden-` {
		t.Fatalf("pipe name %s", e)
	}
}
