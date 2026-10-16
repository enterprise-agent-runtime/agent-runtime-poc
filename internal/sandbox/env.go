package sandbox

// The environment model of design A06 §8 (Linux L1 and L2 values): the
// sandbox starts from an empty environment and gets exactly these keys
// (INV-B "environment cleared; no $HOME", INV-C "no secret in the sandbox
// env"). Nothing here reads the daemon's own environment, so no daemon
// variable, credential or not, can reach a sandbox through this path.

import (
	"strings"
)

// EnvVar is one sandbox environment variable.
type EnvVar struct{ Key, Value string }

// basePath follows the toolchain bin dirs in PATH (A06 §8). It names no
// task root, so a repository cannot shadow npm or git.
const basePath = "/usr/local/bin:/usr/bin:/bin"

// envKeys is the A06 §8 allowlist in sandbox.create env_keys order (§13.2).
var envKeys = []string{
	"HOME", "PATH", "LANG", "TERM", "TMPDIR",
	"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY",
	"npm_config_cache", "GOPATH", "GOMODCACHE", "GOCACHE", "PIP_CACHE_DIR",
	"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "GIT_CEILING_DIRECTORIES",
	"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
	"GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2",
}

// EnvKeys returns the allowlisted keys, for sandbox.create env_keys (the
// names, never the values).
func EnvKeys() []string { return append([]string(nil), envKeys...) }

// Env returns the complete sandbox environment for the Spec, in EnvKeys
// order. HOME is the scratch home (WRD-10 §5.1); proxy variables point at
// the in-sandbox forwarder (L1) or the proxy sidecar (L2, C-03); git runs
// without global or system config and with hooks pointed at the empty
// read-only directory (CLAUDE.md §9 rule 6).
func (s *Spec) Env() ([]EnvVar, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	proxy := "http://" + ForwarderAddr
	if s.Level == LevelL2 {
		proxy = "http://" + s.Proxy.Sidecar
	}
	values := map[string]string{
		"HOME":                    TargetScratch + "/home",
		"PATH":                    s.path(),
		"LANG":                    "C.UTF-8",
		"TERM":                    "dumb",
		"TMPDIR":                  TargetTmp,
		"HTTP_PROXY":              proxy,
		"HTTPS_PROXY":             proxy,
		"http_proxy":              proxy,
		"https_proxy":             proxy,
		"NO_PROXY":                "",
		"npm_config_cache":        TargetCache + "/npm",
		"GOPATH":                  TargetCache + "/go/path",
		"GOMODCACHE":              TargetCache + "/go/mod",
		"GOCACHE":                 TargetCache + "/go/build",
		"PIP_CACHE_DIR":           TargetCache + "/pip",
		"GIT_CONFIG_GLOBAL":       "/dev/null",
		"GIT_CONFIG_SYSTEM":       "/dev/null",
		"GIT_CONFIG_NOSYSTEM":     "1",
		"GIT_TERMINAL_PROMPT":     "0",
		"GIT_CEILING_DIRECTORIES": "/",
		"GIT_CONFIG_COUNT":        "3",
		"GIT_CONFIG_KEY_0":        "core.hooksPath",
		"GIT_CONFIG_VALUE_0":      TargetHooksDir,
		"GIT_CONFIG_KEY_1":        "core.fsmonitor",
		"GIT_CONFIG_VALUE_1":      "false",
		"GIT_CONFIG_KEY_2":        "safe.directory",
		"GIT_CONFIG_VALUE_2":      TargetWorktree,
	}
	env := make([]EnvVar, 0, len(envKeys))
	for _, k := range envKeys {
		env = append(env, EnvVar{Key: k, Value: values[k]})
	}
	return env, nil
}

// path is the toolchain bin dirs, in Spec order without repeats, then the
// base PATH.
func (s *Spec) path() string {
	var dirs []string
	seen := map[string]bool{}
	for _, tc := range s.Toolchains {
		for _, b := range tc.BinDirs {
			if !seen[b] {
				seen[b] = true
				dirs = append(dirs, b)
			}
		}
	}
	return strings.Join(append(dirs, basePath), ":")
}
