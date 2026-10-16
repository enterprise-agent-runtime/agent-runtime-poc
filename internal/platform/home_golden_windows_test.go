//go:build windows

package platform

import "testing"

// goldenSuffix is "-" + the first 8 hex of sha256(`\tmp\warden-golden`).
const goldenSuffix = "-e2fe27a6"

// TestHomeSuffix_DefaultHomeCaseAndSlashInsensitiveOnWindows: Windows paths
// are case-insensitive, so the default home spelled with another case and a
// trailing separator is the same home and must get the plain pipe and the
// plain "warden" keychain service, not a namespace of its own.
func TestHomeSuffix_DefaultHomeCaseAndSlashInsensitiveOnWindows(t *testing.T) {
	def := `C:\Users\X\AppData\Local\Warden`
	home := `C:\Users\X\AppData\Local\WARDEN\`
	if got := homeSuffix(home, def); got != "" {
		t.Errorf("homeSuffix(%q, %q) = %q, want \"\" (same home)", home, def, got)
	}
	if got := keychainService(home, def); got != "warden" {
		t.Errorf("keychainService(%q, %q) = %q, want \"warden\"", home, def, got)
	}
}
