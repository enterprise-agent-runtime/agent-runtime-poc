//go:build linux || darwin

// sandboxed: the child shim ("warden-exec child", design A06 §7.1 step 4)
// runs inside the sandbox as the first process of every handle; it sets the
// handle's rlimits and replaces itself (execve) with the command the
// process table already checked and the policy engine already allowed
// (INV-H).

package exec

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/execproto"
)

// RunChild is "warden-exec child": args are what follows "child" on the
// command line the process table builds,
//
//	--limits <json> --cwd-fd <n> --exec-fd <m> -- <argv0> [<arg>...]
//
// It applies every limit (A06 §7.3) as both soft and hard limit (RLIMIT_CPU
// with a hard limit one second above, see cpuHardLimit), so the command
// cannot raise it again (a value above the current hard limit,
// which an unprivileged process cannot raise, becomes that hard limit);
// changes into the directory open on <n>, which is the cwd the table
// checked; marks every descriptor above 2 close-on-exec, so nothing the
// executor holds (its RPC channel, the roots, the proxy socket) reaches the
// command; and executes the file open on <m> with argv and the environment
// it was given. Using a re-exec shim keeps limits per child, because
// os/exec has no rlimit hook, and the shim never outlives the execve.
//
// The file is executed by descriptor (execveat AT_EMPTY_PATH), never by
// path: <m> is the very file every A06 §6 step 2 and 3 check was made on
// (the shell re-check on its real path, Resolve, the deny-list, the root
// checks), so a concurrent rename or symlink swap between the check and
// the execve cannot substitute another file (CWE-367; CF-19). A script
// (#!) keeps <m> open across the execve, because the kernel hands its
// interpreter /dev/fd/<m> to read; a binary does not get it.
//
// It returns only on failure; the caller prints the error and exits 127,
// like a shell for a command it cannot run. Errors never quote argv or the
// environment.
func RunChild(args []string) error {
	if len(args) < 8 || args[0] != "--limits" || args[2] != "--cwd-fd" || args[4] != "--exec-fd" || args[6] != "--" || args[7] == "" {
		return errors.New("usage: warden-exec child --limits <json> --cwd-fd <n> --exec-fd <m> -- <argv0> [<arg>...]")
	}
	var l ChildLimits
	if err := execproto.Decode([]byte(args[1]), &l); err != nil || l.CPU == 0 || l.Nofile == 0 {
		return errors.New("invalid --limits")
	}
	cwd, ok := fdArg(args[3])
	if !ok {
		return errors.New("invalid --cwd-fd")
	}
	exe, ok := fdArg(args[5])
	if !ok || exe == cwd {
		return errors.New("invalid --exec-fd")
	}
	if err := applyLimits(l); err != nil {
		return err
	}
	if err := unix.Fchdir(cwd); err != nil {
		return fmt.Errorf("cannot enter the working directory: %w", errno(err))
	}
	if err := closeOnExecAbove2(); err != nil {
		return err
	}
	// A script keeps fd 4 open across the execve: with execveat on a
	// script the kernel starts the interpreter with /dev/fd/4 as the
	// script path, and with fd 4 closed on exec the interpreter could not
	// open it (ENOENT). The interpreter never closes it, so fd 4 reaches
	// everything the script runs. That is acceptable: it is read-only, it
	// is the script itself, and the script is a file every process of the
	// handle can already read, so it gives a child nothing it lacks. A
	// binary does not need it and does not get it.
	if isScript(exe) {
		if _, err := unix.FcntlInt(uintptr(exe), unix.F_SETFD, 0); err != nil {
			return fmt.Errorf("cannot keep the script open for its interpreter: %w", errno(err))
		}
	}
	return fmt.Errorf("cannot execute: %w", errno(execFD(exe, args[7:], os.Environ())))
}

// fdArg reads a descriptor number above 2 in canonical decimal.
func fdArg(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 2 && strconv.Itoa(n) == s
}

