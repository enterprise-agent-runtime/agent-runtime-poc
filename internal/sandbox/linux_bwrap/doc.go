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
// Launching the argv, rlimits, cgroups and orphan recovery are M2-15; the
// L2 Docker seccomp.json is M2-18/M2-21.
//
// The argv builder is pure and builds on every OS; only the memfd hand-over,
// the wait-status mapping and the default host probes are Linux-only.
package linux_bwrap
