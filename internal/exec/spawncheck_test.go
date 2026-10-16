package exec

import (
	"errors"
	"strings"
	"testing"

	"warden.dev/warden/internal/execproto"
)

func goodSpawn() execproto.SpawnParams {
	return execproto.SpawnParams{
		CallID:    "call_1",
		Argv:      []string{"npm", "test"},
		TimeoutMS: 60000,
		Authz:     execproto.Authz{DecisionID: "dec_1", Profile: "node-test", Kind: execproto.AuthzTool},
	}
}

// TestValidateSpawn_Shape guards design A06 §6 step 1 (argv non-empty, at
// most 1024 elements, at most 128 KiB in total, no NUL) and the proc.json
// ranges the decoder does not check (timeout_ms 1000…3600000,
// max_output_bytes 4096…1048576, the call_id and decision_id patterns, the
// authz kind). A NUL would be cut by execve, so the process would run
// something other than what the policy engine decided on.
func TestValidateSpawn_Shape(t *testing.T) {
	if err := validateSpawn(goodSpawn()); err != nil {
		t.Fatalf("good params refused: %v", err)
	}
	big := strings.Repeat("a", 32768)
	cases := map[string]func(*execproto.SpawnParams){
		"empty argv":          func(p *execproto.SpawnParams) { p.Argv = nil },
		"empty argv[0]":       func(p *execproto.SpawnParams) { p.Argv = []string{""} },
		"NUL in argv":         func(p *execproto.SpawnParams) { p.Argv = []string{"npm", "te\x00st"} },
		"1025 elements":       func(p *execproto.SpawnParams) { p.Argv = make([]string, 1025); p.Argv[0] = "npm" },
		"element over 32 KiB": func(p *execproto.SpawnParams) { p.Argv = []string{"npm", big + "a"} },
		"total over 128 KiB":  func(p *execproto.SpawnParams) { p.Argv = []string{"npm", big, big, big, big, "a"} },
		"timeout below 1 s":   func(p *execproto.SpawnParams) { p.TimeoutMS = 999 },
		"timeout over 1 h":    func(p *execproto.SpawnParams) { p.TimeoutMS = 3600001 },
		"output cap low":      func(p *execproto.SpawnParams) { p.MaxOutputBytes = 4095 },
		"output cap high":     func(p *execproto.SpawnParams) { p.MaxOutputBytes = 1048577 },
		"call id":             func(p *execproto.SpawnParams) { p.CallID = "call_" },
		"call id chars":       func(p *execproto.SpawnParams) { p.CallID = "call_a-b" },
		"decision id":         func(p *execproto.SpawnParams) { p.Authz.DecisionID = "decision_1" },
		"kind":                func(p *execproto.SpawnParams) { p.Authz.Kind = "admin" },
		"NUL in cwd":          func(p *execproto.SpawnParams) { p.Cwd = "a\x00" },
	}
	for name, mutate := range cases {
		p := goodSpawn()
		mutate(&p)
		if err := validateSpawn(p); !errors.Is(err, execproto.ErrInvalidArgument) {
			t.Errorf("%s: %v, want invalid_argument", name, err)
		}
	}
	p := goodSpawn()
	p.Argv = []string{"npm", big, big, big, strings.Repeat("a", 32768-3)} // 128 KiB exactly, "npm" included
	if err := validateSpawn(p); err != nil {
		t.Errorf("argv of exactly 128 KiB refused: %v", err)
	}
}

