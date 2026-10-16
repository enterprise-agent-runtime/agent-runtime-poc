package sandbox

// Toolchain roots resolved through their symlinks (design A06 §11.1,
// §11.4; INV-B). bwrap follows a bind source's symlinks, so the checks of
// validateToolchains must also hold for the path the link reaches, not only
// for its spelling: /var/run is a link to /run, and a root spelled
// /var/run would bind the host /run with docker.sock in it.

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ResolveToolchains resolves each L1 toolchain root with eval
// (filepath.EvalSymlinks in production, injectable for tests) and runs every
// Spec check again with the resolved roots in place of the configured ones:
// reserved sandbox paths (/run, /sys, /proc, /dev, ...), the user's home and
// the Warden home, the deny-list and overlaps between roots. It returns the
// resolved roots in Spec order; a backend binds resolved root i at the
// configured root i, so inside the sandbox the toolchain stays where PATH
// and its masks expect it. The Spec is not modified.
//
// A root that cannot be resolved is ErrUnavailable (the sandbox is not
// created); a resolved root that fails a check is ErrInvalidSpec naming both
// paths. L2 mounts no toolchain and returns nothing.
func (s *Spec) ResolveToolchains(eval func(string) (string, error)) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Level != LevelL1 || len(s.Toolchains) == 0 {
		return nil, nil
	}
	resolved := make([]string, len(s.Toolchains))
	c := *s
	c.Toolchains = make([]Toolchain, len(s.Toolchains))
	for i, tc := range s.Toolchains {
		r, err := eval(tc.Root)
		if err != nil {
			return nil, fmt.Errorf("%w: toolchain root %q cannot be resolved: %w", ErrUnavailable, tc.Root, err)
		}
		r = filepath.ToSlash(r)
		resolved[i] = r
		c.Toolchains[i] = Toolchain{Root: r, BinDirs: rebase(tc.BinDirs, tc.Root, r), Masks: rebase(tc.Masks, tc.Root, r)}
	}
	if err := c.validateToolchains(); err != nil {
		return nil, fmt.Errorf("%w (toolchain roots %q resolve to %q)", err, roots(s.Toolchains), resolved)
	}
	return resolved, nil
}

// rebase moves paths under from to the same place under to. The paths were
// validated to lie under from.
func rebase(ps []string, from, to string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = path.Join(to, strings.TrimPrefix(p, from))
	}
	return out
}

func roots(tcs []Toolchain) []string {
	out := make([]string, len(tcs))
	for i, tc := range tcs {
		out[i] = tc.Root
	}
	return out
}
