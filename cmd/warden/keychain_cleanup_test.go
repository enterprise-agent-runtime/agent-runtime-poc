package main

import (
	"fmt"
	"testing"

	"warden.dev/warden/internal/platform"
)

// testKeychainService is the keychain service a test home's daemon writes
// to. It refuses the real namespace: a test that would clean up "warden"
// would delete the user's own secrets (M2-1 review, 2026-10-16).
func testKeychainService(home string) (string, error) {
	svc := platform.KeychainService(home)
	if svc == "warden" {
		return "", fmt.Errorf("home %s maps to the real keychain service %q; refusing to touch it", home, svc)
	}
	return svc, nil
}

// TestKeychainCleanupService_RefusesRealNamespace guards the cleanup of the
// L3 stop test: deleting keychain items is only safe in a test home's own
// namespace, so the default home (service "warden") must be refused, and a
// temp home must map to a different service. This test touches no keychain.
func TestKeychainCleanupService_RefusesRealNamespace(t *testing.T) {
	def, err := platform.DefaultHome()
	if err != nil {
		t.Skipf("no default home on this machine: %v", err)
	}
	if svc, err := testKeychainService(def); err == nil {
		t.Errorf("testKeychainService(default home) = %q, nil; want a refusal", svc)
	}
	tmp := t.TempDir()
	svc, err := testKeychainService(tmp)
	if err != nil || svc == "warden" {
		t.Errorf("testKeychainService(%s) = %q, %v; want its own namespace", tmp, svc, err)
	}
}
