package linux_bwrap

// The bwrap argv of design A06 §11.1, rendered from a validated Spec.
// Section order follows A06: namespaces and process flags, system binds,
// generated /etc, toolchains and their masks, kernel file systems, Warden
// files, task roots with the worktree masks, then the read-only root, the
// working directory, the environment and the seccomp program.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"warden.dev/warden/internal/execproto"
	"warden.dev/warden/internal/sandbox"
)

// mergedUsrDirs are the top-level system directories that are symlinks
// into /usr on a merged-/usr host (A06 §11.1, §11.2 first row).
var mergedUsrDirs = []string{"bin", "sbin", "lib", "lib64", "lib32"}

// systemTry are the read-only system paths A06 §11.1 binds when they exist
// (--ro-bind-try), after /usr and the merged-/usr directories.
var systemTry = []string{
	"/libx32",
	"/etc/ssl", "/etc/pki", "/etc/ca-certificates", "/etc/crypto-policies", "/etc/alternatives",
	"/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d", "/etc/os-release",
}

const (
	nixStore    = "/nix/store"
	sslPrivate  = "/etc/ssl/private"
	shmBytes    = 268435456 // /dev/shm, A06 §11.1
	mib         = 1 << 20
	childNofile = 4096 // A06 §7.3
)

// HostLayout is what the host's root looks like where A06 §11.1 depends on
// it. Linked[d] is true when /d is a symlink to usr/d (merged /usr); the
// sandbox then gets the same symlink instead of a bind through it.
// SSLPrivate is true when the host has the directory /etc/ssl/private, which
// the /etc/ssl bind would expose; it is masked with an empty read-only
// tmpfs.
type HostLayout struct {
	Linked     map[string]bool
	SSLPrivate bool
}

// buildInput is everything build needs. ToolchainSources are the resolved
// toolchain roots from Spec.ResolveToolchains, in Spec order; only Prepare
// fills it, so no argv is built from unresolved roots.
type buildInput struct {
	Spec             *sandbox.Spec
	Features         Features
	Host             HostLayout
	ToolchainSources []string
	Exec             ExecFlags
}

// argv accumulates bwrap options.
type argv []string

func (a *argv) add(s ...string) { *a = append(*a, s...) }

