package oci

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"warden.dev/warden/internal/platform"
	"warden.dev/warden/internal/sandbox"
	"warden.dev/warden/internal/sandbox/seccomp"
)

const (
	executorProtocol = "warden.exec/1" // A06 §3.1 --protocol
	maxProcs         = 8               // A06 §3.1 --max-procs
	nofile           = 4096            // A06 §7.3, §15 --ulimit nofile
	mib              = 1 << 20

	// desktopUser runs both containers under Docker Desktop, whose engine
	// is rootful inside its VM: uid 0 there would be the VM's root, so the
	// container gets a fixed unprivileged uid (C-03 "non-root container
	// user"). The worktree is still writable, because Docker Desktop's
	// file sharing does not apply the container uid to host files.
	desktopUser = "65532:65532"
	// rootlessUser is A06 §15's --user 0:0: under a rootless engine the
	// container's uid 0 is the host user (no capabilities, cap-drop ALL),
	// so files in the worktree keep the user's ownership.
	rootlessUser = "0:0"

	// sandboxHostname is the A06 §11.1 --hostname.
	sandboxHostname = "warden"

	// emptyTmpfs is a read-only, empty tmpfs: the hooks directory and every
	// masked worktree directory (A06 §11.3, §15).
	emptyTmpfs = "ro,nosuid,nodev,noexec,size=1k"
)

// Labels (A06 §13.3, §13.5: teardown and orphan cleanup find containers
// and networks by them).
const (
	LabelSandbox = "warden.sandbox"
	LabelSession = "warden.session"
	LabelRole    = "warden.role"
	LabelHome    = "warden.home"
)

