//go:build linux

// sandboxed: regression tests from the M2-8 review (round 1); they spawn
// only the test's own fixed commands: copies of /bin/echo, /bin/sleep, short
// /bin/sh scripts the test writes, and the test binary in helper mode
// (INV-H).

package exec_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/exec"
	"warden.dev/warden/internal/execproto"
)

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
}

// swapLoop exchanges a and b (renameat2 RENAME_EXCHANGE) until stopped, as
// a concurrent process of another handle in the same sandbox could.
func swapLoop(t *testing.T, a, b string) (stop func()) {
	var halt atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !halt.Load() {
			_ = unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
		}
	}()
	return func() { halt.Store(true); wg.Wait() }
}

// TestSpawn_ExecutesTheCheckedFile guards CF-19 and A06 §6 step 3 against
// a check-then-use race (CWE-367), found in review: the shim used to execve
// argv[0] by path, so every check (the shell re-check on the real path,
// Resolve, the deny-list, path_escape, outside_roots) was made on a file
// that a concurrent process could swap before the execve. With /work/x (a
// copy of echo) and a link to /bin/sh exchanged in a loop, an approved
// ["./x", "-c", "echo PWNED"] ran the shell. The shim now executes the
// descriptor the checks were made on (execveat AT_EMPTY_PATH), so every
// spawn either runs the checked echo or is refused, and the executable it
// reports is the one that ran. The same holds for an absolute argv[0]
// outside the roots swapped with a link into the private gitdir, which the
// checks refuse (outside_roots).
func TestSpawn_ExecutesTheCheckedFile(t *testing.T) {
	r := newRig(t)
	copyFile(t, "/bin/echo", r.work+"/x", 0o700)
	if err := os.Symlink("/bin/sh", r.work+"/y"); err != nil {
		t.Fatal(err)
	}
	stop := swapLoop(t, r.work+"/x", r.work+"/y")
	ran := 0
	for i := 0; i < 300; i++ {
		h, err := r.spawn(spawnParams([]string{"./x", "-c", "echo PWNED"}))
		if err != nil {
			continue // the link was there at check time: refused
		}
		r.wait(h.Handle)
		out := r.output(h.Handle)[execproto.StreamStdout]
		if out == "PWNED\n" {
			stop()
			t.Fatalf("attempt %d: a shell ran in place of the checked echo", i)
		}
		if out != "-c echo PWNED\n" || h.Executable != r.work+"/x" {
			t.Errorf("attempt %d: output %q executable %q, want the checked echo at %s", i, out, h.Executable, r.work+"/x")
		}
		ran++
	}
	stop()

	writeScript(t, r.base+"/git/hooks-h", "echo FROMGIT\n")
	copyFile(t, "/bin/echo", r.base+"/outside/z", 0o700)
	if err := os.Symlink(r.base+"/git/hooks-h", r.base+"/outside/w"); err != nil {
		t.Fatal(err)
	}
	stop = swapLoop(t, r.base+"/outside/z", r.base+"/outside/w")
	for i := 0; i < 300; i++ {
		h, err := r.spawn(spawnParams([]string{r.base + "/outside/z", "ok"}))
		if err != nil {
			if !errors.Is(err, execproto.ErrOutsideRoots) && !errors.Is(err, execproto.ErrNotFound) {
				t.Errorf("attempt %d: %v", i, err)
			}
			continue
		}
		r.wait(h.Handle)
		// The echo inode may sit under either name when it is opened; what
		// matters is that it, and not the git file, ran and was reported.
		if out := r.output(h.Handle)[execproto.StreamStdout]; out != "ok\n" || (h.Executable != r.base+"/outside/z" && h.Executable != r.base+"/outside/w") {
			stop()
			t.Fatalf("attempt %d: output %q executable %q: a file under the git root ran", i, out, h.Executable)
		}
		ran++
	}
	stop()
	if ran == 0 {
		t.Error("no attempt ran: the race loop never left the checked file in place")
	}
}

// TestSpawn_CommandCannotRaiseLimits: the shim sets every limit as soft
// and hard limit, so the command can lower but never raise one (review
// probe P2).
func TestSpawn_CommandCannotRaiseLimits(t *testing.T) {
	r := newRig(t)
	writeScript(t, r.work+"/s", "ulimit -S -n 300 2>/dev/null; echo soft=$?; ulimit -H -n 300 2>/dev/null; echo hard=$?; ulimit -t unlimited 2>/dev/null; echo cpu=$?; ulimit -n\n")
	h := r.mustSpawn(spawnParams([]string{"./s"}))
	r.wait(h.Handle)
	got := r.output(h.Handle)[execproto.StreamStdout]
	if strings.Contains(got, "=0\n") || !strings.HasSuffix(got, "\n256\n") {
		t.Errorf("limits after the attempts to raise them:\n%s(want every attempt refused and nofile still 256)", got)
	}
}

