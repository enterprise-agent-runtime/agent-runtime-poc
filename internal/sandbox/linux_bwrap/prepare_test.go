//go:build !windows

package linux_bwrap

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
	"time"

	"warden.dev/warden/internal/sandbox"
)

// fakeInfo is a file-mode-only fs.FileInfo.
type fakeInfo struct {
	name string
	mode fs.FileMode
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

const testUID = 1000

// fakeProbes is an Ubuntu 24.04 host (merged /usr, no /lib32) whose proxy
// socket exists and belongs to the user. calls records the probe order.
type fakeProbes struct {
	links    map[string]string // toolchain root -> resolved
	sockMode fs.FileMode
	sockUID  int
	sockErr  error
	host     map[string]string // /bin etc. -> link target; "" = a directory
	calls    []string
}

func newFakeProbes() *fakeProbes {
	return &fakeProbes{
		links:    map[string]string{},
		sockMode: fs.ModeSocket | 0o600,
		sockUID:  testUID,
		host:     map[string]string{"/bin": "usr/bin", "/sbin": "usr/sbin", "/lib": "usr/lib", "/lib64": "usr/lib64"},
	}
}

func (f *fakeProbes) probes() Probes {
	return Probes{
		EvalSymlinks: func(p string) (string, error) {
			f.calls = append(f.calls, "eval "+p)
			if t, ok := f.links[p]; ok {
				return t, nil
			}
			return p, nil
		},
		LstatSocket: func(p string) (fs.FileMode, int, error) {
			f.calls = append(f.calls, "socket "+p)
			return f.sockMode, f.sockUID, f.sockErr
		},
		Lstat: func(p string) (fs.FileInfo, error) {
			f.calls = append(f.calls, "lstat "+p)
			t, ok := f.host[p]
			switch {
			case !ok:
				return nil, fs.ErrNotExist
			case t == "":
				return fakeInfo{p, fs.ModeDir | 0o755}, nil
			case t == "<file>":
				return fakeInfo{p, 0o644}, nil
			}
			return fakeInfo{p, fs.ModeSymlink | 0o777}, nil
		},
		Readlink: func(p string) (string, error) { return f.host[p], nil },
		UID:      testUID,
	}
}

// TestPrepare_BindsResolvedToolchainRoots: bwrap is given the resolved
// root as the source, so what was checked is what is mounted; the target
// stays the configured path that PATH and the masks name.
func TestPrepare_BindsResolvedToolchainRoots(t *testing.T) {
	f := newFakeProbes()
	f.links["/opt/node-v22"] = "/opt/nodes/22.1.0"
	p, err := Prepare(testSpec(), features090, cgroupExec, f.probes())
	if err != nil {
		t.Fatalf("Prepare() = %v", err)
	}
	ops, _ := parseArgv(t, p.Argv)
	if !hasOp(ops, "--ro-bind", "/opt/nodes/22.1.0", "/opt/node-v22") {
		t.Errorf("argv does not bind the resolved root at the configured path: %q", p.Argv)
	}
	if hasOp(ops, "--ro-bind", "/opt/node-v22", "/opt/node-v22") {
		t.Error("argv binds the unresolved root")
	}
}

// TestPrepare_ResolvesToolchainRootsBeforeBuilding exists for the M2-12
// review finding: /var/run is a symlink to /run, so a toolchain root that is
// a link passed every name check and bwrap bound the host /run, with
// docker.sock in it. Prepare must resolve before it builds.
func TestPrepare_ResolvesToolchainRootsBeforeBuilding(t *testing.T) {
	for _, target := range []string{"/run", "/sys/kernel", "/srv/warden-test/home/ana"} {
		f := newFakeProbes()
		f.links["/opt/node-v22"] = target
		p, err := Prepare(testSpec(), features090, cgroupExec, f.probes())
		if !errors.Is(err, sandbox.ErrInvalidSpec) {
			t.Errorf("toolchain root linked to %s: Prepare() = %v, %v; want %v", target, p, err, sandbox.ErrInvalidSpec)
		}
	}
}

// TestPrepare_ProxySocket: the bind source is the Spec's socket, which
// M2-12a's proxy listener creates. bwrap --bind follows symlinks and binds
// anything, so the path must be a socket (not a link, file or directory) the
// user owns, checked last before the argv exists (A07 §2; PROGRESS "Left
// for M2-13").
func TestPrepare_ProxySocket(t *testing.T) {
	cases := []struct {
		name string
		edit func(f *fakeProbes)
		ok   bool
	}{
		{"socket owned by the user", func(f *fakeProbes) {}, true},
		{"missing", func(f *fakeProbes) { f.sockMode, f.sockErr = 0, fs.ErrNotExist }, false},
		{"regular file", func(f *fakeProbes) { f.sockMode = 0o600 }, false},
		{"directory", func(f *fakeProbes) { f.sockMode = fs.ModeDir | 0o700 }, false},
		{"symlink to a socket", func(f *fakeProbes) { f.sockMode = fs.ModeSymlink | 0o777 }, false},
		{"socket of another user", func(f *fakeProbes) { f.sockUID = 0 }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeProbes()
			c.edit(f)
			s := testSpec()
			_, err := Prepare(s, features090, cgroupExec, f.probes())
			if c.ok != (err == nil) {
				t.Fatalf("Prepare() = %v, want ok=%v", err, c.ok)
			}
			if !c.ok && !errors.Is(err, sandbox.ErrUnavailable) {
				t.Errorf("Prepare() = %v, want %v", err, sandbox.ErrUnavailable)
			}
			if len(f.calls) == 0 {
				t.Fatal("no probe was called")
			}
			if last := f.calls[len(f.calls)-1]; last != "socket "+s.Proxy.Socket {
				t.Errorf("last probe = %q, want the Spec's socket checked last (%v)", last, f.calls)
			}
		})
	}
}