func testProfiles(t *testing.T) *execproto.ProfileSet {
	t.Helper()
	set, err := execproto.ParseProfiles([]byte(`{"version":1,"task_key":"implement","profiles":{"node-test":[["npm","test"],["npm","test","--","<args>"]],"echo":[["echo","<args>"]]}}`))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func testHarness() *HarnessSpec {
	return &HarnessSpec{Version: 1, HarnessID: "copilot", RunMode: "split", Argv: []string{"copilot", "--server"},
		ArgvAlternatives: [][]string{{"copilot", "--server", "--resume"}}, SecretEnvKeys: []string{"COPILOT_TOKEN"}}
}

// TestAuthorize guards design A06 §6 steps 4 and 5, the executor's
// consistency check against daemon bugs: the executor matches argv against
// its own copy of the granted profiles, and authz.profile must name the
// same profile (profile_mismatch otherwise); with no profile, only an
// approval or grant (authz.approved) lets the command run (not_allowed
// otherwise, which is R3 reaching the executor without a decision). In
// harness mode only the harness spec's argv runs (A06 §3.1, §14), with
// authz.kind harness, and secret_env is accepted only there and only for
// the spec's secret_env_keys (HX-1b; INV-C: the one place a secret enters
// a sandbox, never a tool sandbox).
func TestAuthorize(t *testing.T) {
	tool := spawnPolicy{mode: execproto.ModeTool, profiles: testProfiles(t)}
	harness := spawnPolicy{mode: execproto.ModeHarness, profiles: testProfiles(t), harness: testHarness()}
	secret := execproto.NewSecretEnv(map[string]string{"COPILOT_TOKEN": "gho_canary"})

	type tc struct {
		pol     spawnPolicy
		mutate  func(*execproto.SpawnParams)
		want    execproto.Code // 0: allowed
		profile string
	}
	cases := map[string]tc{
		"profile match":             {tool, func(p *execproto.SpawnParams) {}, 0, "node-test"},
		"profile via <args>":        {tool, func(p *execproto.SpawnParams) { p.Argv = []string{"npm", "test", "--", "-t", "x"} }, 0, "node-test"},
		"authz names other profile": {tool, func(p *execproto.SpawnParams) { p.Authz.Profile = "echo" }, execproto.ErrProfileMismatch, ""},
		"authz empty, exec matches": {tool, func(p *execproto.SpawnParams) { p.Authz.Profile = ""; p.Authz.Approved = true }, execproto.ErrProfileMismatch, ""},
		"no profile, not approved": {tool, func(p *execproto.SpawnParams) {
			p.Argv = []string{"npm", "test", "--silent"}
			p.Authz.Profile = ""
		}, execproto.ErrNotAllowed, ""},
		"no profile, approved": {tool, func(p *execproto.SpawnParams) {
			p.Argv = []string{"npm", "test", "--silent"}
			p.Authz.Profile = ""
			p.Authz.Approved = true
		}, 0, ""},
		"path argv[0] never matches": {tool, func(p *execproto.SpawnParams) { p.Argv = []string{"/usr/bin/npm", "test"} }, execproto.ErrProfileMismatch, ""},
		"verifier kind":              {tool, func(p *execproto.SpawnParams) { p.Authz.Kind = execproto.AuthzVerifier }, 0, "node-test"},
		"harness kind in tool mode":  {tool, func(p *execproto.SpawnParams) { p.Authz.Kind = execproto.AuthzHarness }, execproto.ErrInvalidArgument, ""},
		"secret_env in tool mode":    {tool, func(p *execproto.SpawnParams) { p.SecretEnv = secret }, execproto.ErrInvalidArgument, ""},
		"harness argv": {harness, func(p *execproto.SpawnParams) {
			p.Argv = []string{"copilot", "--server"}
			p.Authz = execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzHarness}
			p.SecretEnv = secret
		}, 0, ""},
		"harness alternative": {harness, func(p *execproto.SpawnParams) {
			p.Argv = []string{"copilot", "--server", "--resume"}
			p.Authz = execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzHarness}
		}, 0, ""},
		"harness mode, other argv": {harness, func(p *execproto.SpawnParams) {
			p.Authz.Kind = execproto.AuthzHarness
		}, execproto.ErrModeForbidden, ""},
		"harness mode, argv prefix": {harness, func(p *execproto.SpawnParams) {
			p.Argv = []string{"copilot"}
			p.Authz = execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzHarness}
		}, execproto.ErrModeForbidden, ""},
		"harness mode, tool kind": {harness, func(p *execproto.SpawnParams) {
			p.Argv = []string{"copilot", "--server"}
			p.Authz = execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzTool}
		}, execproto.ErrModeForbidden, ""},
		"harness secret, key not in spec": {harness, func(p *execproto.SpawnParams) {
			p.Argv = []string{"copilot", "--server"}
			p.Authz = execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzHarness}
			p.SecretEnv = execproto.NewSecretEnv(map[string]string{"PATH": "gho_canary"})
		}, execproto.ErrInvalidArgument, ""},
		"harness secret with NUL": {harness, func(p *execproto.SpawnParams) {
			p.Argv = []string{"copilot", "--server"}
			p.Authz = execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzHarness}
			p.SecretEnv = execproto.NewSecretEnv(map[string]string{"COPILOT_TOKEN": "gho\x00x"})
		}, execproto.ErrInvalidArgument, ""},
	}
	for name, c := range cases {
		p := goodSpawn()
		c.mutate(&p)
		got, err := c.pol.authorize(p)
		switch {
		case c.want == 0 && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case c.want == 0 && got != c.profile:
			t.Errorf("%s: profile %q, want %q", name, got, c.profile)
		case c.want != 0 && !errors.Is(err, c.want):
			t.Errorf("%s: %v, want %s", name, err, c.want.Name())
		}
		if err != nil && strings.Contains(err.Error(), "gho_canary") {
			t.Errorf("%s: the error quotes the secret_env value: %v", name, err)
		}
	}
}

