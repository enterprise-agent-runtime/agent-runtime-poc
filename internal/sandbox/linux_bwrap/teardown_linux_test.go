//go:build linux

package linux_bwrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/sandbox"
)

// The teardown and watchdog rows of review round 1 (M2-15): what an
// executor claims, a root that never dies, and the A06 §7.4 rows the
// daemon reads at the end of a sandbox.

// TestManager_ShutdownCountIsBounded (round 1, item 1; CWE-20/345): the
// executor's exec.shutdown killed_pids is its claim. A hostile executor
// answered -2^62 and the destroy event carried it, below the schema's
// minimum 0, so a strict store would refuse the only sandbox.destroy. The
// count is clamped to [0, pids limit]; what the daemon itself kills is
// added (bwrap, at most one more).
func TestManager_ShutdownCountIsBounded(t *testing.T) {
	_, f := hostBwrap(t)
	for _, mode := range []string{"neg", "huge"} {
		t.Run(mode, func(t *testing.T) {
			s := helperSpec(t, mode)
			sink := newSink()
			m := managerFor(t, s, f, sandbox.CgroupStatus{}, sink)
			if _, err := m.Create(context.Background(), s); err != nil {
				t.Fatalf("Create = %v", err)
			}
			if err := m.Destroy(context.Background(), s.ID, sandbox.ReasonTaskEnd); err != nil {
				t.Fatalf("Destroy = %v", err)
			}
			ev := waitDestroy(t, sink)
			if ev.KilledPIDs < 0 || ev.KilledPIDs > s.Limits.Pids+1 {
				t.Errorf("killed_pids = %d after the executor claimed a %s count, want within [0, %d]", ev.KilledPIDs, mode, s.Limits.Pids+1)
			}
		})
	}
}

// TestManager_NoShutdownWithoutHello (round 1, item 8): an executor that
// never completed exec.hello is not sent exec.shutdown (a failing Create
// used to wait out the shutdown bound as well), and the failure is
// recorded as a timeout, not executor_exit. The stand-in in "slow" mode
// writes /scratch/got-shutdown if a shutdown ever reaches it.
func TestManager_NoShutdownWithoutHello(t *testing.T) {
	_, f := hostBwrap(t)
	s := helperSpec(t, "slow")
	sink := newSink(s.Proxy.Socket)
	m := managerFor(t, s, f, sandbox.CgroupStatus{}, sink)
	m.t.hello = 500 * time.Millisecond
	if _, err := m.Create(context.Background(), s); err == nil {
		t.Fatal("Create succeeded without an exec.hello answer")
	}
	_, destroys, still := sink.snapshot()
	if len(destroys) != 1 || destroys[0].Reason != sandbox.ReasonTimeout {
		t.Errorf("sandbox.destroy = %+v, want one with reason timeout", destroys)
	}
	if len(still) != 0 || !gone(s.Proxy.Socket) {
		t.Errorf("proxy socket still there (at the event: %v)", still)
	}
	if !gone(filepath.Join(s.Scratch, "got-shutdown")) {
		t.Error("exec.shutdown was sent to an executor that never answered exec.hello")
	}
}

// fakeRoot is a sandbox root for teardown tests. done is closed by the
// test, or never (a root that survives every kill).
type fakeRoot struct {
	done  chan struct{}
	exit  *RootExit
	kills atomic.Int32
}

func (r *fakeRoot) Pid() int              { return 1 << 22 }
func (r *fakeRoot) Done() <-chan struct{} { return r.done }
func (r *fakeRoot) Kill() error           { r.kills.Add(1); return nil }
func (r *fakeRoot) Exit() (RootExit, bool) {
	select {
	case <-r.done:
		if r.exit != nil {
			return *r.exit, true
		}
	default:
	}
	return RootExit{}, false
}

// fakeBox is a live box around a fake root, with a sandbox directory on
// disk and, when cg is set, a scope cgroup directory with event files.
func fakeBox(t *testing.T, m *Manager, r *fakeRoot, cg string) *box {
	t.Helper()
	_, cancel := context.WithCancel(context.Background())
	b := &box{
		spec:   sandbox.Spec{ID: lcID, SessionID: "ses_01JAXR8Q7M2V9KTC3F6YH5N0PB", Limits: sandbox.DefaultLimits()},
		run:    r,
		cancel: cancel, sbDir: t.TempDir(), stop: make(chan struct{}), cgroupDir: cg, stderr: &capBuffer{max: 64},
	}
	m.mu.Lock()
	m.live[b.spec.ID] = b
	m.mu.Unlock()
	return b
}

func fakeManager(t *testing.T, sink sandbox.EventSink) *Manager {
	t.Helper()
	m, err := New(Config{WardenHome: t.TempDir(), Bwrap: "/usr/bin/bwrap", Events: sink, ProxyConfig: proxyConfig, CPUSeconds: 60, MaxProcs: 8})
	if err != nil {
		t.Fatal(err)
	}
	m.t.killVerify, m.t.killRounds = 10*time.Millisecond, 3
	return m
}

