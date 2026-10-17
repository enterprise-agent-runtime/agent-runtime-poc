package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// HostFS answers the questions the L2 container spec (internal/sandbox/oci,
// design A06 §15, CONFLICTS C-03) has to ask about host directories before
// it binds them into a container. They are OS questions, so they live here
// (CLAUDE.md §3); the spec builder only sees the interface and its tests use
// a fake.
//
// Why each question exists: the worktree masks of A06 §11.3 are bind mounts
// keyed on one spelling of a path. On a host file system that resolves
// other spellings to the same entry, a sandboxed process reaches the masked
// file under a different name and the mask never applies (PROGRESS M2-12,
// CWE-178 and CWE-41):
//   - case: on NTFS (Windows, through Docker Desktop's file sharing) and on
//     APFS (macOS) ".ENV" opens the file masked as ".env";
//   - NTFS 8.3 short names: "ENV~1" opens it as well (D-055);
//   - a junction or symbolic link in a bind source makes Docker bind
//     something other than the path the checks examined.
type HostFS interface {
	// CaseSensitiveDir reports whether name lookups directly inside dir
	// distinguish case. On Windows it is the per-directory flag that
	// `fsutil file setCaseSensitiveInfo <dir> enable` sets; on macOS the
	// volume's case sensitivity; on Linux false only for a casefolded
	// directory or a file system that may fold case.
	CaseSensitiveDir(dir string) (bool, error)
	// ShortNameCreation reports whether the volume holding dir may create
	// NTFS 8.3 short names for new entries. Always false off Windows.
	ShortNameCreation(dir string) (bool, error)
	// HasShortAlias reports whether the entry at p has an 8.3 short name
	// that differs from its long name (created before creation was
	// disabled, for example). Always false off Windows.
	HasShortAlias(p string) (bool, error)
	// FinalPath returns the path the OS opens for p after resolving every
	// junction, symbolic link and short name, in the on-disk spelling. A
	// bind source is accepted only when FinalPath returns it unchanged.
	FinalPath(p string) (string, error)
	// CaseAliased reports whether another case of p's last element (the
	// case-swapped, all-upper and all-lower forms) opens the same entry in
	// the same directory. It asks the directory itself, so it catches what
	// the per-OS case answer cannot see: a zfs dataset with
	// casesensitivity=insensitive, an overlay over a case-insensitive
	// layer, XFS ascii-ci. The same on every OS.
	CaseAliased(p string) (bool, error)
}

// NewHostFS returns the HostFS of this OS.
func NewHostFS() HostFS { return hostFS{} }

// hostFS is the real implementation; its methods are per OS
// (hostfs_windows.go, hostfs_linux.go, hostfs_darwin.go, hostfs_other.go).
type hostFS struct{}

// CaseAliased uses os.Lstat and os.SameFile on every OS.
func (hostFS) CaseAliased(p string) (bool, error) { return caseAliased(p, os.Lstat, os.SameFile) }

// caseAliased is CaseAliased with the lookups injected. The entry at p must
// exist (Build asks only about entries it found on disk). A missing other
// case is not an alias; any other lookup error is returned, so the caller
// refuses (fail closed).
func caseAliased(p string, lstat func(string) (fs.FileInfo, error), same func(a, b fs.FileInfo) bool) (bool, error) {
	orig, err := lstat(p)
	if err != nil {
		return false, fmt.Errorf("case alias of %s: %w", p, err)
	}
	dir, base := filepath.Split(p)
	for _, v := range caseVariants(base) {
		fi, err := lstat(filepath.Join(dir, v))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return false, fmt.Errorf("case alias of %s as %s: %w", p, v, err)
		}
		if same(orig, fi) {
			return true, nil
		}
	}
	return false, nil
}

// caseVariants are the other spellings of name tried as aliases: every
// cased letter swapped, all upper, all lower; without name itself or
// repeats, so a name without a cased letter has none.
func caseVariants(name string) []string {
	swapped := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsUpper(r):
			return unicode.ToLower(r)
		case unicode.IsLower(r):
			return unicode.ToUpper(r)
		}
		return r
	}, name)
	var out []string
	for _, v := range []string{swapped, strings.ToUpper(name), strings.ToLower(name)} {
		if v != name && !containsString(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
