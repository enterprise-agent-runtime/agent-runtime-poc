package secrets

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CheckpointRef is the keychain reference of the checkpoint signing seed.
const CheckpointRef = "secret://keys/checkpoint/ed25519"

// Signer signs chain.checkpoint events (design A04 §11.3). The 32-byte seed
// lives in the keychain; the public key is written to keys/checkpoint.pub
// (PKIX PEM) so exports can be verified elsewhere. The seed is read once per
// daemon start and held in memory.
type Signer struct {
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey
	keyID string
}

// KeyIDOf is the first 16 hex characters of SHA-256(public key).
func KeyIDOf(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])[:16]
}

// LoadSigner loads the checkpoint key, generating it on first use. When the
// keychain seed and keys/checkpoint.pub disagree, the old public key is
// archived as keys/checkpoint-<key_id>.pub and the keychain key wins.
func LoadSigner(ctx context.Context, b *Broker, keysDir string) (*Signer, error) {
	seedB64, err := b.resolve(ctx, CheckpointRef, "checkpoint", "checkpoint_sign")
	var seed []byte
	switch {
	case err == nil:
		seed, err = base64.RawURLEncoding.DecodeString(string(seedB64))
		wipe(seedB64)
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, errors.New("checkpoint key in the keychain is malformed")
		}
	case errors.Is(err, ErrNotFound):
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		r, _ := Parse(CheckpointRef)
		if err := b.backend.Set(ctx, r.Account, []byte(base64.RawURLEncoding.EncodeToString(seed))); err != nil {
			return nil, fmt.Errorf("store checkpoint key: %w", err)
		}
	default:
		return nil, err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	wipe(seed)
	s := &Signer{priv: priv, pub: priv.Public().(ed25519.PublicKey)}
	s.keyID = KeyIDOf(s.pub)
	if err := writePublicKey(keysDir, s.pub); err != nil {
		return nil, err
	}
	return s, nil
}

// KeyID implements store.Signer.
func (s *Signer) KeyID() string { return s.keyID }

// PublicKey implements store.Signer.
func (s *Signer) PublicKey() ed25519.PublicKey { return s.pub }

// Sign implements store.Signer.
func (s *Signer) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, msg), nil
}

func writePublicKey(dir string, pub ed25519.PublicKey) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	path := filepath.Join(dir, "checkpoint.pub")
	if old, err := os.ReadFile(path); err == nil && string(old) != string(p) {
		if oldPub, err := parsePub(old); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "checkpoint-"+KeyIDOf(oldPub)+".pub"), old, 0o644)
		}
	}
	return os.WriteFile(path, p, 0o644)
}

func parsePub(b []byte) (ed25519.PublicKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("no PEM")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not ed25519")
	}
	return pub, nil
}

// PublicKeys loads checkpoint.pub and every archived checkpoint-<id>.pub,
// keyed by key_id, for verification.
func PublicKeys(dir string) map[string]ed25519.PublicKey {
	out := map[string]ed25519.PublicKey{}
	matches, _ := filepath.Glob(filepath.Join(dir, "checkpoint*.pub"))
	for _, m := range matches {
		if b, err := os.ReadFile(m); err == nil {
			if pub, err := parsePub(b); err == nil {
				out[KeyIDOf(pub)] = pub
			}
		}
	}
	return out
}
