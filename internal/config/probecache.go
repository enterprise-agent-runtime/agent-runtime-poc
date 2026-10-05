package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"warden.dev/warden/internal/model"
)

// ProbeCache persists provider.test results under
// <home>/cache/providers/<provider_id>.json, mode 0600 (design A11 §9.4).
type ProbeCache struct {
	mu  sync.Mutex
	dir string
}

// NewProbeCache returns a cache rooted at dir.
func NewProbeCache(dir string) *ProbeCache { return &ProbeCache{dir: dir} }

func (c *ProbeCache) path(id string) string { return filepath.Join(c.dir, id+".json") }

// Load implements model.ProbeCache; a missing entry is (nil, nil).
func (c *ProbeCache) Load(id string) (*model.ProbeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := os.ReadFile(c.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r model.ProbeResult
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Store implements model.ProbeCache.
func (c *ProbeCache) Store(r model.ProbeResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(r)
}

func (c *ProbeCache) writeLocked(r model.ProbeResult) error {
	if strings.ContainsAny(r.ProviderID, `/\.`) || r.ProviderID == "" {
		return errors.New("invalid provider id")
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	tmp := c.path(r.ProviderID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path(r.ProviderID))
}

// LearnQuirk implements model.ProbeCache: it records a quirk learned by a
// shape retry on the model's cached probe entry.
func (c *ProbeCache) LearnQuirk(providerID, modelID, flag string, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var r model.ProbeResult
	if b, err := os.ReadFile(c.path(providerID)); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	r.ProviderID = providerID
	for i := range r.Models {
		if r.Models[i].ModelID == modelID {
			if r.Models[i].Quirks == nil {
				r.Models[i].Quirks = map[string]any{}
			}
			r.Models[i].Quirks[flag] = value
			return c.writeLocked(r)
		}
	}
	// No probe record yet: the adapter keeps the quirk in memory. Writing a
	// synthetic record here would lower the model's effective capabilities.
	return nil
}

// Remove deletes a provider's cache entry.
func (c *ProbeCache) Remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = os.Remove(c.path(id))
}
