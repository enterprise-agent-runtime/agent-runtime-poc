package denylist

import "testing"

// TestMatchHost_SkipsOnlyShortNames: host paths are built by the daemon,
// not proposed by the model, so the 8.3 rule (D-055), which defends against
// model-chosen aliases, does not apply to them. GitHub's Windows runners
// put TEMP under C:\Users\RUNNER~1, and with the rule every daemon-built
// path there was denied as a whole. Every glob still applies, and Match
// itself keeps the rule. These rows are plain strings, so they run on
// every OS.
func TestMatchHost_SkipsOnlyShortNames(t *testing.T) {
	cases := []struct {
		path            string
		host, withShort bool // want MatchHost, want Match
	}{
		{`C:\Users\RUNNER~1\AppData\Local\Temp\x\.git\objects`, false, true},
		{`C:\Users\RUNNER~1\.ssh\id_rsa`, true, true},
		{"/srv/RUNNER~1/warden-test/home/ana/project/.git/objects", false, true},
		{"/srv/RUNNER~1/home/ana/.aws/credentials", true, true},
		{"/home/ana/project/ENV~1", false, true},
		{"/home/ana/project/.env", true, true},
		{"/usr/local/go", false, false},
	}
	for _, c := range cases {
		if _, got := MatchHost(c.path); got != c.host {
			t.Errorf("MatchHost(%q) = %v, want %v", c.path, got, c.host)
		}
		if _, got := Match(c.path); got != c.withShort {
			t.Errorf("Match(%q) = %v, want %v (Match keeps the 8.3 rule)", c.path, got, c.withShort)
		}
	}
}

// TestMatchUnderHost_SkipsOnlyShortNames: the same for MatchUnder. The deny
// roots and the allowed roots still apply; only the 8.3 rule is skipped,
// including for the root-relative part under an allowed root.
func TestMatchUnderHost_SkipsOnlyShortNames(t *testing.T) {
	home := `C:\Users\RUNNER~1\AppData\Local\Warden`
	wt := home + `\sessions\01jax\worktree`
	deny, allow := []string{home}, []string{wt}
	cases := []struct {
		path            string
		host, withShort bool // want MatchUnderHost, want MatchUnder
	}{
		{`C:\Users\RUNNER~1\go\bin\warden-exec`, false, true},
		{`C:\Users\RUNNER~1\.ssh\warden-exec`, true, true},
		{home + `\bin\warden-exec`, true, true}, // the deny root
		{wt + `\src\app.ts`, false, false},
		{wt + `\ENV~1`, false, true},
		{wt + `\.env`, true, true},
	}
	for _, c := range cases {
		if _, got := MatchUnderHost(c.path, deny, allow); got != c.host {
			t.Errorf("MatchUnderHost(%q) = %v, want %v", c.path, got, c.host)
		}
		if _, got := MatchUnder(c.path, deny, allow); got != c.withShort {
			t.Errorf("MatchUnder(%q) = %v, want %v (MatchUnder keeps the 8.3 rule)", c.path, got, c.withShort)
		}
	}
}
