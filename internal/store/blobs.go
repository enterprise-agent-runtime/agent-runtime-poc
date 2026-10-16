package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// blobPath maps "sha256:<hex>" to blobs/sha256/<aa>/<hex> (core §10).
func (s *Store) blobPath(hash string) (string, error) {
	hex, ok := strings.CutPrefix(hash, "sha256:")
	if !ok || len(hex) != 64 {
		return "", fmt.Errorf("store: invalid blob hash %q", hash)
	}
	return filepath.Join(s.blobsDir, "sha256", hex[:2], hex), nil
}

// putBlob writes content-addressed bytes atomically (temp file + rename)
// with owner-only permissions and returns the hash. Writing the same
// content twice is a no-op.
func (s *Store) putBlob(b []byte) (string, error) {
	h := shaBytes(b)
	p, err := s.blobPath(h)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	_, werr := f.Write(b)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := os.Rename(f.Name(), p); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	_ = os.Chmod(p, 0o600)
	return h, nil
}

// GetBlob reads a blob and checks its hash.
func (s *Store) GetBlob(hash string) ([]byte, error) {
	p, err := s.blobPath(hash)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: blob %s", ErrNotFound, hash)
	}
	if err != nil {
		return nil, err
	}
	if shaBytes(b) != hash {
		return nil, fmt.Errorf("store: blob %s content does not match its hash", hash)
	}
	return b, nil
}
