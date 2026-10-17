// Package oci implements the daemon side of the L2 container sandbox:
// design A06 §15 (with §2, §8, §12 and §13), as CONFLICTS C-03 and C-24
// amend it. It turns one validated L2 sandbox.Spec into a Plan: the
// per-task --internal Docker network, an egress network, the sandbox
// container (warden-exec as PID 1 over container stdio) and the proxy
// sidecar container, plus the seccomp profile as an artefact to write.
//
// Everything here is pure. No Docker engine is called and no file is
// written; M2-21 maps the Plan onto the Engine API (docker/docker/client)
// and writes the artefact. The one outside dependency, the host file
// system questions of internal/platform.HostFS (case sensitivity, 8.3
// names, junctions), comes in through Options so tests use a fake.
//
// What the Plan may contain is decided by sandbox.Spec: the mounts are
// Spec.Mounts() and the environment is Spec.Env(), never re-derived here
// (INV-B, INV-C, D-056). The only additions are the host-data-free ones of
// A06 §15: tmpfs /tmp, the empty hooks directory /warden/empty, and the
// worktree masks rendered as /dev/null binds (files) or read-only tmpfs
// (directories) (INV-I, A06 §11.3).
package oci
