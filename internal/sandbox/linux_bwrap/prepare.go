package linux_bwrap

// Prepare is the one way to an argv: it resolves the toolchain roots,
// reads the host layout and checks the proxy socket, then builds. Every
// host observation goes through Probes, so each step is tested without the
// host and DefaultProbes (Linux) is the only code that touches it.

import (
	"errors"
	"fmt"
	"io/fs"

	"warden.dev/warden/internal/sandbox"
)

// Probes are the host observations Prepare needs.
type Probes struct {
	// EvalSymlinks resolves a toolchain root (filepath.EvalSymlinks).
	EvalSymlinks func(string) (string, error)
	// LstatSocket reports the file type and owner of a path without
	// following a final symlink (lstat(2)).
	LstatSocket func(string) (mode fs.FileMode, uid int, err error)
	// Lstat and Readlink classify the host's /bin, /lib, ... (ProbeHost).
	Lstat    func(string) (fs.FileInfo, error)
	Readlink func(string) (string, error)
	// UID is the daemon's uid; the proxy socket must be the user's own.
	UID int
}

// Prepare builds the bwrap plan for an L1 Spec: Spec.ResolveToolchains
// with p.EvalSymlinks (the Spec checks run again on the resolved roots),
// ProbeHost, then the proxy socket check immediately before the argv is
// built, so the socket bound is the one just checked (it is created by the
// proxy listener, A07 §2, and bwrap --bind would follow a link or bind
// anything else). A Spec that fails is ErrInvalidSpec; a host that cannot
// provide what the argv needs is ErrUnavailable.
func Prepare(s *sandbox.Spec, f Features, x ExecFlags, p Probes) (*Plan, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: no spec", sandbox.ErrInvalidSpec)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Level != sandbox.LevelL1 {
		return nil, fmt.Errorf("%w: level %s is not built with bwrap (A06 §11)", sandbox.ErrInvalidSpec, s.Level)
	}
	if p.EvalSymlinks == nil || p.LstatSocket == nil || p.Lstat == nil || p.Readlink == nil {
		return nil, fmt.Errorf("%w: linux_bwrap: probes missing", sandbox.ErrUnavailable)
	}
	sources, err := s.ResolveToolchains(p.EvalSymlinks)
	if err != nil {
		return nil, err
	}
	host, err := ProbeHost(p.Lstat, p.Readlink)
	if err != nil {
		return nil, err
	}
	if err := checkProxySocket(s.Proxy.Socket, p); err != nil {
		return nil, err
	}
	if sources == nil {
		sources = []string{}
	}
	return build(buildInput{Spec: s, Features: f, Host: host, ToolchainSources: sources, Exec: x})
}

// checkProxySocket requires the path itself (not a link to it) to be a
// Unix socket owned by uid.
func checkProxySocket(path string, p Probes) error {
	mode, uid, err := p.LstatSocket(path)
	if err != nil {
		return fmt.Errorf("%w: proxy socket %q: %w", sandbox.ErrUnavailable, path, err)
	}
	if mode.Type() != fs.ModeSocket {
		return fmt.Errorf("%w: proxy socket %q is not a socket (%s)", sandbox.ErrUnavailable, path, mode.Type())
	}
	if uid != p.UID {
		return fmt.Errorf("%w: proxy socket %q is owned by uid %d, not %d", sandbox.ErrUnavailable, path, uid, p.UID)
	}
	return nil
}

// ProbeHost classifies the merged-/usr directories of A06 §11.1. A missing
// directory or a real one is bound with --ro-bind-try; a symlink to usr/<d>
// (or /usr/<d>) becomes the same symlink in the sandbox. A symlink to
// anywhere else is ErrUnavailable: binding it would follow the link to a
// host path A06 does not list, and re-creating it would point the sandbox
// at nothing.
func ProbeHost(lstat func(string) (fs.FileInfo, error), readlink func(string) (string, error)) (HostLayout, error) {
	h := HostLayout{Linked: map[string]bool{}}
	for _, d := range mergedUsrDirs {
		p := "/" + d
		fi, err := lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return HostLayout{}, fmt.Errorf("%w: host %s: %w", sandbox.ErrUnavailable, p, err)
		case fi.Mode().Type() != fs.ModeSymlink:
			continue
		}
		t, err := readlink(p)
		if err != nil {
			return HostLayout{}, fmt.Errorf("%w: host %s: %w", sandbox.ErrUnavailable, p, err)
		}
		if t != "usr/"+d && t != "/usr/"+d {
			return HostLayout{}, fmt.Errorf("%w: host %s is a symlink to %q, not to usr/%s (A06 §11.1)", sandbox.ErrUnavailable, p, t, d)
		}
		h.Linked[d] = true
	}
	// /etc/ssl/private: masked when it is a directory. A link or a file
	// there cannot be covered by a tmpfs safely (a mount onto a link follows
	// it), so the layout is refused.
	switch fi, err := lstat(sslPrivate); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return HostLayout{}, fmt.Errorf("%w: host %s: %w", sandbox.ErrUnavailable, sslPrivate, err)
	case fi.Mode().Type() == fs.ModeDir:
		h.SSLPrivate = true
	default:
		return HostLayout{}, fmt.Errorf("%w: host %s is not a directory (%s); it cannot be masked", sandbox.ErrUnavailable, sslPrivate, fi.Mode().Type())
	}
	return h, nil
}
