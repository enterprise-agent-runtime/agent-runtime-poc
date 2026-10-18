package linux_bwrap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/sandbox"
)

const (
	lcID      = "sb_01JAXS2Q7M2V9KTC3F6YH5N0PB"
	lcSession = "01jaxr8q7m2v9ktc3f6yh5n0pb"
)

func lcState(home string) State {
	return State{SandboxID: lcID, SessionID: "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB", HomeHash: platform.HomeHash(home),
		Backend: Backend, RootPID: 4242, RootStartTime: 987654, ChildPID: 4243, ScopeUnit: sandbox.ScopeUnit(home, lcID),
		BootID: "0b9e7a1c-4f1e-4a57-9d5f-3c2a7e6b1d00"}
}

// TestState_CheckRefusesForeignState: the orphan sweep (A06 §13.5) acts
// on a state file only when it describes a sandbox of this home, of this
// backend, in the directory it was found in, with a scope that is that
// sandbox's own. A copied or edited file would otherwise make the sweep
// kill a process or scope of another home.
func TestState_CheckRefusesForeignState(t *testing.T) {
	home, other := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	if err := lcState(home).Check(home, lcSession, lcID); err != nil {
		t.Fatalf("Check = %v for the home's own state", err)
	}
	noScope := lcState(home)
	noScope.ScopeUnit = ""
	if err := noScope.Check(home, lcSession, lcID); err != nil {
		t.Errorf("Check = %v for a state without a scope (rlimit fallback)", err)
	}
	bad := map[string]func(*State){
		"other home":          func(s *State) { s.HomeHash = platform.HomeHash(other) },
		"other directory":     func(s *State) { s.SandboxID = "sb_OTHER" },
		"other backend":       func(s *State) { s.Backend = "docker" },
		"pid 1":               func(s *State) { s.RootPID = 1 },
		"no start time":       func(s *State) { s.RootStartTime = 0 },
		"other home's scope":  func(s *State) { s.ScopeUnit = sandbox.ScopeUnit(other, lcID) },
		"other sandbox scope": func(s *State) { s.ScopeUnit = sandbox.ScopeUnit(home, "sb_OTHER") },
		"not a scope":         func(s *State) { s.ScopeUnit = "user@1000.service" },
	}
	for name, mod := range bad {
		s := lcState(home)
		mod(&s)
		if err := s.Check(home, lcSession, lcID); err == nil {
			t.Errorf("%s: Check accepted %+v", name, s)
		}
	}
}

func TestParseState_Strict(t *testing.T) {
	b, err := json.Marshal(lcState("/h"))
	if err != nil {
		t.Fatal(err)
	}
	if s, err := ParseState(b); err != nil || s != lcState("/h") {
		t.Errorf("ParseState = %+v, %v", s, err)
	}
	for _, bad := range []string{`{"sandbox_id":"x","extra":1}`, `nope`, strings.Repeat(" ", 5000) + "{}"} {
		if _, err := ParseState([]byte(bad)); err == nil {
			t.Errorf("ParseState(%.40q) accepted", bad)
		}
	}
}

func TestParseInfo(t *testing.T) {
	pid, err := ParseInfo(strings.NewReader(`{"child-pid": 31337, "cgroup-namespace": 4026531835}` + "\n"))
	if err != nil || pid != 31337 {
		t.Errorf("ParseInfo = %d, %v", pid, err)
	}
	for _, bad := range []string{"", `{}`, `{"child-pid": 1}`, `{"child-pid": "x"}`, `[`} {
		if _, err := ParseInfo(strings.NewReader(bad)); !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("ParseInfo(%q) = %v, want sandbox_unavailable", bad, err)
		}
	}
}

func TestParseStartTime(t *testing.T) {
	// A command name with spaces and a ")" must not shift the fields.
	stat := "4242 (we ird) name) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1234 56 18446744073709551615"
	if st, err := ParseStartTime(stat); err != nil || st != 987654 {
		t.Errorf("ParseStartTime = %d, %v; want 987654", st, err)
	}
	if _, err := ParseStartTime("4242 (x) S 1 2"); err == nil {
		t.Error("short stat accepted")
	}
}

func TestParseProcCgroup(t *testing.T) {
	c := "12:pids:/user.slice\n0::/user.slice/user-1000.slice/user@1000.service/app.slice/warden-1a2b3c4d-sb_X.scope\n"
	if got := ParseProcCgroup(c); got != "/user.slice/user-1000.slice/user@1000.service/app.slice/warden-1a2b3c4d-sb_X.scope" {
		t.Errorf("ParseProcCgroup = %q", got)
	}
	if got := ParseProcCgroup("5:memory:/x\n"); got != "" {
		t.Errorf("v1-only cgroup = %q, want none", got)
	}
}

