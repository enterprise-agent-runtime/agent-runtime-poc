package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"warden.dev/warden/internal/execproto/denylist"
)

// threeLayersCorpus is internal/execproto/denylist/testdata/three-layers.json.
type threeLayersCorpus struct {
	Version         int      `json:"version"`
	About           []string `json:"about"`
	WorktreeRefused []string `json:"worktree_refused"`
	WorktreeAllowed []string `json:"worktree_allowed"`
	HostRefused     []string `json:"host_refused"`
}

func loadThreeLayers(t *testing.T) threeLayersCorpus {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "execproto", "denylist", "testdata", "three-layers.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var c threeLayersCorpus
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("three-layers.json: %v", err)
	}
	if c.Version != 1 || len(c.WorktreeRefused) == 0 || len(c.WorktreeAllowed) == 0 || len(c.HostRefused) == 0 {
		t.Fatalf("three-layers.json: version %d, %d/%d/%d entries", c.Version,
			len(c.WorktreeRefused), len(c.WorktreeAllowed), len(c.HostRefused))
	}
	return c
}

// TestDenyListThreeLayers is the INV-I test of CLAUDE.md §5: the platform
// deny-list is enforced at the policy, executor and mount layers, all from
// the one matcher in internal/execproto/denylist (D-014) and all against
// the one shared corpus file, so the layers cannot drift apart.
//
// INV-I is staged. This test covers what M2 can reach from here:
//   - the matcher itself refuses every refused path and no allowed one;
//   - the mount layer (this package, A06 §11.3): every refused worktree
//     path, materialised as a tree, resolves through the sandbox mount
//     table to a mask, every allowed one to the worktree itself, and no
//     refused host path can become a mount source.
//
// The executor layer runs the same corpus in internal/exec (M2-6): design
// A02 R4 forbids this package importing internal/exec, so the layers meet
// in the testdata file, not in one test binary. The policy layer joins in
// M3 (internal/policy, INV-1). Until then docs/PROGRESS.md carries INV-I
// as [~].
func TestDenyListThreeLayers(t *testing.T) {
	c := loadThreeLayers(t)

	t.Run("matcher", func(t *testing.T) {
		for _, p := range append(append([]string{}, c.WorktreeRefused...), c.HostRefused...) {
			if _, ok := denylist.Match(p); !ok {
				t.Errorf("denylist.Match(%q) = false, want refused", p)
			}
		}
		for _, p := range c.WorktreeAllowed {
			if pat, ok := denylist.Match(p); ok {
				t.Errorf("denylist.Match(%q) = %s, want allowed", p, pat)
			}
		}
	})

	t.Run("mount", func(t *testing.T) {
		tree := fstest.MapFS{}
		for _, p := range append(append([]string{}, c.WorktreeRefused...), c.WorktreeAllowed...) {
			tree[p] = &fstest.MapFile{Data: []byte("synthetic")}
		}
		masks, err := WorktreeMasks(context.Background(), tree)
		if err != nil {
			t.Fatal(err)
		}
		l := newLayout(t)
		for _, level := range []Level{LevelL1, LevelL2} {
			s := validSpec(l, level)
			s.WorktreeMasks = masks
			mounts, err := s.Mounts()
			if err != nil {
				t.Fatalf("%s: Mounts() error: %v", level, err)
			}
			for _, p := range c.WorktreeRefused {
				if m := resolveMount(mounts, path.Join(TargetWorktree, p)); m.Kind != KindMask {
					t.Errorf("%s: /work/%s resolves to a %q mount, want a mask", level, p, m.Kind)
				}
			}
			for _, p := range c.WorktreeAllowed {
				if m := resolveMount(mounts, path.Join(TargetWorktree, p)); m.Kind != KindWorktree {
					t.Errorf("%s: /work/%s resolves to a %q mount, want the worktree", level, p, m.Kind)
				}
			}
			for _, p := range c.HostRefused {
				for _, m := range mounts {
					if m.Source != "" && slashOverlaps(filepath.ToSlash(m.Source), p) {
						t.Errorf("%s: mount %s <- %s overlaps the refused host path %s", level, m.Target, m.Source, p)
					}
				}
			}
		}
		// A refused host path offered as a toolchain is refused at the
		// mount layer, not mounted.
		for _, p := range c.HostRefused {
			s := validSpec(l, LevelL1)
			s.Toolchains = []Toolchain{{Root: p}}
			if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "deny-listed") {
				t.Errorf("toolchain root %s: Validate() = %v, want deny-listed", p, err)
			}
		}
	})
}

// resolveMount returns the mount that serves target in the sandbox: the one
// with the longest target that equals or contains it, as the mount
// namespace (later, deeper mounts shadow earlier ones) resolves it.
func resolveMount(mounts []Mount, target string) Mount {
	var best Mount
	for _, m := range mounts {
		if slashWithin(target, m.Target) && len(m.Target) >= len(best.Target) {
			best = m
		}
	}
	return best
}
