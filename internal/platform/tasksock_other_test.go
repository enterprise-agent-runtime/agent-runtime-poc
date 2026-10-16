//go:build !linux && !darwin

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestListenTaskSocket_UnsupportedCreatesNothing: L1, and with it the
// per-task proxy socket, exists only on Linux and macOS (CLAUDE.md §4).
// Elsewhere the call says so and leaves the disk untouched: not even the
// scratch directory is created.
func TestListenTaskSocket_UnsupportedCreatesNothing(t *testing.T) {
	home := t.TempDir()
	scratch := filepath.Join(home, "sessions", "01jaxr8q7m2v9ktc3f6yh5n0pb", "scratch")
	s, err := ListenTaskSocket(home, filepath.Join(scratch, "px-1.sock"))
	if s != nil || !errors.Is(err, ErrTaskSocketUnsupported) {
		t.Fatalf("ListenTaskSocket = %v, %v; want nil, ErrTaskSocketUnsupported", s, err)
	}
	ents, err := os.ReadDir(home)
	if err != nil || len(ents) != 0 {
		t.Fatalf("home holds %v (%v), want nothing", ents, err)
	}
	if removed, err := SweepTaskSockets(home); err != nil || removed != nil {
		t.Fatalf("SweepTaskSockets = %v, %v; want nothing", removed, err)
	}
}
