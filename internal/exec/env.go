package exec

// The child environment (design A06 §7.1 step 2, §8; INV-B, INV-C). The
// package doc is in server.go.

import "strings"

// EnvKeys returns the A06 §8 allowlist, in the table's order (a fresh
// slice). The daemon-side list is internal/sandbox's envKeys, which builds
// the sandbox's environment; this one is the executor's own filter, and
// TestEnvKeys_MatchSandbox keeps the two equal.
func EnvKeys() []string {
	return []string{
		"HOME", "PATH", "LANG", "TERM", "TMPDIR",
		"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY",
		"npm_config_cache", "GOPATH", "GOMODCACHE", "GOCACHE", "PIP_CACHE_DIR",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "GIT_CEILING_DIRECTORIES",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
		"GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2",
	}
}

// allowedEnvKey reports whether k is in the A06 §8 table. Names are
// case-sensitive: http_proxy and HTTP_PROXY are both listed, home is not.
func allowedEnvKey(k string) bool {
	for _, a := range EnvKeys() {
		if a == k {
			return true
		}
	}
	return false
}

// ChildEnv is the environment every tool-mode child gets: the launch
// environment of the executor (A06 §7.1 step 2), restricted to the §8
// allowlist. The sandbox already starts the executor with only those
// variables (--clearenv), so in a correct launch this changes nothing; it
// is the executor's own layer, so that a variable the launch should not
// have passed (a host API key, SSH_AUTH_SOCK, LD_PRELOAD) never reaches an
// agent-requested process. Of duplicated keys the first is kept, so a
// later entry cannot override the value the sandbox set. Entries without
// "=" or with an empty name are dropped.
func ChildEnv(launch []string) []string {
	out := make([]string, 0, len(EnvKeys()))
	seen := map[string]bool{}
	for _, kv := range launch {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || seen[k] || !allowedEnvKey(k) {
			continue
		}
		seen[k] = true
		out = append(out, kv)
	}
	return out
}
