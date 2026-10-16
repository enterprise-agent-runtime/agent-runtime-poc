//go:build !windows

package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPublicKeys_RefusesKeyWritableByOthers: a key file group or others can
// write could have been replaced by another account while keys/ was loose,
// so it fails PublicKeys closed, with the chmod go-w hint (WH-4 round 1).
func TestPublicKeys_RefusesKeyWritableByOthers(t *testing.T) {
	for _, perm := range []os.FileMode{0o664, 0o646, 0o666} {
		dir := t.TempDir()
		writePub(t, filepath.Join(dir, "checkpoint-0123456789abcdef.pub"), perm)
		keys, err := PublicKeys(dir)
		if err == nil || keys != nil || !strings.Contains(err.Error(), "chmod go-w") {
			t.Errorf("%v key file: PublicKeys = %v, %v; want a refusal with the chmod go-w hint", perm, keys, err)
		}
	}
}

// TestLoadSigner_MovesWritableKeyAside: a checkpoint.pub others can write is
// moved aside, not archived as a trusted key and not lost (WH-4 round 1).
func TestLoadSigner_MovesWritableKeyAside(t *testing.T) {
	dir := t.TempDir()
	planted := writePub(t, filepath.Join(dir, "checkpoint.pub"), 0o666)
	content, err := os.ReadFile(filepath.Join(dir, "checkpoint.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigner(ctx, NewBroker(NewMemory(), nil, nil), dir); err != nil {
		t.Fatal(err)
	}
	assertMovedAside(t, dir, content)
	keys, err := PublicKeys(dir)
	if err != nil || keys[KeyIDOf(planted)] != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, %v; want only the signer's, the writable key untrusted", keys, err)
	}
}
