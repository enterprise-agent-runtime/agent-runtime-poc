//go:build windows

package sandbox

import (
	"context"
	"testing"

	"warden.dev/warden/internal/platform"
)

// The real 8.3 query on this machine needs no elevation and must give a
// definite answer for a temp directory (which volume and setting the
// runner has is not asserted).
func TestShortNamesCheck_RealQuery(t *testing.T) {
	c := shortNamesCheck(t.TempDir(), platform.NewHostFS())
	if c.Status != "ok" && c.Status != "warn" {
		t.Fatalf("status %q", c.Status)
	}
	if _, ok := c.Data["short_names"].(bool); !ok {
		t.Fatalf("no definite answer: %+v", c)
	}
	t.Logf("%s: %s", c.Status, c.Detail)
}

func TestProbe_WindowsReportsShortNames(t *testing.T) {
	for _, c := range Probe(context.Background(), Options{DefaultLevel: "L2", Home: t.TempDir()}) {
		if c.ID == "sandbox.short_names" {
			return
		}
	}
	t.Fatal("Probe on Windows lacks sandbox.short_names")
}
