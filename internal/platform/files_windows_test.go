//go:build windows

package platform

import (
	"encoding/binary"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode/utf16"
	"unsafe"

	"github.com/Microsoft/go-winio"
)

// TestOwnerOnly_AbbreviatedSID reproduces, on any Windows machine, the CI
// failure of TestWriteOwnerOnly on windows-latest: Windows renders well-known
// SIDs abbreviated in SDDL (RID 500 as "LA" there), so a check comparing SDDL
// text rejects a correct owner-only DACL. BUILTIN\Administrators
// (S-1-5-32-544) always renders as "BA", which makes the case deterministic.
// The check must decide on the DACL's structure, not on its text.
func TestOwnerOnly_AbbreviatedSID(t *testing.T) {
	const admins = "S-1-5-32-544"
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sd, err := winio.SddlToSecurityDescriptor("D:P(A;;FA;;;" + admins + ")")
	if err != nil {
		t.Fatal(err)
	}
	ptr, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	if r, _, e := procSetFileSecurityW.Call(uintptr(unsafe.Pointer(ptr)), daclSecurityInformation|protectedDACLSecurityInformation, uintptr(unsafe.Pointer(&sd[0]))); r == 0 {
		t.Fatal(e)
	}
	// Give the file back to the current user so TempDir can remove it even
	// where the test user is not an administrator. Cleanups run LIFO, so
	// this runs before TempDir's removal.
	t.Cleanup(func() { _ = setOwnerOnlyACL(p) })

	// The detail carries the owner too since WH-1+2; the DACL part is what
	// must read back abbreviated.
	if got := ownerOnlyDetail(p); strings.Contains(got, "D:P(A;;FA;;;"+admins+")") {
		t.Fatalf("SDDL read back unabbreviated (%s): this test no longer reproduces the text mismatch", got)
	}
	sid, err := syscall.StringToSid(admins)
	if err != nil {
		t.Fatal(err)
	}
	n := syscall.GetLengthSid(sid)
	adminsSID := unsafe.Slice((*byte)(unsafe.Pointer(sid)), n)
	read, err := pathSecurity(p) // by path: the DACL denies the user the SYNCHRONIZE a handle needs
	if err != nil {
		t.Fatal(err)
	}
	// The DACL half of the check decides on structure. The owner is still
	// the current user (making Administrators the owner needs elevation), so
	// since WH-1+2 the whole check refuses the file for Administrators.
	if !singleACEDACL(read, adminsSID, 0) {
		t.Fatalf("DACL check for %s refused; read back %s", admins, ownerOnlyDetail(p))
	}
	if ok, err := ownerOnlyFor(p, adminsSID); err != nil || ok {
		t.Fatalf("owner-only for %s with the current user as owner = %v %v", admins, ok, err)
	}
	// The same DACL is not owner-only for the current user.
	if ok, err := OwnerOnly(p); err != nil || ok {
		t.Fatalf("owner-only for the current user = %v %v", ok, err)
	}
}