// Build turns a validated L2 Spec into a Plan (A06 §15 as amended by
// C-03 and C-24).
//
// The mounts are Spec.Mounts() and the environment Spec.Env(); Build adds
// only tmpfs /tmp, the empty /warden/empty and the rendering of masks.
// Before anything is built, every host bind source must already be its own
// final path (no junction, symbolic link, short name or other case), and
// the worktree must be safe to mask: the worktree and every directory
// leading to a mask case-sensitive, no 8.3 name creation on its volume, and
// no 8.3 alias on any element leading to a mask (PROGRESS M2-12, CWE-178,
// CWE-41). A failed host check is sandbox_unavailable; a Spec or Options
// error is ErrInvalidSpec.
func Build(s *sandbox.Spec, o Options) (*Plan, error) {
	if s.Level != sandbox.LevelL2 {
		return nil, fmt.Errorf("%w: level %q: the container backend builds L2 sandboxes only", sandbox.ErrInvalidSpec, s.Level)
	}
	mounts, err := s.Mounts()
	if err != nil {
		return nil, err
	}
	env, err := s.Env()
	if err != nil {
		return nil, err
	}
	user, err := o.users()
	if err != nil {
		return nil, err
	}
	for _, img := range []string{o.SandboxImage, o.ProxyImage} {
		if err := CheckImageRef(img); err != nil {
			return nil, err
		}
	}
	// validateProxy (D-056) has made Sidecar a plain host:port.
	sidecarHost, _, err := net.SplitHostPort(s.Proxy.Sidecar)
	if err != nil {
		return nil, fmt.Errorf("%w: proxy sidecar: %w", sandbox.ErrInvalidSpec, err)
	}
	if err := checkSidecarHost(sidecarHost); err != nil {
		return nil, err
	}
	if o.FS == nil {
		return nil, fmt.Errorf("%w: no host file system checks (Options.FS)", sandbox.ErrInvalidSpec)
	}

	var binds []Bind
	tmpfs := []Tmpfs{
		{Kind: "tmp", Target: sandbox.TargetTmp, Options: "rw,nosuid,nodev,size=" + strconv.FormatInt(int64(s.Limits.TmpMB)*mib, 10)},
		{Kind: "hooks_dir", Target: sandbox.TargetHooksDir, Options: emptyTmpfs},
	}
	for _, m := range mounts {
		switch {
		case m.Kind == sandbox.KindMask && m.Dir:
			tmpfs = append(tmpfs, Tmpfs{Kind: string(sandbox.KindMask), Target: m.Target, Options: emptyTmpfs})
		case m.Kind == sandbox.KindMask:
			binds = append(binds, Bind{Kind: m.Kind, Source: "/dev/null", EngineSource: true, Target: m.Target, ReadOnly: true})
		default:
			if m.Mode != sandbox.ModeRW && m.Mode != sandbox.ModeRO {
				return nil, fmt.Errorf("%w: %s mount %s has mode %q, which L2 cannot bind (C-03)", sandbox.ErrInvalidSpec, m.Kind, m.Target, m.Mode)
			}
			src, err := MapHostPath(o.HostOS, m.Source)
			if err != nil {
				return nil, err
			}
			if err := checkFinal(o, m.Source); err != nil {
				return nil, err
			}
			binds = append(binds, Bind{Kind: m.Kind, Source: src, Target: m.Target, ReadOnly: m.Mode == sandbox.ModeRO})
		}
	}
	if err := checkWorktree(s, o); err != nil {
		return nil, err
	}

	prof, err := seccomp.DockerProfile()
	if err != nil {
		return nil, fmt.Errorf("%w: seccomp profile: %w", sandbox.ErrInvalidSpec, err)
	}
	sum := sha256.Sum256(prof)
	art := Artefact{
		Path:   filepath.Join(s.SessionDir, "sandbox", s.ID, "seccomp.json"),
		SHA256: "sha256:" + hex.EncodeToString(sum[:]),
		Bytes:  prof,
	}

	internal := Network{Name: "warden-int-" + s.ID, Internal: true, Labels: labels(s, "internal_network")}
	egress := Network{Name: "warden-egress-" + s.ID, Internal: false, Labels: labels(s, "egress_network")}

	var envList []string
	for _, e := range env {
		envList = append(envList, e.Key+"="+e.Value)
	}
	fsize := int64(s.Limits.FsizeMB) * mib
	sb := Container{
		Name:       "warden-" + s.ID,
		Hostname:   sandboxHostname,
		Image:      o.SandboxImage,
		User:       user.sandbox,
		Entrypoint: []string{sandbox.TargetExecBin},
		Cmd:        executorArgs(s),
		Env:        envList,
		WorkingDir: sandbox.TargetWorktree,
		Labels:     labels(s, "sandbox"),

		ReadonlyRootfs:  true,
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
		Seccomp:         art.Path,

		Resources: Resources{
			MemoryBytes:     int64(s.Limits.MemoryMB) * mib,
			MemorySwapBytes: int64(s.Limits.MemoryMB) * mib, // equal to memory: no swap (A06 §12)
			NanoCPUs:        int64(s.Limits.CPUs) * 1e9,
			PidsLimit:       int64(s.Limits.Pids),
			Ulimits:         []Ulimit{{"nofile", nofile, nofile}, {"fsize", fsize, fsize}, {"core", 0, 0}},
		},
		Tmpfs:    tmpfs,
		Binds:    binds,
		Networks: []Attachment{{Network: internal.Name}},

		OpenStdin:  true, // the RPC channel is the container's stdin/stdout (--rpc-stdio)
		StdinOnce:  true, // the container ends when the daemon closes stdin
		AutoRemove: true,
	}
	// The sidecar's process, limits and command line are M2-20's; here it
	// gets its identity, its two networks and the same hardening.
	sc := Container{
		Name:            "warden-proxy-" + s.ID,
		Hostname:        sidecarHost,
		Image:           o.ProxyImage,
		User:            user.sidecar,
		Labels:          labels(s, "proxy"),
		ReadonlyRootfs:  true,
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
		Seccomp:         art.Path,
		Networks: []Attachment{
			{Network: internal.Name, Aliases: []string{sidecarHost}},
			{Network: egress.Name},
		},
		OpenStdin:  true, // events and approvals travel over its stdio (C-03)
		StdinOnce:  true,
		AutoRemove: true,
	}
	return &Plan{Internal: internal, Egress: egress, Sidecar: sc, Sandbox: sb, Seccomp: art}, nil
}

