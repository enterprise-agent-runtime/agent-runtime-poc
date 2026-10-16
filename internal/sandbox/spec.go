package sandbox

// This file is the backend-neutral sandbox Spec of design A06 §2 and §17:
// the one description both backends (bwrap L1 and Docker L2) are built from,
// so that what a sandbox can see (INV-B), what its environment holds (INV-C)
// and where the deny-list bites at the mount layer (INV-I) are decided and
// tested once, in pure code. Validation is strict and fails closed: a Spec
// the design's layout (A06 §2, core §10) does not produce is refused rather
// than mounted.

import (
	"errors"
	"fmt"
	"net"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/execproto/denylist"
	"warden.dev/warden/internal/ids"
)

// Errors returned at the package boundary. The API maps ErrUnavailable to
// sandbox_unavailable (-32006, A05); ErrInvalidSpec is a daemon bug, never
// a user error, and ends the task as failed(tool).
var (
	ErrInvalidSpec    = errors.New("sandbox: invalid spec")
	ErrUnavailable    = errors.New("sandbox_unavailable")
	ErrUnknownSandbox = errors.New("sandbox: unknown sandbox")
)

// Level is the isolation level (WRD-10 §5, CLAUDE.md §4 "Sandbox").
type Level string

// Levels.
const (
	LevelL1 Level = "L1" // native: bubblewrap + seccomp (Linux), Seatbelt (macOS)
	LevelL2 Level = "L2" // container: per-task internal network + proxy sidecar (C-03)
)

// Purpose is sandbox.create purpose (A06 §13.2, §14).
type Purpose string

// Purposes. Harness sandboxes (A06 §14) arrive with the harness adapters
// (M5); until then a Spec with PurposeHarness is refused.
const (
	PurposeTask    Purpose = "task"
	PurposeHarness Purpose = "harness"
)

// Sandbox-side paths of A06 §2 (Linux L1 and L2 columns).
const (
	TargetWorktree    = "/work"
	TargetScratch     = "/scratch"
	TargetCache       = "/cache"
	TargetGit         = "/git"
	TargetGitObjects  = "/git-alt/objects"
	TargetExecBin     = "/warden/bin/warden-exec"
	TargetProfiles    = "/warden/etc/profiles.json"
	TargetProxySocket = "/run/warden/proxy.sock"
	TargetHooksDir    = "/warden/empty"
	TargetTmp         = "/tmp"

	// ForwarderAddr is where the in-sandbox forwarder listens on L1 (A06
	// §9): loopback inside the sandbox's own network namespace.
	ForwarderAddr = "127.0.0.1:3128"
)

// reservedTargets are sandbox paths a toolchain mount may neither equal,
// contain nor lie under: the task roots, the Warden files, and what the
// backend itself creates (/tmp, /proc, /dev, generated /etc files).
var reservedTargets = []string{
	TargetWorktree, TargetScratch, TargetCache, TargetGit, "/git-alt", "/warden", "/run", TargetTmp, "/proc", "/dev", "/etc",
}

// Limits are the per-sandbox resource limits of A06 §12 (config.yaml
// sandbox.limits). Policy may lower them, never raise them (INV-9).
type Limits struct {
	MemoryMB    int
	Pids        int
	CPUs        int
	FsizeMB     int
	TmpMB       int
	DiskFloorMB int
}

// DefaultLimits are the A06 §12 defaults.
func DefaultLimits() Limits {
	return Limits{MemoryMB: 3072, Pids: 512, CPUs: 2, FsizeMB: 1024, TmpMB: 1024, DiskFloorMB: 1024}
}

// Toolchain is one doctor-detected language runtime (A06 §10.5, §11.4).
//
// On L1 Root is a host directory mounted read-only at the same path, BinDirs
// lie under it and go first in PATH, and Masks are files under it that look
// like credentials and are hidden. Masks are files only: the doctor scan
// (A06 §10.5) reports credential-looking files, and the backend covers each
// with /dev/null (A06 §11.1 TOOLCHAIN_MASK), which cannot cover a
// directory. A plain path cannot say "directory", so there is no flag to
// refuse; a directory given here fails the bind at launch, closed. L1
// exists only on Linux and macOS, so these are slash paths. On L2 the
// toolchain is the image (A06 §15): Root and Masks are empty and BinDirs
// name directories inside the image.
type Toolchain struct {
	Root    string
	BinDirs []string
	Masks   []string
}

