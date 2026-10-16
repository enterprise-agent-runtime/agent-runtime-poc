package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warden.dev/warden/internal/platform"
)

// TestWriteToken_VerifiesBeforeServing guards WH-1+2 (review of c8f81ca):
// wardend writes the token file and must verify it before it binds and
// serves. The writer is injected, so a write that leaves the file readable
// by others (the inherited DACL of the directory on Windows, 0644 on Unix)
// is caught here: deleting the verification fails this test. A refused
// file is removed, so no client reads a token from it.
func TestWriteToken_VerifiesBeforeServing(t *testing.T) {
	const tok = "dG9rZW4tZm9yLXRlc3RzLW9ubHktbm90LWEtc2VjcmV0"
	dir := t.TempDir()

	good := filepath.Join(dir, "run", "token")
	if err := writeToken(good, tok, platform.WriteOwnerOnly); err != nil {
		t.Fatalf("owner-only write refused: %v", err)
	}

	loose := filepath.Join(dir, "loose")
	looseWrite := func(p string, b []byte) error {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		return os.Chmod(p, 0o644) // past the umask
	}
	err := writeToken(loose, tok, looseWrite)
	if err == nil || !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("loose token file: %v; want an owner-only refusal", err)
	}
	if _, err := os.Lstat(loose); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused token file left in place (%v)", err)
	}

	other := filepath.Join(dir, "other")
	wrong := func(p string, _ []byte) error { return platform.WriteOwnerOnly(p, []byte("something else")) }
	if err := writeToken(other, tok, wrong); err == nil {
		t.Fatal("token file holding another value accepted")
	}
}
