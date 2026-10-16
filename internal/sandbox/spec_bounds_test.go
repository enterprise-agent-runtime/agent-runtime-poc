package sandbox

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// rejectCase is one Spec no backend may build, in a chosen home layout.
type rejectCase struct {
	name   string
	level  Level
	custom bool // WARDEN_HOME outside the user's home
	slash  bool // needs slash host paths (toolchain roots built from host paths)
	edit   func(l testLayout, s *Spec)
	want   error
	msg    string
}

func runRejects(t *testing.T, cases []rejectCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := layoutAt(t, c.custom)
			if c.slash && !slashHost(l) {
				t.Skip("L1 toolchain roots are slash paths; L1 does not exist on this host")
			}
			s := validSpec(l, c.level)
			c.edit(l, s)
			err := s.Validate()
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("Validate() = %v, want %v mentioning %q", err, c.want, c.msg)
			}
			if _, merr := s.Mounts(); !errors.Is(merr, c.want) {
				t.Errorf("Mounts() error = %v, want %v", merr, c.want)
			}
		})
	}
}

// TestSpecValidate_SidecarHost: the sidecar address becomes HTTP_PROXY
// verbatim, so its host must be a plain lower-case host name or IPv4
// address. "tok@proxy:3128" passes net.SplitHostPort and would otherwise
// put a credential into the sandbox env as http://tok@proxy:3128 (INV-C,
// CWE-200). Upper case is refused, not folded: the daemon names the
// sidecar itself, so a capital letter means the address came from
// somewhere else.
func TestSpecValidate_SidecarHost(t *testing.T) {
	var cases []rejectCase
	for _, side := range []string{"tok@proxy:3128", "proxy%2fx:1", "Proxy:1", "-proxy:1", "proxy..x:1"} {
		cases = append(cases, rejectCase{name: side, level: LevelL2, want: ErrInvalidSpec, msg: "plain host name",
			edit: func(l testLayout, s *Spec) { s.Proxy.Sidecar = side }})
	}
	runRejects(t, cases)
	for _, side := range []string{"proxy:1", "warden-proxy.internal:3128", "172.30.0.2:3128"} {
		s := validSpec(newLayout(t), LevelL2)
		s.Proxy.Sidecar = side
		if err := s.Validate(); err != nil {
			t.Errorf("sidecar %q: Validate() = %v, want nil", side, err)
		}
	}
}

// TestSpecValidate_ProxySocket: A06 §2 puts the L1 proxy socket at
// S/scratch/px-<n>.sock, and the backend binds it (A06 §11.1 --bind, rw).
// Any other path inside S (another task's scratch, S/sandbox) would be a
// writable bind of something that is not the socket (CWE-668).
func TestSpecValidate_ProxySocket(t *testing.T) {
	runRejects(t, []rejectCase{
		{name: "another task's scratch", level: LevelL1, want: ErrInvalidSpec, msg: "proxy socket",
			edit: func(l testLayout, s *Spec) { s.Proxy.Socket = filepath.Join(l.session, "scratch", "verify") }},
		{name: "the sandbox dir", level: LevelL1, want: ErrInvalidSpec, msg: "proxy socket",
			edit: func(l testLayout, s *Spec) { s.Proxy.Socket = filepath.Join(l.session, "sandbox") }},
		{name: "a px name with a suffix", level: LevelL1, want: ErrInvalidSpec, msg: "proxy socket",
			edit: func(l testLayout, s *Spec) { s.Proxy.Socket = filepath.Join(l.session, "scratch", "px-1.sock.d") }},
		{name: "a px socket below scratch", level: LevelL1, want: ErrInvalidSpec, msg: "proxy socket",
			edit: func(l testLayout, s *Spec) { s.Proxy.Socket = filepath.Join(l.session, "scratch", "x", "px-1.sock") }},
	})
	s := validSpec(newLayout(t), LevelL1)
	s.Proxy.Socket = filepath.Join(s.SessionDir, "scratch", "px-17.sock")
	if err := s.Validate(); err != nil {
		t.Errorf("S/scratch/px-17.sock: Validate() = %v, want nil", err)
	}
}

