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
// DACL written by WriteOwnerOnly: one FILE_ALL_ACCESS grant to the current
// user and nothing else. The DACL is parsed, not compared as SDDL text,
// because Windows abbreviates well-known SIDs when rendering SDDL (the RID-500
// administrator reads back as "LA"; CLAUDE.md §4 IPC, DECISIONS D-011).
func OwnerOnly(path string) (bool, error) {
	sid, err := currentUserSID()
	if err != nil {
		return false, err
	}
	return ownerOnlyFor(path, sid)
}

// ownerOnlyFor is OwnerOnly for an explicit binary SID.
func ownerOnlyFor(path string, sid []byte) (bool, error) {
	sd, err := fileDACL(path)
	if err != nil {
		return false, err
	}
	return ownerOnlyDACL(sd, sid), nil
}

// currentUserSID returns the binary SID of the process token's user.
func currentUserSID() ([]byte, error) {
	tok, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	n := syscall.GetLengthSid(tu.User.Sid)
	// Copy: the SID points into the token information buffer.
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(tu.User.Sid)), n)...), nil
}

// fileDACL reads the file's DACL as a self-relative security descriptor.
func fileDACL(path string) ([]byte, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var need uint32
	_, _, _ = procGetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), daclSecurityInformation, 0, 0, uintptr(unsafe.Pointer(&need)))
	if need == 0 {
		return nil, syscall.EINVAL
	}
	buf := make([]byte, need)
	r, _, e := procGetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), daclSecurityInformation, uintptr(unsafe.Pointer(&buf[0])), uintptr(need), uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return nil, e
	}
	return buf, nil
}

// ownerOnlyDetail renders the file's DACL as SDDL for failure messages only;
// SDDL text is not canonical and is never compared.
func ownerOnlyDetail(path string) string {
	sd, err := fileDACL(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	s, err := winio.SecurityDescriptorToSddl(sd)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return s
}
