package platform

// The pure decisions behind HostFS (M2-18 review round 1). They build on
// every OS, so their tables run on every CI runner; the per-OS files only
// gather the inputs.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrCaseUnknown: the case behaviour of a directory's file system cannot be
// established, so the L2 spec builder refuses it (worktree_case_unknown).
var ErrCaseUnknown = errors.New("case sensitivity unknown")

// ioctlResult is how FS_IOC_GETFLAGS answered.
type ioctlResult int

const (
	ioctlOK          ioctlResult = iota
	ioctlUnsupported             // ENOTTY, EOPNOTSUPP, EINVAL: no inode flags on this file system
	ioctlFailed                  // any other error
)

func (r ioctlResult) String() string {
	switch r {
	case ioctlOK:
		return "ok"
	case ioctlUnsupported:
		return "unsupported"
	}
	return "failed"
}

// Linux statfs f_type values (linux/magic.h; checked against x/sys/unix in
// hostfs_linux.go).
const (
	magicExt       = 0xef53 // ext2, ext3, ext4
	magicXFS       = 0x58465342
	magicBtrfs     = 0x9123683e
	magicTmpfs     = 0x01021994
	magicF2FS      = 0xf2f52010
	magicBcachefs  = 0xca451a4e
	fsCasefoldFlag = 0x40000000 // FS_CASEFOLD_FL (chattr +F)
)

// caseSensitiveLocalFS is an allowlist: local file systems whose lookups
// distinguish case, except in a directory carrying FS_CASEFOLD_FL (ext4,
// f2fs, tmpfs and bcachefs support casefolded directories). Every other
// file system is unknown: NFS, FUSE (ntfs-3g with ignore_case, sshfs to a
// Mac, ciopfs, and virtiofs, which is FUSE-based and inherits the host's
// case rules), 9p (WSL drvfs), vfat, exfat, ntfs3, SMB. Unknown is refused,
// because a denylist failed open on exactly these (CWE-178). NTFS mounts
// being refused is also why HasShortAlias may answer false on Linux.
// zfs and overlay are left out too (M2-18 review round 2): a zfs dataset
// with casesensitivity=insensitive, and an overlay whose lower layer folds
// case (measured over drvfs: .ENV and ENV~1 read the masked file), fold
// case without a casefold flag. XFS can be formatted ascii-ci, which this
// table cannot see either; for that the spec builder also runs the lookup
// probe CaseAliased on every mask path.
var caseSensitiveLocalFS = map[int64]string{
	magicExt: "ext2/3/4", magicXFS: "xfs", magicBtrfs: "btrfs", magicTmpfs: "tmpfs",
	magicF2FS: "f2fs", magicBcachefs: "bcachefs",
}

// caseSensitiveFS decides CaseSensitiveDir on Linux from the directory's
// file system magic and its FS_IOC_GETFLAGS answer.
func caseSensitiveFS(magic int64, flags uint32, ioctl ioctlResult) (bool, error) {
	name, ok := caseSensitiveLocalFS[magic]
	if !ok {
		return false, fmt.Errorf("%w: file system type %#x is not a local case-sensitive file system", ErrCaseUnknown, magic)
	}
	switch ioctl {
	case ioctlUnsupported:
		return true, nil // no inode flags, so no casefold flag either
	case ioctlFailed:
		return false, fmt.Errorf("%w: reading the inode flags on %s failed", ErrCaseUnknown, name)
	}
	return flags&fsCasefoldFlag == 0, nil
}

// shortNameCreationDisabled is PERSISTENT_VOLUME_STATE_SHORT_NAME_CREATION_DISABLED.
const shortNameCreationDisabled = 0x00000001

// shortNamesOn decides ShortNameCreation on Windows. fsName is the volume's
// file system name; regVal/regPresent the machine-wide
// NtfsDisable8dot3NameCreation; volFlags the persistent volume state;
// isSystemVolume whether the volume holds the Windows directory.
//
//	0: every volume creates short names
//	1: no volume does
//	2: the volume's own flag decides
//	3: the system volume does, no other volume does
//
// A missing or any other value counts as creating them (fail closed).
// ReFS never creates short names; FAT, exFAT, network and unknown file
// systems count as creating them.
func shortNamesOn(fsName string, regVal uint64, regPresent bool, volFlags uint32, isSystemVolume bool) bool {
	switch fsName {
	case "NTFS":
	case "ReFS":
		return false
	default:
		return true
	}
	if !regPresent {
		return true
	}
	switch regVal {
	case 1:
		return false
	case 2:
		return volFlags&shortNameCreationDisabled == 0
	case 3:
		return isSystemVolume
	}
	return true
}

// HomeHash is the first 8 hex digits of sha256 of the normalised home, for
// every home including the default one (the D-042 hash; HomeSuffix uses
// it for non-default homes). The L2 backend labels containers and
// networks with it, so two daemons sharing one engine never touch each
// other's (warden.home).
func HomeHash(home string) string {
	h := sha256.Sum256([]byte(homeKey(home)))
	return hex.EncodeToString(h[:])[:8]
}
