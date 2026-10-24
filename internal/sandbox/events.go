package sandbox

// The sandbox lifecycle events of design A06 §13.2 (sandbox.create) and
// §13.3 (sandbox.destroy), as the backends hand them to the session chain
// (core §6 event table; internal/api/schema/events/payloads.json). Host
// paths appear only as hashes (A06 §13.2 mounts[].host_path_hash), and the
// environment only as key names, never values (INV-C).

import (
	"context"

	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"warden.dev/warden/internal/execproto"
)

// CreateLimits is sandbox.create.limits (A06 §13.2). Cgroup records
// whether a cgroup enforces memory, pids and cpu (A06 §11.6); without one
// the launch falls back to the rlimits of A06 §7.3 and this is the only
// record of it. MissingFlags (NEW, D-068) lists the optional bwrap flags
// that were dropped.
type CreateLimits struct {
	MemoryMB     int      `json:"memory_mb"`
	Pids         int      `json:"pids"`
	CPUs         int      `json:"cpus"`
	FsizeMB      int      `json:"fsize_mb"`
	TmpMB        int      `json:"tmp_mb"`
	Cgroup       bool     `json:"cgroup"`
	Seccomp      string   `json:"seccomp"`
	MissingFlags []string `json:"missing_flags,omitempty"`
}

// EventMount is one sandbox.create mounts[] entry.
type EventMount struct {
	HostPathHash string `json:"host_path_hash"`
	SandboxPath  string `json:"sandbox_path"`
	Mode         string `json:"mode"`
	Kind         string `json:"kind"`
}

// EventProxy is sandbox.create proxy: the socket as a path hash and the
// in-sandbox forwarder port.
type EventProxy struct {
	Socket string `json:"socket"`
	Port   int    `json:"port"`
}

// CreateEvent is the sandbox.create payload. SessionID routes it to the
// session chain and is not part of the payload (it is in the envelope).
type CreateEvent struct {
	SessionID     string       `json:"-"`
	SandboxID     string       `json:"sandbox_id"`
	Level         Level        `json:"level"`
	Backend       string       `json:"backend"`
	Purpose       Purpose      `json:"purpose"`
	Mounts        []EventMount `json:"mounts"`
	Limits        CreateLimits `json:"limits"`
	EnvKeys       []string     `json:"env_keys"`
	Proxy         EventProxy   `json:"proxy"`
	ProfileDigest string       `json:"profile_digest"`
}

// DestroyEvent is the sandbox.destroy payload (A06 §13.3 step 6).
// KilledPIDs counts the processes the teardown killed, bounded by the
// sandbox's pids limit. Resource and SeccompKill (NEW, optional) carry
// the A06 §7.4 rows only the daemon can observe for the sandbox as a
// whole: the scope's memory.events oom_kill and pids.events max, the disk
// watchdog, and bwrap's exit 159 (warden-exec killed by SIGSYS).
type DestroyEvent struct {
	SessionID   string                  `json:"-"`
	SandboxID   string                  `json:"sandbox_id"`
	Reason      DestroyReason           `json:"reason"`
	KilledPIDs  int                     `json:"killed_pids"`
	DurationMS  int64                   `json:"duration_ms"`
	Resource    *execproto.WaitResource `json:"resource,omitempty"`
	SeccompKill bool                    `json:"seccomp_kill,omitempty"`
}

// ViolationKind is sandbox.violation kind (events schema).
type ViolationKind string

// The kinds the L1 backend emits.
const (
	ViolationRootCheck ViolationKind = "root_check" // processes survived teardown (A06 §13.3 step 3)
	ViolationSeccomp   ViolationKind = "seccomp"    // warden-exec killed by the filter (A06 §11.5)
)

// ViolationEvent is the sandbox.violation payload.
type ViolationEvent struct {
	SessionID string        `json:"-"`
	SandboxID string        `json:"sandbox_id"`
	Kind      ViolationKind `json:"kind"`
	Detail    string        `json:"detail"`
}

// EventSink writes sandbox events to the session chain. A backend calls
// SandboxCreate only after exec.hello succeeded (A06 §13.1),
// SandboxDestroy once per sandbox whose process was started, after its
// proxy listener has been closed (A06 §13.3 step 4 before step 6), and
// SandboxViolation for what the sandbox did that it must not (processes
// that survived teardown, a seccomp kill).
type EventSink interface {
	SandboxCreate(ctx context.Context, ev CreateEvent) error
	SandboxDestroy(ctx context.Context, ev DestroyEvent) error
	SandboxViolation(ctx context.Context, ev ViolationEvent) error
}

// PathHash is the event form of a host path: "sha256:" and the hex digest
// of the path string.
func PathHash(p string) string {
	h := sha256.Sum256([]byte(p))
	return "sha256:" + hex.EncodeToString(h[:])
}

// NewCreateEvent builds sandbox.create for a validated Spec: its mount
// table (Spec.Mounts, so the event lists exactly what was mounted from the
// host), the limits the backend applied, the env key names, the proxy
// socket actually bound and the profile digest. Limits without a seccomp
// version, no env keys or a malformed digest are refused: an event that
// cannot say how the sandbox was confined is not written.
func NewCreateEvent(s *Spec, backend string, limits CreateLimits, envKeys []string, proxySocket, profileDigest string) (CreateEvent, error) {
	ms, err := s.Mounts()
	if err != nil {
		return CreateEvent{}, err
	}
	if limits.Seccomp == "" || limits.MemoryMB <= 0 || limits.Pids <= 0 {
		return CreateEvent{}, invalid("sandbox.create limits %+v are incomplete", limits)
	}
	if len(envKeys) == 0 {
		return CreateEvent{}, invalid("sandbox.create has no env keys")
	}
	if !reDigest.MatchString(profileDigest) {
		return CreateEvent{}, invalid("profile digest %q is not sha256:<hex>", profileDigest)
	}
	ev := CreateEvent{
		SessionID: s.SessionID, SandboxID: s.ID, Level: s.Level, Backend: backend, Purpose: s.Purpose,
		Mounts: EventMounts(ms), Limits: limits, EnvKeys: append([]string(nil), envKeys...), ProfileDigest: profileDigest,
	}
	if s.Level == LevelL1 {
		_, port, err := net.SplitHostPort(ForwarderAddr)
		if err != nil {
			return CreateEvent{}, fmt.Errorf("sandbox: forwarder address: %w", err)
		}
		ev.Proxy.Port, _ = strconv.Atoi(port)
		ev.Proxy.Socket = PathHash(proxySocket)
	}
	return ev, nil
}

// EventMounts renders a mount table for sandbox.create. A mask has no
// source of its own; it is hashed as the host path it hides (the parent
// mount's source plus the mask's path below the parent's target). The
// connect-only proxy socket is bound read-write (A06 §11.1) and reported
// so: the events schema knows only ro and rw.
func EventMounts(ms []Mount) []EventMount {
	out := make([]EventMount, 0, len(ms))
	var parent Mount
	for _, m := range ms {
		src := m.Source
		if m.Kind == KindMask {
			src = filepath.Join(parent.Source, filepath.FromSlash(strings.TrimPrefix(m.Target, parent.Target+"/")))
		} else {
			parent = m
		}
		mode := string(m.Mode)
		if m.Mode == ModeConnect {
			mode = string(ModeRW)
		}
		out = append(out, EventMount{HostPathHash: PathHash(src), SandboxPath: m.Target, Mode: mode, Kind: string(m.Kind)})
	}
	return out
}