// TestCheckHello compares exec.hello with the launch (A06 §13.2 step 5):
// a digest, mode, protocol or root that differs is a hello mismatch, the
// sandbox is torn down and never announced.
func TestCheckHello(t *testing.T) {
	s := &sandbox.Spec{Mode: execproto.ModeTool, ProfilesDigest: "sha256:" + strings.Repeat("a", 64)}
	good := execproto.HelloResult{Protocol: execproto.Protocol, Mode: execproto.ModeTool, ProfilesDigest: s.ProfilesDigest, Roots: ExpectedRoots()}
	slices.Reverse(good.Roots)
	if err := CheckHello(s, good); err != nil {
		t.Fatalf("CheckHello = %v for a matching hello (roots in another order)", err)
	}
	bad := map[string]func(*execproto.HelloResult){
		"protocol":  func(r *execproto.HelloResult) { r.Protocol = "warden.exec/0" },
		"mode":      func(r *execproto.HelloResult) { r.Mode = execproto.ModeHarness },
		"digest":    func(r *execproto.HelloResult) { r.ProfilesDigest = "sha256:" + strings.Repeat("b", 64) },
		"root path": func(r *execproto.HelloResult) { r.Roots[0].Path = "/" },
		"root mode": func(r *execproto.HelloResult) { r.Roots[1].Mode = execproto.RootRO },
		"extra root": func(r *execproto.HelloResult) {
			r.Roots = append(r.Roots, execproto.HelloRoot{Name: "home", Path: "/home", Mode: "rw"})
		},
		"missing one": func(r *execproto.HelloResult) { r.Roots = r.Roots[1:] },
	}
	for name, mod := range bad {
		r := good
		r.Roots = slices.Clone(good.Roots)
		mod(&r)
		if err := CheckHello(s, r); !errors.Is(err, ErrHelloMismatch) {
			t.Errorf("%s: CheckHello = %v, want a hello mismatch", name, err)
		}
	}
}

