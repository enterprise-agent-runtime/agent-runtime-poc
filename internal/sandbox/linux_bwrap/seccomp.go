package linux_bwrap

// The seccomp hand-over of design A06 §11.1 and §11.5 that does not depend
// on the OS: which program to render, and what sandbox.create.limits
// reports (A06 §13.2).

import (
	"fmt"
	"runtime"

	"warden.dev/warden/internal/sandbox"
	"warden.dev/warden/internal/sandbox/seccomp"
)

// SeccompArch maps a Go architecture name to the seccomp program for it.
// Only the two architectures A06 §11.5 has a table for are accepted; any
// other is sandbox.ErrUnavailable (no filter means no L1 sandbox).
func SeccompArch(goarch string) (seccomp.Arch, error) {
	for _, a := range seccomp.Arches() {
		if a.String() == goarch {
			return a, nil
		}
	}
	return 0, fmt.Errorf("%w: no seccomp program for architecture %q (A06 §11.5: amd64, arm64)", sandbox.ErrUnavailable, goarch)
}

// HostSeccompArch is SeccompArch for the architecture the daemon runs on.
// The daemon and the executor are built for the same architecture, and the
// filter's arch check kills anything else (A06 §11.5).
func HostSeccompArch() (seccomp.Arch, error) { return SeccompArch(runtime.GOARCH) }

// CreateLimits is sandbox.create.limits (A06 §13.2), shared with the
// event (sandbox.CreateEvent). MissingFlags (NEW) lists the optional bwrap
// flags that were dropped (A06 §11.1 "recorded in sandbox.create.limits").
type CreateLimits = sandbox.CreateLimits

// Limits builds sandbox.create.limits: the Spec's limits, whether a cgroup
// enforces them (A06 §11.6), seccomp.Version and the missing bwrap flags.
func Limits(l sandbox.Limits, cgroup bool, f Features) CreateLimits {
	return CreateLimits{
		MemoryMB: l.MemoryMB, Pids: l.Pids, CPUs: l.CPUs, FsizeMB: l.FsizeMB, TmpMB: l.TmpMB,
		Cgroup: cgroup, Seccomp: seccomp.Version, MissingFlags: f.Missing(),
	}
}
