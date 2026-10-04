//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/Microsoft/go-winio"
)

var (
	advapi32             = syscall.NewLazyDLL("advapi32.dll")
	procSetFileSecurityW = advapi32.NewProc("SetFileSecurityW")
	procGetFileSecurityW = advapi32.NewProc("GetFileSecurityW")
)

const (
	daclSecurityInformation          = 0x00000004
	protectedDACLSecurityInformation = 0x80000000
)

// fileSDDL is the owner-only DACL for files; Windows stores generic all (GA)
// as file all access (FA), so files use FA to read back identically.
func fileSDDL() (string, error) {
	s, err := ownerSDDL()
	return strings.Replace(s, "(A;;GA;;;", "(A;;FA;;;", 1), err
}

// setOwnerOnlyACL replaces the file's DACL with one granting only the
// current user (no inheritance). os.Chmod cannot express this on Windows.
func setOwnerOnlyACL(path string) error {
	sddl, err := fileSDDL()
	if err != nil {
		return err
	}
	sd, err := winio.SddlToSecurityDescriptor(sddl)
	if err != nil {
		return err
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	r, _, e := procSetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), daclSecurityInformation|protectedDACLSecurityInformation, uintptr(unsafe.Pointer(&sd[0])))
	if r == 0 {
		return e
	}
	return nil
}

// WriteOwnerOnly writes a file with an owner-only DACL, atomically.
func WriteOwnerOnly(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := setOwnerOnlyACL(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Remove(path) // Rename over an existing file fails on some Windows volumes
	return os.Rename(tmp, path)
}

// OwnerOnly reports whether the file's DACL is the protected owner-only
// DACL written by WriteOwnerOnly.
func OwnerOnly(path string) (bool, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	var need uint32
	_, _, _ = procGetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), daclSecurityInformation, 0, 0, uintptr(unsafe.Pointer(&need)))
	if need == 0 {
		return false, syscall.EINVAL
	}
	buf := make([]byte, need)
	r, _, e := procGetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), daclSecurityInformation, uintptr(unsafe.Pointer(&buf[0])), uintptr(need), uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return false, e
	}
	sddl, err := winio.SecurityDescriptorToSddl(buf)
	if err != nil {
		return false, err
	}
	want, err := fileSDDL()
	if err != nil {
		return false, err
	}
	return sddl == want, nil
}
