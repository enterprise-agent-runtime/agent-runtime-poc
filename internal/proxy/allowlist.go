package proxy

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Source says where an allowlist entry came from (A07 §5.1, §12).
type Source string

// The A07 §12 sources.
const (
	SourceCapability Source = "capability" // manifest egress.allow ∩ policy allow
	SourceObligation Source = "obligation" // egress_allow obligation of an allowed call
	SourceGrant      Source = "grant"      // approved proxy.connect grant, or a PDP allow
	SourceHarness    Source = "harness"    // vendor allowlist of a harness sandbox
)

// RuleCapability is the event rule for capability and harness entries,
// whose decision_id is null (A07 §5.2).
const RuleCapability = "capability.egress"

// Entry is one allowlist entry. Key is a canonical host:port (Normalize).
type Entry struct {
	Key        string
	Source     Source
	Rule       string // event rule; empty means RuleCapability
	DecisionID string // "" for capability and harness entries (null in events)
}

// Allowlist is a task's set of allowed destinations. Matching is exact
// string equality on the canonical key: no wildcards, no suffix matches,
// no CIDR entries exist in the PoC (A07 §5.1). It is safe for concurrent
// use; the agent loop adds obligation entries while connections run.
type Allowlist struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

// NewAllowlist returns an allowlist holding entries, or the first error
// Add would return for one of them.
func NewAllowlist(entries ...Entry) (*Allowlist, error) {
	a := &Allowlist{entries: make(map[string]Entry, len(entries))}
	for _, e := range entries {
		if err := a.Add(e); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// Add inserts or replaces the entry for e.Key. A key that is not already
// canonical is refused rather than normalised, so that a manifest typo is
// an error at load, not an entry that silently never matches; a key with
// "*" is refused because A07 §5.1 has no wildcard entries (INV-5 for
// confidential workspaces holds by construction).
func (a *Allowlist) Add(e Entry) error {
	if strings.Contains(e.Key, "*") {
		return fmt.Errorf("proxy: allowlist entry %q: wildcard entries are not supported (design A07 §5.1)", e.Key)
	}
	t, err := Normalize(e.Key)
	if err != nil {
		return fmt.Errorf("proxy: allowlist entry %q: %w", e.Key, err)
	}
	if t.Key() != e.Key {
		return fmt.Errorf("proxy: allowlist entry %q is not canonical (want %q)", e.Key, t.Key())
	}
	switch e.Source {
	case SourceCapability, SourceObligation, SourceGrant, SourceHarness:
	default:
		return fmt.Errorf("proxy: allowlist entry %q: unknown source %q", e.Key, e.Source)
	}
	if e.Rule == "" {
		e.Rule = RuleCapability
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil {
		a.entries = make(map[string]Entry)
	}
	a.entries[e.Key] = e
	return nil
}

// Get returns the entry for a canonical key.
func (a *Allowlist) Get(key string) (Entry, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	e, ok := a.entries[key]
	return e, ok
}

// Keys returns the keys in sorted order (context.task_egress_allow for the
// PDP, A07 §5.3).
func (a *Allowlist) Keys() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.entries))
	for k := range a.entries {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
