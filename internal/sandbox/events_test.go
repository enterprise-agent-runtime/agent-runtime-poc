package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"warden.dev/warden/internal/execproto"
)

func sha(p string) string {
	h := sha256.Sum256([]byte(p))
	return "sha256:" + hex.EncodeToString(h[:])
}

// TestCreateEvent_CarriesWhatA06Lists pins sandbox.create (A06 §13.2):
// every Spec mount with the hash of its host path (a mask hashes the host
// path it hides, not /dev/null), the limits object exactly as the backend
// gave it (cgroup, seccomp version and missing flags included: the event
// is the only record of whether a cgroup enforced the limits, A06 §11.6),
// the env keys, the proxy as a socket hash and the forwarder port, and the
// profile digest. A missing limits object was a mutation the review asked
// to be caught.
func TestCreateEvent_CarriesWhatA06Lists(t *testing.T) {
	l := newLayout(t)
	s := validSpec(l, LevelL1)
	limits := CreateLimits{MemoryMB: 3072, Pids: 512, CPUs: 2, FsizeMB: 1024, TmpMB: 1024, Cgroup: false, Seccomp: "v1", MissingFlags: []string{"--size"}}
	keys := append(EnvKeys(), "PWD")
	ev, err := NewCreateEvent(s, "bwrap", limits, keys, s.Proxy.Socket, testDigest)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sandbox_id", "level", "backend", "purpose", "mounts", "limits", "env_keys", "proxy", "profile_digest"} {
		if _, ok := got[k]; !ok {
			t.Errorf("sandbox.create has no %q: %s", k, b)
		}
	}
	if got["sandbox_id"] != testID || got["level"] != "L1" || got["backend"] != "bwrap" || got["purpose"] != "task" || got["profile_digest"] != testDigest {
		t.Errorf("identity fields = %s", b)
	}
	lim, _ := got["limits"].(map[string]any)
	if lim["cgroup"] != false || lim["seccomp"] != "v1" || lim["memory_mb"] != float64(3072) {
		t.Errorf("limits = %v, want the backend's CreateLimits", lim)
	}
	if mf, _ := lim["missing_flags"].([]any); len(mf) != 1 || mf[0] != "--size" {
		t.Errorf("limits.missing_flags = %v, want [--size]", lim["missing_flags"])
	}
	if !slices.Equal(ev.EnvKeys, keys) {
		t.Errorf("env_keys = %v, want %v", ev.EnvKeys, keys)
	}
	if ev.Proxy.Socket != sha(s.Proxy.Socket) || ev.Proxy.Port != 3128 {
		t.Errorf("proxy = %+v, want the socket hash and port 3128", ev.Proxy)
	}
	ms, err := s.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Mounts) != len(ms) {
		t.Fatalf("%d event mounts for %d Spec mounts", len(ev.Mounts), len(ms))
	}
	byTarget := map[string]EventMount{}
	for _, m := range ev.Mounts {
		byTarget[m.SandboxPath] = m
		if m.Mode != "ro" && m.Mode != "rw" {
			t.Errorf("mount %s mode %q, want ro or rw (events schema)", m.SandboxPath, m.Mode)
		}
	}
	if m := byTarget["/work/.env"]; m.Kind != "mask" || m.HostPathHash != sha(filepath.Join(s.Worktree, ".env")) || m.Mode != "ro" {
		t.Errorf("mask mount = %+v, want kind mask, ro, the hash of the hidden host path", m)
	}
	if m := byTarget[TargetWorktree]; m.Kind != "worktree" || m.HostPathHash != sha(s.Worktree) || m.Mode != "rw" {
		t.Errorf("worktree mount = %+v", m)
	}
	if m := byTarget[TargetProxySocket]; m.Kind != "proxy" || m.Mode != "rw" {
		t.Errorf("proxy mount = %+v, want kind proxy reported rw (bound --bind; connect is not a schema mode)", m)
	}
	for _, m := range ev.Mounts {
		if m.HostPathHash == "" || len(m.HostPathHash) != len("sha256:")+64 {
			t.Errorf("mount %s host_path_hash %q", m.SandboxPath, m.HostPathHash)
		}
	}
}

