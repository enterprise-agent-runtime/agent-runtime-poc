//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultSandboxLevel_Unix: L1 by default on Linux and macOS, L2
// available (CLAUDE.md §3).
func TestDefaultSandboxLevel_Unix(t *testing.T) {
	if DefaultSandboxLevel() != "L1" || len(SandboxLevels()) != 2 {
		t.Fatalf("level %s %v", DefaultSandboxLevel(), SandboxLevels())
	}
}

// TestEndpoint_LongHomeFallsBack: a home whose socket path exceeds
// sun_path gets a hashed socket in a private temp directory.
func TestEndpoint_LongHomeFallsBack(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	e := Endpoint(long)
	if len(e) >= maxSocketPath || !strings.HasPrefix(e, os.TempDir()) {
		t.Fatalf("endpoint %s", e)
	}
	if Endpoint("/h") != "/h/run/wardend.sock" {
		t.Fatalf("short endpoint %s", Endpoint("/h"))
	}
}

func TestSocketIsOwnerOnly(t *testing.T) {
	h := tempHome(t)
	l, err := Listen(h)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	fi, err := os.Stat(Endpoint(h))
	if err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket mode %v %v", fi.Mode(), err)
	}
}