func TestPrepare_RefusesL2(t *testing.T) {
	s := testSpec()
	s.Level, s.Toolchains, s.Proxy = sandbox.LevelL2, nil, sandbox.ProxyEndpoint{Sidecar: "proxy:3128"}
	if _, err := Prepare(s, features090, cgroupExec, newFakeProbes().probes()); !errors.Is(err, sandbox.ErrInvalidSpec) {
		t.Fatalf("Prepare(L2) = %v, want %v", err, sandbox.ErrInvalidSpec)
	}
}

// TestProbeHost: on a merged-/usr host /bin is a symlink and A06 §11.1
// recreates it as one; a bind would go through the link. A system path that
// is a symlink anywhere else is an unknown layout: binding it would follow
// the link to host data A06 does not list, so it is refused.
func TestProbeHost(t *testing.T) {
	cases := []struct {
		name string
		host map[string]string
		want map[string]bool
		ok   bool
	}{
		{"merged /usr", map[string]string{"/bin": "usr/bin", "/sbin": "usr/sbin", "/lib": "usr/lib", "/lib64": "usr/lib64"},
			map[string]bool{"bin": true, "sbin": true, "lib": true, "lib64": true}, true},
		{"absolute links", map[string]string{"/bin": "/usr/bin", "/lib32": "/usr/lib32"}, map[string]bool{"bin": true, "lib32": true}, true},
		{"split /usr", map[string]string{"/bin": "", "/sbin": "", "/lib": "", "/lib64": ""}, map[string]bool{}, true},
		{"nothing present", map[string]string{}, map[string]bool{}, true},
		{"/lib64 elsewhere", map[string]string{"/lib64": "/home/ana/lib"}, nil, false},
		{"/bin to a sibling", map[string]string{"/bin": "usr/sbin"}, nil, false},
		{"/lib relative escape", map[string]string{"/lib": "usr/../home"}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeProbes()
			f.host = c.host
			p := f.probes()
			got, err := ProbeHost(p.Lstat, p.Readlink)
			if c.ok != (err == nil) {
				t.Fatalf("ProbeHost() = %v, want ok=%v", err, c.ok)
			}
			if !c.ok {
				if !errors.Is(err, sandbox.ErrUnavailable) {
					t.Errorf("ProbeHost() = %v, want %v", err, sandbox.ErrUnavailable)
				}
				return
			}
			var gotKeys, wantKeys []string
			for k, v := range got.Linked {
				if v {
					gotKeys = append(gotKeys, k)
				}
			}
			for k := range c.want {
				wantKeys = append(wantKeys, k)
			}
			slices.Sort(gotKeys)
			slices.Sort(wantKeys)
			if !slices.Equal(gotKeys, wantKeys) {
				t.Errorf("Linked = %v, want %v", gotKeys, wantKeys)
			}
		})
	}
	// An Lstat failure other than "does not exist" cannot be classified.
	_, err := ProbeHost(func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }, nil)
	if !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("ProbeHost(permission error) = %v, want %v", err, sandbox.ErrUnavailable)
	}
}

// TestEtcFiles pins the generated files to A06 §11.3: no host resolver
// (lookups fail fast at 127.0.0.1), the host uid and gid mapped 1:1, HOME
// in scratch, no login shell.
func TestEtcFiles(t *testing.T) {
	got := EtcFiles(1000, 1001)
	want := map[string]string{
		"passwd":        "root:x:0:0:root:/root:/usr/sbin/nologin\nwarden:x:1000:1001:warden:/scratch/home:/usr/sbin/nologin\n",
		"group":         "root:x:0:\nwarden:x:1001:\n",
		"hosts":         "127.0.0.1 localhost\n::1 localhost\n",
		"resolv.conf":   "nameserver 127.0.0.1\noptions timeout:1 attempts:1\n",
		"nsswitch.conf": "passwd: files\ngroup: files\nhosts: files dns\n",
	}
	if len(got) != len(want) {
		t.Errorf("EtcFiles() has %d files, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if !slices.Equal(EtcFileNames(), []string{"passwd", "group", "hosts", "resolv.conf", "nsswitch.conf"}) {
		t.Errorf("EtcFileNames() = %q", EtcFileNames())
	}
}

// TestProbeHost_SSLPrivate: /etc/ssl/private is masked only where it
// exists as a directory (Debian, Ubuntu). Anything else there (a link to
// elsewhere, a file) is a layout the mask cannot cover safely: refused.
func TestProbeHost_SSLPrivate(t *testing.T) {
	cases := []struct {
		name  string
		entry string // "" dir, "<file>", "-" absent, else a link target
		want  bool
		ok    bool
	}{
		{"directory", "", true, true},
		{"absent", "-", false, true},
		{"symlink", "/srv/keys", false, false},
		{"file", "<file>", false, false},
	}
	for _, c := range cases {
		f := newFakeProbes()
		if c.entry != "-" {
			f.host["/etc/ssl/private"] = c.entry
		}
		p := f.probes()
		got, err := ProbeHost(p.Lstat, p.Readlink)
		if c.ok != (err == nil) {
			t.Errorf("%s: ProbeHost() = %v, want ok=%v", c.name, err, c.ok)
			continue
		}
		if !c.ok && !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("%s: ProbeHost() = %v, want %v", c.name, err, sandbox.ErrUnavailable)
		}
		if c.ok && got.SSLPrivate != c.want {
			t.Errorf("%s: SSLPrivate = %v, want %v", c.name, got.SSLPrivate, c.want)
		}
	}
}