// ProxyEndpoint is how sandbox egress reaches the per-task proxy
// (CLAUDE.md §4 "Network"). Exactly one field is set, by level.
type ProxyEndpoint struct {
	// Socket is the host path of the per-task Unix socket (L1, A07 §2),
	// bind-mounted at TargetProxySocket and reached through the forwarder.
	Socket string
	// Sidecar is host:port of the proxy sidecar on the task's internal
	// Docker network (L2, CONFLICTS C-03). Nothing is mounted for it.
	Sidecar string
}

// Mask hides one deny-listed worktree entry at the mount layer (A06 §11.3):
// a file is covered by /dev/null, a directory by an empty read-only tmpfs.
type Mask struct {
	RelPath string // worktree-relative, slash separated, as found on disk
	Dir     bool
	Pattern string // the deny-list pattern that matched
}

// Spec is everything a backend needs to build one sandbox (A06 §2, §17).
// Host paths follow core §10: S = <WardenHome>/sessions/<ulid>.
type Spec struct {
	ID        string // sb_<ulid>
	SessionID string
	TaskKey   string
	Level     Level
	Purpose   Purpose
	Mode      execproto.Mode

	// HostHome is the user's home directory, filled by the caller (from
	// os.UserHomeDir). No mount source or toolchain root may equal or
	// contain it (INV-B "no $HOME"); a toolchain root strictly inside it
	// (~/.local/share/mise, ~/.nvm) is allowed unless deny-listed.
	HostHome string

	// WardenHome is the effective Warden home (internal/platform). Mount
	// sources may lie inside it only in sessions/ and cache/; everything
	// else there (run/token, secrets.enc, db/) must stay out (C-53).
	WardenHome  string
	SessionDir  string // S
	Worktree    string // S/worktree
	Scratch     string // S/scratch/<TaskKey>
	Cache       string // <WardenHome>/cache/<wsp_id>
	GitDir      string // S/git, the session's private gitdir (CF-18)
	MainObjects string // the user repository's objects/ directory; "" in A14 fetched object mode

	ExecBin        string // host warden-exec binary
	ProfilesFile   string // S/sandbox/<ID>/profiles.json
	ProfilesDigest string // sha256:<hex> of ProfilesFile, compared with exec.hello

	Toolchains    []Toolchain
	WorktreeMasks []Mask // from WorktreeMasks
	Proxy         ProxyEndpoint
	Limits        Limits

	SessionBranch string // warden/<ulid>
	BaseCommit    string // full object id of the session base (A14 §3)
}

var (
	reSandboxID   = regexp.MustCompile(`^sb_[0-9A-Za-z]{1,64}$`)
	reTaskKey     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	reDigest      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reCommit      = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	reProxySocket = regexp.MustCompile(`^px-[0-9]{1,9}\.sock$`)
	reHostname    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSpec, fmt.Sprintf(format, a...))
}

// Validate checks the Spec against the A06 §2 layout and the INV-B, INV-C
// and INV-I rules. Mounts and Env call it first, so no backend can build a
// sandbox from a Spec that fails it.
func (s *Spec) Validate() error {
	if !reSandboxID.MatchString(s.ID) {
		return invalid("sandbox id %q is not sb_<ulid>", s.ID)
	}
	if !ids.Valid(ids.Session, s.SessionID) {
		return invalid("session id %q is not ses_<ulid>", s.SessionID)
	}
	if !reTaskKey.MatchString(s.TaskKey) {
		return invalid("task key %q is not a plain name", s.TaskKey)
	}
	switch s.Level {
	case LevelL1, LevelL2:
	default:
		return invalid("level %q is neither L1 nor L2", s.Level)
	}
	if s.Purpose != PurposeTask {
		return invalid("purpose %q: only task sandboxes exist before the harness adapters (A06 §14, M5)", s.Purpose)
	}
	if s.Mode != execproto.ModeTool {
		return invalid("mode %q: a task sandbox runs warden-exec in tool mode (A06 §3.1)", s.Mode)
	}
	if err := s.validateHostPaths(); err != nil {
		return err
	}
	if err := s.validateToolchains(); err != nil {
		return err
	}
	if err := validateMasks(s.WorktreeMasks); err != nil {
		return err
	}
	if err := s.validateProxy(); err != nil {
		return err
	}
	l := s.Limits
	if l.MemoryMB <= 0 || l.Pids <= 0 || l.CPUs <= 0 || l.FsizeMB <= 0 || l.TmpMB <= 0 || l.DiskFloorMB <= 0 {
		return invalid("every limit must be positive (A06 §12): %+v", l)
	}
	if !reDigest.MatchString(s.ProfilesDigest) {
		return invalid("profiles digest %q is not sha256:<hex>", s.ProfilesDigest)
	}
	if !strings.HasPrefix(s.SessionBranch, "warden/") || len(s.SessionBranch) == len("warden/") {
		return invalid("session branch %q is not warden/<ulid>", s.SessionBranch)
	}
	if !reCommit.MatchString(s.BaseCommit) {
		return invalid("base commit %q is not a full object id", s.BaseCommit)
	}
	return nil
}

