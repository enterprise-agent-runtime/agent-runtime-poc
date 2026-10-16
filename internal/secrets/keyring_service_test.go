package secrets

import (
	"context"
	"testing"

	"warden.dev/warden/internal/platform"
)

// TestSelect_KeychainServiceFollowsHome guards the M2-1 review finding
// (2026-10-16): a wardend started with a temp WARDEN_HOME used the global
// keychain service "warden", so it read the owner's real checkpoint signing
// seed and wrote and deleted doctor/probe in the real Credential Manager.
// The keychain backend chosen for a home must use that home's service name.
// probe=false, so this test never touches any keychain.
func TestSelect_KeychainServiceFollowsHome(t *testing.T) {
	home := t.TempDir()
	k, ok := Select(context.Background(), home, false).(*Keyring)
	if !ok {
		t.Fatal("Select(probe=false) did not return the keychain backend")
	}
	if got, want := k.Service(), platform.KeychainService(home); got != want {
		t.Errorf("keychain service for %s = %q, want %q", home, got, want)
	}
	if k.Service() == "warden" {
		t.Errorf("a temp home shares the real keychain service %q", k.Service())
	}
}
