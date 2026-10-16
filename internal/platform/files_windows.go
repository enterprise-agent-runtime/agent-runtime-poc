//go:build windows

package platform

import (
	"errors"
	"fmt"
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
	advapi32                    = syscall.NewLazyDLL("advapi32.dll")
	procSetFileSecurityW        = advapi32.NewProc("SetFileSecurityW")
	procGetFileSecurityW        = advapi32.NewProc("GetFileSecurityW")
	procGetKernelObjectSecurity = advapi32.NewProc("GetKernelObjectSecurity")
	procSetKernelObjectSecurity = advapi32.NewProc("SetKernelObjectSecurity")
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
	// os.Rename is MoveFileEx(MOVEFILE_REPLACE_EXISTING): the target is
	// replaced atomically, so a client never finds it missing. Only if that
	// fails (it does on some volumes) is the target removed first.
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return nil
}

// mkdirOwnerOnly creates dir (not its parents) with the owner-only
// descriptor, its ACE inherited by children (OI|CI), set by CreateDirectory
// itself. An existing dir is fs.ErrExist.
func mkdirOwnerOnly(dir string) error {
	sd, err := ownerOnlySecurityDescriptor("OICI")
	if err != nil {
		return err
	}
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	err = syscall.CreateDirectory(p, securityAttributes(sd))
	runtime.KeepAlive(sd)
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return nil
}

// protectDir gives an existing directory the owner-only descriptor through
// a handle opened on the directory itself (FILE_FLAG_OPEN_REPARSE_POINT), so
// a junction swapped in at the path is refused, never followed. MkdirAll's
// mode means nothing on Windows (WH-1+2, CWE-276). SetKernelObjectSecurity
// does not propagate to children that already exist.
func protectDir(dir string, me identity) error {
	sd, err := ownerOnlySecurityDescriptor("OICI")
	if err != nil {
		return err
	}
	h, attrs, err := openNoFollow(dir, readControl|writeDAC|writeOwner)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	if attrs&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("%s is not a directory", dir)
	}
	// The owner is compared on the same handle before anything changes:
	// another account's directory is refused, not taken over (WH-1+2).
	cur, err := handleSecurity(h)
	if err != nil {
		return &os.PathError{Op: "GetKernelObjectSecurity", Path: dir, Err: err}
	}
	if !ownerIs(cur, me.sid) {
		return foreignOwnerError(dir, cur, sddlDetail(cur))
	}
	r, _, e := procSetKernelObjectSecurity.Call(uintptr(h), ownerOnlyInformation|protectedDACLSecurityInformation, uintptr(unsafe.Pointer(&sd[0])))
	runtime.KeepAlive(sd)
	if r == 0 {
		return &os.PathError{Op: "SetKernelObjectSecurity", Path: dir, Err: e}
	}
	return nil
}

// identity is the account Ensure protects directories for: the SID of the
// process token user.
type identity struct{ sid []byte }

func currentIdentity() (identity, error) {
	sid, err := currentUserSID()
	return identity{sid: sid}, err
}

