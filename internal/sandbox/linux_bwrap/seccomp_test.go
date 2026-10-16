package linux_bwrap

import (
	"encoding/json"
	"errors"
	"testing"

	"warden.dev/warden/internal/sandbox"
	"warden.dev/warden/internal/sandbox/seccomp"
)

// TestSeccompArch: the program is chosen for the architecture the daemon
// runs on, and only the two A06 §11.5 supports have one. Any other is
// sandbox_unavailable: a filter for the wrong arch kills every syscall,
// and no filter is no sandbox.
func TestSeccompArch(t *testing.T) {
	for goarch, want := range map[string]seccomp.Arch{"amd64": seccomp.AMD64, "arm64": seccomp.ARM64} {
		if got, err := SeccompArch(goarch); err != nil || got != want {
			t.Errorf("SeccompArch(%q) = %v, %v; want %v", goarch, got, err, want)
		}
	}
	for _, goarch := range []string{"386", "arm", "riscv64", "ppc64le", "s390x", "loong64", "", "AMD64"} {
		if got, err := SeccompArch(goarch); !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("SeccompArch(%q) = %v, %v; want %v", goarch, got, err, sandbox.ErrUnavailable)
		}
	}
}

// TestLimits pins sandbox.create.limits (A06 §13.2) for an L1 sandbox:
// the configured limits, whether a cgroup enforces them, the seccomp
// filter version, and the optional bwrap flags that were dropped (A06
// §11.1 "recorded in sandbox.create.limits"; missing_flags is NEW).
func TestLimits(t *testing.T) {
	got, err := json.Marshal(Limits(sandbox.DefaultLimits(), true, Features{Version: "0.9.0", AddSeccompFD: true, Size: true, DisableUserns: true}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"memory_mb":3072,"pids":512,"cpus":2,"fsize_mb":1024,"tmp_mb":1024,"cgroup":true,"seccomp":"v1"}`
	if string(got) != want {
		t.Errorf("Limits = %s\nwant      %s", got, want)
	}
	got, _ = json.Marshal(Limits(sandbox.DefaultLimits(), false, Features{Version: "0.6.1", AddSeccompFD: true}))
	want = `{"memory_mb":3072,"pids":512,"cpus":2,"fsize_mb":1024,"tmp_mb":1024,"cgroup":false,"seccomp":"v1","missing_flags":["--size","--disable-userns"]}`
	if string(got) != want {
		t.Errorf("Limits = %s\nwant      %s", got, want)
	}
	if seccomp.Version != "v1" {
		t.Errorf("seccomp.Version = %q; update this test and A06 §13.2 together", seccomp.Version)
	}
}
