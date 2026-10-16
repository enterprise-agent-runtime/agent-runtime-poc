//go:build linux && (amd64 || arm64)

package seccomp

// sandboxed: re-executes this test binary itself (os.Args[0]) as a helper
// child that installs the seccomp filter on itself and makes fixed probe
// system calls; it runs no agent input and starts nothing else.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// This is the kernel's half of TestSeccompTable_A06: the interpreter proves
// what the program says, this proves the kernel accepts the same bytes and
// enforces them. A filter applies to the process that installs it, so each
// case runs in a child: this test binary re-executed with childEnv set,
// which TestMain routes to childMain before any test runs.

const childEnv = "WARDEN_SECCOMP_TEST_CHILD"

func TestMain(m *testing.M) {
	if mode := os.Getenv(childEnv); mode != "" {
		os.Exit(childMain(mode))
	}
	os.Exit(m.Run())
}

func nativeArch() Arch {
	if runtime.GOARCH == "arm64" {
		return ARM64
	}
	return AMD64
}

// install sets PR_SET_NO_NEW_PRIVS (as bwrap does) and loads the rendered
// program for every thread of the process (SECCOMP_FILTER_FLAG_TSYNC: the Go
// runtime is already multi-threaded).
func install() error {
	raw, err := Render(nativeArch())
	if err != nil {
		return err
	}
	p, err := decode(raw)
	if err != nil {
		return err
	}
	f := make([]unix.SockFilter, len(p))
	for i, in := range p {
		f[i] = unix.SockFilter{Code: in.Code, Jt: in.Jt, Jf: in.Jf, K: in.K}
	}
	prog := unix.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("PR_SET_NO_NEW_PRIVS: %w", err)
	}
	if _, _, e := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog))); e != 0 {
		return fmt.Errorf("seccomp(SET_MODE_FILTER): %w", e)
	}
	runtime.KeepAlive(f)
	return nil
}

func errnoOf(err error) int {
	var e syscall.Errno
	if err == nil {
		return 0
	}
	if errors.As(err, &e) {
		return int(e)
	}
	return -1
}

func raw3(nr, a0, a1, a2 uintptr) int {
	_, _, e := unix.Syscall(nr, a0, a1, a2)
	return int(e)
}

// probes are the errno-class cases: the call, made after the filter is in,
// and its errno (0 = success).
func probes() map[string]func() int {
	return map[string]func() int{
		"unshare(CLONE_NEWUSER)": func() int { return errnoOf(unix.Unshare(unix.CLONE_NEWUSER)) },
		"unshare(CLONE_NEWNET)":  func() int { return errnoOf(unix.Unshare(unix.CLONE_NEWNET)) },
		"setns(-1, 0)":           func() int { return errnoOf(unix.Setns(-1, 0)) },
		"ptrace(PEEKUSER, ppid)": func() int {
			return raw3(unix.SYS_PTRACE, unix.PTRACE_PEEKUSR, uintptr(os.Getppid()), 0)
		},
		"mount(tmpfs)": func() int { return errnoOf(unix.Mount("none", "/tmp", "tmpfs", 0, "")) },
		"keyctl(GET_KEYRING_ID)": func() int {
			return raw3(unix.SYS_KEYCTL, unix.KEYCTL_GET_KEYRING_ID, uintptr(0xfffffffd) /* KEY_SPEC_SESSION_KEYRING */, 0)
		},
		"clone3(NULL, 0)":         func() int { return raw3(unix.SYS_CLONE3, 0, 0, 0) },
		"io_uring_setup(0, NULL)": func() int { return raw3(unix.SYS_IO_URING_SETUP, 0, 0, 0) },
		"socket(AF_PACKET)": func() int {
			fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, 0)
			if err == nil {
				unix.Close(fd)
			}
			return errnoOf(err)
		},
		"socket(AF_UNIX)": func() int {
			fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
			if err == nil {
				unix.Close(fd)
			}
			return errnoOf(err)
		},
		"ioctl(/dev/null, TIOCSTI)": func() int {
			f, err := os.Open(os.DevNull)
			if err != nil {
				return -2
			}
			defer f.Close()
			c := byte('x')
			return raw3(unix.SYS_IOCTL, f.Fd(), unix.TIOCSTI, uintptr(unsafe.Pointer(&c)))
		},
		"personality(ADDR_NO_RANDOMIZE)": func() int {
			_, _, e := unix.Syscall(unix.SYS_PERSONALITY, 0x0040000, 0, 0)
			return int(e)
		},
		"personality(query)": func() int {
			_, _, e := unix.Syscall(unix.SYS_PERSONALITY, 0xffffffff, 0, 0)
			return int(e)
		},
		// clone without namespace flags: the Go runtime creates OS threads
		// with clone(CLONE_VM|CLONE_THREAD|...); four locked goroutines
		// force new ones.
		"threads via clone": func() int {
			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					runtime.LockOSThread()
					_ = unix.Getpid()
				}()
			}
			wg.Wait()
			return 0
		},
		"openat(/dev/null)": func() int {
			fd, err := unix.Openat(unix.AT_FDCWD, os.DevNull, unix.O_RDONLY, 0)
			if err == nil {
				unix.Close(fd)
			}
			return errnoOf(err)
		},
	}
}