// TestSpecValidate_HostHome: no mount source or toolchain root may equal or
// contain the user's home or the Warden home (INV-B "no $HOME", CWE-552).
// The Warden-home check alone stops Root=$HOME only when the home is the
// default ~/.warden; with a custom WARDEN_HOME, Root=/home/ana would mount
// ~/.ssh read-only. Both layouts are tested. A root strictly inside the
// user's home (~/.local/share/mise, ~/.nvm) is a normal toolchain and stays
// allowed unless deny-listed.
func TestSpecValidate_HostHome(t *testing.T) {
	root := func(s *Spec, p string) { s.Toolchains = []Toolchain{{Root: filepath.ToSlash(p)}} }
	var cases []rejectCase
	for _, custom := range []bool{false, true} {
		name := map[bool]string{false: "default home: ", true: "custom WARDEN_HOME: "}[custom]
		cases = append(cases,
			rejectCase{name: name + "toolchain root is the user's home", level: LevelL1, custom: custom, slash: true,
				want: ErrInvalidSpec, msg: "user's home",
				edit: func(l testLayout, s *Spec) { root(s, l.hostHome) }},
			rejectCase{name: name + "toolchain root contains the user's home", level: LevelL1, custom: custom, slash: true,
				want: ErrInvalidSpec, msg: "user's home",
				edit: func(l testLayout, s *Spec) { root(s, filepath.Dir(l.hostHome)) }},
			rejectCase{name: name + "toolchain root is the warden home", level: LevelL1, custom: custom, slash: true,
				want: ErrInvalidSpec, msg: "overlaps",
				edit: func(l testLayout, s *Spec) { root(s, l.home) }},
			rejectCase{name: name + "toolchain root inside the warden home", level: LevelL1, custom: custom, slash: true,
				want: ErrInvalidSpec, msg: "overlaps",
				edit: func(l testLayout, s *Spec) { root(s, filepath.Join(l.home, "bin", "node")) }},
			rejectCase{name: name + "toolchain root is ~/.ssh", level: LevelL1, custom: custom, slash: true,
				want: ErrInvalidSpec, msg: "deny-listed",
				edit: func(l testLayout, s *Spec) { root(s, filepath.Join(l.hostHome, ".ssh")) }},
			rejectCase{name: name + "main objects contains the user's home", level: LevelL1, custom: custom,
				want: ErrInvalidSpec, msg: "user's home",
				edit: func(l testLayout, s *Spec) { s.HostHome = filepath.Join(s.MainObjects, "ana") }},
			rejectCase{name: name + "no user's home", level: LevelL2, custom: custom,
				want: ErrInvalidSpec, msg: "user's home",
				edit: func(l testLayout, s *Spec) { s.HostHome = "" }},
			rejectCase{name: name + "relative user's home", level: LevelL2, custom: custom,
				want: ErrInvalidSpec, msg: "not an absolute path",
				edit: func(l testLayout, s *Spec) { s.HostHome = filepath.Join("home", "ana") }},
		)
	}
	runRejects(t, cases)

	for _, custom := range []bool{false, true} {
		l := layoutAt(t, custom)
		if !slashHost(l) {
			continue
		}
		s := validSpec(l, LevelL1)
		mise := filepath.ToSlash(filepath.Join(l.hostHome, ".local", "share", "mise", "installs", "node", "22"))
		s.Toolchains = []Toolchain{{Root: mise, BinDirs: []string{mise + "/bin"}}}
		if err := s.Validate(); err != nil {
			t.Errorf("custom=%v: toolchain root %s inside the user's home: Validate() = %v, want nil", custom, mise, err)
		}
	}
}

// TestSpecValidate_SessionAndSources pins the session dir to
// <home>/sessions/<ulid of SessionID> (core §10) and closes two gaps in the
// host sources: a deny-listed objects dir (INV-I) and an executor path that
// is not the warden-exec binary.
func TestSpecValidate_SessionAndSources(t *testing.T) {
	runRejects(t, []rejectCase{
		{name: "session dir nested below another session", level: LevelL1, want: ErrInvalidSpec, msg: "is not <warden home>/sessions/<ulid>",
			edit: func(l testLayout, s *Spec) { s.SessionDir = filepath.Join(l.home, "sessions", "OTHER", "worktree") }},
		// The right ulid one level too deep: only the parent check catches it.
		{name: "session dir nested under its own name", level: LevelL1, want: ErrInvalidSpec, msg: "is not <warden home>/sessions/<ulid>",
			edit: func(l testLayout, s *Spec) {
				s.SessionDir = filepath.Join(l.home, "sessions", "OTHER", filepath.Base(l.session))
			}},
		{name: "session dir of another session", level: LevelL1, want: ErrInvalidSpec, msg: "session id",
			edit: func(l testLayout, s *Spec) { s.SessionID = "ses_01JAXR8Q7M2V9KTC3F6YH5N0PC" }},
		{name: "malformed session id", level: LevelL1, want: ErrInvalidSpec, msg: "session id",
			edit: func(l testLayout, s *Spec) { s.SessionID = "ses_x" }},
		{name: "main objects under .ssh", level: LevelL1, want: ErrInvalidSpec, msg: "deny-listed",
			edit: func(l testLayout, s *Spec) { s.MainObjects = filepath.Join(l.hostHome, ".ssh", "objects") }},
		{name: "executor binary is not warden-exec", level: LevelL1, want: ErrInvalidSpec, msg: "warden-exec",
			edit: func(l testLayout, s *Spec) { s.ExecBin = filepath.Join(l.hostHome, ".bash_history") }},
	})
	for _, custom := range []bool{false, true} {
		for _, level := range []Level{LevelL1, LevelL2} {
			if err := validSpec(layoutAt(t, custom), level).Validate(); err != nil {
				t.Errorf("custom=%v %s: Validate() = %v, want nil", custom, level, err)
			}
		}
	}
}