// isScript reports a file that starts with "#!".
func isScript(fd int) bool {
	var b [2]byte
	n, err := unix.Pread(fd, b[:], 0)
	return err == nil && n == 2 && b[0] == '#' && b[1] == '!'
}

// errno keeps only the error number's text: an *os.PathError would quote
// the path.
func errno(err error) error {
	var e unix.Errno
	if errors.As(err, &e) {
		return e
	}
	return errors.New("system error")
}

// effectiveLimit is the soft and hard limit the child gets for a requested
// value: the request, but never above the current hard limit, which an
// unprivileged process cannot raise (setrlimit would fail and the shim
// would run nothing).
func effectiveLimit(want, curMax uint64) uint64 { return min(want, curMax) }

// cpuHardLimit is RLIMIT_CPU's hard limit for a soft limit of want seconds:
// one second more. At the soft limit Linux sends SIGXCPU, which classify
// reports as resource/cpu exactly; with soft = hard it sends SIGKILL
// instead, and the rusage of the killed process can read a little under
// the limit (981 ms for a 1 s limit was measured), so the kill would be
// misreported as a plain signal. A process that ignores SIGXCPU is killed
// one second later, by then well past the limit.
func cpuHardLimit(want uint64) uint64 {
	if want == ^uint64(0) {
		return want
	}
	return want + 1
}

// applyLimits sets every requested limit as soft and hard limit, except
// RLIMIT_CPU, whose hard limit is cpuHardLimit.
func applyLimits(l ChildLimits) error {
	set := []struct {
		name string
		res  int
		v    *uint64
	}{
		{"cpu", unix.RLIMIT_CPU, &l.CPU},
		{"fsize", unix.RLIMIT_FSIZE, &l.Fsize},
		{"nofile", unix.RLIMIT_NOFILE, &l.Nofile},
		{"core", unix.RLIMIT_CORE, &l.Core},
		{"data", unix.RLIMIT_DATA, l.Data},
		{"nproc", unix.RLIMIT_NPROC, l.Nproc},
	}
	for _, s := range set {
		if s.v == nil {
			continue
		}
		var cur unix.Rlimit
		if err := unix.Getrlimit(s.res, &cur); err != nil {
			return fmt.Errorf("getrlimit %s: %w", s.name, errno(err))
		}
		v := effectiveLimit(*s.v, uint64(cur.Max))
		hard := v
		if s.res == unix.RLIMIT_CPU {
			hard = effectiveLimit(cpuHardLimit(*s.v), uint64(cur.Max))
		}
		// unix.Setrlimit goes through syscall.Setrlimit, which also stops
		// the runtime from restoring its saved RLIMIT_NOFILE at execve.
		if err := unix.Setrlimit(s.res, &unix.Rlimit{Cur: v, Max: hard}); err != nil {
			return fmt.Errorf("setrlimit %s: %w", s.name, errno(err))
		}
	}
	return nil
}

// closeOnExecAbove2 sets FD_CLOEXEC on every descriptor above 2: with
// close_range(CLOSE_RANGE_CLOEXEC) where the kernel has it (Linux 5.11),
// otherwise one by one from /dev/fd. Descriptors stay usable until the
// execve, which the Go runtime's own need. If neither works the shim
// refuses to run the command (fail closed).
func closeOnExecAbove2() error {
	if closeRangeCloexec() == nil {
		return nil
	}
	return cloexecByWalk()
}

// cloexecByWalk marks each descriptor above 2 listed in /dev/fd.
func cloexecByWalk() error {
	ents, err := os.ReadDir("/dev/fd")
	if err != nil {
		return errors.New("cannot list open descriptors")
	}
	for _, e := range ents {
		n, err := strconv.Atoi(strings.TrimSpace(e.Name()))
		if err != nil || n <= 2 {
			continue
		}
		if _, err := unix.FcntlInt(uintptr(n), unix.F_SETFD, unix.FD_CLOEXEC); err != nil && !errors.Is(err, unix.EBADF) {
			return errors.New("cannot mark a descriptor close-on-exec")
		}
	}
	return nil
}