type users struct{ sandbox, sidecar string }

// users picks the container users for the engine. Rootless Docker exists
// only on a Linux host; Docker Desktop runs everywhere. A rootful native
// engine has no Engine value at all: M2-17 refuses it (A06 §15).
func (o Options) users() (users, error) {
	switch o.HostOS {
	case HostLinux, HostWindows, HostDarwin:
	default:
		return users{}, fmt.Errorf("%w: host OS %q has no L2 backend", sandbox.ErrInvalidSpec, o.HostOS)
	}
	switch {
	case o.Engine == EngineRootless && o.HostOS == HostLinux:
		return users{sandbox: rootlessUser, sidecar: desktopUser}, nil
	case o.Engine == EngineDesktop:
		return users{sandbox: desktopUser, sidecar: desktopUser}, nil
	}
	return users{}, fmt.Errorf("%w: engine %q on host %q is not a supported L2 engine", sandbox.ErrInvalidSpec, o.Engine, o.HostOS)
}

// labels identify every container and network of one sandbox. warden.home
// is the D-042 hash of the Warden home (platform.HomeHash), so the orphan
// sweep (A06 §13.5, M2-21) can match the name prefix, every label and its
// own home: two daemons with different homes sharing one engine never
// remove each other's live containers.
func labels(s *sandbox.Spec, role string) map[string]string {
	return map[string]string{
		LabelSandbox: s.ID, LabelSession: s.SessionID, LabelRole: role,
		LabelHome: platform.HomeHash(s.WardenHome),
	}
}

// checkSidecarHost refuses sidecar host names that would not reach the
// sidecar on the internal network: the sandbox's own hostname and
// localhost resolve inside the sandbox, so HTTP_PROXY would point back at
// it, and an IP literal pins an address the engine assigns (D-056 accepts
// both shapes; the L2 backend does not).
func checkSidecarHost(host string) error {
	switch {
	case host == sandboxHostname:
		return fmt.Errorf("%w: proxy sidecar host %q is the sandbox's own hostname", sandbox.ErrInvalidSpec, host)
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return fmt.Errorf("%w: proxy sidecar host %q is loopback", sandbox.ErrInvalidSpec, host)
	case net.ParseIP(host) != nil:
		return fmt.Errorf("%w: proxy sidecar host %q is an IP literal; use its name on the internal network", sandbox.ErrInvalidSpec, host)
	}
	return nil
}

// executorArgs is the A06 §3.1 / §11.1 command line in its L2 form: the RPC
// channel is container stdio (--rpc-stdio, A06 §15), and there is no
// --proxy or --proxy-listen, because on L2 the processes reach the sidecar
// directly through HTTP_PROXY (C-03) and no forwarder runs. The CPU rlimit
// is per call (4 x the call timeout, A06 §7.3), so --child-limits carries
// only the fixed limits.
func executorArgs(s *sandbox.Spec) []string {
	return []string{
		"serve",
		"--protocol", executorProtocol,
		"--mode", string(s.Mode),
		"--rpc-stdio",
		"--roots", "worktree=" + sandbox.TargetWorktree + ":rw,scratch=" + sandbox.TargetScratch + ":rw,cache=" + sandbox.TargetCache + ":rw,git=" + sandbox.TargetGit + ":rw",
		"--allow-profiles", sandbox.TargetProfiles,
		"--git-dir", sandbox.TargetGit,
		"--session-branch", s.SessionBranch,
		"--base-commit", s.BaseCommit,
		"--hooks-dir", sandbox.TargetHooksDir,
		"--child-limits", "fsize=" + strconv.FormatInt(int64(s.Limits.FsizeMB)*mib, 10) + ",nofile=" + strconv.Itoa(nofile) + ",core=0",
		"--max-procs", strconv.Itoa(maxProcs),
	}
}

func unavailable(code, format string, a ...any) error {
	return fmt.Errorf("%w: %s: %s", sandbox.ErrUnavailable, code, fmt.Sprintf(format, a...))
}

