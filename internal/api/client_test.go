package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"warden.dev/warden/internal/platform"
)

// TestDial_RefusesTokenFileNotOwnerOnly guards WH-1+2 (d) (security review
// of FX-3): the client read the token file whatever its permissions and
// sent the token. A token file another account can read has leaked the
// token; one it can write may hold that account's token, so the client
// would authenticate to whatever it planted. The daemon here is real and
// the token in the file is right; only the file's protection is wrong
// (0644 on Unix, the directory's inherited DACL on Windows).
func TestDial_RefusesTokenFileNotOwnerOnly(t *testing.T) {
	e := newEnv(t, nil)
	if err := os.Remove(e.l.Token()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.l.Token(), []byte(token), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.l.Token(), 0o644); err != nil { // past the umask
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, e.l, "warden-test", "0.1.0", nil)
	if err == nil {
		c.Close()
		t.Fatal("Dial sent the token from a token file that is not owner-only")
	}
	if !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("Dial error = %v; want a refusal naming the token file as not owner-only", err)
	}
}

// TestDial_NoTokenFile keeps the message for the common case: no daemon.
func TestDial_NoTokenFile(t *testing.T) {
	e := newEnv(t, nil)
	if err := os.Remove(e.l.Token()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Dial(ctx, e.l, "warden-test", "0.1.0", nil); err == nil || !strings.Contains(err.Error(), "wardend is not running") {
		t.Fatalf("Dial without a token file: %v", err)
	}
}

// TestDial_NotRunningSentinel: the two "no daemon" cases, no token file and
// a token file without an endpoint, are api.ErrNotRunning, on which alone the
// CLI starts a daemon (WH-1+2 review of c8f81ca).
func TestDial_NotRunningSentinel(t *testing.T) {
	l := platform.Layout{Home: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Dial(ctx, l, "warden-test", "0.1.0", nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("no token file: %v; want ErrNotRunning", err)
	}
	if err := platform.WriteOwnerOnly(l.Token(), []byte(token)); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(ctx, l, "warden-test", "0.1.0", nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("token file, no endpoint: %v; want ErrNotRunning", err)
	}
}

// TestDial_RefusesSymlinkedToken guards the security review of c8f81ca
// (WH-1+2, confirmed by probe): readToken checked the token file by path and
// then read it by path, and the Unix check followed symlinks, so run/token
// as a symlink to the user's own 0600 SSH key passed and the key was sent
// as the token. The file is now opened once without following links and
// decided on that handle.
func TestDial_RefusesSymlinkedToken(t *testing.T) {
	e := newEnv(t, nil)
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	if err := platform.WriteOwnerOnly(secret, []byte(token)); err != nil { // owner-only, and even the right token
		t.Fatal(err)
	}
	if err := os.Remove(e.l.Token()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, e.l.Token()); err != nil {
		// Windows without Developer Mode cannot create symlinks; the
		// reparse-point refusal is covered there by
		// TestReadOwnerOnly_RefusesJunction in internal/platform.
		t.Skipf("cannot create a symlink here: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, e.l, "warden-test", "0.1.0", nil)
	if err == nil {
		c.Close()
		t.Fatal("Dial followed a symlinked token file and sent its target")
	}
	if errors.Is(err, ErrNotRunning) {
		t.Fatalf("refusal reported as ErrNotRunning: %v", err)
	}
}

// TestDial_RefusesOversizedToken: the token is one short line (A05 §3.1);
// a token file of 64 KiB is not one, and is not read into memory and sent.
func TestDial_RefusesOversizedToken(t *testing.T) {
	e := newEnv(t, nil)
	if err := platform.WriteOwnerOnly(e.l.Token(), []byte(strings.Repeat("a", 64<<10))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, e.l, "warden-test", "0.1.0", nil)
	if err == nil {
		c.Close()
		t.Fatal("Dial sent a 64 KiB token file")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("Dial error = %v; want a size refusal", err)
	}
}

// TestDialError_Wording guards WH-1+2 (re-review of 330a5ed): the transport
// prefixed every Dial failure with "wardend is not running (…)", so an
// endpoint that exists but refuses this user (ACCESS_DENIED, EACCES), or
// one served by another user (WH-3's ErrEndpointNotOwned), read as "not
// running". Only a missing or refusing endpoint says so and is
// ErrNotRunning; the others say what happened and keep their cause.
func TestDialError_Wording(t *testing.T) {
	const ep = `\.\pipe\warden-test`
	errNotOwned := errors.New("endpoint is served by another user") // stands in for platform.ErrEndpointNotOwned
	transport := func(err error) error { return fmt.Errorf("wardend is not running (%s): %w", ep, err) }
	cases := []struct {
		name       string
		err        error
		notRunning bool
		mention    string
	}{
		{"missing", transport(&fs.PathError{Op: "open", Path: ep, Err: fs.ErrNotExist}), true, "not running"},
		{"refused", transport(syscall.ECONNREFUSED), true, "not running"},
		{"access denied", transport(&fs.PathError{Op: "open", Path: ep, Err: fs.ErrPermission}), false, "access denied"},
		{"not owned", errNotOwned, false, "another user"},
	}
	for _, c := range cases {
		got := dialError(ep, c.err)
		if errors.Is(got, ErrNotRunning) != c.notRunning {
			t.Errorf("%s: ErrNotRunning = %v, want %v (%v)", c.name, !c.notRunning, c.notRunning, got)
		}
		if !strings.Contains(got.Error(), c.mention) || (!c.notRunning && strings.Contains(got.Error(), "not running")) {
			t.Errorf("%s: %q; want it to say %q and not \"not running\"", c.name, got, c.mention)
		}
		if !errors.Is(got, c.err) && !errors.Is(got, errors.Unwrap(c.err)) {
			t.Errorf("%s: cause lost: %v", c.name, got)
		}
	}
	if got := dialError(ep, errNotOwned); !errors.Is(got, errNotOwned) {
		t.Errorf("endpoint-not-owned identity lost: %v", got)
	}
}
