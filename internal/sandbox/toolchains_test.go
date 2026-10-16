package sandbox

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// fakeEval resolves the paths it knows and leaves the rest unchanged, as
// filepath.EvalSymlinks does for a path without links.
func fakeEval(links map[string]string) func(string) (string, error) {
	return func(p string) (string, error) {
		if t, ok := links[p]; ok {
			if t == "" {
				return "", fs.ErrNotExist
			}
			return t, nil
		}
		return p, nil
	}
}

func TestResolveToolchains_ReturnsResolvedRoots(t *testing.T) {
	l := newLayout(t)
	s := validSpec(l, LevelL1)
	got, err := s.ResolveToolchains(fakeEval(map[string]string{"/opt/node-v22": "/opt/nodes/22.1.0"}))
	if err != nil {
		t.Fatalf("ResolveToolchains() = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != "/opt/nodes/22.1.0" {
		t.Fatalf("ResolveToolchains() = %q, want the resolved root", got)
	}
	// The Spec itself is not changed: inside the sandbox the toolchain stays
	// at its configured path, so PATH and the masks keep working.
	if s.Toolchains[0].Root != "/opt/node-v22" {
		t.Errorf("Spec root changed to %q", s.Toolchains[0].Root)
	}
	if got, err := validSpec(l, LevelL2).ResolveToolchains(fakeEval(nil)); err != nil || len(got) != 0 {
		t.Errorf("L2 ResolveToolchains() = %q, %v; want nothing: L2 mounts no toolchain", got, err)
	}
}

// TestResolveToolchains_RefusesWhatTheLinkReaches exists because the M2-12
// checks look at the configured spelling only, and bwrap follows the
// source's symlinks when it binds. /var/run is a link to /run on every
// current distribution, so a root of /var/run passed every check and bound
// the host's /run, docker.sock included (M2-12 review, PROGRESS "Left for
// M2-13"). Each row is a configured root that is harmless by name and
// dangerous by target; the checks must run on the target.
func TestResolveToolchains_RefusesWhatTheLinkReaches(t *testing.T) {
	cases := []struct {
		name, target, msg string
	}{
		{"link to the host /run (docker.sock)", "/run", "/run"},
		{"link into /run", "/run/user/1000", "/run"},
		{"link to /sys", "/sys/fs/cgroup", "/sys"},
		{"link to /proc", "/proc/1/root", "/proc"},
		{"link to /dev", "/dev", "/dev"},
		{"link to /", "/", "overlaps"},
		{"link to a task root path", "/work", "/work"},
		{"link to the user's home", "/srv/warden-test/home/ana", "home"},
		{"link to a parent of the user's home", "/srv/warden-test/home", "home"},
		{"link into a deny-listed directory", "/srv/opt/.ssh/node", "deny-listed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newLayout(t)
			if !slashHost(l) && strings.Contains(c.msg, "home") {
				t.Skip("the user's home is a drive path here; L1 toolchain roots are slash paths")
			}
			s := validSpec(l, LevelL1)
			_, err := s.ResolveToolchains(fakeEval(map[string]string{"/opt/node-v22": c.target}))
			if !errors.Is(err, ErrInvalidSpec) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("ResolveToolchains() = %v, want %v mentioning %q", err, ErrInvalidSpec, c.msg)
			}
			if !strings.Contains(err.Error(), c.target) {
				t.Errorf("error %q does not name the resolved path %q", err, c.target)
			}
		})
	}
}

// Two roots that are disjoint by name can be the same directory on disk;
// the overlap check must see that too.
func TestResolveToolchains_RefusesRootsThatOverlapOnceResolved(t *testing.T) {
	l := newLayout(t)
	s := validSpec(l, LevelL1)
	s.Toolchains = append(s.Toolchains, Toolchain{Root: "/opt/go", BinDirs: []string{"/opt/go/bin"}})
	_, err := s.ResolveToolchains(fakeEval(map[string]string{"/opt/go": "/opt/node-v22/lib/go"}))
	if !errors.Is(err, ErrInvalidSpec) || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("ResolveToolchains() = %v, want overlapping roots refused", err)
	}
}

// A root that does not resolve (dangling link, removed toolchain) cannot be
// checked, so the sandbox is not created (fail closed).
func TestResolveToolchains_UnresolvableRootIsUnavailable(t *testing.T) {
	l := newLayout(t)
	s := validSpec(l, LevelL1)
	_, err := s.ResolveToolchains(fakeEval(map[string]string{"/opt/node-v22": ""}))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ResolveToolchains() = %v, want %v", err, ErrUnavailable)
	}
}

// An invalid Spec is refused before anything is resolved.
func TestResolveToolchains_ValidatesFirst(t *testing.T) {
	l := newLayout(t)
	s := validSpec(l, LevelL1)
	s.Toolchains[0].Root = "/sys"
	called := false
	_, err := s.ResolveToolchains(func(p string) (string, error) { called = true; return p, nil })
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("ResolveToolchains() = %v, want %v", err, ErrInvalidSpec)
	}
	if called {
		t.Error("eval was called for an invalid Spec")
	}
}
