//go:build windows

package platform

import (
	"os"
	"path/filepath"
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

	if got := ownerOnlyDetail(p); got == "D:P(A;;FA;;;"+admins+")" {
		t.Fatalf("SDDL read back unabbreviated (%s): this test no longer reproduces the text mismatch", got)
	}
	sid, err := syscall.StringToSid(admins)
	if err != nil {
		t.Fatal(err)
	}
	n := syscall.GetLengthSid(sid)
	ok, err := ownerOnlyFor(p, unsafe.Slice((*byte)(unsafe.Pointer(sid)), n))
	if err != nil || !ok {
		t.Fatalf("owner-only for %s = %v %v; DACL read back %s", admins, ok, err, ownerOnlyDetail(p))
	}
	// The same DACL is not owner-only for the current user.
	if ok, err := OwnerOnly(p); err != nil || ok {
		t.Fatalf("owner-only for the current user = %v %v", ok, err)
	}
}
