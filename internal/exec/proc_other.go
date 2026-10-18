//go:build !linux && !darwin

package exec

import (
	"context"
	"time"

	"warden.dev/warden/internal/execproto"
)

// Procs is unsupported here: the executor runs only inside a Linux or
// macOS sandbox (on a Windows host, in the L2 Linux container; CLAUDE.md
// §3). The type exists so the package and its registration compile.
type Procs struct{}

// NewProcs always fails on this OS.
func NewProcs(context.Context, ProcConfig, func(execproto.IONotification) error) (*Procs, error) {
	return nil, unsupported()
}

// Spawn always fails on this OS.
func (*Procs) Spawn(context.Context, execproto.SpawnParams) (execproto.SpawnResult, error) {
	return execproto.SpawnResult{}, unsupported()
}

// Wait always fails on this OS.
func (*Procs) Wait(context.Context, execproto.WaitParams) (execproto.WaitResult, error) {
	return execproto.WaitResult{}, unsupported()
}

// Signal always fails on this OS.
func (*Procs) Signal(context.Context, execproto.SignalParams) (execproto.SignalResult, error) {
	return execproto.SignalResult{}, unsupported()
}

// Shutdown always fails on this OS.
func (*Procs) Shutdown(context.Context, time.Duration, execproto.ShutdownReason) (execproto.ShutdownResult, error) {
	return execproto.ShutdownResult{}, unsupported()
}

// Close does nothing.
func (*Procs) Close() {}

// SetChildSubreaper does nothing on this OS.
func SetChildSubreaper() error { return nil }

// RunChild always fails on this OS.
func RunChild([]string) error { return unsupported() }
