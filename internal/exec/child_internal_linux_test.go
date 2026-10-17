//go:build linux

package exec

import (
	"errors"
	"io/fs"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestEffectiveLimit guards the rule the shim applies to every limit (A06
// §7.3): the child gets the requested value as soft and hard limit, except
// that it cannot exceed the current hard limit, which an unprivileged
// process cannot raise. A setrlimit above it would fail, and the shim
// would refuse to run anything.
func TestEffectiveLimit(t *testing.T) {
	inf := uint64(unix.RLIM_INFINITY)
	for _, c := range []struct{ want, curMax, got uint64 }{
		{4096, 1048576, 4096},
		{4096, 1024, 1024},
		{0, 1024, 0},
		{3600, inf, 3600},
		{inf, inf, inf},
		{inf, 10, 10},
	} {
		if got := effectiveLimit(c.want, c.curMax); got != c.got {
			t.Errorf("effectiveLimit(%d, %d) = %d, want %d", c.want, c.curMax, got, c.got)
		}
	}
}

// TestApplyLimits_InProcess runs the shim's limit step in the test process
// with the current hard limits as the requested values (so nothing is
// actually tightened) and checks that each limit ends up with soft equal
// to hard: that is what stops a command from raising its own limit. The
// real child path is covered by the spawn tests; this one makes the code
// visible to coverage, which a child process is not.
func TestApplyLimits_InProcess(t *testing.T) {
	cur := func(res int) uint64 {
		var l unix.Rlimit
		if err := unix.Getrlimit(res, &l); err != nil {
			t.Fatal(err)
		}
		return uint64(l.Max)
	}
	data, nproc := cur(unix.RLIMIT_DATA), cur(unix.RLIMIT_NPROC)
	l := ChildLimits{CPU: cur(unix.RLIMIT_CPU), Fsize: cur(unix.RLIMIT_FSIZE), Nofile: cur(unix.RLIMIT_NOFILE), Core: cur(unix.RLIMIT_CORE), Data: &data, Nproc: &nproc}
	if err := applyLimits(l); err != nil {
		t.Fatal(err)
	}
	for _, res := range []int{unix.RLIMIT_CPU, unix.RLIMIT_FSIZE, unix.RLIMIT_NOFILE, unix.RLIMIT_CORE, unix.RLIMIT_DATA, unix.RLIMIT_NPROC} {
		var got unix.Rlimit
		if err := unix.Getrlimit(res, &got); err != nil {
			t.Fatal(err)
		}
		if got.Cur != got.Max {
			t.Errorf("resource %d: soft %d, hard %d; want equal", res, got.Cur, got.Max)
		}
	}
}

// TestCloseOnExec_InProcess runs both ways the shim marks descriptors
// close-on-exec (close_range, and the /dev/fd walk used where close_range
// is missing) on a descriptor opened without the flag, as one inherited
// from the launch would be. Marking descriptors close-on-exec is harmless
// in the test process: it only affects what a later execve keeps.
func TestCloseOnExec_InProcess(t *testing.T) {
	for name, mark := range map[string]func() error{"close_range": closeOnExecAbove2, "walk": cloexecByWalk} {
		null, err := unix.Open(os.DevNull, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		fd, err := unix.FcntlInt(uintptr(null), unix.F_DUPFD, 100) // F_DUPFD clears close-on-exec
		_ = unix.Close(null)
		if err != nil {
			t.Fatal(err)
		}
		if flags, _ := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); flags&unix.FD_CLOEXEC != 0 {
			t.Fatalf("%s: setup: fd %d already close-on-exec", name, fd)
		}
		if err := mark(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if flags, _ := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); flags&unix.FD_CLOEXEC == 0 {
			t.Errorf("%s: fd %d is not close-on-exec", name, fd)
		}
		_ = unix.Close(fd)
	}
}

// TestErrno: shim errors carry the error number only, never a path (an
// *os.PathError quotes it, and a path may be what the model proposed).
func TestErrno(t *testing.T) {
	if err := errno(&fs.PathError{Op: "open", Path: "/work/secret-name", Err: unix.ENOENT}); !errors.Is(err, unix.ENOENT) || err.Error() != unix.ENOENT.Error() {
		t.Errorf("errno(PathError) = %v", err)
	}
	if err := errno(errors.New("/work/secret-name: odd")); err.Error() != "system error" {
		t.Errorf("errno(other) = %v", err)
	}
}

// TestCPUHardLimit: RLIMIT_CPU's hard limit is one second above the soft
// limit, so the kernel sends SIGXCPU at the limit (see cpuHardLimit), and
// an unlimited request stays unlimited.
func TestCPUHardLimit(t *testing.T) {
	inf := uint64(unix.RLIM_INFINITY)
	for _, c := range []struct{ want, got uint64 }{{1, 2}, {3600, 3601}, {inf, inf}} {
		if got := cpuHardLimit(c.want); got != c.got {
			t.Errorf("cpuHardLimit(%d) = %d, want %d", c.want, got, c.got)
		}
	}
}

// TestShimHelpers covers the shim's small decisions in the test process:
// descriptor arguments must be canonical numbers above 2; a script is a
// file that starts with "#!" (only a script keeps its descriptor across
// the execve, for its interpreter); and execution by descriptor of
// something that is not an executable regular file fails and returns
// (here /dev/null, which execveat refuses with EACCES, so the test process
// is never replaced).
func TestShimHelpers(t *testing.T) {
	for s, want := range map[string]bool{"3": true, "42": true, "2": false, "03": false, "-4": false, "x": false, "": false} {
		if _, ok := fdArg(s); ok != want {
			t.Errorf("fdArg(%q) ok = %v, want %v", s, ok, want)
		}
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"script": "#!/bin/sh\n", "binary": "\x7fELF", "short": "#"} {
		if err := os.WriteFile(dir+"/"+name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(dir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if got := isScript(int(f.Fd())); got != (name == "script") {
			t.Errorf("isScript(%s) = %v", name, got)
		}
		_ = f.Close()
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if err := execFD(int(null.Fd()), []string{"x"}, nil); !errors.Is(err, unix.EACCES) {
		t.Errorf("execFD(/dev/null) = %v, want EACCES", err)
	}
}

// TestParseStat reads /proc/<pid>/stat lines, including a comm with spaces
// and parentheses, and refuses malformed ones rather than guessing.
func TestParseStat(t *testing.T) {
	pi, ok := parseStat(7, "7 (a (b) c) S 1 7 7 0 -1")
	if !ok || pi.PID != 7 || pi.PPID != 1 || pi.PGID != 7 || pi.State != 'S' {
		t.Errorf("parseStat = %+v, %v", pi, ok)
	}
	for _, bad := range []string{"7 no-paren S 1 7", "7 (x) S 1", "7 (x) SS 1 7", "7 (x) S a 7", "7 (x) S 1 b"} {
		if _, ok := parseStat(7, bad); ok {
			t.Errorf("parseStat(%q) accepted", bad)
		}
	}
}
