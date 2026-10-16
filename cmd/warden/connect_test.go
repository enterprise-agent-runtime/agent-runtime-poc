package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"warden.dev/warden/internal/api"
	"warden.dev/warden/internal/platform"
)

// TestConnect_TokenRefusalStartsNothing guards the review of c8f81ca
// (WH-1+2): connect started a daemon on ANY Dial error. A token file that is
// not owner-only made Dial fail, so connect removed the running daemon's
// token and spawned a second wardend, which wrote a new token and exited 3:
// every client of the running daemon then failed, and the refusal never
// reached the user. connect starts a daemon only on api.ErrNotRunning.
func TestConnect_TokenRefusalStartsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.EnvHome, home)
	l := platform.Layout{Home: home}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	ln, err := platform.Listen(home) // the running daemon's endpoint
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := os.WriteFile(l.Token(), []byte("tok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(l.Token(), 0o644); err != nil { // past the umask
		t.Fatal(err)
	}
	started := false
	start := func(context.Context, platform.Layout) error { started = true; return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := connectTo(ctx, l, true, start)
	if err == nil {
		c.Close()
	}
	if started {
		t.Fatal("connect started a daemon because the token file was refused")
	}
	if err == nil || !strings.Contains(err.Error(), "not owner-only") || errors.Is(err, api.ErrNotRunning) {
		t.Fatalf("connect error = %v; want the owner-only refusal, not ErrNotRunning", err)
	}
	if b, _ := os.ReadFile(l.Token()); string(b) != "tok" {
		t.Fatalf("token file changed to %q", b)
	}
}

// TestConnect_NotRunningStarts: with no token file and no endpoint, connect
// still starts the daemon (WRD-16 §5.1).
func TestConnect_NotRunningStarts(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.EnvHome, home)
	l := platform.Layout{Home: home}
	started := false
	errStarter := errors.New("starter called")
	start := func(context.Context, platform.Layout) error { started = true; return errStarter }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := connectTo(ctx, l, true, start); !started || !errors.Is(err, errStarter) {
		t.Fatalf("connect without a daemon: started %v, err %v", started, err)
	}
}

// TestConnect_StartsOnlyOnNotRunning pins the autostart rule on the error
// alone (WH-1+2 review of c8f81ca): only an error matching api.ErrNotRunning
// starts a daemon. Any other Dial error, such as the "endpoint served by
// another user" error WH-3 adds to platform.Dial, is returned unchanged and
// starts nothing: starting wardend there would hand our token to whoever
// owns the endpoint, or replace the running daemon's token.
func TestConnect_StartsOnlyOnNotRunning(t *testing.T) {
	errNotOwned := fmt.Errorf("refusing x: %w", platform.ErrEndpointNotOwned)
	cases := []struct {
		name      string
		dialErr   error
		wantStart bool
	}{
		{"not running", fmt.Errorf("dial: %w", api.ErrNotRunning), true},
		{"endpoint not owned", fmt.Errorf("dial: %w", errNotOwned), false},
		{"token refused", errors.New("refusing to send the token: not owner-only"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			started := false
			dial := func(context.Context, platform.Layout) (*api.Client, error) { return nil, c.dialErr }
			start := func(context.Context, platform.Layout) error { started = true; return nil }
			_, err := connectWith(context.Background(), platform.Layout{Home: t.TempDir()}, true, dial, start)
			if started != c.wantStart {
				t.Fatalf("started = %v, want %v", started, c.wantStart)
			}
			if !c.wantStart && err != c.dialErr {
				t.Fatalf("error = %v; want the Dial error unchanged", err)
			}
		})
	}
}
