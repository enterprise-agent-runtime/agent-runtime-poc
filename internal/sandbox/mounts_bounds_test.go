package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// TestWorktreeMasks_Bounds checks the A06 §11.3 walk bounds with shrunk
// limits, so the test depends neither on building 200,000 entries nor on
// the 2 s clock. Both bounds fail closed with sandbox_unavailable: an
// unfinished walk could leave a deny-listed file unmasked. Reaching a
// bound exactly is still fine.
func TestWorktreeMasks_Bounds(t *testing.T) {
	lim := walkLimits{entries: 10, masks: 3, timeout: time.Hour}
	tree := func(n int, name string) fstest.MapFS {
		fsys := fstest.MapFS{}
		for i := 0; i < n; i++ {
			fsys[fmt.Sprintf(name, i)] = &fstest.MapFile{Data: []byte("x")}
		}
		return fsys
	}
	// 9 files + 1 directory = 10 entries: at the bound.
	if _, err := worktreeMasks(context.Background(), tree(9, "d/f%d.txt"), lim); err != nil {
		t.Errorf("10 entries: err = %v, want nil", err)
	}
	_, err := worktreeMasks(context.Background(), tree(10, "d/f%d.txt"), lim)
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "more than 10 entries") {
		t.Errorf("11 entries: err = %v, want sandbox_unavailable mentioning more than 10 entries", err)
	}
	if m, err := worktreeMasks(context.Background(), tree(3, "k%d.pem"), lim); err != nil || len(m) != 3 {
		t.Errorf("3 matches: %d masks, err = %v, want 3, nil", len(m), err)
	}
	_, err = worktreeMasks(context.Background(), tree(4, "k%d.pem"), lim)
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "too_many_deny_list_matches (more than 3") {
		t.Errorf("4 matches: err = %v, want sandbox_unavailable too_many_deny_list_matches (more than 3", err)
	}
	if d := defaultWalkLimits; d.entries != MaxWalkEntries || d.masks != MaxMasks || d.timeout != MaskWalkTimeout {
		t.Errorf("defaultWalkLimits = %+v, want the A06 §11.3 bounds", d)
	}
}
