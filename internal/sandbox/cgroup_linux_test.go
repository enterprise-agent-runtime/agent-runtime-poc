//go:build linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestProbeCgroup_ReadsTheUserManagersDelegation runs the probe against a
// cgroup tree on disk: it reads the unified marker and the user manager's
// cgroup.controllers at the A06 §11.6 path, and with no systemd-run at the
// fixed paths it starts nothing (doctor never runs a probe scope it cannot
// name, and never looks systemd-run up in PATH).
func TestProbeCgroup_ReadsTheUserManagersDelegation(t *testing.T) {
	root := t.TempDir()
	svc := UserServiceCgroup(root, 1000)
	if err := os.MkdirAll(svc, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc, "cgroup.controllers"), []byte("cpu io memory pids\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000", "FAKE_DAEMON_SECRET": "x"}
	st := probeCgroup(context.Background(), root, 1000, func(k string) string { return env[k] }, []string{filepath.Join(root, "no-systemd-run")})
	if st.SystemdRun != "" || st.ScopeTried {
		t.Errorf("probe found systemd-run %q (tried %v) where there is none", st.SystemdRun, st.ScopeTried)
	}
	if !st.Unified || st.ControllersErr != nil || !slices.Equal(st.Controllers, []string{"cpu", "io", "memory", "pids"}) {
		t.Errorf("status = %+v, want unified with the user manager's controllers", st)
	}
	if !slices.Equal(st.Env, []string{"XDG_RUNTIME_DIR=/run/user/1000"}) {
		t.Errorf("Env = %q, want only the ScopeEnv variables", st.Env)
	}
	if st.Usable() {
		t.Error("Usable without a probe scope")
	}

	// Hybrid: no cgroup.controllers at the root (WSL2's default mount).
	os.Remove(filepath.Join(root, "cgroup.controllers"))
	if st := probeCgroup(context.Background(), root, 1000, func(k string) string { return env[k] }, nil); st.Unified {
		t.Error("a root without cgroup.controllers was taken for the unified hierarchy")
	}
}

// TestProbeCgroup_ThisHost reports what this machine offers, for the
// record (WSL2 runs a user manager but mounts cgroups hybrid); it asserts
// only that the verdict and the status agree.
func TestProbeCgroup_ThisHost(t *testing.T) {
	st := ProbeCgroup(context.Background())
	c := CgroupCheck(st)
	t.Logf("sandbox.cgroup: %s: %s", c.Status, c.Detail)
	if (c.Status == "ok") != st.Usable() {
		t.Errorf("check %s but Usable() = %v", c.Status, st.Usable())
	}
}
