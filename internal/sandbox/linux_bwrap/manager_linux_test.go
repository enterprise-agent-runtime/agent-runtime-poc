//go:build linux

package linux_bwrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"warden.dev/warden/internal/proxy"
	"warden.dev/warden/internal/sandbox"
)

// The L1 manager end to end (A06 §13): real bwrap, the real per-task proxy
// socket, and the stand-in executor (this test binary in helper mode at
// the executor path, serveMain) answering exec.hello and exec.shutdown.

// eventSink records sandbox events. At each sandbox.destroy it notes
// whether the proxy socket and state.json still existed, so a test can
// check the A06 §13.3 order (proxy closed, step 4, before the event, step
// 6). destroyed receives every destroy event.
type eventSink struct {
	mu         sync.Mutex
	creates    []sandbox.CreateEvent
	destroys   []sandbox.DestroyEvent
	atDestroy  []string // per destroy: paths that still existed
	watch      []string // paths checked at each destroy
	createErr  error
	destroyed  chan sandbox.DestroyEvent
	violations []sandbox.ViolationEvent
}

func (s *eventSink) SandboxViolation(_ context.Context, ev sandbox.ViolationEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.violations = append(s.violations, ev)
	return nil
}

func (s *eventSink) violationsSoFar() []sandbox.ViolationEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.violations)
}

func newSink(watch ...string) *eventSink {
	return &eventSink{watch: watch, destroyed: make(chan sandbox.DestroyEvent, 16)}
}

func (s *eventSink) SandboxCreate(_ context.Context, ev sandbox.CreateEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	s.creates = append(s.creates, ev)
	return nil
}

func (s *eventSink) SandboxDestroy(_ context.Context, ev sandbox.DestroyEvent) error {
	s.mu.Lock()
	for _, p := range s.watch {
		if _, err := os.Lstat(p); err == nil {
			s.atDestroy = append(s.atDestroy, p)
		}
	}
	s.destroys = append(s.destroys, ev)
	s.mu.Unlock()
	s.destroyed <- ev
	return nil
}

func (s *eventSink) snapshot() ([]sandbox.CreateEvent, []sandbox.DestroyEvent, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.creates), slices.Clone(s.destroys), slices.Clone(s.atDestroy)
}

type nullProxyEvents struct{}

func (nullProxyEvents) ProxyConnect(context.Context, proxy.ConnectEvent) error { return nil }
func (nullProxyEvents) ProxyDenied(context.Context, proxy.DeniedEvent) error   { return nil }

func proxyConfig(*sandbox.Spec) (proxy.Config, error) {
	al, err := proxy.NewAllowlist()
	if err != nil {
		return proxy.Config{}, err
	}
	return proxy.Config{Allow: al, Events: nullProxyEvents{}, InterfaceAddrs: func() ([]netip.Addr, error) { return nil, nil }}, nil
}