func (s *Spec) validateHostPaths() error {
	for _, p := range []struct{ name, v string }{
		{"user's home", s.HostHome},
		{"warden home", s.WardenHome}, {"session dir", s.SessionDir}, {"worktree", s.Worktree},
		{"scratch", s.Scratch}, {"cache", s.Cache}, {"git dir", s.GitDir},
		{"executor binary", s.ExecBin}, {"profiles file", s.ProfilesFile},
	} {
		if err := hostAbs(p.name, p.v); err != nil {
			return err
		}
	}
	// The session lives in <home>/sessions/<ulid> and its roots are exactly
	// the A06 §2 / core §10 paths. This is what keeps the user's own
	// checkout (and with it the main repository's .git, CLAUDE.md §9 rule
	// 6) and the rest of the Warden home out of every sandbox.
	if filepath.Dir(s.SessionDir) != filepath.Join(s.WardenHome, "sessions") {
		return invalid("session dir %q is not <warden home>/sessions/<ulid>", s.SessionDir)
	}
	if ulid := strings.TrimPrefix(s.SessionID, ids.Session+"_"); !strings.EqualFold(filepath.Base(s.SessionDir), ulid) {
		return invalid("session dir %q does not belong to session id %q", s.SessionDir, s.SessionID)
	}
	// Nothing mounted may be or contain the user's home or the Warden home
	// (INV-B "no $HOME"; the rest of the Warden home holds run/token and
	// secrets.enc, C-53).
	for _, src := range []struct{ name, v string }{
		{"worktree", s.Worktree}, {"scratch", s.Scratch}, {"cache", s.Cache}, {"git dir", s.GitDir},
		{"main objects", s.MainObjects}, {"executor binary", s.ExecBin}, {"profiles file", s.ProfilesFile},
		{"proxy socket", s.Proxy.Socket},
	} {
		for _, h := range []string{s.HostHome, s.WardenHome} {
			if src.v != "" && containsPath(src.v, h) {
				return invalid("%s %q is or contains the user's home or the warden home %q", src.name, src.v, h)
			}
		}
	}
	for _, p := range []struct{ name, got, want string }{
		{"worktree", s.Worktree, filepath.Join(s.SessionDir, "worktree")},
		{"git dir", s.GitDir, filepath.Join(s.SessionDir, "git")},
		{"scratch", s.Scratch, filepath.Join(s.SessionDir, "scratch", s.TaskKey)},
		{"profiles file", s.ProfilesFile, filepath.Join(s.SessionDir, "sandbox", s.ID, "profiles.json")},
	} {
		if p.got != p.want {
			return invalid("%s %q is not %q (A06 §2)", p.name, p.got, p.want)
		}
	}
	if !strictlyWithin(s.Cache, filepath.Join(s.WardenHome, "cache")) {
		return invalid("cache %q is not inside <warden home>/cache (core §10)", s.Cache)
	}
	rw := []string{s.Worktree, s.Scratch, s.Cache, s.GitDir}
	if s.MainObjects != "" {
		if err := hostAbs("main objects", s.MainObjects); err != nil {
			return err
		}
		// Objects only: no refs, no config, no hooks (CF-18, C-24).
		if filepath.Base(s.MainObjects) != "objects" {
			return invalid("main objects %q is not an objects/ directory (CF-18)", s.MainObjects)
		}
		if overlaps(s.MainObjects, s.WardenHome) {
			return invalid("main objects %q overlaps the warden home", s.MainObjects)
		}
		if pat, ok := denylist.MatchHost(s.MainObjects); ok {
			return invalid("main objects %q is deny-listed (%s)", s.MainObjects, pat)
		}
	}
	// Only the executor binary itself is bound at /warden/bin/warden-exec.
	if filepath.Base(s.ExecBin) != "warden-exec" {
		return invalid("executor binary %q is not a warden-exec binary", s.ExecBin)
	}
	// The executor is bound read-only, but a copy inside a writable root
	// could be replaced by one sandbox for the next.
	for _, r := range rw {
		if overlaps(s.ExecBin, r) {
			return invalid("executor binary %q lies in the writable root %q", s.ExecBin, r)
		}
	}
	// The Warden home counts as deny-listed (C-53): the executor is never
	// installed there.
	if pat, ok := denylist.MatchUnderHost(s.ExecBin, []string{s.WardenHome}, nil); ok {
		return invalid("executor binary %q is deny-listed (%s)", s.ExecBin, pat)
	}
	return nil
}

