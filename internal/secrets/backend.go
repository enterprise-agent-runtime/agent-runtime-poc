package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// Backend errors (A15 §2.3).
var (
	ErrNotFound    = errors.New("credential_missing")
	ErrLocked      = errors.New("keychain_locked")
	ErrUnavailable = errors.New("keychain_unavailable")
	ErrTimeout     = errors.New("keychain_timeout")
)

// Backend stores secret values. Name is reported by doctor and in
// secret.access events as "backend" (CLAUDE.md §3).
type Backend interface {
	Name() string // secret-service | keychain | wincred | file | memory
	Get(ctx context.Context, account string) ([]byte, error)
	Set(ctx context.Context, account string, value []byte) error
	Delete(ctx context.Context, account string) error
}

// Memory is the in-memory backend for tests (CLAUDE.md §8.1).
type Memory struct {
	mu sync.Mutex
	m  map[string][]byte
}

// NewMemory returns an empty in-memory backend.
func NewMemory() *Memory { return &Memory{m: map[string][]byte{}} }

// Name implements Backend.
func (*Memory) Name() string { return "memory" }

// Get implements Backend.
func (b *Memory) Get(_ context.Context, a string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.m[a]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

// Set implements Backend.
func (b *Memory) Set(_ context.Context, a string, v []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[a] = append([]byte(nil), v...)
	return nil
}

// Delete implements Backend.
func (b *Memory) Delete(_ context.Context, a string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.m[a]; !ok {
		return ErrNotFound
	}
	delete(b.m, a)
	return nil
}

// KeyringName is the OS keychain backend name for this OS.
func KeyringName() string {
	switch runtime.GOOS {
	case "darwin":
		return "keychain"
	case "windows":
		return "wincred"
	}
	return "secret-service"
}

// Keyring is the OS keychain through go-keyring. Calls are serialized (one
// unlock prompt at a time) and bounded by a 30 s timeout; go-keyring calls
// cannot be cancelled, so a stuck call is abandoned (A15 §2.3).
type Keyring struct {
	mu      sync.Mutex
	timeout time.Duration
}

// NewKeyring returns the OS keychain backend.
func NewKeyring() *Keyring { return &Keyring{timeout: 30 * time.Second} }

// Name implements Backend.
func (*Keyring) Name() string { return KeyringName() }

func (k *Keyring) call(ctx context.Context, fn func() (string, error)) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	type res struct {
		v   string
		err error
	}
	ch := make(chan res, 1)
	go func() { v, err := fn(); ch <- res{v, err} }()
	t := time.NewTimer(k.timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		switch {
		case r.err == nil:
			return r.v, nil
		case errors.Is(r.err, keyring.ErrNotFound):
			return "", ErrNotFound
		case errors.Is(r.err, keyring.ErrSetDataTooBig):
			return "", fmt.Errorf("value too large for the keychain (Windows Credential Manager holds at most 2560 bytes; use auth.cert_file for certificate chains): %w", ErrUnavailable)
		}
		return "", fmt.Errorf("%w: %v", ErrUnavailable, r.err)
	case <-t.C:
		return "", ErrTimeout
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Get implements Backend.
func (k *Keyring) Get(ctx context.Context, a string) ([]byte, error) {
	v, err := k.call(ctx, func() (string, error) { return keyring.Get(Service, a) })
	if err != nil {
		return nil, err
	}
	return []byte(v), nil
}

// Set implements Backend.
func (k *Keyring) Set(ctx context.Context, a string, v []byte) error {
	_, err := k.call(ctx, func() (string, error) { return "", keyring.Set(Service, a, string(v)) })
	return err
}

// Delete implements Backend.
func (k *Keyring) Delete(ctx context.Context, a string) error {
	_, err := k.call(ctx, func() (string, error) { return "", keyring.Delete(Service, a) })
	return err
}

// File is the encrypted-file backend of CLAUDE.md §3 (format in
// docs/DECISIONS-poc.md D-010): PBKDF2-HMAC-SHA-256 (600,000 iterations,
// 16-byte salt) derives an AES-256-GCM key from a passphrase that lives only
// in memory for the daemon's lifetime. The store starts locked.
type File struct {
	mu   sync.Mutex
	path string
	key  []byte // nil while locked
	salt []byte
	m    map[string][]byte
}

type fileFormat struct {
	V     int    `json:"v"`
	KDF   string `json:"kdf"`
	Iter  int    `json:"iter"`
	Salt  string `json:"salt"`
	Nonce string `json:"nonce"`
	CT    string `json:"ct"`
}

// PBKDF2Iterations is the work factor of the file backend.
const PBKDF2Iterations = 600000

// MinPassphrase is the minimum passphrase length when creating the file.
const MinPassphrase = 12

// NewFile returns the backend for path (locked).
func NewFile(path string) *File { return &File{path: path} }

// Name implements Backend.
func (*File) Name() string { return "file" }

// Exists reports whether the encrypted file has been created.
func (f *File) Exists() bool { _, err := os.Stat(f.path); return err == nil }

// Locked reports whether the store still needs its passphrase.
func (f *File) Locked() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.key == nil }

// Unlock opens the file with the passphrase, or creates it when absent.
// A wrong passphrase fails the AEAD and leaves the store locked.
func (f *File) Unlock(passphrase []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		if len(passphrase) < MinPassphrase {
			return fmt.Errorf("passphrase must be at least %d characters", MinPassphrase)
		}
		f.salt = make([]byte, 16)
		if _, err := rand.Read(f.salt); err != nil {
			return err
		}
		if f.key, err = derive(passphrase, f.salt, PBKDF2Iterations); err != nil {
			return err
		}
		f.m = map[string][]byte{}
		return f.writeLocked()
	}
	if err != nil {
		return err
	}
	var ff fileFormat
	if err := json.Unmarshal(b, &ff); err != nil || ff.V != 1 || ff.KDF != "pbkdf2-sha256" {
		return fmt.Errorf("%s is not a Warden secrets file", f.path)
	}
	salt, _ := base64.StdEncoding.DecodeString(ff.Salt)
	nonce, _ := base64.StdEncoding.DecodeString(ff.Nonce)
	ct, _ := base64.StdEncoding.DecodeString(ff.CT)
	key, err := derive(passphrase, salt, ff.Iter)
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	pt, err := gcm.Open(nil, nonce, ct, aad(ff))
	if err != nil {
		return fmt.Errorf("%w: wrong passphrase", ErrLocked)
	}
	m := map[string][]byte{}
	if err := json.Unmarshal(pt, &m); err != nil {
		return err
	}
	f.key, f.salt, f.m = key, salt, m
	return nil
}

