//go:build windows

package platform

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
)

// errorSharingViolation is ERROR_SHARING_VIOLATION: another handle has the
// file open and this open asked for no sharing.
const errorSharingViolation = syscall.Errno(32)

// LockDaemon takes the per-home daemon lock: run/wardend.lock opened with
// no sharing at all, held until the returned unlock is called or the
// process ends (the handle closes on exit, so a crash leaves no stale
// lock). On Windows the pipe is already created exclusively (Listen), so
// this lock adds the Unix guarantee on top: wardend holds it from before
// Listen until after the shutdown cleanup, so its token is never removed
// over another daemon's (WH-4). A lock another process holds is
// ErrDaemonRunning (wardend exits 3).
//
// The file is created with the owner-only descriptor passed to CreateFile
// itself; an existing one is opened without following a reparse point and
// must be a regular, owner-only file on that handle (backup semantics lets
// a directory or junction there open, so it is refused by name rather than
// with "access denied").
func LockDaemon(home string) (unlock func(), err error) {
	p := Layout{Home: home}.Lock()
	sd, err := ownerOnlySecurityDescriptor("")
	if err != nil {
		return nil, err
	}
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, securityAttributes(sd), syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL|fileFlagOpenReparsePoint|fileFlagBackupSemantics, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, ErrDaemonRunning
		}
		return nil, fmt.Errorf("open the daemon lock %s: %w", p, err)
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		syscall.CloseHandle(h)
		return nil, fmt.Errorf("the daemon lock %s: %w", p, err)
	}
	if info.FileAttributes&(syscall.FILE_ATTRIBUTE_REPARSE_POINT|syscall.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		syscall.CloseHandle(h)
		return nil, fmt.Errorf("the daemon lock %s is a symbolic link, junction or directory; remove it: %w", p, errNotOwnerOnly)
	}
	cur, err := handleSecurity(h)
	if err != nil {
		syscall.CloseHandle(h)
		return nil, fmt.Errorf("the daemon lock %s: %w", p, err)
	}
	me, err := currentUserSID()
	if err != nil {
		syscall.CloseHandle(h)
		return nil, err
	}
	if !ownerOnlyDACL(cur, me) {
		syscall.CloseHandle(h)
		return nil, notOwnerOnly(p, sddlDetail(cur))
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.CloseHandle(h) }) }, nil
}
