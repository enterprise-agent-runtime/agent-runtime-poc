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
//	--limits <json> --cwd-fd <n> -- <path> <argv0> [<arg>...]
//
// It applies every limit (A06 §7.3) as both soft and hard limit, so the
// command cannot raise it again (a value above the current hard limit,
// which an unprivileged process cannot raise, becomes that hard limit);
// changes into the directory open on <n>, which is the cwd the table
// checked; marks every descriptor above 2 close-on-exec, so nothing the
// executor holds (its RPC channel, the roots, the proxy socket) reaches the
// command; and executes <path> with argv and the environment it was given.
// Using a re-exec shim keeps limits per child, because os/exec has no
// rlimit hook, and the shim never outlives the execve.
//
// It returns only on failure; the caller prints the error and exits 127,
// like a shell for a command it cannot run. Errors never quote argv or the
// environment.
func RunChild(args []string) error {
	if len(args) < 7 || args[0] != "--limits" || args[2] != "--cwd-fd" || args[4] != "--" || args[5] == "" {
		return errors.New("usage: warden-exec child --limits <json> --cwd-fd <n> -- <path> <argv0> [<arg>...]")
	}
	var l ChildLimits
	if err := execproto.Decode([]byte(args[1]), &l); err != nil || l.CPU == 0 || l.Nofile == 0 {
		return errors.New("invalid --limits")
	}
	fd, err := strconv.Atoi(args[3])
	if err != nil || fd < 3 || strconv.Itoa(fd) != args[3] {
		return errors.New("invalid --cwd-fd")
	}
	if err := applyLimits(l); err != nil {
		return err
	}
	if err := unix.Fchdir(fd); err != nil {
		return fmt.Errorf("cannot enter the working directory: %w", errno(err))
	}
	if err := closeOnExecAbove2(); err != nil {
		return err
	}
	err = unix.Exec(args[5], args[6:], os.Environ())
	return fmt.Errorf("cannot execute: %w", errno(err))
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
		v := min(*s.v, uint64(cur.Max))
		// unix.Setrlimit goes through syscall.Setrlimit, which also stops
		// the runtime from restoring its saved RLIMIT_NOFILE at execve.
		if err := unix.Setrlimit(s.res, &unix.Rlimit{Cur: v, Max: v}); err != nil {
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