// TestNoProcessSurvives_OrphanAndExecutorGroup (review probe P4): a
// double-forked orphan in the leader's group dies with the group, and a
// process that moved into the executor's own process group, where no
// group kill can reach it without killing the executor, is a stray and is
// killed by the sweep.
func TestNoProcessSurvives_OrphanAndExecutorGroup(t *testing.T) {
	r := newRig(t)
	writeScript(t, r.work+"/d", "( /bin/sleep 30 >/dev/null 2>&1 & echo $! )\n")
	h := r.mustSpawn(spawnParams([]string{"./d"}))
	r.wait(h.Handle)
	orphan, _ := strconv.Atoi(strings.TrimSpace(r.output(h.Handle)[execproto.StreamStdout]))
	h = r.mustSpawn(spawnParams(helper(t, "join-group", strconv.Itoa(unix.Getpgrp()), "no")))
	w := r.wait(h.Handle)
	joined, _ := strconv.Atoi(r.output(h.Handle)[execproto.StreamStdout])
	for _, pid := range []int{orphan, joined} {
		if pid <= 0 {
			t.Fatalf("helper output %q", r.output(h.Handle))
		}
		waitFor(t, "pid "+strconv.Itoa(pid)+" to die", func() bool { return !alive(pid) })
	}
	if w.StraysKilled != 1 {
		t.Errorf("strays_killed %d, want 1 (the process in the executor's group)", w.StraysKilled)
	}
}

// TestOutputCap_BothStreams (review probe P5): many small interleaved
// writes on both streams, with invalid UTF-8, are capped together: the
// daemon receives max_output_bytes in all, the tail ends with what the
// process wrote last, and the marker counts the rest.
func TestOutputCap_BothStreams(t *testing.T) {
	r := newRig(t)
	writeScript(t, r.work+"/c", "i=0; while [ $i -lt 3000 ]; do printf 'ab\\377' ; printf 'zz' >&2; i=$((i+1)); done; echo END >&2\n")
	p := spawnParams([]string{"./c"})
	p.MaxOutputBytes = 4096
	h := r.mustSpawn(p)
	w := r.wait(h.Handle)
	o := r.output(h.Handle)
	if w.Bytes == nil || w.Bytes.Stdout != 9000 || w.Bytes.Stderr != 6004 || w.Bytes.Forwarded != 4096 || w.Bytes.Discarded != 15004-4096 || !w.Truncated {
		t.Fatalf("bytes %+v truncated %v", w.Bytes, w.Truncated)
	}
	if n := len(o[execproto.StreamStdout]) + len(o[execproto.StreamStderr]); n != 4096 {
		t.Errorf("forwarded %d bytes on the two streams, want 4096", n)
	}
	if !strings.HasSuffix(o[execproto.StreamStderr], "zzEND\n") || !strings.Contains(o[execproto.StreamMarker], strconv.Itoa(15004-4096)+" bytes omitted") {
		t.Errorf("tail ends %q, marker %q", o[execproto.StreamStderr][max(0, len(o[execproto.StreamStderr])-8):], o[execproto.StreamMarker])
	}
}

// TestWait_CPULimitIsResource guards the cpu rows of A06 §7.4 as they fire
// in production, found in review: with RLIMIT_CPU's soft limit equal to
// its hard limit, Linux sends SIGKILL at the limit, never SIGXCPU, and the
// killed process's rusage can read a little under the limit (981 + 9 ms
// for 1 s was measured under -race), so a runaway was reported as a plain
// signal about one time in three. The hard limit is now one second above
// the soft one: a busy loop gets SIGXCPU at 1 s (resource/cpu, exactly),
// and a loop that ignores SIGXCPU gets SIGKILL at 2 s, by then well past
// the 1 s the classifier compares with (resource/cpu through the SIGKILL
// row). About 3 s of CPU in all.
func TestWait_CPULimitIsResource(t *testing.T) {
	limits, err := exec.ParseChildLimits("cpu=1,fsize=1073741824,nofile=256,core=0")
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, func(c *exec.ProcConfig) { c.Limits = limits })
	writeScript(t, r.work+"/b", "while :; do :; done\n")
	writeScript(t, r.work+"/i", "trap '' XCPU\nwhile :; do :; done\n")
	for script, sig := range map[string]string{"./b": "SIGXCPU", "./i": "SIGKILL"} {
		h := r.mustSpawn(spawnParams([]string{script}))
		w := r.wait(h.Handle)
		if w.Reason != execproto.ReasonResource || w.Resource == nil || w.Resource.Kind != execproto.ResourceCPU || ptrStr(w.Signal) != sig {
			t.Errorf("%s: reason %s (signal %s, resource %+v, rusage %+v), want resource cpu after %s", script, w.Reason, ptrStr(w.Signal), w.Resource, w.Rusage, sig)
		}
	}
}