func build(in buildInput) (*Plan, error) {
	s := in.Spec
	if s == nil {
		return nil, fmt.Errorf("%w: no spec", sandbox.ErrInvalidSpec)
	}
	ms, err := s.Mounts()
	if err != nil {
		return nil, err
	}
	env, err := s.Env()
	if err != nil {
		return nil, err
	}
	if s.Level != sandbox.LevelL1 {
		return nil, fmt.Errorf("%w: level %s is not built with bwrap (A06 §11)", sandbox.ErrInvalidSpec, s.Level)
	}
	if len(in.ToolchainSources) != len(s.Toolchains) {
		return nil, fmt.Errorf("%w: %d resolved toolchain roots for %d toolchains", sandbox.ErrInvalidSpec, len(in.ToolchainSources), len(s.Toolchains))
	}
	if _, err := ParseFeatures("bubblewrap "+in.Features.Version, ""); err != nil {
		return nil, err
	}
	x := in.Exec
	if x.CPUSeconds <= 0 || x.MaxProcs <= 0 || (!x.Cgroup && x.Nproc <= 0) {
		return nil, fmt.Errorf("%w: executor flags %+v: cpu and max procs must be positive, and nproc too without a cgroup (A06 §7.3)", sandbox.ErrInvalidSpec, x)
	}
	groups, err := groupMounts(ms)
	if err != nil {
		return nil, err
	}

	var a argv
	a.add("bwrap")
	// Namespaces and process (A06 §11.1). --unshare-user is explicit:
	// --unshare-all only tries a user namespace, and bwrap refuses
	// --disable-userns unless --unshare-user was given.
	a.add("--unshare-all", "--unshare-user")
	if in.Features.DisableUserns {
		a.add(FlagDisableUserns)
	}
	a.add("--die-with-parent", "--new-session", "--cap-drop", "ALL", "--hostname", "warden", "--clearenv",
		"--info-fd", strconv.Itoa(FdInfo))

	// System, read-only.
	a.add("--ro-bind", "/usr", "/usr")
	for _, d := range mergedUsrDirs {
		if in.Host.Linked[d] {
			a.add("--symlink", "usr/"+d, "/"+d)
		} else {
			a.add("--ro-bind-try", "/"+d, "/"+d)
		}
	}
	for _, p := range systemTry {
		a.add("--ro-bind-try", p, p)
		if p == "/etc/ssl" && in.Host.SSLPrivate {
			// TLS private keys: 0640 root:ssl-cert on Debian, and the
			// sandbox keeps the user's supplementary groups (CWE-250).
			a.add("--tmpfs", sslPrivate, "--remount-ro", sslPrivate)
		}
	}
	for _, src := range in.ToolchainSources {
		if src == nixStore || strings.HasPrefix(src, nixStore+"/") {
			a.add("--ro-bind-try", nixStore, nixStore)
			break
		}
	}
	etc := filepath.Join(filepath.Dir(s.ProfilesFile), "etc")
	for _, f := range EtcFileNames() {
		a.add("--ro-bind", filepath.Join(etc, f), "/etc/"+f)
	}

	// Toolchains at their configured path, bound from the resolved root,
	// then the masks inside them.
	tc := 0
	for _, g := range groups {
		if g.parent.Kind != sandbox.KindToolchain {
			continue
		}
		g.parent.Source = in.ToolchainSources[tc]
		tc++
		a.group(g)
	}

	// Kernel file systems and tmpfs.
	a.add("--proc", "/proc", "--dev", "/dev")
	if in.Features.Size {
		a.add(FlagSize, strconv.Itoa(shmBytes))
	}
	a.add("--tmpfs", "/dev/shm")
	if in.Features.Size {
		a.add(FlagSize, strconv.FormatInt(int64(s.Limits.TmpMB)*mib, 10))
	}
	a.add("--tmpfs", sandbox.TargetTmp)

	// Warden files.
	for _, k := range []sandbox.MountKind{sandbox.KindExecutor, sandbox.KindGenerated} {
		for _, g := range groups {
			if g.parent.Kind == k {
				a.group(g)
			}
		}
		if k == sandbox.KindExecutor {
			a.add("--dir", sandbox.TargetHooksDir)
		}
	}

	// Task roots with the worktree masks, and the proxy socket.
	for _, g := range groups {
		switch g.parent.Kind {
		case sandbox.KindToolchain, sandbox.KindExecutor, sandbox.KindGenerated:
			continue
		}
		a.group(g)
	}

	// Finish.
	a.add("--remount-ro", "/", "--chdir", sandbox.TargetWorktree)
	for _, e := range env {
		a.add("--setenv", e.Key, e.Value)
	}
	if in.Features.AddSeccompFD {
		a.add(FlagAddSeccompFD, strconv.Itoa(FdSeccomp))
	} else {
		a.add("--seccomp", strconv.Itoa(FdSeccomp)) // bwrap without --add-seccomp-fd (A06 §11.2)
	}
	a.add("--")
	a.add(executorCommand(s, x)...)
	return &Plan{Argv: a, Fds: fdPlan(), Missing: in.Features.Missing()}, nil
}

// mountGroup is one non-mask mount and the masks that follow it.
type mountGroup struct {
	parent sandbox.Mount
	masks  []sandbox.Mount
}

