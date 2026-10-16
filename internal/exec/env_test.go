package exec

import (
	"slices"
	"testing"
)

// TestChildEnv_OnlyTheAllowlist guards design A06 §7.1 step 2 and §8 (INV-B
// "environment cleared", INV-C "no secret in the sandbox env"): a child
// gets exactly the launch environment's §8 variables. The sandbox already
// starts the executor with only those, so this is the executor's own
// layer: should a launch ever pass anything else (a host API key, an
// SSH_AUTH_SOCK, LD_PRELOAD), it stops here. The first of duplicated keys
// wins, so a later entry cannot override the one the sandbox set.
func TestChildEnv_OnlyTheAllowlist(t *testing.T) {
	launch := []string{
		"HOME=/scratch/home",
		"PATH=/usr/bin:/bin",
		"OPENAI_API_KEY=sk-canary",
		"SSH_AUTH_SOCK=/tmp/agent",
		"LD_PRELOAD=/work/evil.so",
		"home=/lowercase/is/another/key",
		"PATH=/work/bin",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"noequals",
		"=nokey",
		"TZ=Europe/Berlin",
	}
	got := ChildEnv(launch)
	want := []string{"HOME=/scratch/home", "PATH=/usr/bin:/bin", "GIT_CONFIG_KEY_0=core.hooksPath"}
	if !slices.Equal(got, want) {
		t.Errorf("ChildEnv = %q, want %q", got, want)
	}
}

// TestEnvKeys_A06Table pins the allowlist to A06 §8's table, so a key
// cannot be added here by accident. (TestEnvKeys_MatchSandbox, in the
// external test package, also compares it with the daemon-side list.)
func TestEnvKeys_A06Table(t *testing.T) {
	want := []string{
		"HOME", "PATH", "LANG", "TERM", "TMPDIR",
		"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY",
		"npm_config_cache", "GOPATH", "GOMODCACHE", "GOCACHE", "PIP_CACHE_DIR",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "GIT_CEILING_DIRECTORIES",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
		"GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2",
	}
	if got := EnvKeys(); !slices.Equal(got, want) {
		t.Errorf("EnvKeys = %q, want %q", got, want)
	}
	for _, k := range []string{"USER", "LOGNAME", "SHELL", "SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "DISPLAY", "ANTHROPIC_API_KEY", "TZ", "ALL_PROXY", "LD_PRELOAD"} {
		if allowedEnvKey(k) {
			t.Errorf("%s is allowed; A06 §8 excludes it", k)
		}
	}
}