// TestTeardown_RootThatSurvivesIsAViolation (round 1, item 2): A06 §13.3
// step 3 requires sandbox.violation{root_check, "processes survived
// teardown"} when processes remain after the kill; teardown then keeps
// killing for a bounded number of rounds and still writes sandbox.destroy.
// It used to wait on the context, which for the watchdog never ends.
func TestTeardown_RootThatSurvivesIsAViolation(t *testing.T) {
	sink := newSink()
	m := fakeManager(t, sink)
	r := &fakeRoot{done: make(chan struct{})} // never closed
	fakeBox(t, m, r, "")
	done := make(chan error, 1)
	go func() { done <- m.Destroy(context.Background(), lcID, sandbox.ReasonTaskEnd) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("teardown blocked on a root that never ends")
	}
	if !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Destroy = %v, want the survival reported", err)
	}
	vs := sink.violationsSoFar()
	if len(vs) != 1 || vs[0].Kind != sandbox.ViolationRootCheck || vs[0].Detail != "processes survived teardown" || vs[0].SandboxID != lcID {
		t.Errorf("violations = %+v, want one root_check", vs)
	}
	if _, destroys, _ := sink.snapshot(); len(destroys) != 1 {
		t.Errorf("sandbox.destroy written %d times, want once", len(destroys))
	}
	if n := r.kills.Load(); n != int32(1+m.t.killRounds) {
		t.Errorf("root killed %d times, want 1 + %d rounds", n, m.t.killRounds)
	}
}

// TestWatch_ClassifiesTheEnd is the A06 §7.4 rows the daemon owns, read
// when the executor ends on its own (round 1, item 3; PROGRESS M2-8 "left
// for M2-15"): the scope's memory.events oom_kill and pids.events max
// rising since launch make the end resource/memory and resource/pids;
// bwrap's exit 159 (warden-exec killed by SIGSYS) is seccomp_kill and a
// sandbox.violation{seccomp}; the free-space watchdog ends a running
// sandbox as resource/disk. A plain exit stays executor_exit. tmp_full is
// the executor's (TestClassifyEnd says why).
func TestWatch_ClassifiesTheEnd(t *testing.T) {
	cases := []struct {
		name      string
		mem, pids string // the scope's files at the end; "" : no scope
		exit      *RootExit
		lowDisk   bool
		reason    sandbox.DestroyReason
		kind      execproto.ResourceKind
		seccomp   bool
	}{
		{"plain exit", "", "", &RootExit{Exited: true}, false, sandbox.ReasonExecutorExit, "", false},
		{"oom kill", "oom 1\noom_kill 1\n", "max 0\n", &RootExit{Exited: true, Code: 137}, false, sandbox.ReasonResource, execproto.ResourceMemory, false},
		{"pids max", "oom_kill 0\n", "max 3\n", &RootExit{Exited: true, Code: 1}, false, sandbox.ReasonResource, execproto.ResourcePids, false},
		{"seccomp kill", "", "", &RootExit{Exited: true, Code: 159}, false, sandbox.ReasonExecutorExit, "", true},
		{"disk floor", "", "", nil, true, sandbox.ReasonResource, execproto.ResourceDisk, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := newSink()
			m := fakeManager(t, sink)
			r := &fakeRoot{done: make(chan struct{}), exit: tc.exit}
			cg := ""
			if tc.mem != "" {
				cg = t.TempDir()
				for name, s := range map[string]string{"memory.events": "oom_kill 0\n", "pids.events": "max 0\n", "cgroup.kill": "", "cgroup.procs": ""} {
					if err := os.WriteFile(filepath.Join(cg, name), []byte(s), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			b := fakeBox(t, m, r, cg)
			if cg != "" {
				var err error
				if b.counters, err = ReadCgroupCounters(cg); err != nil {
					t.Fatal(err)
				}
				os.WriteFile(filepath.Join(cg, "memory.events"), []byte(tc.mem), 0o600)
				os.WriteFile(filepath.Join(cg, "pids.events"), []byte(tc.pids), 0o600)
			}
			if tc.lowDisk {
				m.t.diskEvery = 5 * time.Millisecond
				m.cfg.FreeBytes = func(string) (uint64, error) { return 1 << 20, nil }
				go func() { <-b.stop; close(r.done) }() // the kill ends the root
			} else {
				m.cfg.FreeBytes = func(string) (uint64, error) { return 1 << 40, nil }
				close(r.done)
			}
			go m.watch(b)
			ev := waitDestroy(t, sink)
			var kind execproto.ResourceKind
			if ev.Resource != nil {
				kind = ev.Resource.Kind
			}
			if ev.Reason != tc.reason || kind != tc.kind || ev.SeccompKill != tc.seccomp {
				t.Errorf("sandbox.destroy = %+v (kind %q), want reason %s kind %q seccomp_kill %v", ev, kind, tc.reason, tc.kind, tc.seccomp)
			}
			vs := sink.violationsSoFar()
			if tc.seccomp != (len(vs) == 1 && vs[0].Kind == sandbox.ViolationSeccomp) || (!tc.seccomp && len(vs) != 0) {
				t.Errorf("violations = %+v, want a seccomp violation only for the seccomp kill", vs)
			}
		})
	}
}
