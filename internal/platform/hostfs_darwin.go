//go:build darwin

package platform

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// pcCaseSensitive is _PC_CASE_SENSITIVE from <sys/unistd.h>.
const pcCaseSensitive = 11

// CaseSensitiveDir asks pathconf(_PC_CASE_SENSITIVE): APFS and HFS+ are
// case-insensitive unless formatted case-sensitive, per volume. An error
// is returned as is; the spec builder refuses on it.
func (hostFS) CaseSensitiveDir(dir string) (bool, error) {
	v, err := unix.Pathconf(dir, pcCaseSensitive)
	if err != nil {
		return false, fmt.Errorf("case sensitivity of %s: %w", dir, err)
	}
	return v == 1, nil
}

// ShortNameCreation: macOS file systems create no 8.3 aliases.
func (hostFS) ShortNameCreation(string) (bool, error) { return false, nil }

// HasShortAlias: see ShortNameCreation.
func (hostFS) HasShortAlias(string) (bool, error) { return false, nil }

// FinalPath resolves every symbolic link.
func (hostFS) FinalPath(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("final path of %s: %w", p, err)
	}
	return r, nil
}
