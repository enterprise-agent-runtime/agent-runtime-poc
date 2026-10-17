// Package linux_bwrap is the Linux L1 sandbox backend of design A06 §11
// (WRD-16 §10.3, WRD-10 §5): it turns a validated sandbox.Spec into the
// bubblewrap argv and file-descriptor plan of A06 §11.1, probes the bwrap
// features that argv depends on, hands the A06 §11.5 seccomp program to
// bwrap through a sealed memfd, and maps a SIGSYS death to A06 §7.4's
// seccomp_kill.
//
// What a sandbox can see (INV-B) and what its environment holds (INV-C) are
// decided by sandbox.Spec.Mounts and sandbox.Spec.Env; this package renders
// them and never derives a mount or an environment value of its own. It
// adds only the mounts A06 §11.1 lists that carry no host data or only the
// read-only system files every process needs: the system binds, the
// generated /etc files, /proc, /dev, the tmpfs mounts, /warden/empty and
// the read-only root. Worktree masks (INV-I, mount layer) are rendered from
// the Spec as /dev/null binds and read-only tmpfs mounts.
//
// The launch (A06 §13.2) runs only through Plan.Start in command.go:
// bwrap directly with the rlimit fallback, or under the systemd-run --user
// --scope wrapper when cgroup v2 delegation is available (A06 §11.6),
// where systemd-run gets only the bus variables and bwrap runs under
// "env -i". Manager (Linux) implements sandbox.Manager: create, destroy
// (§13.3), the executor-exit watchdog (§13.4) and orphan recovery of its
// own home (§13.5). The L2 Docker seccomp.json is M2-18/M2-21.
//
// The argv builder, the wrapper and the lifecycle helpers are pure and
// build on every OS; the memfd hand-over, the wait-status mapping, the
// default host probes and the Manager are Linux-only.
package linux_bwrap