// checkFinal refuses a bind source that is not its own final path: a
// junction or symbolic link would bind a directory the Spec checks never
// saw, and a short or differently cased spelling would defeat the mapping
// rules (D-055 amendment: callers pass resolved long-form paths).
func checkFinal(o Options, src string) error {
	final, err := o.FS.FinalPath(src)
	if err != nil {
		return unavailable("bind_source_unresolvable", "%v", err)
	}
	if final != src {
		return unavailable("bind_source_not_final", "%q resolves to %q (a junction, link, short name or other case); pass the resolved long form", src, final)
	}
	return nil
}

// checkWorktree is the L2 must-fix of PROGRESS M2-12. A /dev/null or
// tmpfs mask covers one spelling of a path. Through Docker Desktop's file
// sharing a lookup inside the container is answered by the host file
// system, so on a case-insensitive directory ".ENV" opens the masked
// ".env", and on NTFS "ENV~1" opens it too. The case-sensitivity flag does
// not stop 8.3 aliasing (measured on NTFS for M2-18), so both are checked:
//   - the worktree and every directory on the way to a mask are
//     case-sensitive (lookups of any element below the worktree root are
//     answered by its parent directory);
//   - the worktree's volume does not create 8.3 names;
//   - no element from the worktree down to a masked entry has an 8.3 alias
//     already (names created before creation was turned off survive).
func checkWorktree(s *sandbox.Spec, o Options) error {
	host := func(rel string) string { return filepath.Join(s.Worktree, filepath.FromSlash(rel)) }
	dirs := []string{s.Worktree}
	var elems []string
	seenDir, seenElem := map[string]bool{s.Worktree: true}, map[string]bool{}
	for _, m := range s.WorktreeMasks {
		var chain []string
		for rel := m.RelPath; rel != "."; rel = path.Dir(rel) {
			chain = append([]string{rel}, chain...)
		}
		for i, rel := range chain {
			if !seenElem[rel] {
				seenElem[rel] = true
				elems = append(elems, host(rel))
			}
			if i < len(chain)-1 && !seenDir[rel] {
				seenDir[rel] = true
				dirs = append(dirs, host(rel))
			}
		}
	}
	for _, d := range dirs {
		cs, err := o.FS.CaseSensitiveDir(d)
		if err != nil {
			return unavailable("worktree_case_unknown", "%v", err)
		}
		if !cs {
			return unavailable("worktree_case_insensitive", "%q is case-insensitive, so a mask on .env would not cover .ENV; %s", d, caseHint(o.HostOS, s.Worktree))
		}
	}
	on, err := o.FS.ShortNameCreation(s.Worktree)
	if err != nil {
		return unavailable("short_names_unknown", "%v", err)
	}
	if on {
		return unavailable("short_names_enabled", "%s", shortNamesHint(s.Worktree))
	}
	for _, e := range elems {
		alias, err := o.FS.HasShortAlias(e)
		if err != nil {
			return unavailable("short_names_unknown", "%v", err)
		}
		if alias {
			return unavailable("short_name_alias", "%q has an 8.3 alias that would bypass its mask; `fsutil 8dot3name strip` (administrator) removes it", e)
		}
	}
	return nil
}

// shortNamesHint follows the owner's decision of 2026-10-16: the first
// remedy is to move the Warden home to a volume without 8.3 name creation
// (on the owner's machine D:, rather than changing C:); the second is an
// administrator turning creation off for the volume.
func shortNamesHint(worktree string) string {
	return fmt.Sprintf("the volume of %q creates 8.3 names, so a sandboxed process could open a masked .env as ENV~1, "+
		"or rename a directory to give it a new alias. %s", worktree, sandbox.ShortNamesFix)
}

func caseHint(hostOS, worktree string) string {
	switch hostOS {
	case HostWindows:
		return "the session must run `fsutil file setCaseSensitiveInfo " + worktree + " enable` on the empty worktree before checkout"
	case HostDarwin:
		return "the Warden home must be on a case-sensitive APFS volume"
	}
	return "the worktree must not be casefolded (chattr -F) or on a case-folding file system (vfat, exfat, ntfs3, SMB, WSL drvfs)"
}
