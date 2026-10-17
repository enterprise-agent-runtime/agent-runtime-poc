//go:build linux || darwin

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The Linux and macOS side of HostFS (CLAUDE.md §8.6). The L2 spec builder
// refuses a worktree these call case-insensitive, so the answer is checked
// against what a lookup of another case actually does.
func TestHostFS_CaseSensitiveMatchesLookup(t *testing.T) {
	h := NewHostFS()
	dir, err := h.FinalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = os.Stat(filepath.Join(dir, ".ENV"))
	folds := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	cs, err := h.CaseSensitiveDir(dir)
	if errors.Is(err, ErrCaseUnknown) {
		t.Skipf("temp dir file system is not on the allowlist (refused, fail closed): %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	// Refusing more than needed is allowed (an unknown file system counts
	// as folding); calling a folding directory case-sensitive is not.
	if folds && cs {
		t.Fatalf("CaseSensitiveDir(%s) = true, but .ENV opens .env", dir)
	}
	if !folds && !cs {
		t.Logf("CaseSensitiveDir(%s) = false on a case-sensitive file system (fail closed)", dir)
	}
}

func TestHostFS_NoShortNames(t *testing.T) {
	h := NewHostFS()
	dir := t.TempDir()
	if on, err := h.ShortNameCreation(dir); err != nil || on {
		t.Errorf("ShortNameCreation = %v, %v; want false", on, err)
	}
	if a, err := h.HasShortAlias(dir); err != nil || a {
		t.Errorf("HasShortAlias = %v, %v; want false", a, err)
	}
}

func TestHostFS_FinalPathResolvesLinks(t *testing.T) {
	h := NewHostFS()
	dir, err := h.FinalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target dir")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got, err := h.FinalPath(target); err != nil || got != target {
		t.Errorf("FinalPath(target) = %q, %v; want it unchanged", got, err)
	}
	if got, err := h.FinalPath(link); err != nil || got != target {
		t.Errorf("FinalPath(link) = %q, %v; want %q", got, err, target)
	}
	if _, err := h.FinalPath(filepath.Join(dir, "missing")); err == nil {
		t.Errorf("FinalPath of a missing path succeeded")
	}
}

// CaseAliased on Linux and macOS (M2-18 review round 2). A local temp dir
// (ext4/tmpfs, or a case-sensitive APFS volume) has no alias. When
// WARDEN_TEST_FOLDING_DIR names a directory on a case-folding file system
// (WSL drvfs under /mnt/c, an overlay over it, a default APFS volume), the
// probe must find the alias there.
func TestHostFS_CaseAliased(t *testing.T) {
	h := NewHostFS()
	// check asks the probe about dir/.env and compares it with a direct
	// lookup of .ENV, the answer the probe must agree with. mustFold, when
	// set, skips a directory that turns out not to fold.
	//
	// The temp-dir row asserts agreement whatever the file system does: on
	// Linux /tmp is case-sensitive, but on macOS it is a default APFS volume,
	// which folds case, so the probe must report the alias there (macOS CI,
	// 2026-10-17: the row first assumed every Unix temp dir is sensitive, and
	// it skipped through the parent test from inside a subtest).
	check := func(t *testing.T, dir string, mustFold bool) {
		t.Helper()
		env := filepath.Join(dir, ".env")
		if err := os.WriteFile(env, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(filepath.Join(dir, ".ENV"))
		folds := err == nil
		if mustFold && !folds {
			t.Skipf("%s: .ENV does not open .env; not a folding file system", dir)
		}
		if got, err := h.CaseAliased(env); err != nil || got != folds {
			t.Fatalf("CaseAliased(%s) = %v, %v; a direct lookup of .ENV says %v", env, got, err, folds)
		}
	}
	t.Run("temp dir", func(t *testing.T) { check(t, t.TempDir(), false) })
	t.Run("folding dir", func(t *testing.T) {
		base := os.Getenv("WARDEN_TEST_FOLDING_DIR")
		if base == "" {
			t.Skip("WARDEN_TEST_FOLDING_DIR not set")
		}
		dir, err := os.MkdirTemp(base, "casealias-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		check(t, dir, true)
	})
}
