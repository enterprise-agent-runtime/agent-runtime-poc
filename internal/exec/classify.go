package exec

// Exit classification (design A06 §7.4) and the stray selection of §7.7,
// both pure so that every row is tested on every OS. The package doc is in
// server.go.

import (
	"sort"
	"time"

	"warden.dev/warden/internal/execproto"
)

// killReason records why the executor itself signalled a handle's group,
// first reason wins.
type killReason int

const (
	killNone     killReason = iota
	killTimeout             // the per-call watchdog (timeout_ms)
	killCancel              // exec.proc.signal, or exec.shutdown with reason cancel
	killShutdown            // exec.shutdown for any other reason, RPC EOF, executor exit
)

// exitObs is what the executor saw of a leader's end.
type exitObs struct {
	Exited   bool // exited normally with Code
	Code     int
	Signal   string // "SIGSYS" etc. when killed by a signal
	Killed   killReason
	CPUTime  time.Duration // user + system
	CPULimit uint64        // the RLIMIT_CPU the child had, seconds
}

// classify maps an end to exec.proc.wait's reason, resource and
// seccomp_kill (A06 §7.4). Precedence:
//
//  1. SIGSYS is the seccomp KILL class (§11.5): reason signal with
//     seccomp_kill, whatever else happened, because it is the trace of a
//     blocked call and the daemon reports it as such.
//  2. SIGXFSZ and SIGXCPU are the kernel enforcing RLIMIT_FSIZE and
//     RLIMIT_CPU: resource.
//  3. A kill the executor started is reported as its cause (timeout,
//     cancelled, executor_shutdown), not as the signal that carried it,
//     and also when the process caught the signal and exited by itself.
//  4. SIGKILL with the CPU time at the limit is RLIMIT_CPU's hard limit.
//  5. Otherwise exit, or signal.
//
// The memory, pids, tmp_full and disk rows need the cgroup and the
// watchdogs (A06 §11.6, §13.4); they are the sandbox layer's, not the
// executor's. The bwrap-side exit 159 belongs to the daemon.
func classify(o exitObs) (execproto.WaitReason, *execproto.WaitResource, bool) {
	switch {
	case !o.Exited && o.Signal == "SIGSYS":
		return execproto.ReasonSignal, nil, true
	case !o.Exited && o.Signal == "SIGXFSZ":
		return execproto.ReasonResource, &execproto.WaitResource{Kind: execproto.ResourceFsize, Detail: "RLIMIT_FSIZE exceeded"}, false
	case !o.Exited && o.Signal == "SIGXCPU":
		return execproto.ReasonResource, &execproto.WaitResource{Kind: execproto.ResourceCPU, Detail: "RLIMIT_CPU exceeded"}, false
	case o.Killed == killTimeout:
		return execproto.ReasonTimeout, nil, false
	case o.Killed == killCancel:
		return execproto.ReasonCancelled, nil, false
	case o.Killed == killShutdown:
		return execproto.ReasonExecutorShutdown, nil, false
	case !o.Exited && o.Signal == "SIGKILL" && o.CPULimit > 0 && o.CPUTime >= time.Duration(o.CPULimit)*time.Second:
		return execproto.ReasonResource, &execproto.WaitResource{Kind: execproto.ResourceCPU, Detail: "RLIMIT_CPU hard limit"}, false
	case o.Exited:
		return execproto.ReasonExit, nil, false
	}
	return execproto.ReasonSignal, nil, false
}

// procInfo is one /proc/<pid>/stat entry.
type procInfo struct {
	PID, PPID, PGID int
	State           byte
}

// liveSet describes the handles that are not the one ending: leaders are
// the leader pids of every handle not yet reaped (including the ending
// one, whose zombie holds its exit status), pgids the process groups of
// handles still running.
type liveSet struct {
	leaders map[int]bool
	pgids   map[int]bool
}

// selectStrays is the A06 §7.7 choice: the processes to kill after a
// leader exits, in pid order. A process is spared when it is a zombie
// (already dead; kill(2) on it would succeed and count it again), the
// executor, a handle leader, or in a live handle's tree (its group, or a
// descendant of a live leader). Outside a PID namespace only the
// executor's descendants are candidates, so a sweep can never reach a
// process the executor did not start (the executor's own tests run there);
// inside the namespace every process is the sandbox's, except pid 1 and
// the executor's ancestors (bwrap's init), which are never touched.
func selectStrays(procs []procInfo, self int, live liveSet, namespace bool) []int {
	ppid := make(map[int]int, len(procs))
	pgid := make(map[int]int, len(procs))
	for _, p := range procs {
		ppid[p.PID] = p.PPID
		pgid[p.PID] = p.PGID
	}
	// ancestors walks up from pid; it is bounded, so a cycle in data read
	// while pids were reused cannot hang it.
	ancestors := func(pid int, visit func(int) bool) {
		for i := 0; i < len(procs) && pid > 0; i++ {
			next, ok := ppid[pid]
			if !ok {
				return
			}
			pid = next
			if !visit(pid) {
				return
			}
		}
	}
	selfAnc := map[int]bool{}
	ancestors(self, func(a int) bool { selfAnc[a] = true; return true })

	var out []int
	for _, p := range procs {
		if p.PID == self || p.PID == 1 || p.State == 'Z' || selfAnc[p.PID] || live.leaders[p.PID] || live.pgids[p.PGID] {
			continue
		}
		underSelf, underLive := false, false
		ancestors(p.PID, func(a int) bool {
			if live.leaders[a] && !underSelf {
				underLive = live.pgids[pgid[a]] || underLive
			}
			if a == self {
				underSelf = true
				return false
			}
			return true
		})
		if underLive || (!namespace && !underSelf) {
			continue
		}
		out = append(out, p.PID)
	}
	sort.Ints(out)
	return out
}
