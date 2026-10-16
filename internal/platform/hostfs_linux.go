//go:build linux

package platform

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// The magic numbers of hostfs_decide.go must be the kernel's: a compile
// error here if one drifts from x/sys/unix (ZFS is not in x/sys).
// One array per constant, so two errors cannot cancel out.
var (
	_ = [1]struct{}{}[magicExt-unix.EXT4_SUPER_MAGIC]
	_ = [1]struct{}{}[magicXFS-unix.XFS_SUPER_MAGIC]
	_ = [1]struct{}{}[magicBtrfs-unix.BTRFS_SUPER_MAGIC]
	_ = [1]struct{}{}[magicTmpfs-unix.TMPFS_MAGIC]
	_ = [1]struct{}{}[magicOverlay-unix.OVERLAYFS_SUPER_MAGIC]
	_ = [1]struct{}{}[magicF2FS-unix.F2FS_SUPER_MAGIC]
	_ = [1]struct{}{}[magicBcachefs-unix.BCACHEFS_SUPER_MAGIC]
)

// CaseSensitiveDir: true only on an allowlisted local case-sensitive file
// system, for a directory without FS_CASEFOLD_FL (caseSensitiveFS).
// Anything else is ErrCaseUnknown.
func (hostFS) CaseSensitiveDir(dir string) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false, fmt.Errorf("case sensitivity of %s: %w", dir, err)
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, fmt.Errorf("case sensitivity of %s: %w", dir, err)
	}
	defer unix.Close(fd)
	flags, err := unix.IoctlGetUint32(fd, unix.FS_IOC_GETFLAGS)
	res := ioctlOK
	switch {
	case errors.Is(err, unix.ENOTTY), errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.EINVAL):
		res = ioctlUnsupported
	case err != nil:
		res = ioctlFailed
	}
	cs, err := caseSensitiveFS(int64(st.Type), flags, res)
	if err != nil {
		return false, fmt.Errorf("case sensitivity of %s: %w", dir, err)
	}
	return cs, nil
}

// ShortNameCreation: the Linux file systems CaseSensitiveDir accepts
// create no 8.3 aliases.
func (hostFS) ShortNameCreation(string) (bool, error) { return false, nil }

// HasShortAlias is always false on Linux. That is safe only together with
// CaseSensitiveDir: an NTFS volume mounted through ntfs3 or ntfs-3g (FUSE),
// where Linux might match DOS names, is not on the allowlist, so a
// worktree there is refused before aliases matter.
func (hostFS) HasShortAlias(string) (bool, error) { return false, nil }

// FinalPath resolves every symbolic link.
func (hostFS) FinalPath(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("final path of %s: %w", p, err)
	}
	return r, nil
}
