package platform

import (
	"path/filepath"
	"strings"
	"testing"
)

// The keychain service name follows the home, like the named pipe does.
// Why these tests exist (M2-1 review, 2026-10-16): the L3 stop test ran
// wardend with a temp WARDEN_HOME, but the keychain service was the global
// "warden", so the test daemon read the owner's real checkpoint signing seed
// and wrote and deleted doctor/probe in the real Credential Manager.
// CLAUDE.md §8.4: tests never touch the real Warden home, and the keychain
// items are part of it.

var (
	testDefaultHome = filepath.FromSlash("/home/me/.warden")
	testOtherHome   = filepath.FromSlash("/tmp/warden-test-1")
)

func TestKeychainService_DefaultHomeIsWarden(t *testing.T) {
	for _, home := range []string{testDefaultHome, testDefaultHome + string(filepath.Separator)} {
		if got := keychainService(home, testDefaultHome); got != "warden" {
			t.Errorf("keychainService(%q) = %q, want \"warden\" for the default home", home, got)
		}
	}
}

func TestKeychainService_OtherHomeIsSuffixed(t *testing.T) {
	got := keychainService(testOtherHome, testDefaultHome)
	want := "warden" + homeSuffix(testOtherHome, testDefaultHome)
	if got != want || !strings.HasPrefix(got, "warden-") || len(got) != len("warden-")+8 {
		t.Errorf("keychainService(%q) = %q, want %q (warden-<8 hex>, the pipe's suffix)", testOtherHome, got, want)
	}
}

func TestKeychainService_TwoHomesTwoNames(t *testing.T) {
	a := keychainService(testOtherHome, testDefaultHome)
	b := keychainService(filepath.FromSlash("/tmp/warden-test-2"), testDefaultHome)
	if a == b {
		t.Errorf("two different homes share the keychain service %q", a)
	}
}
