//go:build !windows

package platform

import "syscall"

// FreeBytes returns the space available to the user on path's volume.
func FreeBytes(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return uint64(s.Bavail) * uint64(s.Bsize), nil //nolint:unconvert // field types differ between Linux and macOS
}
