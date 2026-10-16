package sandbox

// The mount model of design A06 §2 and §11.3 (L1) / §15 (L2), restricted
// to the list of CONFLICTS C-24 (INV-B) and masked with the platform
// deny-list (INV-I, mount layer).

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"time"

	"warden.dev/warden/internal/execproto/denylist"
)

// Bounds of the worktree mask walk (A06 §11.3).
const (
	MaxMasks        = 256
	MaxWalkEntries  = 200_000
	MaskWalkTimeout = 2 * time.Second
)

// skipDirs are generated dependency trees the mask walk does not enter
// (A06 §11.3). The executor and the PDP still deny agent access to
// deny-listed paths inside them.
var skipDirs = map[string]bool{"node_modules": true, ".venv": true, "venv": true, "__pycache__": true}

// MountKind is sandbox.create mounts[].kind (A06 §13.2).
type MountKind string

// The kinds a task sandbox's Spec produces. A06 also names system, login
// and hook mounts: system binds belong to the L1 backend (A06 §11.1), login
// and hook to harness sandboxes (§14, M5).
const (
	KindWorktree   MountKind = "worktree"
	KindScratch    MountKind = "scratch"
	KindCache      MountKind = "cache"
	KindGit        MountKind = "git"
	KindGitObjects MountKind = "git_objects"
	KindToolchain  MountKind = "toolchain"
	KindExecutor   MountKind = "executor"
	KindGenerated  MountKind = "generated"
	KindMask       MountKind = "mask"
	KindProxy      MountKind = "proxy"
)

// MountMode is a mount's access mode (A06 §2 Mode column).
type MountMode string

// Mount modes. A socket is mounted for connecting only.
const (
	ModeRW      MountMode = "rw"
	ModeRO      MountMode = "ro"
	ModeConnect MountMode = "connect"
)

// Mount is one entry of the sandbox mount table, in mount order: a later
// mount shadows an earlier one at or under its target, which is how masks
// hide deny-listed worktree entries.
type Mount struct {
	Kind   MountKind
	Source string // host path; empty for a mask
	Target string // sandbox path
	Mode   MountMode
	Dir    bool // masks only: an empty read-only tmpfs rather than /dev/null
}

// Mounts returns the sandbox mount table for the Spec: exactly the C-24
// list (INV-B) and nothing else. Backends render it (bwrap argv, Docker
// mounts); they add only what carries no host data (tmpfs /tmp, the empty
// hooks dir, /proc, /dev, generated /etc) and, on L1, the system binds of
// A06 §11.1.
func (s *Spec) Mounts() ([]Mount, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	ms := []Mount{{Kind: KindWorktree, Source: s.Worktree, Target: TargetWorktree, Mode: ModeRW}}
	for _, m := range s.WorktreeMasks {
		ms = append(ms, Mount{Kind: KindMask, Target: path.Join(TargetWorktree, m.RelPath), Mode: ModeRO, Dir: m.Dir})
	}
	ms = append(ms,
		Mount{Kind: KindScratch, Source: s.Scratch, Target: TargetScratch, Mode: ModeRW},
		Mount{Kind: KindCache, Source: s.Cache, Target: TargetCache, Mode: ModeRW},
		Mount{Kind: KindGit, Source: s.GitDir, Target: TargetGit, Mode: ModeRW},
	)
	if s.MainObjects != "" {
		ms = append(ms, Mount{Kind: KindGitObjects, Source: s.MainObjects, Target: TargetGitObjects, Mode: ModeRO})
	}
	if s.Level == LevelL1 {
		for _, tc := range s.Toolchains {
			ms = append(ms, Mount{Kind: KindToolchain, Source: tc.Root, Target: tc.Root, Mode: ModeRO})
			for _, m := range tc.Masks {
				ms = append(ms, Mount{Kind: KindMask, Target: m, Mode: ModeRO})
			}
		}
	}
	ms = append(ms,
		Mount{Kind: KindExecutor, Source: s.ExecBin, Target: TargetExecBin, Mode: ModeRO},
		Mount{Kind: KindGenerated, Source: s.ProfilesFile, Target: TargetProfiles, Mode: ModeRO},
	)
	if s.Level == LevelL1 {
		ms = append(ms, Mount{Kind: KindProxy, Source: s.Proxy.Socket, Target: TargetProxySocket, Mode: ModeConnect})
	}
	return ms, nil
}

var errStopWalk = errors.New("stop")

// WorktreeMasks walks the worktree (A06 §11.3) and returns a mask for
// every entry the platform deny-list names, matched root-relative (CF-17).
// The walk never follows symlinks (fs.WalkDir does not) and does not mask
// them: a bind onto a symlink would mask its target, and the target is
// checked by its own entry. A masked directory is not entered. The generated
// dependency trees are skipped.
//
// It fails closed with ErrUnavailable when the walk cannot complete (an
// unreadable directory, more than MaxWalkEntries entries, MaskWalkTimeout
// or ctx expiring) or finds more than MaxMasks matches: an incomplete mask
// list could leave a deny-listed file visible, so the sandbox is not
// created.
func WorktreeMasks(ctx context.Context, worktree fs.FS) ([]Mask, error) {
	ctx, cancel := context.WithTimeout(ctx, MaskWalkTimeout)
	defer cancel()
	var masks []Mask
	var n int
	var stop error
	err := fs.WalkDir(worktree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if n++; n > MaxWalkEntries {
			stop = fmt.Errorf("%w: worktree has more than %d entries (A06 §11.3)", ErrUnavailable, MaxWalkEntries)
			return errStopWalk
		}
		if err := ctx.Err(); err != nil {
			stop = fmt.Errorf("%w: worktree mask walk: %w", ErrUnavailable, err)
			return errStopWalk
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return fs.SkipDir
		}
		pat, ok := denylist.Match(p)
		if !ok {
			return nil
		}
		if masks = append(masks, Mask{RelPath: p, Dir: d.IsDir(), Pattern: pat}); len(masks) > MaxMasks {
			stop = fmt.Errorf("%w: too_many_deny_list_matches (more than %d, A06 §11.3)", ErrUnavailable, MaxMasks)
			return errStopWalk
		}
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	if stop != nil {
		return nil, stop
	}
	if err != nil {
		return nil, fmt.Errorf("%w: worktree mask walk: %w", ErrUnavailable, err)
	}
	return masks, nil
}
