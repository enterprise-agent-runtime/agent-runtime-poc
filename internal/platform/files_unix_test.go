//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
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

// TestEnsure_ExistingWardenHomeTightened: an existing Warden home (it has the
// marker) left 0777 is chmod-ed 0700, with run (WH-1+2 review of c8f81ca).
func TestEnsure_ExistingWardenHomeTightened(t *testing.T) {
	l := Layout{Home: filepath.Join(t.TempDir(), "home")}
	if err := os.MkdirAll(l.Run(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Home, HomeMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.Home, l.Run()} {
		if err := os.Chmod(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.Home, l.Run()} {
		if fi, err := os.Lstat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v %v; want 0700", d, fi.Mode(), err)
		}
	}
}

// TestEnsure_RefusesForeignOwnerAndSymlink: a home owned by another uid is
// refused, not chmod-ed (the seam passes a uid that is not the owner, since
// the test cannot chown without root); a home that is a symlink is refused
// and its target left alone (WH-1+2 review of c8f81ca).
func TestEnsure_RefusesForeignOwnerAndSymlink(t *testing.T) {
	l := Layout{Home: filepath.Join(t.TempDir(), "home")}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := l.ensure(identity{uid: os.Getuid() + 1}); err == nil || !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("Ensure for another uid = %v; want an ownership refusal", err)
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.Symlink(target, home); err != nil {
		t.Fatal(err)
	}
	if err := (Layout{Home: home}).Ensure(); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Ensure on a symlink home = %v; want a symlink refusal", err)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target re-permissioned to %v", fi.Mode())
	}
}

// TestEnsure_ExistingSubdirsTightened guards WH-1+2 (re-review of 330a5ed,
// P9): Ensure tightened only home and run, so a pre-planted 0777 keys/ stayed
// 0777, and keys/checkpoint*.pub are trusted. Every existing directory
// Ensure manages goes through the same check: 0700 if ours, refused if it is
// a symlink.
func TestEnsure_ExistingSubdirsTightened(t *testing.T) {
	l := Layout{Home: filepath.Join(t.TempDir(), "home")}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.Keys(), l.DB(), l.Blobs(), l.Logs(), l.Cache(), l.Exports()} {
		if err := os.Chmod(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.Keys(), l.DB(), l.Blobs(), l.Logs(), l.Cache(), l.Exports()} {
		if fi, err := os.Lstat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v %v; want 0700", d, fi.Mode(), err)
		}
	}

	elsewhere := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(l.Keys()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, l.Keys()); err != nil {
		t.Fatal(err)
	}
	// The refusal names what is refused: every managed directory, not just
	// "a Warden home and its run directory" as it used to (WH-4).
	if err := l.Ensure(); err == nil || !strings.Contains(err.Error(), "symbolic link") || !strings.Contains(err.Error(), wantLinkRefusal) {
		t.Fatalf("Ensure with keys/ a symlink = %v; want a refusal saying %q", err, wantLinkRefusal)
	}
}
