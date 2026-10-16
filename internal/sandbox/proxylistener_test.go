package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"warden.dev/warden/internal/proxy"
)

type nullSink struct{}

func (nullSink) ProxyConnect(context.Context, proxy.ConnectEvent) error { return nil }
func (nullSink) ProxyDenied(context.Context, proxy.DeniedEvent) error   { return nil }

// TestOpenProxy_RefusesBeforeCreatingAnything: the listener is opened only
// for a valid L1 Spec, and only for a proxy bound to that Spec's sandbox
// (A07 §2: "the listener is created with the task allowlist and the sandbox
// id"). Each refusal comes before anything is created on disk; the layout
// here is in a real temp directory so a created entry would be seen.
func TestOpenProxy_RefusesBeforeCreatingAnything(t *testing.T) {
	al, err := proxy.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	good := proxy.Config{Allow: al, Events: nullSink{}}
	cases := []struct {
		name string
		mod  func(s *Spec, c *proxy.Config)
		want error
	}{
		{"L2 spec", func(s *Spec, c *proxy.Config) {
			s.Level, s.Toolchains = LevelL2, []Toolchain{{BinDirs: []string{"/usr/local/go/bin"}}}
			s.Proxy = ProxyEndpoint{Sidecar: testSide}
		}, ErrInvalidSpec},
		{"invalid spec", func(s *Spec, c *proxy.Config) {
			s.Proxy.Socket = filepath.Join(s.SessionDir, "scratch", "proxy.sock")
		}, ErrInvalidSpec},
		{"proxy of another sandbox", func(s *Spec, c *proxy.Config) {
			c.SandboxID = "sb_01JAXS2Q7M2V9KTC3F6YH5N0PC"
		}, ErrInvalidSpec},
		// A Config proxy.New refuses is a daemon bug like an invalid Spec
		// (review of 2aa3972: it was returned unwrapped).
		{"proxy config refused", func(s *Spec, c *proxy.Config) { c.Events = nil }, ErrInvalidSpec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := layoutIn(t.TempDir(), true)
			s, cfg := validSpec(l, LevelL1), good
			tc.mod(s, &cfg)
			pl, err := OpenProxy(context.Background(), s, cfg)
			if err == nil {
				pl.Close()
				t.Fatal("OpenProxy succeeded")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if _, err := os.Lstat(l.home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("something was created under the home: %v", err)
			}
		})
	}
}
