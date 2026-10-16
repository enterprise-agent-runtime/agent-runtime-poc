//go:build windows

package platform

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
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
	ownerSecurityInformation         = 0x00000001
	daclSecurityInformation          = 0x00000004
	protectedDACLSecurityInformation = 0x80000000

	// ownerOnlyInformation is what WriteOwnerOnly and Ensure set and what
	// OwnerOnly reads: the owner and the DACL (WH-1+2).
	ownerOnlyInformation = ownerSecurityInformation | daclSecurityInformation
)

// ownerOnlySecurityDescriptor is the self-relative owner-only descriptor:
// owner the process token user, written explicitly so an elevated token's
// default owner (BUILTIN\Administrators) is not used, and a protected DACL
// with one FILE_ALL_ACCESS ACE for that user. aceFlags is "" for files and
// "OICI" for directories, whose children inherit the ACE. FA rather than GA,
// because Windows stores generic all as file all access on files.
func ownerOnlySecurityDescriptor(aceFlags string) ([]byte, error) {
	u, err := user.Current()
	if err != nil {
		return nil, err
	}
	return winio.SddlToSecurityDescriptor("O:" + u.Uid + "D:P(A;" + aceFlags + ";FA;;;" + u.Uid + ")")
}

// securityAttributes wraps sd for CreateFile and CreateDirectory; the caller
// keeps sd alive (runtime.KeepAlive) until the call returns.
func securityAttributes(sd []byte) *syscall.SecurityAttributes {
	return &syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), SecurityDescriptor: uintptr(unsafe.Pointer(&sd[0]))}
}

// setSecurity replaces the owner and the DACL of path with those of sd and
// protects the DACL from inheritance.
func setSecurity(path string, sd []byte) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	r, _, e := procSetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), ownerOnlyInformation|protectedDACLSecurityInformation, uintptr(unsafe.Pointer(&sd[0])))
	runtime.KeepAlive(sd)
	if r == 0 {
		return &os.PathError{Op: "SetFileSecurity", Path: path, Err: e}
	}
	return nil
}

// setOwnerOnlyACL gives an existing file the owner-only descriptor.
func setOwnerOnlyACL(path string) error {
	sd, err := ownerOnlySecurityDescriptor("")
	if err != nil {
		return err
	}
	return setSecurity(path, sd)
}

// WriteOwnerOnly writes a file with an owner-only security descriptor,
// atomically (A05 §3.1; CLAUDE.md §4 IPC; D-011).
func WriteOwnerOnly(path string, data []byte) error {
	return writeOwnerOnly(path, data, writeHooks{})
}

// writeOwnerOnly creates a temp file with a random name, CREATE_NEW, and the
// owner-only descriptor applied by CreateFile itself, so the file never
// exists with an inherited DACL (a handle opened in that window would keep
// its access) and a name another account pre-created is refused rather than
// reused (WH-1+2, security review of FX-3, CWE-367/377). It then writes,
// flushes and renames the temp file over path; the rename keeps the
// descriptor.
func writeOwnerOnly(path string, data []byte, h writeHooks) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	sd, err := ownerOnlySecurityDescriptor("")
	if err != nil {
		return err
	}
	tmp, err := h.temp(path)
	if err != nil {
		return err
	}
	p, err := syscall.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	fh, err := syscall.CreateFile(p, syscall.GENERIC_WRITE, 0, securityAttributes(sd), syscall.CREATE_NEW, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		return &os.PathError{Op: "create", Path: tmp, Err: err}
	}
	if err := writeSyncClose(os.NewFile(uintptr(fh), tmp), data, h); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Remove(path) // Rename over an existing file fails on some Windows volumes
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ensureOwnerOnlyDir creates dir with the owner-only descriptor, its ACE
// inherited by children (OI|CI), or gives an existing dir that descriptor.
// MkdirAll's mode means nothing on Windows, so without this the home and run
// directories kept the DACL inherited from %LOCALAPPDATA% (WH-1+2,
// CWE-276). SetFileSecurity does not propagate to children that already
// exist; the token file carries its own descriptor regardless.
func ensureOwnerOnlyDir(dir string) error {
	sd, err := ownerOnlySecurityDescriptor("OICI")
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		p, err := syscall.UTF16PtrFromString(dir)
		if err != nil {
			return err
		}
		err = syscall.CreateDirectory(p, securityAttributes(sd))
		runtime.KeepAlive(sd)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
			return &os.PathError{Op: "mkdir", Path: dir, Err: err}
		}
		// Created concurrently: give it the descriptor below.
	} else if err != nil {
		return err
	}
	return setSecurity(dir, sd)
}

// OwnerOnly reports whether the file is owned by the current user and its
// DACL is the protected owner-only DACL written by WriteOwnerOnly: one
// FILE_ALL_ACCESS grant to the current user and nothing else. The
// descriptor is parsed, not compared as SDDL text, because Windows
// abbreviates well-known SIDs when rendering SDDL (the RID-500
// administrator reads back as "LA"; CLAUDE.md §4 IPC, D-011, D-027).
func OwnerOnly(path string) (bool, error) {
	sid, err := currentUserSID()
	if err != nil {
		return false, err
	}
	return ownerOnlyFor(path, sid)
}

// ownerOnlyFor is OwnerOnly for an explicit binary SID.
func ownerOnlyFor(path string, sid []byte) (bool, error) {
	sd, err := fileSecurity(path)
	if err != nil {
		return false, err
	}
	return ownerOnlyDACL(sd, sid), nil
}

// dirOwnerOnly is OwnerOnly for a directory written by ensureOwnerOnlyDir.
func dirOwnerOnly(path string) (bool, error) {
	sid, err := currentUserSID()
	if err != nil {
		return false, err
	}
	sd, err := fileSecurity(path)
	if err != nil {
		return false, err
	}
	return ownerOnlyDirDACL(sd, sid), nil
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

// fileSecurity reads the file's owner and DACL as a self-relative security
// descriptor.
func fileSecurity(path string) ([]byte, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var need uint32
	_, _, _ = procGetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), ownerOnlyInformation, 0, 0, uintptr(unsafe.Pointer(&need)))
	if need == 0 {
		return nil, syscall.EINVAL
	}
	buf := make([]byte, need)
	r, _, e := procGetFileSecurityW.Call(uintptr(unsafe.Pointer(p)), ownerOnlyInformation, uintptr(unsafe.Pointer(&buf[0])), uintptr(need), uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return nil, e
	}
	return buf, nil
}

// ownerOnlyDetail renders the file's owner and DACL as SDDL for failure
// messages only; SDDL text is not canonical and is never compared.
func ownerOnlyDetail(path string) string {
	sd, err := fileSecurity(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	s, err := winio.SecurityDescriptorToSddl(sd)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return s
}
