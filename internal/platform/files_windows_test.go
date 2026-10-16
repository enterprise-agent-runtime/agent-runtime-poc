//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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
	read, err := fileSecurity(p)
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
// directories kept the DACL inherited from %LOCALAPPDATA%. Both a new home
// and one that already exists with an inherited DACL end up owned by the
// user with a protected owner-only DACL inherited by their children.
func TestEnsure_HomeAndRunOwnerOnly(t *testing.T) {
	for name, pre := range map[string]bool{"new home": false, "existing home": true} {
		t.Run(name, func(t *testing.T) {
			l := Layout{Home: filepath.Join(t.TempDir(), "home")}
			if pre {
				if err := os.MkdirAll(l.Run(), 0o700); err != nil {
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
