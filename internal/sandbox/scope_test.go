package sandbox

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"warden.dev/warden/internal/platform"
)

// TestScopeEnv_OnlyTheBusVariables guards the M2-13 "must hold" for the
// cgroup wrapper (A06 §11.6; INV-C, CWE-214/526): with --scope,
// systemd-run execs the command itself, so whatever environment it is
// given is the environment of the process that becomes bwrap. systemd-run
// needs exactly XDG_RUNTIME_DIR (and DBUS_SESSION_BUS_ADDRESS when set) to
// reach the user manager; nothing else of the daemon's environment may be
// passed, an exported API key least of all.
func TestScopeEnv_OnlyTheBusVariables(t *testing.T) {
	env := map[string]string{
		"XDG_RUNTIME_DIR":          "/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus",
		"FAKE_DAEMON_SECRET":       "fake-not-a-secret",
		"HOME":                     "/home/ana",
		"PATH":                     "/usr/bin",
	}
	got, err := ScopeEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"}
	if !slices.Equal(got, want) {
		t.Errorf("ScopeEnv = %q, want %q", got, want)
	}

	delete(env, "DBUS_SESSION_BUS_ADDRESS")
	if got, err := ScopeEnv(func(k string) string { return env[k] }); err != nil || !slices.Equal(got, want[:1]) {
		t.Errorf("without a bus address ScopeEnv = %q, %v; want only XDG_RUNTIME_DIR", got, err)
	}
	for name, bad := range map[string]map[string]string{
		"no runtime dir":       {},
		"relative runtime dir": {"XDG_RUNTIME_DIR": "run/user/1000"},
		"newline":              {"XDG_RUNTIME_DIR": "/run/user/1000\nFAKE=1"},
		"nul in bus":           {"XDG_RUNTIME_DIR": "/run/user/1000", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/x\x00y"},
		// Only a socket path (round 1, item 9): no TCP bus, no abstract
		// socket, no relative path, no extra keys, no second address.
		"tcp bus":         {"XDG_RUNTIME_DIR": "/run/user/1000", "DBUS_SESSION_BUS_ADDRESS": "tcp:host=10.0.0.1,port=1234"},
		"abstract bus":    {"XDG_RUNTIME_DIR": "/run/user/1000", "DBUS_SESSION_BUS_ADDRESS": "unix:abstract=/tmp/dbus-x"},
		"relative bus":    {"XDG_RUNTIME_DIR": "/run/user/1000", "DBUS_SESSION_BUS_ADDRESS": "unix:path=run/bus"},
		"bus with keys":   {"XDG_RUNTIME_DIR": "/run/user/1000", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus,guid=abc"},
		"two bus entries": {"XDG_RUNTIME_DIR": "/run/user/1000", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus;unix:path=/x"},
	} {
		if got, err := ScopeEnv(func(k string) string { return bad[k] }); err == nil || !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: ScopeEnv = %q, %v; want sandbox_unavailable", name, got, err)
		}
	}
}

// TestScopeUnit_NamesTheHome: the scope unit carries the D-042 home hash,
// so the orphan sweep of one Warden home (A06 §13.5) recognises only its
// own scopes, never those of a second daemon with another WARDEN_HOME
// under the same user manager.
func TestScopeUnit_NamesTheHome(t *testing.T) {
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	ua := ScopeUnit(a, testID)
	if ua != "warden-"+platform.HomeHash(a)+"-"+testID+".scope" {
		t.Errorf("ScopeUnit = %q", ua)
	}
	if ua == ScopeUnit(b, testID) {
		t.Error("two homes give the same scope unit")
	}
	if !strings.HasPrefix(ua, ScopeUnitPrefix(a)) || strings.HasPrefix(ua, ScopeUnitPrefix(b)) {
		t.Errorf("prefixes %q / %q do not separate %q", ScopeUnitPrefix(a), ScopeUnitPrefix(b), ua)
	}
	if id, ok := ScopeSandbox(a, ua); !ok || id != testID {
		t.Errorf("ScopeSandbox(a, %q) = %q, %v", ua, id, ok)
	}
	for _, name := range []string{
		ScopeUnit(b, testID),
		ScopeUnitPrefix(a) + "sb_x.service",
		ScopeUnitPrefix(a) + ".scope",
		ScopeUnitPrefix(a) + "sb_../x.scope",
		ScopeUnitPrefix(a) + "nope.scope",
		"user@1000.service",
	} {
		if _, ok := ScopeSandbox(a, name); ok {
			t.Errorf("ScopeSandbox(a, %q) accepted a unit that is not home a's sandbox scope", name)
		}
	}
}