func derive(pass, salt []byte, iter int) ([]byte, error) {
	if iter < 100000 {
		return nil, errors.New("pbkdf2 iteration count too low")
	}
	return pbkdf2.Key(sha256.New, string(pass), salt, iter, 32)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// aad binds the header fields to the ciphertext.
func aad(ff fileFormat) []byte {
	return []byte(fmt.Sprintf("warden-secrets/v%d|%s|%d|%s", ff.V, ff.KDF, ff.Iter, ff.Salt))
}

func (f *File) writeLocked() error {
	pt, err := json.Marshal(f.m)
	if err != nil {
		return err
	}
	gcm, err := newGCM(f.key)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ff := fileFormat{V: 1, KDF: "pbkdf2-sha256", Iter: PBKDF2Iterations, Salt: base64.StdEncoding.EncodeToString(f.salt), Nonce: base64.StdEncoding.EncodeToString(nonce)}
	ff.CT = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, pt, aad(ff)))
	b, _ := json.Marshal(ff)
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// Get implements Backend.
func (f *File) Get(_ context.Context, a string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key == nil {
		return nil, ErrLocked
	}
	v, ok := f.m[a]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

// Set implements Backend.
func (f *File) Set(_ context.Context, a string, v []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key == nil {
		return ErrLocked
	}
	f.m[a] = append([]byte(nil), v...)
	return f.writeLocked()
}

// Delete implements Backend.
func (f *File) Delete(_ context.Context, a string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key == nil {
		return ErrLocked
	}
	if _, ok := f.m[a]; !ok {
		return ErrNotFound
	}
	delete(f.m, a)
	return f.writeLocked()
}

// Select picks the backend (D-ENV-05): the OS keychain when a probe
// round-trip works, else the encrypted file. A locked keychain is not a
// reason to switch (that would split the store).
func Select(ctx context.Context, home string, probe bool) Backend {
	k := NewKeyring()
	if !probe {
		return k
	}
	err := k.Set(ctx, "doctor/probe", []byte("probe"))
	if err == nil {
		_ = k.Delete(ctx, "doctor/probe")
		return k
	}
	if errors.Is(err, ErrLocked) || errors.Is(err, ErrTimeout) {
		return k
	}
	return NewFile(filepath.Join(home, "secrets.enc"))
}
