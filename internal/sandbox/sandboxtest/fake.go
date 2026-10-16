// Package sandboxtest provides FakeSandbox, the in-memory sandbox.Manager of
// CLAUDE.md §8.4 for unit tests above internal/sandbox (design A06 §13,
// §17). It starts no process and mounts nothing, but it builds every
// sandbox from the same Spec, mount and environment code as the real
// backends, so a test using it still cannot create a sandbox that breaks
// INV-B, INV-C or INV-I.
package sandboxtest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"warden.dev/warden/internal/sandbox"
)

// Destroyed records one Destroy call.
type Destroyed struct {
	ID     string
	Reason sandbox.DestroyReason
}

// FakeSandbox is an in-memory sandbox.Manager. The zero value is ready to
// use; it is safe for concurrent use.
type FakeSandbox struct {
	// Now is the clock for Handle.Created; nil means time.Now.
	Now func() time.Time
	// CreateErr, when set, is returned by every Create (backend failure).
	CreateErr error

	mu        sync.Mutex
	live      map[string]*sandbox.Handle
	specs     map[string]sandbox.Spec
	destroyed []Destroyed
}

var _ sandbox.Manager = (*FakeSandbox)(nil)

// Create implements sandbox.Manager.
func (f *FakeSandbox) Create(ctx context.Context, s *sandbox.Spec) (*sandbox.Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.CreateErr != nil {
		return nil, f.CreateErr
	}
	mounts, err := s.Mounts()
	if err != nil {
		return nil, err
	}
	if _, err := s.Env(); err != nil {
		return nil, err
	}
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, dup := f.live[s.ID]; dup {
		return nil, fmt.Errorf("%w: sandbox %s already exists", sandbox.ErrInvalidSpec, s.ID)
	}
	if f.live == nil {
		f.live = map[string]*sandbox.Handle{}
		f.specs = map[string]sandbox.Spec{}
	}
	h := &sandbox.Handle{ID: s.ID, Level: s.Level, Backend: "fake", Mounts: mounts, EnvKeys: sandbox.EnvKeys(), Created: now()}
	f.live[s.ID] = h
	f.specs[s.ID] = *s
	cp := *h
	cp.Mounts = slices.Clone(h.Mounts)
	return &cp, nil
}

// Destroy implements sandbox.Manager.
func (f *FakeSandbox) Destroy(ctx context.Context, id string, reason sandbox.DestroyReason) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !reason.Valid() {
		return fmt.Errorf("sandboxtest: destroy reason %q is not an A06 §13.3 reason", reason)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.live[id]; !ok {
		return fmt.Errorf("%w: %s", sandbox.ErrUnknownSandbox, id)
	}
	delete(f.live, id)
	delete(f.specs, id)
	f.destroyed = append(f.destroyed, Destroyed{ID: id, Reason: reason})
	return nil
}

// Live returns the ids of the sandboxes not yet destroyed, sorted.
func (f *FakeSandbox) Live() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.live))
	for id := range f.live {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Spec returns the Spec a live sandbox was created from.
func (f *FakeSandbox) Spec(id string) (sandbox.Spec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.specs[id]
	return s, ok
}

// Destroyed returns every Destroy call so far, in order.
func (f *FakeSandbox) Destroyed() []Destroyed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.destroyed)
}
