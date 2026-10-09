// Package storetest holds test doubles for the store (CLAUDE.md §8.4): a
// deterministic Ed25519 Signer and a deterministic clock and id source, so
// fixture stores and exports are reproducible byte for byte.
package storetest

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"warden.dev/warden/internal/ids"
)

// RFC8032Seed is the private seed of RFC 8032 §7.1 TEST 1, a published test
// vector (never a real key).
const RFC8032Seed = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"

// Signer signs with a fixed key; Locked makes Sign fail like a locked keychain.
type Signer struct {
	priv   ed25519.PrivateKey
	Locked bool
}

// NewSigner returns a signer for a hex seed (RFC8032Seed when empty).
func NewSigner(seedHex string) *Signer {
	if seedHex == "" {
		seedHex = RFC8032Seed
	}
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		panic("storetest: bad seed")
	}
	return &Signer{priv: ed25519.NewKeyFromSeed(seed)}
}

// KeyID is the first 16 hex characters of SHA-256(public key).
func (s *Signer) KeyID() string {
	h := sha256.Sum256(s.PublicKey())
	return hex.EncodeToString(h[:])[:16]
}

// PublicKey returns the public key.
func (s *Signer) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

// Sign signs msg.
func (s *Signer) Sign(_ context.Context, msg []byte) ([]byte, error) {
	if s.Locked {
		return nil, errors.New("keychain locked")
	}
	return ed25519.Sign(s.priv, msg), nil
}

// Clock is a deterministic clock that advances by Step on every call.
type Clock struct {
	mu   sync.Mutex
	t    time.Time
	Step time.Duration
}

// NewClock starts at t and advances by step per reading.
func NewClock(t time.Time, step time.Duration) *Clock { return &Clock{t: t, Step: step} }

// Now returns the current time and advances the clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.t
	c.t = c.t.Add(c.Step)
	return t
}

// IDs returns a deterministic id generator driven by the clock.
func IDs(c *Clock) *ids.Generator {
	return ids.NewWith(c.Now, zeroReader{})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
