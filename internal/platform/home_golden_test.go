package platform

import (
	"path/filepath"
	"testing"
)

// TestHomeSuffix_Golden pins the suffix bytes (M2-1 re-review, 2026-10-16).
// The other home tests compare keychainService against homeSuffix, which is
// circular: a refactor of homeKey (dropping the Windows ToLower, hashing
// before Clean) would pass them and silently rename every non-default named
// pipe and keychain namespace, orphaning the secrets stored under the old
// name. The expected value is a literal computed once, independently, with
// sha256sum over the normalized key (goldenSuffix, per OS: Windows hashes
// `\tmp\warden-golden`, others `/tmp/Warden-Golden`).
func TestHomeSuffix_Golden(t *testing.T) {
	// Mixed case, a "." segment and a trailing separator: only a key that is
	// cleaned (and lower-cased on Windows) before hashing gives the golden.
	home := filepath.FromSlash("/tmp/./Warden-Golden/")
	def := filepath.FromSlash("/home/me/.warden")
	if got := homeSuffix(home, def); got != goldenSuffix {
		t.Errorf("homeSuffix(%q) = %q, want %q (changing it renames every test pipe and keychain namespace)", home, got, goldenSuffix)
	}
	if got, want := keychainService(home, def), "warden"+goldenSuffix; got != want {
		t.Errorf("keychainService(%q) = %q, want %q", home, got, want)
	}
}
