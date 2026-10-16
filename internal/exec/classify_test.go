package exec

import (
	"testing"
	"time"

	"warden.dev/warden/internal/execproto"
)

// TestClassify is the table of design A06 §7.4 (exec.proc.wait.reason).
// The agent loop ends an execution with failed(resource) when reason is
// resource, and the escape check (WRD-16 §10.7 row 5) relies on it; a
// SIGSYS is the seccomp KILL class (§11.5) and must be reported as such,
// never as an ordinary signal or exit, because it is the trace of a
// blocked escape attempt. A kill the executor itself started (the call's
// timeout, a cancel, a shutdown) is reported as that, not as the signal
// that carried it.
func TestClassify(t *testing.T) {
	type want struct {
		reason   execproto.WaitReason
		resource execproto.ResourceKind
		seccomp  bool
	}
	cases := map[string]struct {
		o exitObs
		w want
	}{
		"exit 0":                 {exitObs{Exited: true, Code: 0}, want{execproto.ReasonExit, "", false}},
		"exit 1":                 {exitObs{Exited: true, Code: 1}, want{execproto.ReasonExit, "", false}},
		"SIGSEGV":                {exitObs{Signal: "SIGSEGV"}, want{execproto.ReasonSignal, "", false}},
		"SIGSYS":                 {exitObs{Signal: "SIGSYS"}, want{execproto.ReasonSignal, "", true}},
		"SIGSYS while cancelled": {exitObs{Signal: "SIGSYS", Killed: killCancel}, want{execproto.ReasonSignal, "", true}},
		"SIGXFSZ":                {exitObs{Signal: "SIGXFSZ"}, want{execproto.ReasonResource, execproto.ResourceFsize, false}},
		"SIGXCPU":                {exitObs{Signal: "SIGXCPU"}, want{execproto.ReasonResource, execproto.ResourceCPU, false}},
		"SIGKILL at the CPU limit": {exitObs{Signal: "SIGKILL", CPUTime: 4 * time.Second, CPULimit: 4},
			want{execproto.ReasonResource, execproto.ResourceCPU, false}},
		"SIGKILL under the CPU limit": {exitObs{Signal: "SIGKILL", CPUTime: 3 * time.Second, CPULimit: 4},
			want{execproto.ReasonSignal, "", false}},
		"timeout, TERM":          {exitObs{Signal: "SIGTERM", Killed: killTimeout}, want{execproto.ReasonTimeout, "", false}},
		"timeout, exit":          {exitObs{Exited: true, Code: 143, Killed: killTimeout}, want{execproto.ReasonTimeout, "", false}},
		"cancel, KILL":           {exitObs{Signal: "SIGKILL", Killed: killCancel, CPUTime: time.Hour, CPULimit: 4}, want{execproto.ReasonCancelled, "", false}},
		"shutdown, TERM":         {exitObs{Signal: "SIGTERM", Killed: killShutdown}, want{execproto.ReasonExecutorShutdown, "", false}},
		"SIGTERM from elsewhere": {exitObs{Signal: "SIGTERM"}, want{execproto.ReasonSignal, "", false}},
	}
	for name, c := range cases {
		reason, res, seccomp := classify(c.o)
		kind := execproto.ResourceKind("")
		if res != nil {
			kind = res.Kind
		}
		if reason != c.w.reason || kind != c.w.resource || seccomp != c.w.seccomp {
			t.Errorf("%s: (%s, %q, seccomp %v), want (%s, %q, %v)", name, reason, kind, seccomp, c.w.reason, c.w.resource, c.w.seccomp)
		}
		if reason == execproto.ReasonResource && res == nil {
			t.Errorf("%s: reason resource without a resource object", name)
		}
	}
}

// TestSelectStrays guards design A06 §7.7 "no process survives its call":
// when a handle's leader exits, every other process except the executor
// and the members of other live handles' trees is killed. Outside a PID
// namespace (the executor's own tests, or a sandbox without one) only the
// executor's descendants are candidates, so a sweep can never reach a
// process the executor did not start; inside the namespace every process
// is the sandbox's, except the namespace init and the executor's
// ancestors, which are never touched. A handle leader that has exited but
// is not yet reaped is excluded too: its exit status belongs to its wait. A zombie is excluded
// as well: it is already dead, and kill(2) on it succeeds, so it would be
// counted in strays_killed again.
func TestSelectStrays(t *testing.T) {
	const self = 10
	procs := []procInfo{
		{PID: 1, PPID: 0, PGID: 1},                  // init (bwrap) or the host's
		{PID: 5, PPID: 1, PGID: 5},                  // executor's parent
		{PID: self, PPID: 5, PGID: 5},               // executor
		{PID: 20, PPID: self, PGID: 20},             // live handle leader
		{PID: 21, PPID: 20, PGID: 20},               // its child, same group
		{PID: 22, PPID: 21, PGID: 22},               // its grandchild in a new group: still its tree
		{PID: 30, PPID: self, PGID: 30, State: 'Z'}, // finished handle's leader, zombie
		{PID: 31, PPID: self, PGID: 30},             // reparented to the executor (subreaper)
		{PID: 32, PPID: 31, PGID: 32},               // its child
		{PID: 33, PPID: self, PGID: 33, State: 'Z'}, // already dead, waiting to be reaped: killing it would count a death that happened earlier
		{PID: 40, PPID: 1, PGID: 40},                // a setsid daemon reparented to init
		{PID: 50, PPID: 5, PGID: 5},                 // a sibling of the executor (the host's)
	}
	live := liveSet{leaders: map[int]bool{20: true, 30: true}, pgids: map[int]bool{20: true}}
	got := selectStrays(procs, self, live, false)
	if want := []int{31, 32}; !equalInts(got, want) {
		t.Errorf("outside a PID namespace: %v, want %v", got, want)
	}
	got = selectStrays(procs, self, live, true)
	if want := []int{31, 32, 40, 50}; !equalInts(got, want) {
		t.Errorf("inside the PID namespace: %v, want %v", got, want)
	}
	// A cycle in the ppid data (a pid reused while /proc was being read)
	// must not hang the sweep.
	cyc := []procInfo{{PID: self, PPID: 1}, {PID: 60, PPID: 61}, {PID: 61, PPID: 60}}
	if got := selectStrays(cyc, self, liveSet{}, false); len(got) != 0 {
		t.Errorf("cycle: %v, want none", got)
	}
}

func equalInts(a, b []int) bool {
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
