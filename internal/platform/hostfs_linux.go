//go:build linux

package platform

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// fsCasefold is FS_CASEFOLD_FL, the ext4/f2fs/tmpfs per-directory
// case-folding attribute (`chattr +F`).
const fsCasefold = 0x40000000

// foldingFS are file systems whose lookups may ignore case, or whose case
// behaviour depends on a server or mount option this check cannot see:
// vfat, exfat, ntfs3, cifs/smb, and 9p (WSL2 drvfs over NTFS, where the
// M2-12 review reproduced the .ENV bypass). They count as case-insensitive.
var foldingFS = map[int64]string{
	0x4d44:     "vfat",
	0x2011bab0: "exfat",
	0x7366746e: "ntfs3",
	0xff534d42: "cifs",
	0xfe534d42: "smb2",
	0x01021997: "9p",
}

// CaseSensitiveDir is false on a file system that may fold case, or for a
// directory with FS_CASEFOLD_FL. A file system that does not implement
// FS_IOC_GETFLAGS has no casefold attribute.
func (hostFS) CaseSensitiveDir(dir string) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false, fmt.Errorf("case sensitivity of %s: %w", dir, err)
	}
	if _, ok := foldingFS[int64(st.Type)]; ok {
		return false, nil
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, fmt.Errorf("case sensitivity of %s: %w", dir, err)
	}
	defer unix.Close(fd)
	flags, err := unix.IoctlGetUint32(fd, unix.FS_IOC_GETFLAGS)
	switch {
	case errors.Is(err, unix.ENOTTY), errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.EINVAL):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("case sensitivity of %s: %w", dir, err)
	}
	return flags&fsCasefold == 0, nil
}

// ShortNameCreation: Linux file systems create no 8.3 aliases (vfat's
// short names are refused above as case folding).
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
