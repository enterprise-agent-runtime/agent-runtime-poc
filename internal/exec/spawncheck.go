package exec

// The pure half of exec.proc.spawn's checks (WRD-16 §9, design A06 §3.3,
// §6 steps 1, 4 and 5, §14): the params shape, the harness spec, and the
// consistency of argv with the granted profiles and the PDP's decision.
// Executable resolution (step 3) and the cwd (step 6) need the file system
// and live in proc_unix.go. The package doc is in server.go.
//
// These checks are a consistency check against daemon bugs, not a boundary
// against a compromised daemon (A06 §6: the daemon is in the TCB). Error
// details are fixed text: they never quote argv, which may hold file
// content, or a secret_env value (INV-C).

import (
	"fmt"
	"path"
	"strings"

	"warden.dev/warden/internal/execproto"
)

// Limits of proc.json SpawnParams and A06 §6 step 1.
const (
	maxArgvElems     = 1024
	maxArgvElemBytes = 32768
	maxArgvBytes     = 128 << 10
	minTimeoutMS     = 1000
	maxTimeoutMS     = 3600000
	minOutputBytes   = 4096
	maxOutputBytes   = 1 << 20
	defaultOutput    = 262144
	maxIDChars       = 40
)

// HarnessSpec is the --harness-spec file of design A06 §3.3 (mode harness
// only): the exact argv the harness engine may be started with, its
// working directory, the non-secret variables added for the engine
// process, and the one variable whose value arrives in
// exec.proc.spawn.secret_env (HX-1b).
type HarnessSpec struct {
	Version          int               `json:"version"`
	HarnessID        string            `json:"harness_id"`
	RunMode          string            `json:"run_mode"`
	Argv             []string          `json:"argv"`
	ArgvAlternatives [][]string        `json:"argv_alternatives,omitempty"`
	Cwd              string            `json:"cwd,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	SecretEnvKeys    []string          `json:"secret_env_keys,omitempty"`
}

// ParseHarnessSpec decodes the spec strictly (execproto.Decode: unknown,
// duplicate and case-variant keys refused) and checks the A06 §3.3 schema.
// Two rules go beyond it, both closing a way around §8: a spec variable
// may not replace an allowlisted variable (a PATH, HOME or HTTPS_PROXY of
// the spec's choosing would undo the sandbox's), and the secret key may be
// neither an allowlisted key nor a spec variable, so the one secret cannot
// be smuggled into PATH or shadow a spec value.
//
// A06 §3.3 also says spec env values are scanned with the A15 patterns;
// execproto has no A15 pattern set yet, so that scan is not done here
// (left for the task that adds the set).
func ParseHarnessSpec(data []byte) (*HarnessSpec, error) {
	var h HarnessSpec
	if err := execproto.Decode(data, &h); err != nil {
		return nil, fmt.Errorf("exec: harness spec: %w", err)
	}
	bad := func(what string) (*HarnessSpec, error) { return nil, fmt.Errorf("exec: harness spec: %s", what) }
	switch {
	case h.Version != 1:
		return bad("version must be 1")
	case h.HarnessID != "copilot" && h.HarnessID != "codex" && h.HarnessID != "claude-code":
		return bad("unknown harness_id")
	case h.RunMode != "split" && h.RunMode != "colocated":
		return bad("unknown run_mode")
	case !validSpecArgv(h.Argv):
		return bad("argv must have 1 to 64 elements of at most 4096 bytes, no NUL")
	case len(h.ArgvAlternatives) > 2:
		return bad("at most 2 argv_alternatives")
	case h.Cwd != "" && (!strings.HasPrefix(h.Cwd, "/") || path.Clean(h.Cwd) != h.Cwd || strings.IndexByte(h.Cwd, 0) >= 0):
		return bad("cwd must be a clean absolute sandbox path")
	case len(h.Env) > 16:
		return bad("at most 16 env variables")
	case len(h.SecretEnvKeys) > execproto.MaxSecretEnvKeys:
		return bad("at most one secret_env key")
	}
	for _, alt := range h.ArgvAlternatives {
		if !validSpecArgv(alt) {
			return bad("argv_alternatives elements must be valid argv")
		}
	}
	for k, v := range h.Env {
		if !validEnvName(k) || len(v) > 1024 || strings.IndexByte(v, 0) >= 0 {
			return bad("env names must match ^[A-Z][A-Z0-9_]{0,63}$ and values be at most 1024 bytes")
		}
		if allowedEnvKey(k) {
			return bad("env may not replace an A06 §8 variable")
		}
	}
	for _, k := range h.SecretEnvKeys {
		if !validEnvName(k) {
			return bad("secret_env_keys must match ^[A-Z][A-Z0-9_]{0,63}$")
		}
		if _, dup := h.Env[k]; dup || allowedEnvKey(k) {
			return bad("a secret_env key may not be an A06 §8 variable or a spec env variable")
		}
	}
	return &h, nil
}

func validSpecArgv(a []string) bool {
	if len(a) < 1 || len(a) > 64 || a[0] == "" {
		return false
	}
	for _, s := range a {
		if len(s) > 4096 || strings.IndexByte(s, 0) >= 0 {
			return false
		}
	}
	return true
}

// validEnvName is ^[A-Z][A-Z0-9_]{0,63}$.
func validEnvName(k string) bool {
	if len(k) < 1 || len(k) > 64 || k[0] < 'A' || k[0] > 'Z' {
		return false
	}
	for i := 1; i < len(k); i++ {
		c := k[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// Allows reports whether argv equals the spec argv or one of its
// alternatives, element by element (A06 §6 step 5: "exactly").
func (h *HarnessSpec) Allows(argv []string) bool {
	if h == nil {
		return false
	}
	if equalArgv(h.Argv, argv) {
		return true
	}
	for _, alt := range h.ArgvAlternatives {
		if equalArgv(alt, argv) {
			return true
		}
	}
	return false
}

func equalArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// spawnPolicy is what the executor knows about what may run: its mode,
// the granted profiles (--allow-profiles) and, in harness mode, the spec.
type spawnPolicy struct {
	mode     execproto.Mode
	profiles *execproto.ProfileSet
	harness  *HarnessSpec
}

func invalid(detail string) error { return execproto.NewError(execproto.ErrInvalidArgument, detail) }

// validateSpawn is A06 §6 step 1 (argv shape) plus the proc.json ranges
// the strict decoder does not check.
func validateSpawn(p execproto.SpawnParams) error {
	if !validID(p.CallID, "call_") {
		return invalid("call_id must match ^call_[0-9A-Za-z]{1,40}$")
	}
	if !validID(p.Authz.DecisionID, "dec_") {
		return invalid("authz.decision_id must match ^dec_[0-9A-Za-z]{1,40}$")
	}
	switch p.Authz.Kind {
	case execproto.AuthzTool, execproto.AuthzVerifier, execproto.AuthzHarness:
	default:
		return invalid("authz.kind must be tool, verifier or harness")
	}
	if len(p.Argv) == 0 || len(p.Argv) > maxArgvElems || p.Argv[0] == "" {
		return invalid("argv must have 1 to 1024 elements and a non-empty argv[0]")
	}
	total := 0
	for _, a := range p.Argv {
		if len(a) > maxArgvElemBytes || strings.IndexByte(a, 0) >= 0 {
			return invalid("argv elements must be at most 32768 bytes, without NUL")
		}
		total += len(a)
	}
	if total > maxArgvBytes {
		return invalid("argv must be at most 128 KiB in total")
	}
	if strings.IndexByte(p.Cwd, 0) >= 0 {
		return invalid("invalid cwd")
	}
	if p.TimeoutMS < minTimeoutMS || p.TimeoutMS > maxTimeoutMS {
		return invalid("timeout_ms must be 1000 to 3600000")
	}
	if p.MaxOutputBytes != 0 && (p.MaxOutputBytes < minOutputBytes || p.MaxOutputBytes > maxOutputBytes) {
		return invalid("max_output_bytes must be 4096 to 1048576")
	}
	return nil
}

func validID(s, prefix string) bool {
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok || len(rest) < 1 || len(rest) > maxIDChars {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// authorize is A06 §6 steps 4 and 5. It returns the executor's own profile
// match ("" when none).
//
//   - Mode first. In harness mode argv must equal the spec argv or an
//     alternative and authz.kind must be harness; anything else is
//     mode_forbidden (A06 names no code for it; this is the code for "not
//     available in harness mode", and it carries a violation). In tool
//     mode authz.kind harness is a daemon bug: invalid_argument.
//   - Profile consistency: authz.profile must equal the executor's match
//     (profile_mismatch), so the daemon and the executor never disagree on
//     why a command runs.
//   - No profile: in tool mode only an approval or grant lets the command
//     run (not_allowed otherwise). A harness argv is fixed by the spec the
//     daemon generated, so it needs no approval.
//   - secret_env: only in harness mode, only for the spec's key, and no
//     NUL (an environment cannot carry one). Details never quote a value.
func (pol spawnPolicy) authorize(p execproto.SpawnParams) (string, error) {
	harness := pol.mode == execproto.ModeHarness
	switch {
	case harness && (!pol.harness.Allows(p.Argv) || p.Authz.Kind != execproto.AuthzHarness):
		return "", execproto.NewError(execproto.ErrModeForbidden, "in harness mode only the harness spec argv may be spawned, with authz.kind harness")
	case !harness && p.Authz.Kind == execproto.AuthzHarness:
		return "", invalid("authz.kind harness outside harness mode")
	}
	profile, _ := pol.profiles.Match(p.Argv)
	if p.Authz.Profile != profile {
		return "", execproto.NewError(execproto.ErrProfileMismatch, "authz.profile differs from the executor's profile match")
	}
	if profile == "" && !harness && !p.Authz.Approved {
		return "", execproto.NewError(execproto.ErrNotAllowed, "argv matches no granted profile and was not approved")
	}
	if !p.SecretEnv.IsZero() {
		if !harness {
			return "", invalid("secret_env is accepted only in harness mode")
		}
		for _, k := range p.SecretEnv.Keys() {
			v, _ := p.SecretEnv.Value(k)
			if !specListsSecret(pol.harness, k) || strings.IndexByte(v, 0) >= 0 {
				return "", invalid("secret_env must use the harness spec's secret_env_keys, without NUL")
			}
		}
	}
	return profile, nil
}

func specListsSecret(h *HarnessSpec, k string) bool {
	if h == nil {
		return false
	}
	for _, s := range h.SecretEnvKeys {
		if s == k {
			return true
		}
	}
	return false
}
