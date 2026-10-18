package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		v, min string
		ok     bool
	}{
		{"0.9.0", MinBwrap, true}, {"0.6.1", MinBwrap, true}, {"0.6.0", MinBwrap, true},
		{"0.5.9", MinBwrap, false}, {"1.0", MinBwrap, true}, {"0.10.0", "0.9.0", true}, {"", MinBwrap, false},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.v, c.min); got != c.ok {
			t.Errorf("versionAtLeast(%q, %q) = %v", c.v, c.min, got)
		}
	}
}

// TestProbe_ReportsEveryCheckHonestly: whatever the machine has, Probe
// returns the backend and L2 checks with a definite status, and an
// unusable engine (unreachable, or not serving Linux containers) is a
// failure exactly when L2 is the default.
func TestProbe_ReportsEveryCheckHonestly(t *testing.T) {
	for _, level := range []string{"L1", "L2"} {
		checks := Probe(context.Background(), Options{DefaultLevel: level})
		ids := map[string]Check{}
		for _, c := range checks {
			ids[c.ID] = c
			if c.Status != "ok" && c.Status != "warn" && c.Status != "fail" {
				t.Fatalf("%s: status %q", c.ID, c.Status)
			}
			if c.Status != "ok" && c.FixHint == nil && c.ID != "sandbox.backend" {
				t.Errorf("%s fails without a fix hint", c.ID)
			}
		}
		l2, ok := ids["sandbox.l2"]
		if !ok || ids["sandbox.backend"].ID == "" {
			t.Fatalf("checks = %+v", checks)
		}
		if l2.Status != "ok" && (l2.Status == "fail") != (level == "L2") {
			t.Errorf("level %s: engine not usable, status %s", level, l2.Status)
		}
	}
}

// TestL2Check_StatusFollowsBlocking guards the bug CI hit on windows-latest:
// the runner's Docker engine answered with Windows containers, and l2Check
// reported "fail" at L1 although L2 was not blocking, while an unreachable
// engine was correctly downgraded to "warn". Any unusable engine must be
// "fail" exactly when L2 blocks (default_level L2, always so on Windows) and
// "warn" otherwise (CONFLICTS C-02, design A05 §8.1).
func TestL2Check_StatusFollowsBlocking(t *testing.T) {
	engines := []struct {
		name   string
		fetch  func(context.Context) (EngineFacts, error)
		usable bool
		hint   string
		detail string // must appear in Detail: what was seen, so the user knows why
	}{
		{"reachable linux", facts(t, "27.0.1", "linux", nil), true, "", "27.0.1"},
		{"reachable windows", facts(t, "27.0.1", "windows", nil), false, "Switch Docker Desktop to Linux containers", "Linux containers are required"},
		// Only "linux" is usable; an engine that reports no OS type is not
		// (mutation audit of FX-2: os == "windows" in place of os != "linux"
		// survived the two real OS types).
		{"reachable, OS type empty", facts(t, "27.0.1", "", nil), false, "Switch Docker Desktop to Linux containers", "Linux containers are required"},
		{"unreachable", facts(t, "", "", errors.New("connection refused")), false, "Start Docker Desktop", "connection refused"},
	}
	for _, e := range engines {
		for _, level := range []string{"L1", "L2"} {
			t.Run(e.name+"/"+level, func(t *testing.T) {
				c := l2CheckWith(context.Background(), Options{DefaultLevel: level}, "linux", e.fetch)
				if c.ID != "sandbox.l2" {
					t.Fatalf("id %q", c.ID)
				}
				if c.Blocking != (level == "L2") {
					t.Errorf("blocking = %v", c.Blocking)
				}
				want := "ok"
				if !e.usable {
					want = "warn"
					if level == "L2" {
						want = "fail"
					}
				}
				if c.Status != want {
					t.Errorf("status = %s, want %s (detail %q)", c.Status, want, c.Detail)
				}
				// Mutation audit of FX-2: dropping the engine error or the
				// Linux-containers note from Detail survived every other check.
				if !strings.Contains(c.Detail, e.detail) {
					t.Errorf("detail %q lacks %q", c.Detail, e.detail)
				}
				if e.usable {
					if c.FixHint != nil {
						t.Errorf("unexpected hint %q", *c.FixHint)
					}
					if c.Data["os_type"] != "linux" {
						t.Errorf("data = %v", c.Data)
					}
					return
				}
				if c.FixHint == nil || !strings.Contains(*c.FixHint, e.hint) {
					t.Errorf("hint = %v, want containing %q", c.FixHint, e.hint)
				}
			})
		}
	}
}

