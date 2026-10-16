//go:build !linux && !darwin

package sandbox

import (
	"context"
	"errors"
	"os"
	"testing"

	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/proxy"
)

// TestOpenProxy_UnsupportedWithoutL1: on Windows (L2 only) a valid L1 Spec
// still gets no listener: OpenProxy reports sandbox_unavailable wrapping
// platform.ErrTaskSocketUnsupported and creates nothing.
func TestOpenProxy_UnsupportedWithoutL1(t *testing.T) {
	l := layoutIn(t.TempDir(), true)
	al, err := proxy.NewAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	pl, err := OpenProxy(context.Background(), validSpec(l, LevelL1), proxy.Config{Allow: al, Events: nullSink{}})
	if pl != nil || !errors.Is(err, platform.ErrTaskSocketUnsupported) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("OpenProxy = %v, %v; want nil, sandbox_unavailable (unsupported)", pl, err)
	}
	if _, err := os.Lstat(l.home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something was created under the home: %v", err)
	}
}
