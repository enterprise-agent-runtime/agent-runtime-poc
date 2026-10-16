package main

import (
	"bytes"
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

// The commands that talk to wardend without starting it used to treat
// every connect error as "not running" (WH-1+2 re-review of 330a5ed,
// confirmed by repro): "warden daemon start" then started a second wardend
// and replaced the running daemon's token, locking every client out, and
// version/stop/status hid a refused token file behind "not running". Only
// api.ErrNotRunning means not running.

var (
	errNotRunningDial = fmt.Errorf("dial: %w", api.ErrNotRunning)
	errRefusedDial    = errors.New("refusing to send the token: run/token is not owner-only")
)

func failingDial(err error) func(context.Context, platform.Layout) (*api.Client, error) {
	return func(context.Context, platform.Layout) (*api.Client, error) { return nil, err }
}

// TestDaemonStart_RefusedTokenStartsNothing: with a running endpoint and a
// loose token file, "daemon start" returns the refusal, starts nothing and
// leaves the token file byte-identical.
func TestDaemonStart_RefusedTokenStartsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.EnvHome, home)
	l := platform.Layout{Home: home}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	ln, err := platform.Listen(home)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := os.WriteFile(l.Token(), []byte("live-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(l.Token(), 0o644); err != nil {
		t.Fatal(err)
	}
	started := false
	start := func(context.Context, platform.Layout) error { started = true; return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	err = daemonStart(ctx, l, dialCLI, start, &out)
	if started {
		t.Fatal("daemon start started a second wardend")
	}
	if err == nil || !strings.Contains(err.Error(), "not owner-only") {
		t.Fatalf("daemon start = %v; want the refusal", err)
	}
	if b, _ := os.ReadFile(l.Token()); string(b) != "live-token" {
		t.Fatalf("token file changed to %q", b)
	}

	started = false
	if err := daemonStart(ctx, l, failingDial(errNotRunningDial), start, &out); err != nil || !started {
		t.Fatalf("daemon start when not running: started %v, err %v", started, err)
	}
}

// TestNotRunningOnlyOnErrNotRunning: version, stop and status say "not
// running" for api.ErrNotRunning only; any other error is shown, and stop
// and status exit non-zero for it.
func TestNotRunningOnlyOnErrNotRunning(t *testing.T) {
	l := platform.Layout{Home: t.TempDir()}
	ctx := context.Background()
	for _, c := range []struct {
		name string
		run  func(dial func(context.Context, platform.Layout) (*api.Client, error), out *bytes.Buffer) error
		// exit status for the two errors: not running, other
		notRunningErr, otherErr bool
	}{
		{"version", func(d func(context.Context, platform.Layout) (*api.Client, error), out *bytes.Buffer) error {
			return printDaemonVersion(ctx, l, d, out)
		}, false, false},
		{"stop", func(d func(context.Context, platform.Layout) (*api.Client, error), out *bytes.Buffer) error {
			return daemonStop(ctx, l, d, out, out)
		}, false, true},
		{"status", func(d func(context.Context, platform.Layout) (*api.Client, error), out *bytes.Buffer) error {
			return daemonStatus(ctx, l, d, out)
		}, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			err := c.run(failingDial(errNotRunningDial), &out)
			if !strings.Contains(out.String(), "not running") || (err != nil) != c.notRunningErr {
				t.Fatalf("not running: out %q err %v", out.String(), err)
			}
			out.Reset()
			err = c.run(failingDial(errRefusedDial), &out)
			shown := strings.Contains(out.String(), "not owner-only") || (err != nil && strings.Contains(err.Error(), "not owner-only"))
			if strings.Contains(out.String(), "not running") || !shown || (err != nil) != c.otherErr {
				t.Fatalf("refused token: out %q err %v; want the real error, not \"not running\"", out.String(), err)
			}
		})
	}
}
