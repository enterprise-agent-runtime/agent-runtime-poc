//go:build windows

package platform

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// openForQuery opens p (file or directory) for attribute queries only,
// following reparse points: the questions are about what a bind of p
// reaches.
func openForQuery(p string) (windows.Handle, error) {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(u, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

// CaseSensitiveDir reads FILE_CASE_SENSITIVE_INFO through
// GetFileInformationByHandleEx(FileCaseSensitiveInfo), the read side of
// `fsutil file setCaseSensitiveInfo`. A file system that does not know the
// class (FAT, exFAT, older ReFS) answers with an error that means "no
// per-directory case sensitivity", which is reported as case-insensitive.
func (hostFS) CaseSensitiveDir(dir string) (bool, error) {
	h, err := openForQuery(dir)
	if err != nil {
		return false, fmt.Errorf("case sensitivity of %s: %w", dir, err)
	}
	defer windows.CloseHandle(h)
	var flags uint32
	err = windows.GetFileInformationByHandleEx(h, windows.FileCaseSensitiveInfo, (*byte)(unsafe.Pointer(&flags)), uint32(unsafe.Sizeof(flags)))
	switch {
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER), errors.Is(err, windows.ERROR_NOT_SUPPORTED), errors.Is(err, windows.ERROR_INVALID_FUNCTION):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("case sensitivity of %s: %w", dir, err)
	}
	return flags&windows.FILE_CS_FLAG_CASE_SENSITIVE_DIR != 0, nil
}

// fsctlQueryPersistentVolumeState is FSCTL_QUERY_PERSISTENT_VOLUME_STATE.
// Issued on a handle to any directory of the volume it needs no
// elevation, unlike `fsutil 8dot3name query`, which opens the volume
// itself (measured for M2-18: access denied without elevation).
const (
	fsctlQueryPersistentVolumeState = 0x0009023C
	shortNameCreationDisabled       = 0x00000001 // PERSISTENT_VOLUME_STATE_SHORT_NAME_CREATION_DISABLED
)

// GetFinalPathNameByHandle flags (fileapi.h), absent from x/sys/windows.
const (
	fileNameNormalized = 0x0
	volumeNameDOS      = 0x0
)

type fileFSPersistentVolumeInformation struct {
	VolumeFlags, FlagMask, Version, Reserved uint32
}

// ShortNameCreation combines the two settings `fsutil 8dot3name query`
// reports. The machine-wide registry value NtfsDisable8dot3NameCreation:
// 0 creates short names on every volume, 1 on none, 2 decides per volume
// (the persistent volume flag), 3 on the system volume only. Value 3 is
// answered with the per-volume flag only when that flag disables creation;
// otherwise it counts as enabled, because telling the system volume apart
// is not needed to fail closed. A volume that is not NTFS: ReFS never
// creates short names; anything else (FAT) counts as creating them.
func (hostFS) ShortNameCreation(dir string) (bool, error) {
	h, err := openForQuery(dir)
	if err != nil {
		return true, fmt.Errorf("8.3 name creation for %s: %w", dir, err)
	}
	defer windows.CloseHandle(h)
	fsName := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumeInformationByHandle(h, nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))); err != nil {
		return true, fmt.Errorf("8.3 name creation for %s: volume information: %w", dir, err)
	}
	switch windows.UTF16ToString(fsName) {
	case "NTFS":
	case "ReFS":
		return false, nil
	default:
		return true, nil
	}
	setting := uint64(2) // the Windows default when the value is absent
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\FileSystem`, registry.QUERY_VALUE); err == nil {
		v, _, err := k.GetIntegerValue("NtfsDisable8dot3NameCreation")
		k.Close()
		switch {
		case err == nil:
			setting = v
		case !errors.Is(err, registry.ErrNotExist):
			return true, fmt.Errorf("8.3 name creation: NtfsDisable8dot3NameCreation: %w", err)
		}
	}
	switch setting {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	in := fileFSPersistentVolumeInformation{FlagMask: shortNameCreationDisabled, Version: 1}
	var out fileFSPersistentVolumeInformation
	var n uint32
	if err := windows.DeviceIoControl(h, fsctlQueryPersistentVolumeState, (*byte)(unsafe.Pointer(&in)), uint32(unsafe.Sizeof(in)),
		(*byte)(unsafe.Pointer(&out)), uint32(unsafe.Sizeof(out)), &n, nil); err != nil {
		return true, fmt.Errorf("8.3 name creation for %s: persistent volume state: %w", dir, err)
	}
	return out.VolumeFlags&shortNameCreationDisabled == 0, nil
}

// HasShortAlias asks GetShortPathName for p and compares the last element:
// a name that already is a valid 8.3 name has no separate short name and
// comes back unchanged.
func (hostFS) HasShortAlias(p string) (bool, error) {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return true, err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(u, &buf[0], uint32(len(buf)))
	if err != nil {
		return true, fmt.Errorf("short name of %s: %w", p, err)
	}
	if int(n) >= len(buf) {
		return true, fmt.Errorf("short name of %s: longer than %d characters", p, len(buf))
	}
	return lastElem(windows.UTF16ToString(buf[:n])) != lastElem(p), nil
}

func lastElem(p string) string { return p[strings.LastIndexByte(p, '\\')+1:] }

// FinalPath is GetFinalPathNameByHandle(FILE_NAME_NORMALIZED,
// VOLUME_NAME_DOS) of the opened path, so junctions, symbolic links, short
// names and case are all resolved to what is on disk. The \\?\ prefix of a
// drive path is removed; a UNC result (\\?\UNC\server\share) is returned as
// \\server\share, which the L2 path mapping refuses.
func (hostFS) FinalPath(p string) (string, error) {
	h, err := openForQuery(p)
	if err != nil {
		return "", fmt.Errorf("final path of %s: %w", p, err)
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), fileNameNormalized|volumeNameDOS)
	if err != nil {
		return "", fmt.Errorf("final path of %s: %w", p, err)
	}
	if int(n) >= len(buf) {
		return "", fmt.Errorf("final path of %s: longer than %d characters", p, len(buf))
	}
	s := windows.UTF16ToString(buf[:n])
	switch {
	case strings.HasPrefix(s, `\\?\UNC\`):
		return `\\` + s[len(`\\?\UNC\`):], nil
	case strings.HasPrefix(s, `\\?\`):
		return s[len(`\\?\`):], nil
	}
	return s, nil
}
