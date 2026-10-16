package sandboxtest

import (
	"context"
	"reflect"
	"testing"

	"warden.dev/warden/internal/sandbox"
)

// TestFakeSandbox_KeepsItsOwnCopy: the fake must record the Spec as it was
// at Create. A shallow copy shares the Toolchains and WorktreeMasks
// backing arrays with the caller, so a caller editing its Spec afterwards
// would silently rewrite what the fake reports, and a test above this
// package could assert on a sandbox that was never created.
func TestFakeSandbox_KeepsItsOwnCopy(t *testing.T) {
	f := &FakeSandbox{}
	s := spec(t, "sb_E")
	s.Toolchains = []sandbox.Toolchain{{BinDirs: []string{"/usr/local/go/bin"}}}
	s.WorktreeMasks = []sandbox.Mask{{RelPath: ".env", Pattern: "**/.env"}}
	h, err := f.Create(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	mountsBefore := append([]sandbox.Mount(nil), h.Mounts...)

	s.Toolchains[0].BinDirs[0] = "/work/evil"
	s.Toolchains = append(s.Toolchains, sandbox.Toolchain{BinDirs: []string{"/x"}})
	s.WorktreeMasks[0].RelPath = "README.md"

	got, ok := f.Spec("sb_E")
	if !ok {
		t.Fatal("Spec(sb_E) missing")
	}
	if want := []sandbox.Toolchain{{BinDirs: []string{"/usr/local/go/bin"}}}; !reflect.DeepEqual(got.Toolchains, want) {
		t.Errorf("stored Toolchains = %+v, want %+v (changed by the caller after Create)", got.Toolchains, want)
	}
	if got.WorktreeMasks[0].RelPath != ".env" {
		t.Errorf("stored WorktreeMasks = %+v, changed by the caller after Create", got.WorktreeMasks)
	}
	// What Spec returns is a copy too.
	got.WorktreeMasks[0].RelPath = "other"
	got.Toolchains[0].BinDirs[0] = "/other"
	if again, _ := f.Spec("sb_E"); again.WorktreeMasks[0].RelPath != ".env" || again.Toolchains[0].BinDirs[0] != "/usr/local/go/bin" {
		t.Errorf("Spec() returned the stored slices: %+v", again)
	}
	if !reflect.DeepEqual(h.Mounts, mountsBefore) {
		t.Errorf("handle mounts changed: %+v", h.Mounts)
	}
}
