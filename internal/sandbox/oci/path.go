package oci

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"warden.dev/warden/internal/execproto/denylist"
	"warden.dev/warden/internal/sandbox"
)

// maxWindowsPath is MAX_PATH less the terminating NUL. Longer sources are
// refused rather than sent in a \\?\ form: one spelling, the one Docker
// Desktop's file sharing reads the same way we do.
const maxWindowsPath = 259

// windowsReserved are the DOS device names Win32 resolves in any directory
// and with any extension (CWE-67). The superscript digits ¹ ² ³ count as
// digits for COM and LPT; they are built numerically here.
var windowsReserved = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}
	digits := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", string(rune(0xB9)), string(rune(0xB2)), string(rune(0xB3))}
	for _, d := range digits {
		m["COM"+d] = true
		m["LPT"+d] = true
	}
	return m
}()

// MapHostPath returns p in the form the engine on hostOS accepts as a bind
// source (A06 §15 -v sources; the Engine API "Source" of a bind mount).
// Bind targets are always Linux paths and are not mapped.
//
// Linux and macOS: p is used as is. It must be absolute and clean, and
// contain no ':' or ',' (the separators of the -v and --mount forms).
//
// Windows (Docker Desktop): the plain drive form C:\dir\..., which Docker
// Desktop translates into its VM (WSL2 backend: /run/desktop/mnt/host/c/...).
// The drive letter must be upper case, the one spelling FinalPath returns
// (Build compares each source with it); nothing else is touched, because on a
// case-sensitive worktree the case is part of the name. Refused: UNC paths
// (\\server\share), the \\?\ long-path and \\.\ device prefixes (refused,
// not stripped: platform.HostFS.FinalPath already strips \\?\ for a drive
// path, so a prefixed input is a caller that skipped it), drive-relative
// and rooted-without-drive paths, forward slashes, empty, "." and ".."
// elements, a trailing separator, a drive root, elements ending in a dot
// or space (Win32 strips those, so they alias another name), 8.3
// short-name elements (the caller passes long-form paths, D-055
// amendment), alternate data streams, reserved device names, characters
// Win32 forbids in names, ',' and control bytes, and anything longer than
// MAX_PATH - 1. Junctions cannot be seen in a string; Build compares every
// source with HostFS.FinalPath for that.
func MapHostPath(hostOS, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%w: empty bind source", sandbox.ErrInvalidSpec)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: bind source %q contains a control character", sandbox.ErrInvalidSpec, p)
		}
	}
	switch hostOS {
	case HostLinux, HostDarwin:
		return slashSource(p)
	case HostWindows:
		return windowsSource(p)
	}
	return "", fmt.Errorf("%w: host OS %q has no L2 path mapping", sandbox.ErrInvalidSpec, hostOS)
}

func slashSource(p string) (string, error) {
	if !path.IsAbs(p) || path.Clean(p) != p || p == "/" || strings.ContainsAny(p, `:,\`) {
		return "", fmt.Errorf("%w: bind source %q is not an absolute, clean slash path without ':', ',' or '\\'", sandbox.ErrInvalidSpec, p)
	}
	return p, nil
}

func windowsSource(p string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%w: bind source %q: %s", sandbox.ErrInvalidSpec, p, why)
	}
	switch {
	case strings.HasPrefix(p, `\\?\`), strings.HasPrefix(p, `\??\`):
		return bad(`the \\?\ long-path form is refused; pass the plain drive path`)
	case strings.HasPrefix(p, `\\.\`):
		return bad("a device path is not a bind source")
	case strings.HasPrefix(p, `\\`), strings.HasPrefix(p, `//`):
		return bad("a UNC path (network share) is not a bind source")
	case strings.Contains(p, "/"):
		return bad("forward slashes; pass the clean backslash form")
	case len(p) > maxWindowsPath:
		return bad(fmt.Sprintf("longer than %d characters", maxWindowsPath))
	case len(p) < 3 || !isDriveLetter(p[0]) || p[1] != ':' || p[2] != '\\':
		return bad(`not an absolute drive path with an upper-case drive letter (C:\...)`)
	}
	rest := p[3:]
	if rest == "" {
		return bad("a drive root is never a bind source")
	}
	for _, el := range strings.Split(rest, `\`) {
		switch {
		case el == "":
			return bad("empty element or trailing separator")
		case el == "." || el == "..":
			return bad("a . or .. element")
		case strings.HasSuffix(el, ".") || strings.HasSuffix(el, " "):
			return bad("an element ending in a dot or space, which Win32 strips")
		case strings.ContainsAny(el, `<>:"|?*,`):
			return bad("a character Windows forbids in names, ':' (data streams) or ','")
		case denylist.IsShortName(el):
			return bad("an 8.3 short-name element; pass the long form (D-055)")
		}
		base := el
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		if windowsReserved[strings.ToUpper(strings.TrimRight(base, " "))] {
			return bad("a reserved device name")
		}
	}
	return p, nil
}

// isDriveLetter accepts upper case only: GetFinalPathNameByHandle returns
// the drive in upper case, so a lower-case source never passes Build.
func isDriveLetter(c byte) bool { return c >= 'A' && c <= 'Z' }

// reImageRef is a repository (optional registry host with port, then
// lower-case path components) pinned by a sha256 digest, with no tag: the
// image is the toolchain and the trusted base (A06 §15), so it is named by
// content only.
var reImageRef = regexp.MustCompile(`^(?:[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?/)?[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*@sha256:[0-9a-f]{64}$`)

// CheckImageRef accepts only an image pinned by digest
// (repository@sha256:<64 hex>), never a tag (A06 §15; CWE-494).
func CheckImageRef(ref string) error {
	if !reImageRef.MatchString(ref) {
		return fmt.Errorf("%w: image %q is not repository@sha256:<64 hex> (a tag is refused)", sandbox.ErrInvalidSpec, ref)
	}
	return nil
}