// childMain runs one mode in the helper child and returns its exit code.
// "errno" and "baseline" print a JSON object of probe -> errno; "baseline"
// skips the filter, so the parent can see that the filter changed the
// outcome. "kill-*" make one call that must kill the process with SIGSYS and
// print "survived" otherwise.
func childMain(mode string) int {
	runtime.LockOSThread()
	// SIGSYS dumps core by default. RLIMIT_CORE 0 stops core files, and
	// PR_SET_DUMPABLE 0 also stops a piped core_pattern helper (WSL's crash
	// capture), which ignores the rlimit.
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		fmt.Fprintln(os.Stderr, "RLIMIT_CORE:", err)
		return 3
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		fmt.Fprintln(os.Stderr, "PR_SET_DUMPABLE:", err)
		return 3
	}
	if mode != "baseline" && mode != "kill-init_module-baseline" {
		if err := install(); err != nil {
			fmt.Fprintln(os.Stderr, "install:", err)
			return 3
		}
	}
	switch mode {
	case "errno", "baseline":
		out := map[string]int{}
		for name, f := range probes() {
			out[name] = f()
		}
		if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
			return 4
		}
		return 0
	case "kill-init_module", "kill-init_module-baseline":
		e := raw3(unix.SYS_INIT_MODULE, 0, 0, 0)
		fmt.Printf("survived errno=%d\n", e)
		return 0
	case "kill-x32":
		// The x32 ABI: getpid with __X32_SYSCALL_BIT (amd64 only).
		e := raw3(x32SyscallBit+unix.SYS_GETPID, 0, 0, 0)
		fmt.Printf("survived errno=%d\n", e)
		return 0
	}
	fmt.Fprintln(os.Stderr, "unknown mode", mode)
	return 2
}

func runChild(t *testing.T, mode string) (stdout []byte, state *os.ProcessState) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"="+mode)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("child %s: %v", mode, err)
	}
	if errb.Len() > 0 {
		t.Logf("child %s stderr: %s", mode, errb.Bytes())
	}
	return out.Bytes(), cmd.ProcessState
}

func childErrnos(t *testing.T, mode string) map[string]int {
	t.Helper()
	out, st := runChild(t, mode)
	if st.ExitCode() != 0 {
		t.Fatalf("child %s exited %v, output %q", mode, st, out)
	}
	var m map[string]int
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("child %s output %q: %v", mode, out, err)
	}
	return m
}

// TestSeccompKernel_EnforcesErrnoRows installs the rendered program in a
// child and checks each probe's errno against A06 §11.5. distinct marks the
// probes whose unfiltered outcome differs on an ordinary host, so the
// baseline child proves the filter, not the host, produced the errno.
func TestSeccompKernel_EnforcesErrnoRows(t *testing.T) {
	want := []struct {
		probe    string
		errno    syscall.Errno
		distinct bool
	}{
		{"unshare(CLONE_NEWUSER)", unix.EPERM, true}, // unfiltered: EINVAL (multi-threaded)
		{"unshare(CLONE_NEWNET)", unix.EPERM, false},
		{"setns(-1, 0)", unix.EPERM, true}, // unfiltered: EBADF
		{"ptrace(PEEKUSER, ppid)", unix.EPERM, true},
		{"mount(tmpfs)", unix.EPERM, false},
		{"keyctl(GET_KEYRING_ID)", unix.EPERM, true}, // unfiltered: success
		{"clone3(NULL, 0)", unix.ENOSYS, true},
		{"io_uring_setup(0, NULL)", unix.ENOSYS, false},
		{"socket(AF_PACKET)", unix.EAFNOSUPPORT, true}, // unfiltered: EPERM (no CAP_NET_RAW)
		{"socket(AF_UNIX)", 0, false},
		{"ioctl(/dev/null, TIOCSTI)", unix.EPERM, true}, // unfiltered: ENOTTY
		{"personality(ADDR_NO_RANDOMIZE)", unix.EPERM, true},
		{"personality(query)", 0, false},
		{"threads via clone", 0, false},
		{"openat(/dev/null)", 0, false},
	}
	got := childErrnos(t, "errno")
	base := childErrnos(t, "baseline")
	if len(got) != len(want) {
		t.Errorf("child ran %d probes, test checks %d", len(got), len(want))
	}
	for _, w := range want {
		g, ok := got[w.probe]
		if !ok {
			t.Errorf("%s: not run", w.probe)
			continue
		}
		if g != int(w.errno) {
			t.Errorf("%s under the filter: errno %d (%v), want %d (%v)", w.probe, g, syscall.Errno(g), int(w.errno), w.errno)
		}
		if w.distinct && base[w.probe] == g {
			t.Errorf("%s: unfiltered errno %d equals the filtered one, so this probe proves nothing", w.probe, g)
		}
		t.Logf("%-32s filtered %-14v unfiltered %v", w.probe, syscall.Errno(g), syscall.Errno(base[w.probe]))
	}
}

// TestSeccompKernel_KillRows checks that a KILL_PROCESS row really kills the
// process with SIGSYS, and that the same call survives without the filter.
func TestSeccompKernel_KillRows(t *testing.T) {
	modes := []string{"kill-init_module"}
	if nativeArch() == AMD64 {
		modes = append(modes, "kill-x32")
	}
	for _, mode := range modes {
		out, st := runChild(t, mode)
		ws, ok := st.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGSYS {
			t.Errorf("%s: child ended %v with output %q, want death by SIGSYS", mode, st, out)
		}
	}
	out, st := runChild(t, "kill-init_module-baseline")
	if st.ExitCode() != 0 || !bytes.HasPrefix(out, []byte("survived")) {
		t.Errorf("baseline init_module: %v %q, want survival without the filter", st, out)
	}
}
