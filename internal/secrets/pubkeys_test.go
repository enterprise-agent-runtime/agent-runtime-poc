package secrets

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantLoosePub writes an ed25519 public key that another account could
// have written while keys/ was loose: mode 0644 on Unix, the directory's
// inherited DACL on Windows (os.WriteFile sets no descriptor of its own).
func plantLoosePub(t *testing.T, path string) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // past the umask
		t.Fatal(err)
	}
	return pub
}

// TestPublicKeys_RefusesLooseKeyFile guards WH-4 (WH-1+2 review, CWE-732):
// keys/checkpoint*.pub are the keys checkpoint signatures are verified
// with, and PublicKeys read them with os.ReadFile, so a key another account
// planted while keys/ was loose (before Ensure tightened it, D-052) was
// trusted. Each file must pass ReadOwnerOnly; one that does not makes
// PublicKeys fail (fail closed) rather than be trusted or silently skipped.
func TestPublicKeys_RefusesLooseKeyFile(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadSigner(ctx, NewBroker(NewMemory(), nil, nil), dir)
	if err != nil {
		t.Fatal(err)
	}
	planted := plantLoosePub(t, filepath.Join(dir, "checkpoint-planted.pub"))
	keys, err := PublicKeys(dir)
	if err == nil || !strings.Contains(err.Error(), "checkpoint-planted.pub") || !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("PublicKeys with a loose, planted key file = %v, %v; want a refusal naming it", keys, err)
	}
	if _, ok := keys[KeyIDOf(planted)]; ok {
		t.Fatalf("a loose, planted key file is trusted: %v", keys)
	}
	// The daemon's own files pass: the signer's key alone is accepted.
	if err := os.Remove(filepath.Join(dir, "checkpoint-planted.pub")); err != nil {
		t.Fatal(err)
	}
	if keys, err := PublicKeys(dir); err != nil || len(keys) != 1 || keys[s.KeyID()] == nil {
		t.Fatalf("PublicKeys of the signer's own files = %v, %v", keys, err)
	}
}

// TestLoadSigner_DoesNotArchivePlantedKey: when the keychain key and
// checkpoint.pub disagree, LoadSigner archives the old public key as
// checkpoint-<id>.pub. Reading the old file with os.ReadFile and archiving
// it owner-only would launder a planted key into a trusted one; a
// checkpoint.pub that fails ReadOwnerOnly is replaced, never archived.
func TestLoadSigner_DoesNotArchivePlantedKey(t *testing.T) {
	dir := t.TempDir()
	planted := plantLoosePub(t, filepath.Join(dir, "checkpoint.pub"))
	s, err := LoadSigner(ctx, NewBroker(NewMemory(), nil, nil), dir)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := PublicKeys(dir)
	if err != nil {
		t.Fatalf("PublicKeys after LoadSigner replaced the planted checkpoint.pub: %v", err)
	}
	if _, ok := keys[KeyIDOf(planted)]; ok {
		t.Fatalf("the planted checkpoint.pub was archived and is trusted: %v", keys)
	}
	if _, ok := keys[s.KeyID()]; !ok || len(keys) != 1 {
		t.Fatalf("keys = %v; want only the signer's", keys)
	}
}
