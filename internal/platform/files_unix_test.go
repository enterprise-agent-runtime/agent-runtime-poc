//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"syscall"
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

// TestReadOwnerOnly_RefusesSymlinkAndFIFO guards the review of c8f81ca
// (WH-1+2): OwnerOnly used os.Stat, so run/token as a symlink to the user's
// own 0600 SSH key passed and the key was sent as the token. ReadOwnerOnly
// opens with O_NOFOLLOW and decides on the descriptor; a FIFO planted at
// the path is refused without blocking the client (O_NONBLOCK).
func TestReadOwnerOnly_RefusesSymlinkAndFIFO(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	if err := WriteOwnerOnly(key, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "token")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadOwnerOnly(link, 4096); err == nil {
		t.Fatalf("ReadOwnerOnly followed a symlink and read %q", b)
	}
	if ok, err := OwnerOnly(link); err != nil || ok {
		t.Fatalf("OwnerOnly(symlink to a 0600 file) = %v %v; want false", ok, err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOwnerOnly(fifo, 4096); err == nil {
		t.Fatal("ReadOwnerOnly read a FIFO")
	}
}