// TestWait_DrainBoundEndsTheWait guards the drain bound of the monitor
// (review probe P6): a process in another live handle's group, which
// neither the group kill nor the sweep may touch, holds handle A's stdout
// open after A's leader exited. A's wait must still return, with A's exit,
// once the drain bound has passed on the fake clock, while the holder
// lives on in B's tree.
func TestWait_DrainBoundEndsTheWait(t *testing.T) {
	r := newRig(t)
	p := spawnParams([]string{"sleep", "60"})
	p.Authz.Profile, p.Authz.Approved = "sleep", false
	b := r.mustSpawn(p)
	a := r.mustSpawn(spawnParams(helper(t, "join-group", strconv.Itoa(b.PGID), "hold")))
	ctx, cancel := context.WithTimeout(context.Background(), hangGuard)
	defer cancel()
	done := make(chan execproto.WaitResult, 1)
	go func() {
		res, err := r.c.Wait(ctx, execproto.WaitParams{Handle: a.Handle})
		if err != nil {
			res.State = "error: " + execproto.WaitState(err.Error())
		}
		done <- res
	}()
	var w execproto.WaitResult
	// The clock is moved in steps until the wait returns, at most 20 s of
	// fake time: less than either call's timeout (600 s), so nothing but
	// the drain bound can end it.
	for i := 0; ; i++ {
		select {
		case w = <-done:
		default:
			if i == 100 {
				t.Fatal("A's wait did not return: the drain bound did not end it")
			}
			r.fc.Advance(200 * time.Millisecond)
			time.Sleep(5 * time.Millisecond)
			continue
		}
		break
	}
	holder, _ := strconv.Atoi(r.output(a.Handle)[execproto.StreamStdout])
	if w.State != execproto.StateExited || w.Reason != execproto.ReasonExit || exitCode(w) != 0 {
		t.Errorf("A's wait = %+v", w)
	}
	if holder <= 0 || !alive(holder) {
		t.Errorf("the holder (%d) is gone; it belongs to B's live group and must not have been killed", holder)
	}
	if _, err := r.c.Signal(ctx, execproto.SignalParams{Handle: b.Handle, Signal: execproto.SignalKILL}); err != nil {
		t.Fatal(err)
	}
	r.wait(b.Handle)
}

// TestHarness_CwdAndRelativeArgv0 guards the harness-mode cwd and argv[0]
// (review, low): the spec's cwd is opened one component at a time without
// following a link, as the tool-mode cwd is resolved without one, so a
// link a sandbox process planted cannot move the engine elsewhere; and a
// relative argv[0] is refused, because it would name a file in the
// worktree the agent writes.
func TestHarness_CwdAndRelativeArgv0(t *testing.T) {
	bin := helperBin(t)
	newHarness := func(cwd string, argv []string) *rig {
		spec := &exec.HarnessSpec{Version: 1, HarnessID: "copilot", RunMode: "split", Argv: argv, Cwd: cwd}
		return newRig(t, func(c *exec.ProcConfig) {
			if strings.HasPrefix(spec.Cwd, "BASE") {
				spec.Cwd = rigBase(c) + strings.TrimPrefix(spec.Cwd, "BASE")
			}
			c.Mode, c.Harness = execproto.ModeHarness, spec
		})
	}
	harnessParams := func(argv []string) execproto.SpawnParams {
		p := spawnParams(argv)
		p.Authz = execproto.Authz{DecisionID: "dec_1", Kind: execproto.AuthzHarness}
		return p
	}
	pwd := []string{bin, "helper", "pwd"}
	r := newHarness("BASE/scratch", pwd)
	h := r.mustSpawn(harnessParams(pwd))
	r.wait(h.Handle)
	if got := r.output(h.Handle)[execproto.StreamStdout]; got != r.base+"/scratch" {
		t.Errorf("harness cwd %q, want %q", got, r.base+"/scratch")
	}

	r = newHarness("BASE/scratch/link", pwd)
	if err := os.Symlink(r.base+"/outside", r.base+"/scratch/link"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.spawn(harnessParams(pwd)); err == nil {
		t.Error("a harness cwd through a symlink was followed")
	}

	rel := []string{"./engine"}
	r = newHarness("", rel)
	copyFile(t, "/bin/echo", filepath.Join(r.work, "engine"), 0o700)
	if _, err := r.spawn(harnessParams(rel)); !errors.Is(err, execproto.ErrInvalidArgument) {
		t.Errorf("relative harness argv[0]: %v, want invalid_argument", err)
	}
}
