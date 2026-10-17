package exec

// Linux specifics of the process table (design A06 §7.1, §7.7). The package
// doc is in server.go.

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// defaultShim is /proc/self/exe (A06 §7.1 step 3): the running executor,
// whatever path it was started by.
func defaultShim() (string, error) { return "/proc/self/exe", nil }

// sysProcAttr puts the child in a new process group and kills it if the
// executor dies (A06 §7.1 step 3). Pdeathsig survives the shim's execve.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// waitExited blocks until pid has exited, without reaping it (WNOWAIT): the
// zombie keeps the pid, which is also the group id, from being reused
// while the group is killed. It reports false when that wait failed, and
// the caller then reaps first.
func waitExited(pid int) bool {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err == nil
	}
}

// maxRSSKB: Linux reports ru_maxrss in KiB.
func maxRSSKB(ru *syscall.Rusage) int64 { return int64(ru.Maxrss) }

// SetChildSubreaper makes the calling process the reaper of orphaned
// descendants (PR_SET_CHILD_SUBREAPER), so that a process that leaves its
// handle's tree by double-forking is reparented to the executor and
// remains visible to the A06 §7.7 sweep. warden-exec calls it at start
// (M2-10); it is process-wide, so it is not done by NewProcs.
func SetChildSubreaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

// listProcs reads pid, ppid, pgid and state of every process in /proc.
// Inside the sandbox's PID namespace that is the sandbox's processes only.
// Entries that vanish while being read are skipped.
func listProcs() ([]procInfo, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]procInfo, 0, len(ents))
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		if pi, ok := parseStat(pid, string(b)); ok {
			out = append(out, pi)
		}
	}
	return out, nil
}

// parseStat reads "pid (comm) state ppid pgrp ...". comm may hold spaces
// and parentheses, so the fields start after the last ')'.
func parseStat(pid int, s string) (procInfo, bool) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return procInfo{}, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 3 || len(f[0]) != 1 {
		return procInfo{}, false
	}
	ppid, err1 := strconv.Atoi(f[1])
	pgid, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil {
		return procInfo{}, false
	}
	return procInfo{PID: pid, PPID: ppid, PGID: pgid, State: f[0][0]}, true
}

// closeRangeCloexec marks every descriptor from 3 up close-on-exec in one
// call (Linux 5.11+).
func closeRangeCloexec() error {
	return unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC)
}

// execByFD is true: Linux has execveat (3.19+), so the shim executes the
// checked descriptor.
const execByFD = true

// execFD is execveat(fd, "", argv, envv, AT_EMPTY_PATH): it executes the
// file open on fd, whatever its name now names (CWE-367). x/sys v0.48 has
// no typed Execveat, so this is the raw system call, with argv and envv
// built by syscall.SlicePtrFromStrings exactly as syscall.Exec does.
func execFD(fd int, argv, envv []string) error {
	argvp, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return err
	}
	envp, err := syscall.SlicePtrFromStrings(envv)
	if err != nil {
		return err
	}
	empty, err := unix.BytePtrFromString("")
	if err != nil {
		return err
	}
	_, _, e := unix.Syscall6(unix.SYS_EXECVEAT, uintptr(fd), uintptr(unsafe.Pointer(empty)),
		uintptr(unsafe.Pointer(&argvp[0])), uintptr(unsafe.Pointer(&envp[0])), unix.AT_EMPTY_PATH, 0)
	return e
}

// fdPath is the path the kernel has for the file open on f: the file the
// checks must be made on, whatever a later rename does to the name.
func fdPath(f *os.File) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
}