func (s *Spec) validateProxy() error {
	px := s.Proxy
	switch s.Level {
	case LevelL1:
		if px.Sidecar != "" {
			return invalid("an L1 sandbox has no proxy sidecar")
		}
		if err := hostAbs("proxy socket", px.Socket); err != nil {
			return err
		}
		// Exactly S/scratch/px-<n>.sock (A06 §2, A07 §2): the backend binds
		// it writable (A06 §11.1), so any other path in S would be a
		// writable bind of something that is not the socket.
		if filepath.Dir(px.Socket) != filepath.Join(s.SessionDir, "scratch") || !reProxySocket.MatchString(filepath.Base(px.Socket)) {
			return invalid("proxy socket %q is not <session dir>/scratch/px-<n>.sock (A06 §2)", px.Socket)
		}
		for _, r := range []string{s.Worktree, s.Scratch, s.Cache, s.GitDir} {
			if overlaps(px.Socket, r) {
				return invalid("proxy socket %q lies in the writable root %q", px.Socket, r)
			}
		}
	case LevelL2:
		if px.Socket != "" {
			return invalid("an L2 sandbox reaches its proxy sidecar over the network, not a mounted socket (C-03)")
		}
		host, port, err := net.SplitHostPort(px.Sidecar)
		if err != nil {
			return invalid("proxy sidecar %q is not host:port", px.Sidecar)
		}
		// A plain lower-case host name or IPv4 address: no scheme, no
		// userinfo, so no credential can travel into HTTP_PROXY (INV-C).
		if !reHostname.MatchString(host) {
			return invalid("proxy sidecar host %q is not a plain host name", host)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return invalid("proxy sidecar port %q is not 1-65535", port)
		}
	}
	return nil
}

