//go:build !windows

package linux_bwrap

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"warden.dev/warden/internal/sandbox"
)

func fourFiles(t *testing.T) []*os.File {
	t.Helper()
	var files []*os.File
	for range 2 {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close(); w.Close() })
		files = append(files, r, w)
	}
	return files
}

func testScope() Scope {
	return Scope{
		SystemdRun: "/usr/bin/systemd-run", EnvBin: "/usr/bin/env",
		Unit:   sandbox.ScopeUnit(testSpec().WardenHome, testID),
		Env:    []string{"XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"},
		Limits: sandbox.DefaultLimits(),
	}
}

// TestPlan_ScopedCommandOwnsTheWrapper guards the M2-13 "must hold" for
// the cgroup launch (A06 §11.6; INV-C, CWE-214/526). With --scope,
// systemd-run execs its command in place, so bwrap would inherit whatever
// environment systemd-run had, and bwrap's PID 1 keeps it readable at
// /proc/1/environ inside the sandbox. The Plan therefore owns the whole
// wrapped command line: systemd-run gets exactly the ScopeEnv variables
// (it needs them to reach the user manager), and bwrap runs under
// "env -i", so its own environment is empty. No daemon variable may reach
// either, and the limits are the Spec's.
func TestPlan_ScopedCommandOwnsTheWrapper(t *testing.T) {
	t.Setenv("FAKE_DAEMON_SECRET", "fake-not-a-secret")
	p := mustBuild(t, buildInput{Spec: testSpec(), Features: features090, Host: mergedUsr, Exec: cgroupExec})
	files := fourFiles(t)
	sc := testScope()
	cmd, err := p.ScopedCommand(context.Background(), sc, "/usr/bin/bwrap", files[0], files[1], files[2], files[3])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit=" + sc.Unit,
		"--property=MemoryMax=3072M", "--property=MemorySwapMax=0", "--property=TasksMax=512", "--property=CPUQuota=200%",
		"--", "/usr/bin/env", "-i", "/usr/bin/bwrap"}
	want = append(want, p.Argv[1:]...)
	if cmd.Path != "/usr/bin/systemd-run" || !slices.Equal(cmd.Args, want) {
		t.Errorf("Cmd = %q\n%q\nwant\n%q", cmd.Path, cmd.Args, want)
	}
	if !slices.Equal(cmd.Env, sc.Env) {
		t.Errorf("Cmd.Env = %q, want exactly the ScopeEnv variables %q", cmd.Env, sc.Env)
	}
	for _, e := range cmd.Env {
		if strings.Contains(e, "FAKE_DAEMON_SECRET") {
			t.Errorf("daemon variable in systemd-run's environment: %q", e)
		}
	}
	if !slices.Equal(cmd.ExtraFiles, files) {
		t.Errorf("Cmd.ExtraFiles = %v, want the four files as fds 3-6", cmd.ExtraFiles)
	}

	bad := map[string]func(*Scope){
		"relative systemd-run":   func(s *Scope) { s.SystemdRun = "systemd-run" },
		"relative env":           func(s *Scope) { s.EnvBin = "env" },
		"unit of no sandbox":     func(s *Scope) { s.Unit = "user@1000.service" },
		"unit of another id":     func(s *Scope) { s.Unit = sandbox.ScopeUnit(testSpec().WardenHome, "sb_OTHER") },
		"daemon variable":        func(s *Scope) { s.Env = append(s.Env, "FAKE_DAEMON_SECRET=fake-not-a-secret") },
		"variable without value": func(s *Scope) { s.Env = []string{"XDG_RUNTIME_DIR"} },
		"no runtime dir":         func(s *Scope) { s.Env = s.Env[1:] },
		"no memory limit":        func(s *Scope) { s.Limits.MemoryMB = 0 },
		"no pids limit":          func(s *Scope) { s.Limits.Pids = 0 },
		"no cpu limit":           func(s *Scope) { s.Limits.CPUs = 0 },
	}
	for name, mod := range bad {
		s := testScope()
		mod(&s)
		if _, err := p.ScopedCommand(context.Background(), s, "/usr/bin/bwrap", files[0], files[1], files[2], files[3]); err == nil {
			t.Errorf("%s: ScopedCommand accepted the scope", name)
		}
	}
	if _, err := p.ScopedCommand(context.Background(), sc, "bwrap", files[0], files[1], files[2], files[3]); err == nil {
		t.Error("ScopedCommand accepted a relative bwrap path")
	}
}

// TestPlan_ScopedCommandUnitMatchesTheArgv: the unit must be the one of
// the sandbox the argv was built for, so a scope can never be named after
// (and later killed as) another sandbox.
func TestPlan_ScopedCommandUnitMatchesTheArgv(t *testing.T) {
	p := mustBuild(t, buildInput{Spec: testSpec(), Features: features090, Host: mergedUsr, Exec: cgroupExec})
	if p.SandboxID != testID || p.WardenHome != testSpec().WardenHome {
		t.Errorf("Plan records sandbox %q home %q, want the Spec's", p.SandboxID, p.WardenHome)
	}
}