// TestCreateEvent_RefusesIncompleteInput: no event without limits (a
// zero CreateLimits has no seccomp version), without env keys, or for a
// Spec that does not validate.
func TestCreateEvent_RefusesIncompleteInput(t *testing.T) {
	l := newLayout(t)
	good := CreateLimits{MemoryMB: 3072, Pids: 512, CPUs: 2, FsizeMB: 1024, TmpMB: 1024, Seccomp: "v1"}
	if _, err := NewCreateEvent(validSpec(l, LevelL1), "bwrap", CreateLimits{}, EnvKeys(), validSpec(l, LevelL1).Proxy.Socket, testDigest); err == nil {
		t.Error("NewCreateEvent accepted zero limits")
	}
	if _, err := NewCreateEvent(validSpec(l, LevelL1), "bwrap", good, nil, validSpec(l, LevelL1).Proxy.Socket, testDigest); err == nil {
		t.Error("NewCreateEvent accepted no env keys")
	}
	if _, err := NewCreateEvent(validSpec(l, LevelL1), "bwrap", good, EnvKeys(), validSpec(l, LevelL1).Proxy.Socket, "sha256:short"); err == nil {
		t.Error("NewCreateEvent accepted a malformed profile digest")
	}
	bad := validSpec(l, LevelL1)
	bad.ID = "nope"
	if _, err := NewCreateEvent(bad, "bwrap", good, EnvKeys(), bad.Proxy.Socket, testDigest); err == nil {
		t.Error("NewCreateEvent accepted an invalid Spec")
	}
}

// TestDestroyEvent_JSON pins sandbox.destroy (A06 §13.3 step 6): the
// session id routes the event to its chain and is not in the payload.
func TestDestroyEvent_JSON(t *testing.T) {
	b, err := json.Marshal(DestroyEvent{SessionID: "ses_X", SandboxID: testID, Reason: ReasonOrphan, KilledPIDs: 2, DurationMS: 7})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"sandbox_id":"` + testID + `","reason":"orphan","killed_pids":2,"duration_ms":7}`
	if string(b) != want {
		t.Errorf("sandbox.destroy = %s, want %s", b, want)
	}
}

// TestViolationEvent_JSON pins sandbox.violation (A06 §13.3 step 3,
// §11.5; events schema): kind and detail, the session id only routes it.
func TestViolationEvent_JSON(t *testing.T) {
	b, err := json.Marshal(ViolationEvent{SessionID: "ses_X", SandboxID: testID, Kind: ViolationRootCheck, Detail: "processes survived teardown"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"sandbox_id":"` + testID + `","kind":"root_check","detail":"processes survived teardown"}`
	if string(b) != want {
		t.Errorf("sandbox.violation = %s, want %s", b, want)
	}
}

// TestDestroyEvent_CarriesTheEnd: a sandbox that ended on a resource
// limit or a seccomp kill says so on sandbox.destroy (A06 §7.4 rows read
// by the daemon: memory.events, pids.events, the disk watchdog, bwrap's
// exit 159); an ordinary end adds nothing.
func TestDestroyEvent_CarriesTheEnd(t *testing.T) {
	b, err := json.Marshal(DestroyEvent{SandboxID: testID, Reason: ReasonResource, Resource: &execproto.WaitResource{Kind: execproto.ResourceMemory, Detail: "oom_kill"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"resource":{"kind":"memory","detail":"oom_kill"}`) {
		t.Errorf("sandbox.destroy = %s, want the resource kind", b)
	}
	b, _ = json.Marshal(DestroyEvent{SandboxID: testID, Reason: ReasonExecutorExit, SeccompKill: true})
	if !strings.Contains(string(b), `"seccomp_kill":true`) {
		t.Errorf("sandbox.destroy = %s, want seccomp_kill", b)
	}
	b, _ = json.Marshal(DestroyEvent{SandboxID: testID, Reason: ReasonTaskEnd})
	if strings.Contains(string(b), "resource") || strings.Contains(string(b), "seccomp") {
		t.Errorf("ordinary sandbox.destroy = %s carries an end detail", b)
	}
}