// isLink reports a symlink, junction or any other reparse point.
func isLink(fi fs.FileInfo) bool {
	if fi.Mode()&os.ModeSymlink != 0 {
		return true
	}
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return ok && d.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

const (
	fileFlagOpenReparsePoint = 0x00200000 // FILE_FLAG_OPEN_REPARSE_POINT
	fileFlagBackupSemantics  = 0x02000000 // FILE_FLAG_BACKUP_SEMANTICS: required to open a directory
	readControl              = 0x00020000 // READ_CONTROL
	writeDAC                 = 0x00040000 // WRITE_DAC
	writeOwner               = 0x00080000 // WRITE_OWNER
)

// errReparsePoint: the path is a symbolic link, junction or other reparse
// point, which an owner-only check never follows.
var errReparsePoint = errors.New("is a symbolic link, junction or other reparse point")

// openNoFollow opens path itself, never the target of a reparse point at it
// (FILE_FLAG_OPEN_REPARSE_POINT), and refuses one. Files and directories
// alike; the attributes are returned for the caller's own checks.
func openNoFollow(path string, access uint32) (syscall.Handle, uint32, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	h, err := syscall.CreateFile(p, access, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, fileFlagOpenReparsePoint|fileFlagBackupSemantics, 0)
	if err != nil {
		return 0, 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		syscall.CloseHandle(h)
		return 0, 0, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		syscall.CloseHandle(h)
		return 0, 0, fmt.Errorf("%s %w", path, errReparsePoint)
	}
	return h, info.FileAttributes, nil
}

// handleSecurity reads the owner and DACL of an open handle as a
// self-relative security descriptor.
func handleSecurity(h syscall.Handle) ([]byte, error) {
	var need uint32
	_, _, _ = procGetKernelObjectSecurity.Call(uintptr(h), ownerOnlyInformation, 0, 0, uintptr(unsafe.Pointer(&need)))
	if need == 0 {
		return nil, syscall.EINVAL
	}
	buf := make([]byte, need)
	r, _, e := procGetKernelObjectSecurity.Call(uintptr(h), ownerOnlyInformation, uintptr(unsafe.Pointer(&buf[0])), uintptr(need), uintptr(unsafe.Pointer(&need)))
	if r == 0 {
		return nil, e
	}
	return buf, nil
}

// OwnerOnly reports whether the file is owned by the current user and its
// DACL is the protected owner-only DACL written by WriteOwnerOnly: one
// FILE_ALL_ACCESS grant to the current user and nothing else. A reparse
// point at path is never followed and never owner-only. The descriptor is
// parsed, not compared as SDDL text, because Windows abbreviates well-known
// SIDs when rendering SDDL (the RID-500 administrator reads back as "LA";
// CLAUDE.md §4 IPC, D-011, D-027).
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
	// A reparse point is never followed; a file the user cannot even open
	// (CreateFile adds SYNCHRONIZE, which owning a file does not grant) is
	// not the user's owner-only file. Neither is an error: missing is.
	if errors.Is(err, errReparsePoint) || errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ownerOnlyDACL(sd, sid), nil
}

// ReadOwnerOnly opens path once, without following a reparse point, decides
// on that handle that it is an owner-only file (GetKernelObjectSecurity),
// and reads at most max bytes from the same handle. Checking by path and
// then reading by path let the file be swapped in between (WH-1+2 review
// of c8f81ca, CWE-367/59).
func ReadOwnerOnly(path string, max int) ([]byte, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	h, attrs, err := openNoFollow(path, syscall.GENERIC_READ|readControl)
	if errors.Is(err, errReparsePoint) {
		return nil, fmt.Errorf("%w: %w", err, errNotOwnerOnly)
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	if attrs&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return nil, fmt.Errorf("%s is a directory: %w", path, errNotOwnerOnly)
	}
	sd, err := handleSecurity(h)
	if err != nil {
		return nil, &os.PathError{Op: "GetKernelObjectSecurity", Path: path, Err: err}
	}
	if !ownerOnlyDACL(sd, sid) {
		return nil, notOwnerOnly(path, sddlDetail(sd))
	}
	return readLimited(f, path, max)
}

// dirOwnerOnly is OwnerOnly for a directory written by mkdirOwnerOnly or protectDir.
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

// fileSecurity reads the owner and DACL of path itself (never a reparse
// point's target) as a self-relative security descriptor.
func fileSecurity(path string) ([]byte, error) {
	h, _, err := openNoFollow(path, readControl)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(h)
	return handleSecurity(h)
}

// ownerOnlyDetail renders the file's owner and DACL as SDDL for failure
// messages only; SDDL text is not canonical and is never compared.
func ownerOnlyDetail(path string) string {
	sd, err := fileSecurity(path)
	if err != nil {
		sd, err = pathSecurity(path)
	}
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return sddlDetail(sd)
}

func sddlDetail(sd []byte) string {
	s, err := winio.SecurityDescriptorToSddl(sd)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return s
}

// pathSecurity reads the owner and DACL by path (GetFileSecurity), which
// needs only READ_CONTROL but follows a reparse point at path. For failure
// messages and tests only; decisions use fileSecurity.
func pathSecurity(path string) ([]byte, error) {
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