// TestWriteOwnerOnly_DescriptorFromCreation guards WH-1+2 (a) (security
// review of FX-3, CWE-367): WriteOwnerOnly wrote the token into a file with
// the DACL inherited from the directory and set the owner-only DACL only
// afterwards, so a process that opened a handle in between kept its access.
// The descriptor is now part of CreateFile; the hook reads it after the
// create and before the write.
func TestWriteOwnerOnly_DescriptorFromCreation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run", "token")
	var seen string
	err := writeOwnerOnly(p, []byte("tok"), writeHooks{afterCreate: func(tmp string) error {
		seen = tmp
		ok, err := OwnerOnly(tmp)
		if err != nil || !ok {
			t.Errorf("temp file %s before the write: owner-only %v %v; read back %s", tmp, ok, err, ownerOnlyDetail(tmp))
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if seen == "" {
		t.Fatal("afterCreate was not called")
	}
}

// TestEnsure_HomeAndRunOwnerOnly guards WH-1+2 (b) (CWE-276): Ensure used
// MkdirAll alone, whose mode means nothing on Windows, so the home and run
// directories kept the DACL inherited from %LOCALAPPDATA%. A new home, an
// empty one, and an existing Warden home (it has the marker) with an
// inherited DACL end up owned by the user with a protected owner-only DACL
// inherited by their children. Other existing directories are refused
// (TestEnsure_RefusesForeignDirectory).
func TestEnsure_HomeAndRunOwnerOnly(t *testing.T) {
	for name, pre := range map[string]string{"new home": "", "empty home": "empty", "existing Warden home": "marker"} {
		t.Run(name, func(t *testing.T) {
			l := Layout{Home: filepath.Join(t.TempDir(), "home")}
			if pre != "" {
				if err := os.MkdirAll(l.Home, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if pre == "marker" {
				if err := os.MkdirAll(l.Run(), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := WriteOwnerOnly(filepath.Join(l.Home, HomeMarker), nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := l.Ensure(); err != nil {
				t.Fatal(err)
			}
			for _, d := range []string{l.Home, l.Run()} {
				if ok, err := dirOwnerOnly(d); err != nil || !ok {
					t.Errorf("%s: owner-only %v %v; read back %s", d, ok, err, ownerOnlyDetail(d))
				}
			}
		})
	}
}

// TestCheckOwnerOnly_RefusesLooseTokenFile guards WH-1+2 (d): wardend checks
// the token file right after writing it and the client before sending the
// token, both through CheckOwnerOnly. An extra ACE, or a DACL inherited
// from the directory, is refused. (A foreign owner cannot be set without
// privilege; the pure rows of TestOwnerOnlyDACL cover it.)
func TestCheckOwnerOnly_RefusesLooseTokenFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "token")
	if err := WriteOwnerOnly(good, []byte("tok")); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnerOnly(good); err != nil {
		t.Fatalf("owner-only token refused: %v", err)
	}

	extra := filepath.Join(dir, "extra")
	if err := WriteOwnerOnly(extra, []byte("tok")); err != nil {
		t.Fatal(err)
	}
	u, err := currentUserSDDLSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := winio.SddlToSecurityDescriptor("O:" + u + "D:P(A;;FA;;;" + u + ")(A;;FR;;;WD)") // plus Everyone: read
	if err != nil {
		t.Fatal(err)
	}
	if err := setSecurity(extra, sd); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnerOnly(extra); err == nil {
		t.Errorf("token file with an Everyone ACE accepted: %s", ownerOnlyDetail(extra))
	}

	inherited := filepath.Join(dir, "inherited")
	if err := os.WriteFile(inherited, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnerOnly(inherited); err == nil {
		t.Errorf("token file with an inherited DACL accepted: %s", ownerOnlyDetail(inherited))
	}
}

func currentUserSDDLSID() (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	s, err := (*syscall.SID)(unsafe.Pointer(&sid[0])).String()
	return s, err
}

// TestReadOwnerOnly_RefusesJunction guards the review of c8f81ca (WH-1+2):
// the token file is opened with FILE_FLAG_OPEN_REPARSE_POINT and a reparse
// point at the path is refused, never followed. A junction needs no
// privilege to create, unlike a symlink, so this runs on every Windows
// machine; the symlinked-token Dial test in internal/api needs Developer
// Mode here and runs everywhere on Unix.
func TestReadOwnerOnly_RefusesJunction(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := mkdirOwnerOnly(target); err != nil { // owner-only, so only the link is wrong
		t.Fatal(err)
	}
	link := filepath.Join(dir, "token")
	makeJunction(t, link, target)
	if _, err := ReadOwnerOnly(link, 4096); err == nil || !errors.Is(err, errReparsePoint) {
		t.Fatalf("ReadOwnerOnly(junction) = %v; want a reparse-point refusal", err)
	}
	if ok, err := OwnerOnly(link); err != nil || ok {
		t.Fatalf("OwnerOnly(junction) = %v %v; want false", ok, err)
	}
}

// makeJunction creates link as a directory junction (mount point) to
// target with FSCTL_SET_REPARSE_POINT; unlike a symlink it needs no
// privilege.
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatal(err)
	}
	subst := utf16.Encode([]rune(`\??\` + target))
	print := utf16.Encode([]rune(target))
	var path []uint16
	path = append(path, subst...)
	path = append(path, 0)
	path = append(path, print...)
	path = append(path, 0)
	buf := make([]byte, 0, 16+2*len(path))
	buf = binary.LittleEndian.AppendUint32(buf, 0xA0000003) // IO_REPARSE_TAG_MOUNT_POINT
	buf = binary.LittleEndian.AppendUint16(buf, uint16(8+2*len(path)))
	buf = binary.LittleEndian.AppendUint16(buf, 0)
	buf = binary.LittleEndian.AppendUint16(buf, 0)                        // SubstituteNameOffset
	buf = binary.LittleEndian.AppendUint16(buf, uint16(2*len(subst)))     // SubstituteNameLength
	buf = binary.LittleEndian.AppendUint16(buf, uint16(2*(len(subst)+1))) // PrintNameOffset
	buf = binary.LittleEndian.AppendUint16(buf, uint16(2*len(print)))     // PrintNameLength
	for _, c := range path {
		buf = binary.LittleEndian.AppendUint16(buf, c)
	}
	p, err := syscall.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_EXISTING, fileFlagOpenReparsePoint|fileFlagBackupSemantics, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(h)
	var n uint32
	if err := syscall.DeviceIoControl(h, 0x000900A4, &buf[0], uint32(len(buf)), nil, 0, &n, nil); err != nil { // FSCTL_SET_REPARSE_POINT
		t.Fatalf("FSCTL_SET_REPARSE_POINT: %v", err)
	}
}

// TestEnsure_RefusesJunctionHome guards the review of c8f81ca (WH-1+2):
// SetFileSecurity follows a junction, so Ensure on a home that is one would
// re-permission its target. A reparse point as the home is refused and the
// target is left alone.
func TestEnsure_RefusesJunctionHome(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := pathSecurity(target)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	makeJunction(t, home, target)
	if err := (Layout{Home: home}).Ensure(); err == nil || !strings.Contains(err.Error(), "reparse point") {
		t.Fatalf("Ensure on a junction home = %v; want a refusal", err)
	}
	after, err := pathSecurity(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("junction target re-permissioned: %s", sddlDetail(after))
	}
}

// TestEnsure_ExistingSubdirsAndForeignOwner guards WH-1+2 (re-review of
// 330a5ed): every existing directory Ensure manages (keys/ holds the trusted
// checkpoint keys) gets the owner-only descriptor, a junction there is
// refused, and protectDir compares the directory's owner with the user
// before changing anything, so another account's directory is refused, not
// taken over. The owner seam passes Administrators as "the user", since a
// test cannot give a directory to another account.
func TestEnsure_ExistingSubdirsAndForeignOwner(t *testing.T) {
	l := Layout{Home: filepath.Join(t.TempDir(), "home")}
	if err := os.MkdirAll(l.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.Keys(), l.DB(), l.Blobs()} { // inherited DACLs
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteOwnerOnly(filepath.Join(l.Home, HomeMarker), nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.Keys(), l.DB(), l.Blobs(), l.Logs(), l.Cache(), l.Exports()} {
		if ok, err := dirOwnerOnly(d); err != nil || !ok {
			t.Errorf("%s: owner-only %v %v; read back %s", d, ok, err, ownerOnlyDetail(d))
		}
	}

	admins, err := syscall.StringToSid("S-1-5-32-544")
	if err != nil {
		t.Fatal(err)
	}
	other := identity{sid: unsafe.Slice((*byte)(unsafe.Pointer(admins)), syscall.GetLengthSid(admins))}
	// foreignOwnerError names elevation when the owner is this SID (WH-4).
	if string(builtinAdministratorsSID()) != string(other.sid) {
		t.Fatalf("builtinAdministratorsSID = %x; Windows says S-1-5-32-544 is %x", builtinAdministratorsSID(), other.sid)
	}
	before, err := pathSecurity(l.Home)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.ensure(other); err == nil || !strings.Contains(err.Error(), "is owned by") {
		t.Fatalf("Ensure for another owner = %v; want an ownership refusal", err)
	}
	if after, _ := pathSecurity(l.Home); string(after) != string(before) {
		t.Fatalf("home changed for a foreign owner: %s", sddlDetail(after))
	}

	if err := os.RemoveAll(l.Keys()); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, l.Keys(), target)
	// The refusal names what is refused: every managed directory, not just
	// "a Warden home and its run directory" as it used to (WH-4).
	if err := l.Ensure(); err == nil || !strings.Contains(err.Error(), "reparse point") || !strings.Contains(err.Error(), wantLinkRefusal) {
		t.Fatalf("Ensure with keys/ a junction = %v; want a refusal saying %q", err, wantLinkRefusal)
	}
}

// TestReadOwnerWritable_DACL guards WH-4 round 1 (CWE-1188) on Windows: the
// integrity check for key files accepts the inherited DACL (SYSTEM,
// Administrators, the user) every build before WH-4 wrote, and a DACL that
// lets others read; it refuses a DACL that grants another SID a write-type
// right, with the icacls /reset hint, and a junction.
func TestReadOwnerWritable_DACL(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	inherited := filepath.Join(dir, "inherited.pub")
	if err := os.WriteFile(inherited, []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOwnerWritable(inherited, 64); err != nil {
		t.Fatalf("inherited DACL (%s): %v; want accepted", ownerOnlyDetail(inherited), err)
	}
	withDACL := func(name, aces string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("k"), 0o644); err != nil {
			t.Fatal(err)
		}
		sd, err := winio.SddlToSecurityDescriptor("O:" + u.Uid + "D:P(A;;FA;;;" + u.Uid + ")" + aces)
		if err != nil {
			t.Fatal(err)
		}
		if err := setSecurity(p, sd); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := ReadOwnerWritable(withDACL("readable.pub", "(A;;FR;;;WD)"), 64); err != nil {
		t.Errorf("Everyone may read: %v; want accepted", err)
	}
	for name, aces := range map[string]string{
		"everyone-write.pub": "(A;;FW;;;WD)",
		"users-modify.pub":   "(A;;0x1301bf;;;BU)",
		"users-writedac.pub": "(A;;WD;;;BU)",
	} {
		p := withDACL(name, aces)
		if _, err := ReadOwnerWritable(p, 64); err == nil || !strings.Contains(err.Error(), "icacls") || strings.Contains(err.Error(), "remove it") {
			t.Errorf("%s (%s): ReadOwnerWritable = %v; want a refusal with the icacls hint", name, ownerOnlyDetail(p), err)
		}
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "junction.pub")
	makeJunction(t, link, target)
	if _, err := ReadOwnerWritable(link, 64); err == nil {
		t.Error("junction accepted")
	}
}