// TestFindScopes_OnlyThisHome guards the orphan sweep of A06 §13.5 at the
// scope level: below the user manager's cgroup it finds the scopes whose
// unit name carries this home's hash, and nothing else: not a second
// home's sandbox scope, not the user's other scopes and services, not a
// symlinked directory.
func TestFindScopes_OnlyThisHome(t *testing.T) {
	svc := t.TempDir()
	home, other := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	mine := filepath.Join(svc, "app.slice", sandbox.ScopeUnit(home, lcID))
	theirs := filepath.Join(svc, "app.slice", sandbox.ScopeUnit(other, lcID))
	for _, d := range []string{mine, theirs, filepath.Join(svc, "app.slice", "run-r1.scope"), filepath.Join(svc, "init.scope")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FindScopes(svc, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[lcID] != mine {
		t.Errorf("FindScopes = %v, want only %s", got, mine)
	}
	if got, err := FindScopes(filepath.Join(svc, "missing"), home); err != nil || len(got) != 0 {
		t.Errorf("missing service dir: %v, %v", got, err)
	}
}

// TestKillCgroup writes cgroup.kill when the kernel has it, kills each pid
// of cgroup.procs otherwise, never creates a file, and treats a collected
// scope as empty.
func TestKillCgroup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("101\n102\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var killed []int
	kill := func(p int) error { killed = append(killed, p); return nil }
	n, err := KillCgroup(dir, kill)
	if err != nil || n != 2 || !slices.Equal(killed, []int{101, 102}) {
		t.Errorf("without cgroup.kill: %d, %v, killed %v; want both pids killed", n, err, killed)
	}
	if _, err := os.Stat(filepath.Join(dir, "cgroup.kill")); !errors.Is(err, os.ErrNotExist) {
		t.Error("KillCgroup created cgroup.kill")
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	killed = nil
	if n, err := KillCgroup(dir, kill); err != nil || n != 2 || len(killed) != 0 {
		t.Errorf("with cgroup.kill: %d, %v, killed %v", n, err, killed)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "cgroup.kill")); string(b) != "1" {
		t.Errorf("cgroup.kill = %q, want 1", b)
	}
	if n, err := KillCgroup(filepath.Join(dir, "gone"), kill); n != 0 || err != nil {
		t.Errorf("collected scope: %d, %v", n, err)
	}
}

// TestReadCgroupCounters reads the scope's memory.events oom_kill and
// pids.events max (A06 §11.6, §7.4). A scope without the files (no
// delegation, or collected) has zero counters; a malformed file is an
// error, never a silent zero.
func TestReadCgroupCounters(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if c, err := ReadCgroupCounters(dir); err != nil || c != (CgroupCounters{}) {
		t.Errorf("no files: %+v, %v; want zero", c, err)
	}
	write("memory.events", "low 0\nhigh 0\nmax 3\noom 1\noom_kill 2\noom_group_kill 0\n")
	write("pids.events", "max 5\n")
	if c, err := ReadCgroupCounters(dir); err != nil || c != (CgroupCounters{OOMKill: 2, PidsMax: 5}) {
		t.Errorf("ReadCgroupCounters = %+v, %v; want oom_kill 2, pids max 5", c, err)
	}
	write("pids.events", "max lots\n")
	if _, err := ReadCgroupCounters(dir); err == nil {
		t.Error("malformed pids.events accepted")
	}
}

// TestClassifyEnd maps what the daemon observes of a sandbox's end to the
// A06 §7.4 rows it owns: memory (oom_kill rose), pids (pids max rose),
// disk (the free-space watchdog), and seccomp_kill (bwrap, PID 1 of the
// namespace, exits 128+SIGSYS = 159 when warden-exec died of the filter,
// or bwrap itself died of SIGSYS). tmp_full is not among them: the /tmp
// tmpfs exists only in the sandbox's mount namespace, and the daemon
// could reach it only through /proc/<pid>/root, which needs ptrace access
// that warden-exec's PR_SET_DUMPABLE 0 (D-062, M2-10) removes on purpose;
// the executor reports it from inside.
func TestClassifyEnd(t *testing.T) {
	base := CgroupCounters{OOMKill: 1, PidsMax: 4}
	exit0 := &RootExit{Exited: true, Code: 0}
	cases := []struct {
		name        string
		after       CgroupCounters
		root        *RootExit
		disk        bool
		kind        execproto.ResourceKind // "" : no resource
		seccompKill bool
	}{
		{"normal exit", base, exit0, false, "", false},
		{"still running", base, nil, false, "", false},
		{"oom kill", CgroupCounters{OOMKill: 2, PidsMax: 4}, exit0, false, execproto.ResourceMemory, false},
		{"pids max", CgroupCounters{OOMKill: 1, PidsMax: 9}, exit0, false, execproto.ResourcePids, false},
		{"memory before pids", CgroupCounters{OOMKill: 2, PidsMax: 9}, exit0, false, execproto.ResourceMemory, false},
		{"disk watchdog", base, nil, true, execproto.ResourceDisk, false},
		{"bwrap exit 159", base, &RootExit{Exited: true, Code: 159}, false, "", true},
		{"bwrap killed by SIGSYS", base, &RootExit{Signal: "SIGSYS"}, false, "", true},
		{"other exit code", base, &RootExit{Exited: true, Code: 137}, false, "", false},
		{"seccomp and oom", CgroupCounters{OOMKill: 3, PidsMax: 4}, &RootExit{Exited: true, Code: 159}, false, execproto.ResourceMemory, true},
	}
	for _, tc := range cases {
		e := ClassifyEnd(base, tc.after, tc.root, tc.disk)
		var kind execproto.ResourceKind
		if e.Resource != nil {
			kind = e.Resource.Kind
		}
		if kind != tc.kind || e.SeccompKill != tc.seccompKill {
			t.Errorf("%s: ClassifyEnd = %+v (kind %q), want kind %q seccomp_kill %v", tc.name, e, kind, tc.kind, tc.seccompKill)
		}
	}
}

func TestDiskLow(t *testing.T) {
	if !DiskLow(1023<<20, 1024) || DiskLow(1024<<20, 1024) || DiskLow(1, 0) {
		t.Error("DiskLow does not compare free bytes with the floor in MiB (a zero floor disables it)")
	}
}

// TestState_CheckBindsTheSession (round 1, item 6): a state file is
// accepted only in the session directory of the session it names, so a
// file moved into another session cannot have its destroy event written
// to, and its files removed from, the wrong session.
func TestState_CheckBindsTheSession(t *testing.T) {
	home := filepath.Join(t.TempDir(), "a")
	s := lcState(home)
	if err := s.Check(home, "01jaxr8q7m2v9ktc3f6yh5n0pb", lcID); err != nil {
		t.Errorf("Check in its own session dir (lower case, as on disk) = %v", err)
	}
	if err := s.Check(home, "01JAXR8Q7M2V9KTC3F6YH5OTHR", lcID); err == nil {
		t.Error("Check accepted a state file found in another session's directory")
	}
	s.BootID = ""
	if err := s.Check(home, "01jaxr8q7m2v9ktc3f6yh5n0pb", lcID); err == nil {
		t.Error("Check accepted a state file without a boot id")
	}
}