// TestL2Check_RejectsMalformedEngineFields guards CWE-150 (security review
// of FX-2, task M2-3): whoever answers on the Docker endpoint, a genuine
// engine or a process squatting on the socket or pipe, chooses Version and
// Os. They went into Detail and Data verbatim, so an escape sequence or a
// megabyte of text reached every client. A malformed field now makes the
// engine unusable under the FX-2 rule (fail when L2 is the default, warn
// otherwise), and the raw value is echoed nowhere.
func TestL2Check_RejectsMalformedEngineFields(t *testing.T) {
	long := strings.Repeat("1", 65)
	engines := []struct {
		name, ver, os string
		usable        bool
	}{
		{"version with OSC title sequence", "27\x1b]0;x\x07", "linux", false},
		{"version of 65 characters", long, "linux", false},
		{"version empty", "", "linux", false},
		{"version with a space", "27.0.1 evil", "linux", false},
		{"os capitalised", "27.0.1", "Linux", false},
		{"os empty", "27.0.1", "", false},
		{"os unknown", "27.0.1", "plan9", false},
		{"os with escape", "27.0.1", "linux\x1b[2J", false},
		// Boundaries that must stay usable: 64 characters, distro suffixes.
		{"version of 64 characters", "27.0.1+" + strings.Repeat("a", 57), "linux", true},
		{"version with distro suffix", "27.0.1+dfsg-1ubuntu1", "linux", true},
	}
	for _, e := range engines {
		for _, level := range []string{"L1", "L2"} {
			t.Run(e.name+"/"+level, func(t *testing.T) {
				fetch := facts(t, e.ver, e.os, nil)
				c := l2CheckWith(context.Background(), Options{DefaultLevel: level}, "linux", fetch)
				for _, r := range c.Detail {
					if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
						t.Fatalf("detail %q contains control character %U", c.Detail, r)
					}
				}
				if e.usable {
					if c.Status != "ok" {
						t.Errorf("status = %s, want ok (detail %q)", c.Status, c.Detail)
					}
					return
				}
				want := "warn"
				if level == "L2" {
					want = "fail"
				}
				if c.Status != want || c.Blocking != (level == "L2") {
					t.Errorf("status = %s blocking = %v, want %s (detail %q)", c.Status, c.Blocking, want, c.Detail)
				}
				if c.FixHint == nil {
					t.Error("unusable engine without a fix hint")
				}
				for _, raw := range []string{e.ver, e.os} {
					if raw == "" || raw == "linux" || raw == "27.0.1" {
						continue // well-formed fields may be shown
					}
					// The fixed note "Linux containers are required" is not an
					// echo of os "Linux".
					if strings.Contains(strings.ReplaceAll(c.Detail, "Linux containers are required", ""), raw) {
						t.Errorf("detail %q echoes malformed %q", c.Detail, raw)
					}
					for k, v := range c.Data {
						if v == raw {
							t.Errorf("data[%s] carries malformed %q", k, raw)
						}
					}
				}
			})
		}
	}
}