// TestParseHarnessSpec guards the A06 §3.3 schema of the --harness-spec
// file: the only argv a harness sandbox may run, and the one secret key it
// may receive. Spec variables may not replace a §8 variable (a PATH or
// HTTPS_PROXY of the spec's choosing would undo the sandbox's), and the
// secret key may not be one either, nor collide with a spec variable.
func TestParseHarnessSpec(t *testing.T) {
	good := `{"version":1,"harness_id":"claude-code","run_mode":"colocated","argv":["claude","--print"],"cwd":"/work","env":{"DISABLE_AUTOUPDATER":"1"},"secret_env_keys":["ANTHROPIC_API_KEY"]}`
	h, err := ParseHarnessSpec([]byte(good))
	if err != nil {
		t.Fatalf("good spec: %v", err)
	}
	if !h.Allows([]string{"claude", "--print"}) || h.Allows([]string{"claude"}) || h.Allows([]string{"claude", "--print", "x"}) {
		t.Error("Allows is not exact argv equality")
	}
	bad := map[string]string{
		"version":            `{"version":2,"harness_id":"copilot","run_mode":"split","argv":["a"]}`,
		"harness id":         `{"version":1,"harness_id":"other","run_mode":"split","argv":["a"]}`,
		"run mode":           `{"version":1,"harness_id":"copilot","run_mode":"x","argv":["a"]}`,
		"empty argv":         `{"version":1,"harness_id":"copilot","run_mode":"split","argv":[]}`,
		"unknown field":      `{"version":1,"harness_id":"copilot","run_mode":"split","argv":["a"],"shell":true}`,
		"env replaces PATH":  `{"version":1,"harness_id":"copilot","run_mode":"split","argv":["a"],"env":{"PATH":"/x"}}`,
		"env key pattern":    `{"version":1,"harness_id":"copilot","run_mode":"split","argv":["a"],"env":{"lower":"x"}}`,
		"secret is §8 key":   `{"version":1,"harness_id":"copilot","run_mode":"split","argv":["a"],"secret_env_keys":["HOME"]}`,
		"secret is env key":  `{"version":1,"harness_id":"copilot","run_mode":"split","argv":["a"],"env":{"TOKEN":"x"},"secret_env_keys":["TOKEN"]}`,
		"two secret keys":    `{"version":1,"harness_id":"copilot","run_mode":"split","argv":["a"],"secret_env_keys":["A","B"]}`,
		"three alternatives": `{"version":1,"harness_id":"copilot","run_mode":"split","argv":["a"],"argv_alternatives":[["b"],["c"],["d"]]}`,
		"relative cwd":       `{"version":1,"harness_id":"copilot","run_mode":"split","argv":["a"],"cwd":"work"}`,
		"missing argv":       `{"version":1,"harness_id":"copilot","run_mode":"split"}`,
	}
	for name, in := range bad {
		if _, err := ParseHarnessSpec([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
