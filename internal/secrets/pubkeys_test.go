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

// writePub writes an ed25519 public key with os.WriteFile and mode perm,
// as a build before WH-4 wrote checkpoint.pub and its archives: mode 0644
// on Unix, the directory's inherited DACL on Windows (os.WriteFile sets no
// descriptor of its own).
func writePub(t *testing.T, path string, perm os.FileMode) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil { // past the umask
		t.Fatal(err)
	}
	return pub
}

// TestPublicKeys_AcceptsOwnReadableKeyFiles guards WH-4 round 1 (F1,
// CWE-1188, reproduced in WSL): PublicKeys required ReadOwnerOnly, a
// secrecy check, but every checkpoint.pub and archived checkpoint-<id>.pub
// a build before WH-4 wrote is 0644 (Unix) or has an inherited DACL
// (Windows). wardend then refused to start on an upgraded home, and with a
// locked encrypted-file backend nothing rewrote checkpoint.pub, so there
// was no way back in the product. A public key needs integrity, not
// secrecy: a regular file the user owns that nobody else can write loads.
func TestPublicKeys_AcceptsOwnReadableKeyFiles(t *testing.T) {
	dir := t.TempDir()
	cur := writePub(t, filepath.Join(dir, "checkpoint.pub"), 0o644)
	old := writePub(t, filepath.Join(dir, "checkpoint-0123456789abcdef.pub"), 0o644)
	keys, err := PublicKeys(dir)
	if err != nil {
		t.Fatalf("PublicKeys of an upgraded home's 0644 / inherited-DACL key files = %v", err)
	}
	if keys[KeyIDOf(cur)] == nil || keys[KeyIDOf(old)] == nil || len(keys) != 2 {
		t.Fatalf("keys = %v; want checkpoint.pub and the archived key", keys)
	}
}

// TestPublicKeys_RefusesUntrustedEntry: an entry that fails the integrity
// check (here a directory named like a key, refused on every OS; the
// writable-by-others cases are in pubkeys_unix_test.go and, for the DACL,
// in internal/platform) fails PublicKeys closed with an error naming it,
// and the hint never says to remove a key file (WH-4).
func TestPublicKeys_RefusesUntrustedEntry(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadSigner(ctx, NewBroker(NewMemory(), nil, nil), dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "checkpoint-odd.pub"), 0o700); err != nil {
		t.Fatal(err)
	}
	keys, err := PublicKeys(dir)
	if err == nil || keys != nil || !strings.Contains(err.Error(), "checkpoint-odd.pub") {
		t.Fatalf("PublicKeys with an untrusted entry = %v, %v; want a refusal naming it", keys, err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "remove it") {
		t.Fatalf("refusal %q tells the user to remove a verification key", err)
	}
}

// TestLoadSigner_ArchivesUpgradedKey: a 0644 / inherited-DACL checkpoint.pub
// written by a build before WH-4 is the user's own key; when the keychain
// key differs it is archived (owner-only now) and stays trusted, so old
// checkpoints remain verifiable (D-042's upgrade note).
func TestLoadSigner_ArchivesUpgradedKey(t *testing.T) {
	dir := t.TempDir()
	old := writePub(t, filepath.Join(dir, "checkpoint.pub"), 0o644)
	s, err := LoadSigner(ctx, NewBroker(NewMemory(), nil, nil), dir)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := PublicKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if keys[KeyIDOf(old)] == nil || keys[s.KeyID()] == nil || len(keys) != 2 {
		t.Fatalf("keys = %v; want the archived upgraded key and the signer's", keys)
	}
}

// TestLoadSigner_MovesRefusedKeyAside guards WH-4 round 1 (F1): a
// checkpoint.pub that fails the integrity check must neither be archived
// (that would launder a planted key into a trusted one) nor be overwritten
// and lost (it may be a legitimate old key). It is moved aside as
// checkpoint.pub.refused-<unix nanos>, which PublicKeys does not load.
// Here the refusal is a file too large to be a key, which fails on every
// OS; the writable-by-others case is in pubkeys_unix_test.go.
func TestLoadSigner_MovesRefusedKeyAside(t *testing.T) {
	dir := t.TempDir()
	big := []byte(strings.Repeat("x", maxPubFile+1))
	if err := os.WriteFile(filepath.Join(dir, "checkpoint.pub"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSigner(ctx, NewBroker(NewMemory(), nil, nil), dir)
	if err != nil {
		t.Fatal(err)
	}
	assertMovedAside(t, dir, big)
	keys, err := PublicKeys(dir)
	if err != nil || len(keys) != 1 || keys[s.KeyID()] == nil {
		t.Fatalf("keys = %v, %v; want only the signer's", keys, err)
	}
}

// assertMovedAside checks that exactly one checkpoint.pub.refused-* exists
// and holds want.
func assertMovedAside(t *testing.T, dir string, want []byte) {
	t.Helper()
	moved, err := filepath.Glob(filepath.Join(dir, "checkpoint.pub.refused-*"))
	if err != nil || len(moved) != 1 {
		ents, _ := os.ReadDir(dir)
		t.Fatalf("refused checkpoint.pub not moved aside: %v %v; keys/ holds %v", moved, err, ents)
	}
	if got, err := os.ReadFile(moved[0]); err != nil || string(got) != string(want) {
		t.Fatalf("moved-aside file %s does not hold the refused content (%v)", moved[0], err)
	}
}
