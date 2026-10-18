package sandbox

// The backend-neutral manager contract of design A06 §13 and §17. The real
// managers (bwrap L1, Docker L2) arrive with M2-13..M2-21; the in-memory
// double is sandboxtest.FakeSandbox (CLAUDE.md §8.4).

import (
	"context"
	"time"
)

// DestroyReason is sandbox.destroy reason (A06 §13.3 step 6).
type DestroyReason string

// Destroy reasons.
const (
	ReasonTaskEnd       DestroyReason = "task_end"
	ReasonCancel        DestroyReason = "cancel"
	ReasonTimeout       DestroyReason = "timeout"
	ReasonResource      DestroyReason = "resource"
	ReasonExecutorExit  DestroyReason = "executor_exit"
	ReasonHelloMismatch DestroyReason = "hello_mismatch"
	ReasonDaemonStop    DestroyReason = "daemon_stop"
	ReasonOrphan        DestroyReason = "orphan"
	// ReasonStoreUnavailable (events schema; not in A06 §13.3): the
	// sandbox was torn down because sandbox.create could not be written.
	ReasonStoreUnavailable DestroyReason = "store_unavailable"
)

// Valid reports whether r is one of the A06 §13.3 reasons.
func (r DestroyReason) Valid() bool {
	switch r {
	case ReasonTaskEnd, ReasonCancel, ReasonTimeout, ReasonResource, ReasonExecutorExit,
		ReasonHelloMismatch, ReasonDaemonStop, ReasonOrphan, ReasonStoreUnavailable:
		return true
	}
	return false
}

// Handle is a sandbox in state ready (A06 §13.1). The executor client joins
// it with the protocol client (internal/tools).
type Handle struct {
	ID      string
	Level   Level
	Backend string    // "bwrap" | "seatbelt" | "docker" | "fake"
	Mounts  []Mount   // as built from the Spec
	EnvKeys []string  // names only, never values (sandbox.create env_keys)
	Created time.Time // when exec.hello succeeded
}

// Manager creates and destroys sandboxes. One execution owns one sandbox
// (CLAUDE.md §7); Create returns only once the sandbox is ready, and every
// Create that returned a Handle is matched by exactly one Destroy.
//
// Events: the real backends emit sandbox.create after exec.hello and
// sandbox.destroy at the end of teardown (A06 §13.2, §13.3) through the
// EventSink they are configured with (events.go; the L1 backend is
// linux_bwrap.Manager). sandboxtest.FakeSandbox emits no events.
//
// The L2 manager (M2-21) calls CheckEngine immediately before it creates
// any network or container, on every Create, and refuses with its error;
// doctor's sandbox.l2 check only reports (CWE-636).
type Manager interface {
	// Create validates the Spec, builds and launches the sandbox and
	// returns it ready.
	Create(ctx context.Context, s *Spec) (*Handle, error)
	// Destroy tears the sandbox down. An unknown id is ErrUnknownSandbox.
	Destroy(ctx context.Context, id string, reason DestroyReason) error
}