// facts is a fetcher answering as Docker Desktop with the given version and
// OS type (or failing with err), so the doctor tests above do not depend on
// the host: Desktop is accepted on every host OS.
func facts(t *testing.T, ver, osType string, err error) func(context.Context) (EngineFacts, error) {
	f := loadSample(t, "desktop-windows")
	f.Version, f.ServerVersion, f.Os, f.OSType = ver, ver, osType, osType
	return func(context.Context) (EngineFacts, error) { return f, err }
}

// TestL2Check_SameVerdictAsGate guards CWE-636 from the doctor side: doctor
// reports exactly the gate's verdict and code (one shared function), keeps
// D-026's blocking rule (an unusable engine is fail when L2 is the default,
// warn otherwise), and names the fix for each code. Before M2-17 doctor
// called a rootful native engine, Podman and an engine with the
// CVE-2024-29018 resolver "ok".
func TestL2Check_SameVerdictAsGate(t *testing.T) {
	hints := map[string]string{
		EngineUnreachableCode: "Start Docker Desktop",
		EngineOSCode:          "Switch Docker Desktop to Linux containers",
		EngineRootfulCode:     "rootless Docker or Docker Desktop",
		EngineVersionCode:     "Upgrade Docker",
		EnginePodmanCode:      "rootless Docker or Docker Desktop",
		EngineUnknownCode:     "genuine Docker engine",
	}
	type row struct {
		name, host string
		fetch      func(context.Context) (EngineFacts, error)
	}
	var rows []row
	for _, s := range []string{"rootless-linux", "rootful-linux", "desktop-windows", "desktop-macos", "desktop-windows-containers", "podman-rootless"} {
		f := loadSample(t, s)
		for _, host := range []string{"linux", "windows", "darwin"} {
			rows = append(rows, row{s, host, func(context.Context) (EngineFacts, error) { return f, nil }})
		}
	}
	old := withVersion(loadSample(t, "rootless-linux"), "25.0.4")
	rows = append(rows,
		row{"rootless 25.0.4", "linux", func(context.Context) (EngineFacts, error) { return old, nil }},
		row{"unreachable", "linux", func(context.Context) (EngineFacts, error) { return EngineFacts{}, errors.New("connection refused") }},
		row{"not an engine", "linux", func(context.Context) (EngineFacts, error) {
			return EngineFacts{}, &engineAnswerError{"engine answered HTTP 404 to GET /info"}
		}},
	)
	seen := map[string]bool{}
	for _, r := range rows {
		for _, level := range []string{"L1", "L2"} {
			t.Run(r.name+"/"+r.host+"/"+level, func(t *testing.T) {
				gate := engineVerdict(context.Background(), r.host, r.fetch)
				c := l2CheckWith(context.Background(), Options{DefaultLevel: level}, r.host, r.fetch)
				if c.Blocking != (level == "L2") {
					t.Errorf("blocking = %v", c.Blocking)
				}
				if gate.Code == "" {
					if c.Status != "ok" || c.FixHint != nil || c.Data["kind"] != string(gate.Kind) {
						t.Fatalf("gate accepts %s, doctor says %s (%q, data %v)", gate.Kind, c.Status, c.Detail, c.Data)
					}
					return
				}
				seen[gate.Code] = true
				want := "warn"
				if level == "L2" {
					want = "fail"
				}
				if c.Status != want || c.Data["code"] != gate.Code {
					t.Fatalf("gate refuses %s, doctor says %s with code %v (%q)", gate.Code, c.Status, c.Data["code"], c.Detail)
				}
				if !strings.Contains(c.Detail, gate.Code) {
					t.Errorf("detail %q does not name the code %s", c.Detail, gate.Code)
				}
				if c.FixHint == nil || !strings.Contains(*c.FixHint, hints[gate.Code]) {
					t.Errorf("code %s: hint %v, want containing %q", gate.Code, c.FixHint, hints[gate.Code])
				}
			})
		}
	}
	for code := range hints {
		if !seen[code] {
			t.Errorf("no row produced %s", code)
		}
	}
}