// wantMode is the mode each kind must have; anything else is a Spec this
// backend does not know how to render, refused rather than guessed.
var wantMode = map[sandbox.MountKind]sandbox.MountMode{
	sandbox.KindWorktree: sandbox.ModeRW, sandbox.KindScratch: sandbox.ModeRW, sandbox.KindCache: sandbox.ModeRW,
	sandbox.KindGit: sandbox.ModeRW, sandbox.KindGitObjects: sandbox.ModeRO, sandbox.KindToolchain: sandbox.ModeRO,
	sandbox.KindExecutor: sandbox.ModeRO, sandbox.KindGenerated: sandbox.ModeRO, sandbox.KindProxy: sandbox.ModeConnect,
}

// groupMounts attaches each mask to the mount before it (Spec.Mounts emits
// masks right after the mount they hide) and checks that it lies inside it.
// Every other mount needs a source and the mode its kind has.
func groupMounts(ms []sandbox.Mount) ([]mountGroup, error) {
	var gs []mountGroup
	for _, m := range ms {
		if m.Kind == sandbox.KindMask {
			if len(gs) == 0 || !strings.HasPrefix(m.Target, gs[len(gs)-1].parent.Target+"/") {
				return nil, fmt.Errorf("%w: mask %q does not follow the mount it lies in", sandbox.ErrInvalidSpec, m.Target)
			}
			gs[len(gs)-1].masks = append(gs[len(gs)-1].masks, m)
			continue
		}
		if want, ok := wantMode[m.Kind]; !ok || m.Mode != want || m.Source == "" {
			return nil, fmt.Errorf("%w: %s mount %q -> %q (mode %q) is not rendered by bwrap", sandbox.ErrInvalidSpec, m.Kind, m.Source, m.Target, m.Mode)
		}
		gs = append(gs, mountGroup{parent: m})
	}
	return gs, nil
}

// group renders one mount and its masks: a file mask is /dev/null bound
// read-only over it, a directory mask an empty tmpfs remounted read-only
// (A06 §11.3). The proxy socket (connect only) is a plain --bind: a
// read-only bind does not stop connect(2), and A06 §11.1 binds it so.
func (a *argv) group(g mountGroup) {
	m := g.parent
	if m.Mode == sandbox.ModeRO {
		a.add("--ro-bind", m.Source, m.Target)
	} else {
		a.add("--bind", m.Source, m.Target)
	}
	for _, k := range g.masks {
		if k.Dir {
			a.add("--tmpfs", k.Target, "--remount-ro", k.Target)
		} else {
			a.add("--ro-bind", "/dev/null", k.Target)
		}
	}
}

// executorCommand is the warden-exec command line of A06 §3.1 for a tool
// sandbox, with the per-child rlimits of A06 §7.3.
func executorCommand(s *sandbox.Spec, x ExecFlags) []string {
	limits := fmt.Sprintf("cpu=%d,fsize=%d,nofile=%d,core=0", x.CPUSeconds, int64(s.Limits.FsizeMB)*mib, childNofile)
	if !x.Cgroup {
		limits += fmt.Sprintf(",data=%d,nproc=%d", int64(s.Limits.MemoryMB)*mib, x.Nproc)
	}
	roots := strings.Join([]string{
		"worktree=" + sandbox.TargetWorktree + ":rw",
		"scratch=" + sandbox.TargetScratch + ":rw",
		"cache=" + sandbox.TargetCache + ":rw",
		"git=" + sandbox.TargetGit + ":rw",
	}, ",")
	return []string{
		sandbox.TargetExecBin, "serve",
		"--protocol", execproto.Protocol,
		"--mode", string(execproto.ModeTool),
		"--rpc-fd", strconv.Itoa(FdRPC),
		"--roots", roots,
		"--allow-profiles", sandbox.TargetProfiles,
		"--git-dir", sandbox.TargetGit,
		"--session-branch", s.SessionBranch,
		"--base-commit", s.BaseCommit,
		"--hooks-dir", sandbox.TargetHooksDir,
		"--proxy", sandbox.TargetProxySocket,
		"--proxy-listen", sandbox.ForwarderAddr,
		"--child-limits", limits,
		"--max-procs", strconv.Itoa(x.MaxProcs),
	}
}
