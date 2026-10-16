package exec

// macOS specifics of the process table (design A06 §7.1, §7.7, §10.7). The
// package doc is in server.go.

import (
	"errors"
	"os"
	"syscall"
)

// defaultShim is the running executor's path (A06 §7.1 step 3).
func defaultShim() (string, error) { return os.Executable() }

// sysProcAttr puts the child in a new process group. macOS has no parent
// death signal; the executor's EOF handling kills the children (§7.8).
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// waitExited has no WNOWAIT form here: the leader is reaped first and
// killGroup refuses a pgid a running handle uses.
func waitExited(int) bool { return false }

// maxRSSKB: macOS reports ru_maxrss in bytes.
func maxRSSKB(ru *syscall.Rusage) int64 { return int64(ru.Maxrss) / 1024 }

// SetChildSubreaper does nothing on macOS, which has no subreaper.
func SetChildSubreaper() error { return nil }

// listProcs returns nothing on macOS: there the daemon's process tracker
// provides the set of sandbox processes (A06 §7.7, §10.7), so the stray
// sweep is the daemon's.
func listProcs() ([]procInfo, error) { return nil, nil }

// closeRangeCloexec is unavailable on macOS; the shim walks /dev/fd.
func closeRangeCloexec() error { return errors.ErrUnsupported }
