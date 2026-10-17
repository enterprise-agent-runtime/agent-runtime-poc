package linux_bwrap

// The file-descriptor plan of design A06 §11.1 and the executor flags the
// argv carries after "--" (A06 §3.1).

import (
	"fmt"
	"os"
)

// File descriptors the launcher passes with exec.Cmd.ExtraFiles (A06
// §11.1). ExtraFiles[i] becomes fd 3+i in bwrap.
const (
	FdRPC          = 3 // executor end of the RPC socketpair; passed through to warden-exec (--rpc-fd 3)
	FdHarnessStdio = 4 // harness stdio in harness mode, otherwise /dev/null; passed through
	FdSeccomp      = 5 // the seccomp program (sealed memfd); read by bwrap (--add-seccomp-fd 5)
	FdInfo         = 6 // write end of the info pipe; bwrap writes the child pid as JSON (--info-fd 6)
)

// Fd is one entry of the fd plan.
type Fd struct {
	Num      int    `json:"fd"`
	Name     string `json:"name"`
	Consumer string `json:"consumer"` // "warden-exec" (passed through bwrap) or "bwrap" (consumed before exec)
}

// Plan is a built bwrap invocation: the argv (argv[0] is "bwrap", resolved
// by the launcher) and the fds it expects. Missing names the optional bwrap
// flags that were dropped, for sandbox.create.limits.
type Plan struct {
	Argv    []string `json:"argv"`
	Fds     []Fd     `json:"fds"`
	Missing []string `json:"missing_flags,omitempty"`
	// SandboxID and WardenHome name the sandbox the argv was built for;
	// a scope unit must be that sandbox's (ScopedCommand).
	SandboxID  string `json:"-"`
	WardenHome string `json:"-"`
}

// fdPlan is the A06 §11.1 fd list, in ExtraFiles order.
func fdPlan() []Fd {
	return []Fd{
		{FdRPC, "rpc", "warden-exec"},
		{FdHarnessStdio, "harness_stdio", "warden-exec"},
		{FdSeccomp, "seccomp", "bwrap"},
		{FdInfo, "info", "bwrap"},
	}
}

// extraFiles orders the four files as the plan's fds (fd 3, 4, 5, 6). Every
// file is required: harnessStdio is /dev/null for a tool sandbox. A missing
// file would shift the numbering or leave bwrap reading a closed fd, so it
// is refused.
func (p *Plan) extraFiles(rpc, harnessStdio, seccompProg, info *os.File) ([]*os.File, error) {
	files := []*os.File{rpc, harnessStdio, seccompProg, info}
	for i, f := range files {
		if f == nil {
			return nil, fmt.Errorf("linux_bwrap: fd %d (%s) is missing", p.Fds[i].Num, p.Fds[i].Name)
		}
	}
	return files, nil
}

// ExecFlags are the warden-exec launch values that do not come from the
// Spec (A06 §3.1, §7.3).
type ExecFlags struct {
	// CPUSeconds is the RLIMIT_CPU runaway guard, 4 × the call timeout.
	CPUSeconds int
	// Cgroup is true when the launch runs in a systemd scope that enforces
	// the memory and pids limits (A06 §11.6). Without it the executor falls
	// back to RLIMIT_DATA and RLIMIT_NPROC per child (A06 §7.3).
	Cgroup bool
	// Nproc is RLIMIT_NPROC without a cgroup: the user's current process
	// count plus Spec.Limits.Pids (the kernel counts per real uid).
	Nproc int
	// MaxProcs bounds concurrent exec.proc.spawn handles.
	MaxProcs int
}