// managerFor builds a Manager for the Spec's home with the given cgroup
// status (the zero value is the rlimit fallback).
func managerFor(t *testing.T, s *sandbox.Spec, f Features, cg sandbox.CgroupStatus, sink sandbox.EventSink) *Manager {
	t.Helper()
	bwrap, _ := hostBwrap(t)
	m, err := New(Config{WardenHome: s.WardenHome, Bwrap: bwrap, Features: f, Probes: DefaultProbes(), Cgroup: cg,
		Events: sink, ProxyConfig: proxyConfig, CPUSeconds: 60, MaxProcs: 8, DaemonVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func helperSpec(t *testing.T, mode string) *sandbox.Spec {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return realLayout(t, self, helperMarker+`"`+mode+`"}`+"\n")
}

func sbFile(s *sandbox.Spec, name string) string {
	return filepath.Join(filepath.Dir(s.ProfilesFile), name)
}

// ownerOnlyFile requires a regular file of the user's with no group or
// other access.
func ownerOnlyFile(t *testing.T, p string) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Errorf("%s: %v", p, err)
		return
	}
	st := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 || int(st.Uid) != os.Getuid() {
		t.Errorf("%s is %v uid %d, want a regular owner-only file of uid %d", p, fi.Mode(), st.Uid, os.Getuid())
	}
}

func gone(p string) bool {
	_, err := os.Lstat(p)
	return errors.Is(err, fs.ErrNotExist)
}

func waitDestroy(t *testing.T, sink *eventSink) sandbox.DestroyEvent {
	t.Helper()
	select {
	case ev := <-sink.destroyed:
		return ev
	case <-time.After(30 * time.Second):
		t.Fatal("no sandbox.destroy")
	}
	return sandbox.DestroyEvent{}
}

// TestManager_CreateDestroy is A06 §13.2 and §13.3 on the rlimit fallback
// (no cgroup), the path WSL2 and any host without delegation take:
//
//   - sandbox.create is written once, after exec.hello, with the limits
//     (cgroup false recorded, seccomp v1, the missing bwrap flags), the env
//     keys including PWD, the socket bound and profile_digest = sha256 of
//     argv.json; the argv carries the RLIMIT_DATA/RLIMIT_NPROC fallback;
//   - state.json, argv.json and the generated /etc files are owner-only
//     (0600, the user's), and state.json names the live root;
//   - Destroy closes the proxy listener (its socket is gone) before
//     sandbox.destroy, removes state.json and /etc, keeps argv.json, and
//     leaves no process.
func TestManager_CreateDestroy(t *testing.T) {
	_, f := hostBwrap(t)
	s := helperSpec(t, "serve")
	sink := newSink(s.Proxy.Socket, sbFile(s, StateFile))
	m := managerFor(t, s, f, sandbox.CgroupStatus{}, sink)
	ctx := context.Background()
	h, err := m.Create(ctx, s)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	creates, destroys, _ := sink.snapshot()
	if len(creates) != 1 || len(destroys) != 0 {
		t.Fatalf("events after Create: %d create, %d destroy; want 1, 0", len(creates), len(destroys))
	}
	ev := creates[0]
	if ev.SandboxID != s.ID || ev.SessionID != s.SessionID || ev.Backend != "bwrap" || ev.Level != sandbox.LevelL1 {
		t.Errorf("sandbox.create identity = %+v", ev)
	}
	want := Limits(s.Limits, false, f)
	if ev.Limits.Cgroup || ev.Limits.Seccomp != "v1" || ev.Limits.MemoryMB != s.Limits.MemoryMB || !slices.Equal(ev.Limits.MissingFlags, f.Missing()) {
		t.Errorf("sandbox.create limits = %+v, want %+v (cgroup false: the fallback is recorded)", ev.Limits, want)
	}
	if !slices.Contains(ev.EnvKeys, "PWD") || !slices.Equal(ev.EnvKeys, h.EnvKeys) {
		t.Errorf("env_keys = %v (handle %v), want the allowlist and PWD", ev.EnvKeys, h.EnvKeys)
	}
	if ev.Proxy.Socket != sandbox.PathHash(s.Proxy.Socket) || ev.Proxy.Port != 3128 {
		t.Errorf("proxy = %+v", ev.Proxy)
	}
	argv, err := os.ReadFile(sbFile(s, ArgvFile))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(argv)
	if ev.ProfileDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("profile_digest %s is not the sha256 of argv.json", ev.ProfileDigest)
	}
	var args []string
	if err := json.Unmarshal(argv, &args); err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(args, "--child-limits"); i < 0 || !strings.Contains(args[i+1], ",data=") || !strings.Contains(args[i+1], ",nproc=") {
		t.Errorf("argv child limits lack the rlimit fallback: %q", args)
	}
	for _, p := range []string{sbFile(s, StateFile), sbFile(s, ArgvFile)} {
		ownerOnlyFile(t, p)
	}
	for _, n := range EtcFileNames() {
		ownerOnlyFile(t, filepath.Join(sbFile(s, EtcDir), n))
	}
	raw, err := os.ReadFile(sbFile(s, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	st, err := ParseState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Check(s.WardenHome, filepath.Base(s.SessionDir), s.ID); err != nil || st.ScopeUnit != "" || st.ChildPID <= 1 {
		t.Errorf("state.json = %+v (%v), want this sandbox, no scope, a child pid", st, err)
	}
	if t0, err := procStartTime(st.RootPID); err != nil || t0 != st.RootStartTime {
		t.Errorf("recorded root %d is not alive with its start time: %v", st.RootPID, err)
	}
	if _, ok := m.Client(s.ID); !ok {
		t.Error("no executor client for the ready sandbox")
	}

	if err := m.Destroy(ctx, s.ID, sandbox.ReasonTaskEnd); err != nil {
		t.Fatalf("Destroy = %v", err)
	}
	dev := waitDestroy(t, sink)
	_, _, still := sink.snapshot()
	if dev.SandboxID != s.ID || dev.SessionID != s.SessionID || dev.Reason != sandbox.ReasonTaskEnd {
		t.Errorf("sandbox.destroy = %+v", dev)
	}
	if len(still) != 0 {
		t.Errorf("at sandbox.destroy these still existed: %v (the proxy socket must be closed and state.json removed first, A06 §13.3)", still)
	}
	for _, p := range []string{sbFile(s, StateFile), sbFile(s, EtcDir), s.Proxy.Socket} {
		if !gone(p) {
			t.Errorf("%s left after Destroy", p)
		}
	}
	if gone(sbFile(s, ArgvFile)) {
		t.Error("argv.json removed; it stays until session retention (A06 §13.3 step 5)")
	}
	if _, err := procStartTime(st.RootPID); err == nil {
		t.Errorf("sandbox root %d still exists after Destroy", st.RootPID)
	}
	if err := m.Destroy(ctx, s.ID, sandbox.ReasonTaskEnd); !errors.Is(err, sandbox.ErrUnknownSandbox) {
		t.Errorf("second Destroy = %v, want ErrUnknownSandbox", err)
	}
}

// TestManager_CreateInScope is the cgroup path (A06 §11.6): the launch
// runs in the scope warden-<home hash>-<sb_id>.scope, sandbox.create
// records cgroup true, the argv drops the rlimit fallback, state.json
// names the scope, and Destroy leaves no process. Where cgroup v2 does
// not delegate (WSL2's hybrid mount) the delegation half of the status is
// forced, so the wrapped launch itself is still exercised.
func TestManager_CreateInScope(t *testing.T) {
	_, f := hostBwrap(t)
	cg, _ := hostScope(t)
	delegated := cg.Usable()
	if !delegated && requireCgroup() {
		t.Fatalf("WARDEN_CI_REQUIRE_CGROUP=1 but cgroup v2 delegation is not usable: %s", sandbox.CgroupCheck(cg).Detail)
	}
	if !delegated {
		t.Logf("cgroup v2 delegation not present here (%s); forcing it to test the wrapped launch", sandbox.CgroupCheck(cg).Detail)
		cg.Unified, cg.Controllers, cg.ControllersErr = true, sandbox.CgroupControllers, nil
	}
	s := helperSpec(t, "serve")
	sink := newSink(s.Proxy.Socket)
	m := managerFor(t, s, f, cg, sink)
	if !m.Cgroup() {
		t.Fatal("manager not in cgroup mode with a usable status")
	}
	if _, err := m.Create(context.Background(), s); err != nil {
		t.Fatalf("Create = %v", err)
	}
	creates, _, _ := sink.snapshot()
	if len(creates) != 1 || !creates[0].Limits.Cgroup {
		t.Fatalf("sandbox.create = %+v, want limits.cgroup true", creates)
	}
	raw, err := os.ReadFile(sbFile(s, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	st, err := ParseState(raw)
	if err != nil {
		t.Fatal(err)
	}
	unit := sandbox.ScopeUnit(s.WardenHome, s.ID)
	if st.ScopeUnit != unit {
		t.Errorf("state scope = %q, want %q", st.ScopeUnit, unit)
	}
	cgroups, err := os.ReadFile("/proc/" + strconv.Itoa(st.RootPID) + "/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cgroups), "/"+unit+"\n") {
		t.Errorf("sandbox root is not in %s:\n%s", unit, cgroups)
	}
	// The limits the scope actually enforces (round 1, item 4): A06 §12
	// memory 3 GiB with no swap, 512 tasks, 2 CPUs.
	if delegated {
		dir := filepath.Join(sandbox.CgroupRoot, ParseProcCgroup(string(cgroups)))
		for file, want := range map[string]string{
			"memory.max":      strconv.Itoa(s.Limits.MemoryMB << 20),
			"memory.swap.max": "0",
			"pids.max":        strconv.Itoa(s.Limits.Pids),
			"cpu.max":         strconv.Itoa(s.Limits.CPUs*100000) + " 100000",
		} {
			b, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil || strings.TrimSpace(string(b)) != want {
				t.Errorf("scope %s = %q (%v), want %q", file, strings.TrimSpace(string(b)), err, want)
			}
		}
	} else {
		t.Log("limit files not checked: delegation was forced")
	}
	argv, _ := os.ReadFile(sbFile(s, ArgvFile))
	if strings.Contains(string(argv), ",data=") || strings.Contains(string(argv), ",nproc=") {
		t.Error("argv carries the rlimit fallback although a cgroup holds the limits")
	}
	if err := m.Destroy(context.Background(), s.ID, sandbox.ReasonCancel); err != nil {
		t.Fatalf("Destroy = %v", err)
	}
	waitDestroy(t, sink)
	if _, err := procStartTime(st.RootPID); err == nil {
		t.Errorf("sandbox root %d still exists after Destroy", st.RootPID)
	}
}

// TestManager_FailureClosesTheProxy guards the M2-12a wiring rule: the
// proxy listener opened at the start of Create is closed, and its socket
// removed, on every failure path; files Create wrote are removed (argv.json
// excepted); nothing is left live.
//
//   - before launch (a bwrap version Prepare refuses): no event at all,
//     since no process ran;
//   - after launch, hello mismatch: the sandbox is torn down and its one
//     sandbox.destroy says hello_mismatch; no sandbox.create;
//   - after launch, sandbox.create cannot be written: torn down with
//     store_unavailable.
func TestManager_FailureClosesTheProxy(t *testing.T) {
	_, f := hostBwrap(t)
	cases := []struct {
		name     string
		mode     string
		features Features
		sinkErr  error
		reason   sandbox.DestroyReason // "" : no destroy event
	}{
		{"before launch", "serve", Features{Version: "0.0.1"}, nil, ""},
		{"hello mismatch", "mismatch", f, nil, sandbox.ReasonHelloMismatch},
		{"create event lost", "serve", f, errors.New("injected store failure"), sandbox.ReasonStoreUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := helperSpec(t, tc.mode)
			sink := newSink(s.Proxy.Socket)
			sink.createErr = tc.sinkErr
			m := managerFor(t, s, f, sandbox.CgroupStatus{}, sink)
			m.cfg.Features = tc.features
			h, err := m.Create(context.Background(), s)
			if err == nil || h != nil {
				t.Fatalf("Create = %v, %v; want a failure", h, err)
			}
			if !gone(s.Proxy.Socket) {
				t.Errorf("proxy socket %s left after a failed Create (listener not closed)", s.Proxy.Socket)
			}
			for _, p := range []string{sbFile(s, StateFile), sbFile(s, EtcDir)} {
				if !gone(p) {
					t.Errorf("%s left after a failed Create", p)
				}
			}
			if _, ok := m.Client(s.ID); ok {
				t.Error("failed sandbox is live")
			}
			creates, destroys, still := sink.snapshot()
			if len(creates) != 0 {
				t.Errorf("sandbox.create written for a failed sandbox: %+v", creates)
			}
			switch {
			case tc.reason == "" && len(destroys) != 0:
				t.Errorf("sandbox.destroy %+v although no process ran", destroys)
			case tc.reason != "" && (len(destroys) != 1 || destroys[0].Reason != tc.reason):
				t.Errorf("sandbox.destroy = %+v, want one with reason %s", destroys, tc.reason)
			}
			if len(still) != 0 {
				t.Errorf("at sandbox.destroy the proxy socket still existed: %v", still)
			}
			if tc.reason == sandbox.ReasonHelloMismatch && !errors.Is(err, ErrHelloMismatch) {
				t.Errorf("error %v is not a hello mismatch", err)
			}
		})
	}
}

// TestManager_ExecutorExitWatchdog is the A06 §13.4 executor-EOF row: an
// executor that exits on its own (here right after the hello) takes the
// sandbox to killing without anyone calling Destroy, with one
// sandbox.destroy{executor_exit}, the proxy closed first.
func TestManager_ExecutorExitWatchdog(t *testing.T) {
	_, f := hostBwrap(t)
	s := helperSpec(t, "exit")
	sink := newSink(s.Proxy.Socket)
	m := managerFor(t, s, f, sandbox.CgroupStatus{}, sink)
	if _, err := m.Create(context.Background(), s); err != nil {
		t.Fatalf("Create = %v", err)
	}
	ev := waitDestroy(t, sink)
	if ev.Reason != sandbox.ReasonExecutorExit || ev.SandboxID != s.ID {
		t.Errorf("sandbox.destroy = %+v, want executor_exit", ev)
	}
	_, _, still := sink.snapshot()
	if len(still) != 0 || !gone(sbFile(s, StateFile)) {
		t.Errorf("after the watchdog: still %v at the event, state.json gone %v", still, gone(sbFile(s, StateFile)))
	}
	if _, ok := m.Client(s.ID); ok {
		t.Error("sandbox still live after its executor exited")
	}
}

// abandon drops a live sandbox from the manager without tearing it down,
// as a daemon crash would (tests only). The caller owns the box.
func (m *Manager) abandon(id string) *box {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.live[id]
	delete(m.live, id)
	close(b.stop)
	return b
}

// TestManager_RecoverOrphans_OnlyThisHome is A06 §13.5: a daemon of home A
// that restarts finds the sandbox its previous run left (state.json under
// A's sessions), kills its root (same pid, same start time), writes
// sandbox.destroy{orphan} to its session and removes its owner files. A
// sandbox of a second home B, with the same sandbox id, is neither read
// nor touched: its root stays alive and its state.json stays, even when a
// stale state file of A names B's root pid (pid reuse: the start time
// differs, so it is not killed).
func TestManager_RecoverOrphans_OnlyThisHome(t *testing.T) {
	_, f := hostBwrap(t)
	sa, sb := helperSpec(t, "serve"), helperSpec(t, "serve")
	sinkA, sinkB := newSink(), newSink()
	ma, mb := managerFor(t, sa, f, sandbox.CgroupStatus{}, sinkA), managerFor(t, sb, f, sandbox.CgroupStatus{}, sinkB)
	for _, x := range []struct {
		m *Manager
		s *sandbox.Spec
	}{{ma, sa}, {mb, sb}} {
		if _, err := x.m.Create(context.Background(), x.s); err != nil {
			t.Fatalf("Create = %v", err)
		}
	}
	defer func() {
		if err := mb.Destroy(context.Background(), sb.ID, sandbox.ReasonTaskEnd); err != nil {
			t.Errorf("Destroy B = %v", err)
		}
	}()
	readState := func(s *sandbox.Spec) State {
		raw, err := os.ReadFile(sbFile(s, StateFile))
		if err != nil {
			t.Fatal(err)
		}
		st, err := ParseState(raw)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	stA, stB := readState(sa), readState(sb)
	// A stale state file of home A whose pid has since been reused, here by
	// home B's live root: same pid, another start time. It must be cleaned
	// up as an orphan without killing that process (A06 §13.5: pid and
	// start time).
	staleID := "sb_01JAXS2Q7M2V9KTC3F6YH5STAL"
	staleDir := filepath.Join(sa.WardenHome, "sessions", "01jaxr8q7m2v9ktc3f6yh5stal", "sandbox", staleID)
	stale := State{SandboxID: staleID, SessionID: "ses_01JAXR8Q7M2V9KTC3F6YH5STAL", HomeHash: stA.HomeHash, Backend: Backend,
		RootPID: stB.RootPID, RootStartTime: stB.RootStartTime + 1, ChildPID: stB.ChildPID, BootID: stA.BootID}
	raw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, StateFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Round 1, items 6 and 7: three more files of home A that name B's
	// live root exactly (pid and start time). None may get it killed:
	//   - one recorded in an earlier boot (pid and start time are unique
	//     only within a boot): cleaned up as an orphan, nothing killed;
	//   - one found in the directory of another session than it names:
	//     skipped;
	//   - one reached through a symlinked sessions/*/sandbox directory that
	//     points outside the home: skipped, never read.
	writeState := func(dir string, st State) {
		t.Helper()
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, StateFile), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bLive := func(id, session, boot string) State {
		return State{SandboxID: id, SessionID: session, HomeHash: stA.HomeHash, Backend: Backend,
			RootPID: stB.RootPID, RootStartTime: stB.RootStartTime, ChildPID: stB.ChildPID, BootID: boot}
	}
	sessions := filepath.Join(sa.WardenHome, "sessions")
	bootID := "sb_01JAXS2Q7M2V9KTC3F6YH5BOOT"
	writeState(filepath.Join(sessions, "01jaxr8q7m2v9ktc3f6yh5boot", "sandbox", bootID),
		bLive(bootID, "ses_01JAXR8Q7M2V9KTC3F6YH5BOOT", "00000000-0000-0000-0000-000000000000"))
	mispID := "sb_01JAXS2Q7M2V9KTC3F6YH5MISP"
	writeState(filepath.Join(sessions, "01jaxr8q7m2v9ktc3f6yh5misp", "sandbox", mispID),
		bLive(mispID, "ses_01JAXR8Q7M2V9KTC3F6YH5OTHR", stA.BootID))
	linkID := "sb_01JAXS2Q7M2V9KTC3F6YH5LINK"
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	writeState(filepath.Join(elsewhere, linkID), bLive(linkID, "ses_01JAXR8Q7M2V9KTC3F6YH5LINK", stA.BootID))
	if err := os.MkdirAll(filepath.Join(sessions, "01jaxr8q7m2v9ktc3f6yh5link"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(sessions, "01jaxr8q7m2v9ktc3f6yh5link", "sandbox")); err != nil {
		t.Fatal(err)
	}
	crashed := ma.abandon(sa.ID)
	defer func() { crashed.client.Close(); crashed.proxy.Close(); crashed.cancel() }()

	sinkA2 := newSink()
	restarted := managerFor(t, sa, f, sandbox.CgroupStatus{}, sinkA2)
	rec, err := restarted.RecoverOrphans(context.Background())
	if err != nil {
		t.Fatalf("RecoverOrphans = %v", err)
	}
	got := slices.Sorted(slices.Values(rec.Sandboxes))
	if want := slices.Sorted(slices.Values([]string{sa.ID, staleID, bootID})); !slices.Equal(got, want) || len(rec.Skipped) != 2 {
		t.Errorf("recovered %+v, want A's three sandboxes, the misplaced state and the symlinked directory skipped", rec)
	}
	if gone(filepath.Join(elsewhere, linkID, StateFile)) {
		t.Error("the state file behind the symlinked sandbox directory was removed")
	}
	select {
	case <-crashed.run.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the orphaned root was not killed")
	}
	evs := map[string]sandbox.DestroyEvent{}
	for range 3 {
		ev := waitDestroy(t, sinkA2)
		evs[ev.SandboxID] = ev
	}
	if ev := evs[sa.ID]; ev.Reason != sandbox.ReasonOrphan || ev.SessionID != stA.SessionID || ev.KilledPIDs < 1 {
		t.Errorf("sandbox.destroy = %+v, want orphan for A's session with the root counted", ev)
	}
	if ev := evs[staleID]; ev.Reason != sandbox.ReasonOrphan || ev.SessionID != stale.SessionID || ev.KilledPIDs != 0 {
		t.Errorf("stale sandbox.destroy = %+v, want orphan with nothing killed", ev)
	}
	if ev := evs[bootID]; ev.Reason != sandbox.ReasonOrphan || ev.KilledPIDs != 0 {
		t.Errorf("earlier-boot sandbox.destroy = %+v, want orphan with nothing killed", ev)
	}
	if !gone(sbFile(sa, StateFile)) || !gone(sbFile(sa, EtcDir)) || !gone(filepath.Join(staleDir, StateFile)) {
		t.Error("A's owner files left after recovery")
	}
	if t0, err := procStartTime(stB.RootPID); err != nil || t0 != stB.RootStartTime {
		t.Errorf("home B's sandbox root %d was touched by A's recovery: %v", stB.RootPID, err)
	}
	if gone(sbFile(sb, StateFile)) {
		t.Error("home B's state.json was removed by A's recovery")
	}
	if _, destroys, _ := sinkB.snapshot(); len(destroys) != 0 {
		t.Errorf("home B got sandbox.destroy %+v", destroys)
	}
}
