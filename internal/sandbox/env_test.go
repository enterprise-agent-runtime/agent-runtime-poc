package sandbox

import (
	"reflect"
	"strings"
	"testing"

	"warden.dev/warden/internal/model/providertest"
)

// wantEnvKeys is the A06 §8 allowlist in sandbox.create env_keys order
// (A06 §13.2), restated here rather than taken from the code under test.
var wantEnvKeys = []string{
	"HOME", "PATH", "LANG", "TERM", "TMPDIR",
	"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY",
	"npm_config_cache", "GOPATH", "GOMODCACHE", "GOCACHE", "PIP_CACHE_DIR",
	"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "GIT_CEILING_DIRECTORIES",
	"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
	"GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2",
}

// TestSandboxEnvAllowlist is the INV-B / INV-C environment test of
// CLAUDE.md §5: the sandbox environment is exactly the A06 §8 key set with
// the Linux / L2 values, HOME is the scratch home (WRD-10 §5.1), and
// nothing comes from the daemon's own environment. The daemon environment
// is seeded with canary credentials and host session variables first; if
// any of them reaches the sandbox (an os.Environ() pass-through, a merge),
// this fails.
func TestSandboxEnvAllowlist(t *testing.T) {
	canary := providertest.FakeKey
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "WARDEN_CANARY_API_KEY", "GH_TOKEN", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
		t.Setenv(k, canary)
	}
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-canary/agent.1")
	t.Setenv("HOME", "/home/canary")
	t.Setenv("USERPROFILE", `C:\Users\canary`)
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/canary")

	common := map[string]string{
		"HOME": "/scratch/home", "LANG": "C.UTF-8", "TERM": "dumb", "TMPDIR": "/tmp", "NO_PROXY": "",
		"npm_config_cache": "/cache/npm", "GOPATH": "/cache/go/path", "GOMODCACHE": "/cache/go/mod",
		"GOCACHE": "/cache/go/build", "PIP_CACHE_DIR": "/cache/pip",
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0", "GIT_CEILING_DIRECTORIES": "/", "GIT_CONFIG_COUNT": "3",
		"GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": "/warden/empty",
		"GIT_CONFIG_KEY_1": "core.fsmonitor", "GIT_CONFIG_VALUE_1": "false",
		"GIT_CONFIG_KEY_2": "safe.directory", "GIT_CONFIG_VALUE_2": "/work",
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range common {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	proxyVars := func(u string) map[string]string {
		return map[string]string{"HTTP_PROXY": u, "HTTPS_PROXY": u, "http_proxy": u, "https_proxy": u}
	}
	l := newLayout(t)
	cases := []struct {
		name string
		spec *Spec
		want map[string]string
	}{
		{"L1", validSpec(l, LevelL1), with(merge(proxyVars("http://127.0.0.1:3128"),
			map[string]string{"PATH": "/opt/node-v22/bin:/usr/local/bin:/usr/bin:/bin"}))},
		{"L1 without toolchains", func() *Spec { s := validSpec(l, LevelL1); s.Toolchains = nil; return s }(),
			with(merge(proxyVars("http://127.0.0.1:3128"), map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin"}))},
		{"L2", validSpec(l, LevelL2), with(merge(proxyVars("http://"+testSide),
			map[string]string{"PATH": "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"}))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, err := c.spec.Env()
			if err != nil {
				t.Fatalf("Env() error: %v", err)
			}
			var keys []string
			got := map[string]string{}
			for _, v := range env {
				keys = append(keys, v.Key)
				got[v.Key] = v.Value
			}
			if !reflect.DeepEqual(keys, wantEnvKeys) {
				t.Errorf("env keys differ from the A06 §8 allowlist\n got %q\nwant %q", keys, wantEnvKeys)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("env values\n got %q\nwant %q", got, c.want)
			}
			if !reflect.DeepEqual(EnvKeys(), wantEnvKeys) {
				t.Errorf("EnvKeys() = %q, want %q", EnvKeys(), wantEnvKeys)
			}
			for _, v := range env {
				if strings.Contains(v.Value, canary) || strings.Contains(v.Value, "canary") {
					t.Errorf("%s carries a value from the daemon environment (INV-C)", v.Key)
				}
				for _, bad := range []string{"_API_KEY", "_TOKEN", "SECRET", "SSH_AUTH_SOCK", "USER", "XDG_", "DISPLAY", "TZ"} {
					if strings.Contains(v.Key, bad) {
						t.Errorf("forbidden key %s in the sandbox env (A06 §8)", v.Key)
					}
				}
				if strings.Contains(v.Value, "/work") && v.Key != "GIT_CONFIG_VALUE_2" {
					t.Errorf("%s=%q names the worktree; only safe.directory may", v.Key, v.Value)
				}
			}
			// HOME is inside the scratch mount, so $HOME is never a host
			// directory (INV-B, WRD-10 §5.1).
			if !strings.HasPrefix(got["HOME"], TargetScratch+"/") {
				t.Errorf("HOME = %q is not inside %s", got["HOME"], TargetScratch)
			}
		})
	}
}

func merge(a, b map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		m[k] = v
	}
	return m
}
