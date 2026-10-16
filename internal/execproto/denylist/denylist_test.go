package denylist

import "testing"

// TestMatch_A15Table is the matching table of design A15 §4.2 plus the
// patterns added by the full WRD-10 §6 list.
func TestMatch_A15Table(t *testing.T) {
	cases := []struct {
		path string
		deny bool
	}{
		{".env", true},
		{"config/.env.production", true},
		{".env.example", true}, // accepted false positive
		{"test/fixtures/server.key", true},
		{"docs/keys.md", false},
		{".warden/policy.yaml", true},
		{"src/app.ts", false},
		{"/Users/ana/.ssh/id_ed25519", true},
		{"/home/ana/.ssh/config", true},
		{"C:\\Users\\ana\\.ssh\\id_rsa", true},
		{"id_rsa.pub", true}, // accepted false positive
		{".ENV", true},       // case-insensitive volumes
		{"deploy/service-account-prod.json", true},
		{".aws", true},
		{".aws/credentials", true},
		{"home/.docker/config.json", true},
		{"home/.docker/daemon.json", false},
		{"vault.kdbx", true},
		{"src/environment.ts", false},
		{"a/b/.terraform.d/plugins/x", true},
		{"./.npmrc", true},
		{"package.json", false},
		{"", false},
	}
	for _, c := range cases {
		if _, got := Match(c.path); got != c.deny {
			t.Errorf("Match(%q) = %v, want %v", c.path, got, c.deny)
		}
	}
}

// TestMatchUnder_CF17 checks the root-relative rule: a worktree under the
// Warden home is allowed, the home itself is denied, also on Windows where
// the home has no ".warden" segment (CONFLICTS C-53).
func TestMatchUnder_CF17(t *testing.T) {
	home := "C:\\Users\\ana\\AppData\\Local\\Warden"
	wt := home + "\\sessions\\01jax\\worktree"
	cases := []struct {
		path string
		deny bool
	}{
		{wt + "\\src\\app.ts", false},
		{wt + "\\.env", true},
		{wt + "\\.warden\\policy.yaml", true},
		{home + "\\run\\token", true},
		{home + "\\secrets.enc", true},
		{"C:\\Users\\ana\\project\\README.md", false},
		{"c:/users/ana/appdata/local/warden/db/warden.sqlite", true},
	}
	for _, c := range cases {
		if _, got := MatchUnder(c.path, []string{home}, []string{wt}); got != c.deny {
			t.Errorf("MatchUnder(%q) = %v, want %v", c.path, got, c.deny)
		}
	}
	unixHome := "/home/ana/.warden"
	if _, got := MatchUnder(unixHome+"/sessions/x/worktree/src/a.go", []string{unixHome}, []string{unixHome + "/sessions/x/worktree"}); got {
		t.Error("worktree under ~/.warden denied")
	}
	if _, got := MatchUnder(unixHome+"/run/token", nil, nil); !got {
		t.Error("~/.warden/run/token allowed")
	}
}

func TestArgv_Heuristic(t *testing.T) {
	cases := []struct {
		argv []string
		cwd  string
		deny bool
	}{
		{[]string{"cat", ".env"}, "", true},
		{[]string{"node", "--env-file=.env", "app.js"}, "", true},
		{[]string{"cat", "id_ed25519"}, ".ssh", true},
		{[]string{"npm", "test"}, "", false},
		{[]string{"grep", "-r", "API_KEY", "src"}, "", false},
		{[]string{"echo", "not a path .env"}, "", false},
		{[]string{".env"}, "", false}, // argv[0] is the program
		{nil, "", false},
	}
	for _, c := range cases {
		if _, got := Argv(c.argv, c.cwd); got != c.deny {
			t.Errorf("Argv(%q, %q) = %v, want %v", c.argv, c.cwd, got, c.deny)
		}
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"C:\\a\\b\\..\\c": "c:/a/c",
		"\\\\?\\D:\\x":    "d:/x",
		"/home/a/./b/":    "/home/a/b",
		"":                "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