func (s *Spec) validateToolchains() error {
	var roots []string
	for i, tc := range s.Toolchains {
		if s.Level == LevelL2 {
			if tc.Root != "" || len(tc.Masks) != 0 {
				return invalid("toolchain %d: on L2 the toolchain is the image; nothing is mounted (A06 §15)", i)
			}
		} else {
			if err := slashAbs("toolchain root", tc.Root); err != nil {
				return err
			}
			if tc.Root == "/" {
				return invalid("toolchain root %q overlaps the sandbox path %q", tc.Root, "/")
			}
			for _, r := range reservedTargets {
				if slashOverlaps(tc.Root, r) {
					return invalid("toolchain root %q overlaps the sandbox path %q", tc.Root, r)
				}
			}
			// The user's home: a root inside it is a normal toolchain
			// (~/.nvm, ~/.local/share/mise), a root that is or contains it
			// would mount all of it (INV-B "no $HOME", CWE-552). This does
			// not depend on the Warden home living in the user's home.
			if slashWithin(strings.ToLower(filepath.ToSlash(s.HostHome)), strings.ToLower(tc.Root)) {
				return invalid("toolchain root %q is or contains the user's home %q", tc.Root, s.HostHome)
			}
			for _, h := range []string{s.WardenHome, s.SessionDir, s.MainObjects} {
				if h != "" && slashOverlaps(tc.Root, filepath.ToSlash(h)) {
					return invalid("toolchain root %q overlaps %q", tc.Root, h)
				}
			}
			if pat, ok := denylist.MatchHost(tc.Root); ok {
				return invalid("toolchain root %q is deny-listed (%s)", tc.Root, pat)
			}
			for _, r := range roots {
				if slashOverlaps(tc.Root, r) {
					return invalid("toolchain roots %q and %q overlap", r, tc.Root)
				}
			}
			roots = append(roots, tc.Root)
			for _, m := range tc.Masks {
				if err := slashAbs("toolchain mask", m); err != nil {
					return err
				}
				if !slashStrictlyWithin(m, tc.Root) {
					return invalid("toolchain mask %q is not under its root %q", m, tc.Root)
				}
			}
		}
		for _, b := range tc.BinDirs {
			if err := slashAbs("toolchain bin dir", b); err != nil {
				return err
			}
			if s.Level == LevelL1 && !slashWithin(b, tc.Root) {
				return invalid("toolchain bin dir %q is not under its root %q", b, tc.Root)
			}
			// PATH never contains a task root, so a repository cannot
			// shadow npm or git (A06 §6 step 3, §8).
			for _, r := range []string{TargetWorktree, TargetScratch, TargetCache, TargetGit, "/git-alt", TargetTmp} {
				if slashOverlaps(b, r) {
					return invalid("toolchain bin dir %q overlaps the task root %q", b, r)
				}
			}
		}
	}
	return nil
}

func validateMasks(masks []Mask) error {
	if len(masks) > MaxMasks {
		return fmt.Errorf("%w: too_many_deny_list_matches (%d > %d, A06 §11.3)", ErrUnavailable, len(masks), MaxMasks)
	}
	seen := make(map[string]bool, len(masks))
	for _, m := range masks {
		r := m.RelPath
		if r == "" || r == "." || path.IsAbs(r) || path.Clean(r) != r || r == ".." || strings.HasPrefix(r, "../") ||
			strings.ContainsAny(r, "\\\x00\n") {
			return invalid("worktree mask %q is not a clean worktree-relative path", r)
		}
		if seen[r] {
			return invalid("worktree mask %q appears twice", r)
		}
		seen[r] = true
	}
	return nil
}

// hostAbs requires an absolute, clean host path without control bytes.
func hostAbs(name, p string) error {
	if p == "" {
		return invalid("%s is empty", name)
	}
	if strings.ContainsAny(p, "\x00\n\r") {
		return invalid("%s %q contains a control byte", name, p)
	}
	if !filepath.IsAbs(p) {
		return invalid("%s %q is not an absolute path", name, p)
	}
	if filepath.Clean(p) != p {
		return invalid("%s %q is not a clean path", name, p)
	}
	return nil
}

// slashAbs requires an absolute, clean slash path that can be a PATH entry.
func slashAbs(name, p string) error {
	if p == "" || !path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, ":\\\x00\n\r") {
		return invalid("%s %q is not a clean absolute slash path", name, p)
	}
	return nil
}

// strictlyWithin is exact (case-sensitive): it is used where a path must
// lie inside a root, so a spelling it does not recognise is refused.
func strictlyWithin(p, root string) bool {
	root = strings.TrimRight(root, `/\`)
	return len(p) > len(root)+1 && strings.HasPrefix(p, root) && (p[len(root)] == '/' || p[len(root)] == '\\')
}

// overlaps is case-insensitive: it is used where two paths must be
// disjoint, so ignoring case can only refuse more (macOS and Windows file
// systems are case-insensitive).
func overlaps(a, b string) bool {
	a, b = strings.ToLower(denylist.Normalize(a)), strings.ToLower(denylist.Normalize(b))
	return slashWithin(a, b) || slashWithin(b, a)
}

// containsPath reports whether p equals or contains root, case-insensitively
// (refusing more on case-insensitive file systems).
func containsPath(p, root string) bool {
	return slashWithin(strings.ToLower(denylist.Normalize(root)), strings.ToLower(denylist.Normalize(p)))
}

func slashOverlaps(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return slashWithin(a, b) || slashWithin(b, a)
}

func slashWithin(p, root string) bool {
	if p == root || root == "/" {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

func slashStrictlyWithin(p, root string) bool { return p != root && slashWithin(p, root) }
