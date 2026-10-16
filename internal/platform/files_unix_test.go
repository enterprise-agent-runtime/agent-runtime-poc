//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteOwnerOnly_LooseOldTempName guards WH-1+2 (a) on Unix (security
// review of FX-3, CWE-377): the temp file was path+".tmp" opened with
// O_CREATE|O_TRUNC, so a file planted there by anyone who could write the
// directory was reused with its own mode (0644 here) and renamed into
// place as the token. The temp name is now random and opened O_EXCL.
func TestWriteOwnerOnly_LooseOldTempName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p+".tmp", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p+".tmp", 0o644); err != nil { // past the umask
		t.Fatal(err)
	}
	if err := WriteOwnerOnly(p, []byte("tok")); err != nil {
		t.Fatal(err)
	}
	if ok, err := OwnerOnly(p); err != nil || !ok {
		t.Fatalf("token after a planted %s.tmp: owner-only %v %v (%s)", p, ok, err, ownerOnlyDetail(p))
	}
}
