package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warden.dev/warden/internal/platform"
)

// TestWriteToken_VerifiesBeforeServing guards WH-1+2 (review of c8f81ca):
// wardend writes the token file and must verify it before it binds and
// serves. The writer is injected, so a write that leaves the file readable
// by others (the inherited DACL of the directory on Windows, 0644 on Unix)
// is caught here: deleting the verification fails this test. A refused
// file is removed, so no client reads a token from it.
func TestWriteToken_VerifiesBeforeServing(t *testing.T) {
	const tok = "dG9rZW4tZm9yLXRlc3RzLW9ubHktbm90LWEtc2VjcmV0"
	dir := t.TempDir()

	good := filepath.Join(dir, "run", "token")
	if err := writeToken(good, tok, platform.WriteOwnerOnly); err != nil {
		t.Fatalf("owner-only write refused: %v", err)
	}

	loose := filepath.Join(dir, "loose")
	looseWrite := func(p string, b []byte) error {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		return os.Chmod(p, 0o644) // past the umask
	}
	err := writeToken(loose, tok, looseWrite)
	if err == nil || !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("loose token file: %v; want an owner-only refusal", err)
	}
	if _, err := os.Lstat(loose); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused token file left in place (%v)", err)
	}

	other := filepath.Join(dir, "other")
	wrong := func(p string, _ []byte) error { return platform.WriteOwnerOnly(p, []byte("something else")) }
	if err := writeToken(other, tok, wrong); err == nil {
		t.Fatal("token file holding another value accepted")
	}
}

// TestListenThenPublish_SecondDaemonKeepsToken guards WH-1+2 (re-review of
// 330a5ed, confirmed race, pre-existing): wardend wrote its token file
// before Listen told it another daemon was running, and exited 3 without
// restoring it, so any second wardend (run by hand, or two autostarts at
// once) replaced the live daemon's token and locked its clients out. The
// endpoint is now bound first, exclusively; only its owner writes the token.
func TestListenThenPublish_SecondDaemonKeepsToken(t *testing.T) {
	home := t.TempDir()
	l := platform.Layout{Home: home}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	const live = "bGl2ZS1kYWVtb24tdG9rZW4tZm9yLXRlc3RzLW9ubHk"
	ln, err := listenThenPublish(home, l.Token(), live, platform.Listen, platform.WriteOwnerOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	before, err := os.ReadFile(l.Token())
	if err != nil || string(before) != live {
		t.Fatalf("token after the first daemon: %q %v", before, err)
	}
	if ln2, err := listenThenPublish(home, l.Token(), "c2Vjb25kLWRhZW1vbi10b2tlbi1mb3ItdGVzdHMtb25seQ", platform.Listen, platform.WriteOwnerOnly); !errors.Is(err, platform.ErrDaemonRunning) {
		if ln2 != nil {
			ln2.Close()
		}
		t.Fatalf("second daemon: %v; want ErrDaemonRunning", err)
	}
	after, err := os.ReadFile(l.Token())
	if err != nil || string(after) != string(before) {
		t.Fatalf("second daemon changed the token file: %q -> %q (%v)", before, after, err)
	}
}
