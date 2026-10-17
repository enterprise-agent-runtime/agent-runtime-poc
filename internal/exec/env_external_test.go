package exec_test

import (
	"slices"
	"testing"

	"warden.dev/warden/internal/exec"
	"warden.dev/warden/internal/sandbox"
)

// TestEnvKeys_MatchSandbox keeps the executor's environment filter equal to
// the daemon-side list that builds the sandbox environment (A06 §8,
// TestSandboxEnvAllowlist). If the two drifted, a variable the sandbox sets
// would silently be dropped for every child, or the executor would pass one
// the sandbox layer never meant to allow. (R3 keeps internal/exec from
// importing internal/sandbox; test files are exempt, which is what makes
// this comparison possible.)
func TestEnvKeys_MatchSandbox(t *testing.T) {
	if got, want := exec.EnvKeys(), sandbox.EnvKeys(); !slices.Equal(got, want) {
		t.Errorf("internal/exec allows %q, internal/sandbox sets %q", got, want)
	}
}
