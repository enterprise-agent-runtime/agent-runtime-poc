//go:build linux

// sandboxed: the executor's own tests of its spawn path. The test binary is
// also the child shim ("warden-exec child") and a helper process, and the
// helpers start /bin/sleep: every process these tests start is one of the
// test's own fixed, harmless commands (/bin/echo, /bin/sleep, /usr/bin/dd,
// the helpers below), never anything an agent or a model asked for, so this
// stays within INV-H ("no agent-requested process outside a sandbox,
// including tests").

package exec_test

import (
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"warden.dev/warden/internal/exec"
)

// TestMain dispatches the two roles the test binary plays besides running
// tests. As "warden-exec child" it is the shim the process table starts
// (A06 §7.1 step 3, /proc/self/exe); as "<bin> helper <name>" it is a
// command with known behaviour that the tests spawn through that shim.
// Otherwise it makes itself child subreaper, as warden-exec does at start,
// so the stray sweep of A06 §7.7 can see double-forked processes.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "child":
			err := exec.RunChild(os.Args[2:])
			fmt.Fprintln(os.Stderr, "warden-exec child:", err)
			unix.Exit(127)
		case "helper":
			// unix.Exit, not os.Exit: a coverage build would print a
			// GOCOVERDIR warning to the helper's stderr at os.Exit.
			unix.Exit(runHelper(os.Args[2:]))
		}
	}
	if err := exec.SetChildSubreaper(); err != nil {
		fmt.Fprintln(os.Stderr, "PR_SET_CHILD_SUBREAPER:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func runHelper(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "env":
		env := os.Environ()
		sort.Strings(env)
		fmt.Print(strings.Join(env, "\n"))
	case "has-env":
		if v, ok := os.LookupEnv(args[1]); ok {
			fmt.Printf("set, %d bytes", len(v))
		} else {
			fmt.Print("unset")
		}
	case "rlimits":
		for _, r := range []struct {
			name string
			res  int
		}{{"cpu", unix.RLIMIT_CPU}, {"fsize", unix.RLIMIT_FSIZE}, {"nofile", unix.RLIMIT_NOFILE}, {"core", unix.RLIMIT_CORE}, {"data", unix.RLIMIT_DATA}, {"nproc", unix.RLIMIT_NPROC}} {
			var l unix.Rlimit
			if err := unix.Getrlimit(r.res, &l); err != nil {
				return 1
			}
			fmt.Printf("%s=%d/%d\n", r.name, l.Cur, l.Max)
		}
	case "pwd":
		wd, err := os.Getwd()
		if err != nil {
			return 1
		}
		fmt.Print(wd)
	case "both":
		fmt.Fprint(os.Stdout, "to stdout")
		fmt.Fprint(os.Stderr, "to stderr")
	case "spew":
		n, _ := strconv.Atoi(args[1])
		chunk := []byte(strings.Repeat("x", 1000))
		for ; n >= 1000; n -= 1000 {
			_, _ = os.Stdout.Write(chunk)
		}
		_, _ = os.Stdout.Write(chunk[:n])
		fmt.Print(args[2])
	case "join-group":
		// A grandchild that moves into process group args[1] (another
		// handle's, or the executor's own) and, with args[2] == "hold",
		// keeps this handle's stdout pipe open.
		pgid, _ := strconv.Atoi(args[1])
		c := osexec.Command("/bin/sleep", "30")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
		if args[2] == "hold" {
			c.Stdout = os.Stdout
		}
		if err := c.Start(); err != nil {
			return 1
		}
		fmt.Printf("%d", c.Process.Pid)
	case "fd-open":
		n, _ := strconv.Atoi(args[1])
		if _, err := unix.FcntlInt(uintptr(n), unix.F_GETFD, 0); err != nil {
			fmt.Print("closed")
		} else {
			fmt.Print("open")
		}
	case "fork-sleep":
		// A grandchild in the leader's group, holding the stdout pipe.
		c := osexec.Command("/bin/sleep", "60")
		c.Stdout = os.Stdout
		if err := c.Start(); err != nil {
			return 1
		}
		fmt.Printf("%d", c.Process.Pid)
	case "setsid-sleep":
		// A grandchild that leaves the group and the session (a daemon).
		c := osexec.Command("/bin/sleep", "60")
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			return 1
		}
		fmt.Printf("%d", c.Process.Pid)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(args[1], nil, 0o600); err != nil {
			return 1
		}
		time.Sleep(time.Hour)
	default:
		return 2
	}
	return 0
}
