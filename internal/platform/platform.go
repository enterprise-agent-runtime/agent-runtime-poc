// Package platform confines every operating-system difference of the PoC
// (CLAUDE.md §3, §6; WRD-02 §3): the Warden home directory, the daemon
// transport (Unix socket on Linux/macOS, named pipe with an owner-only DACL
// on Windows), the L1 per-task proxy socket (design A07 §2, tasksock_*.go),
// owner-only files, the default sandbox level, detached daemon
// start, process-group termination and terminal input without echo.
// Nothing above this package (and internal/sandbox, internal/secrets) may
// branch on runtime.GOOS.
package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// EnvHome overrides the Warden home (tests and parallel CI, CLAUDE.md §8.4).
const EnvHome = "WARDEN_HOME"

// Home returns the Warden home: $WARDEN_HOME if set (must be absolute),
// else ~/.warden on Linux/macOS and %LOCALAPPDATA%\Warden on Windows.
func Home() (string, error) {
	if h := os.Getenv(EnvHome); h != "" {
		if !filepath.IsAbs(h) {
			return "", fmt.Errorf("%s must be an absolute path", EnvHome)
		}
		return filepath.Clean(h), nil
	}
	return DefaultHome()
}

// Layout is the directory layout under the home (design core §10).
type Layout struct{ Home string }

func (l Layout) path(p ...string) string { return filepath.Join(append([]string{l.Home}, p...)...) }

// Run holds the socket, token and pid file.
func (l Layout) Run() string { return l.path("run") }

// DB holds warden.sqlite.
func (l Layout) DB() string { return l.path("db") }

// Blobs is the content-addressed blob root.
func (l Layout) Blobs() string { return l.path("blobs") }

// Keys holds checkpoint.pub and archived keys.
func (l Layout) Keys() string { return l.path("keys") }

// Exports is the default audit export directory.
func (l Layout) Exports() string { return l.path("exports") }

// Logs holds wardend.log.
func (l Layout) Logs() string { return l.path("logs") }

// Cache holds probe caches and package caches.
func (l Layout) Cache() string { return l.path("cache") }

// Models is models.yaml.
func (l Layout) Models() string { return l.path("models.yaml") }

// Config is config.yaml.
func (l Layout) Config() string { return l.path("config.yaml") }

// Token is the API token file.
func (l Layout) Token() string { return l.path("run", "token") }

// Lock is the daemon lock file, held for the daemon's lifetime (LockDaemon).
func (l Layout) Lock() string { return l.path("run", "wardend.lock") }

// PID is the daemon pid file.
func (l Layout) PID() string { return l.path("run", "wardend.pid") }

// Ensure creates the home and its directories owner-only (design core §10;
// WH-1+2). The home and every directory it manages (run, db, blobs, keys,
// exports, logs, cache) get an explicit owner-only descriptor on Windows
// (0700 on Unix), inherited by what is created in them (D-052).
//
// An existing directory is re-permissioned only if it is a Warden home:
// the platform default home, one holding HomeMarker, or an empty one. Any
// other existing directory (WARDEN_HOME=D:\work, say) is refused, as is a
// home that is a symlink, junction or other reparse point: Ensure never
// silently takes over a directory it did not make. Ensure leaves the marker,
// owner-only, in every home it creates or accepts.
func (l Layout) Ensure() error {
	me, err := currentIdentity()
	if err != nil {
		return err
	}
	return l.ensure(me)
}

// ensure is Ensure for a given owner (the uid on Unix, the SID on Windows):
// a test seam, since a test cannot give a directory to another account.
func (l Layout) ensure(me identity) error {
	if err := ensureDir(l.Home, me, l.acceptHome); err != nil {
		return err
	}
	marker := filepath.Join(l.Home, HomeMarker)
	if _, err := os.Lstat(marker); errors.Is(err, fs.ErrNotExist) {
		if err := WriteOwnerOnly(marker, []byte("This directory is a Warden home.\n")); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// Every directory Ensure manages follows the home's rule (the home is
	// accepted by now): created owner-only, or an existing one checked for
	// links and ownership and made owner-only. keys/ holds the trusted
	// checkpoint keys, so a pre-planted loose keys/ must not survive (P9).
	for _, d := range []string{l.Run(), l.DB(), l.Blobs(), l.Keys(), l.Exports(), l.Logs(), l.Cache()} {
		if err := ensureDir(d, me, func(string, fs.FileInfo) error { return nil }); err != nil {
			return err
		}
	}
	return nil
}

// ensureDir creates dir owner-only, or makes an existing dir that accept
// allows owner-only. A symlink, junction or other reparse point, or a
// non-directory, is refused before accept is asked.
func ensureDir(dir string, me identity, accept func(string, fs.FileInfo) error) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		err = mkdirOwnerOnly(dir)
		if err == nil {
			return nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err = os.Lstat(dir) // created meanwhile: judged like any existing dir
	}
	if err != nil {
		return err
	}
	if isLink(fi) {
		return fmt.Errorf("%s is a symbolic link, junction or other reparse point; Warden's directories must be real directories", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", dir)
	}
	if err := accept(dir, fi); err != nil {
		return err
	}
	return protectDir(dir, me)
}

// acceptHome allows an existing home directory to be made owner-only: the
// platform default home, one holding HomeMarker, or an empty one.
func (l Layout) acceptHome(dir string, _ fs.FileInfo) error {
	if def, err := DefaultHome(); err == nil && sameHome(dir, def) {
		return nil
	}
	// A marker others can write proves nothing: it must pass ReadOwnerOnly.
	if _, err := ReadOwnerOnly(filepath.Join(dir, HomeMarker), 4<<10); err == nil {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("%s %s exists and is not a Warden home; use an empty or new directory", EnvHome, dir)
}

// CheckOwnerOnly returns an error unless path is owner-only (OwnerOnly).
// Code that reads the file uses ReadOwnerOnly instead, which decides on the
// handle it reads from (WH-1+2).
func CheckOwnerOnly(path string) error {
	ok, err := OwnerOnly(path)
	if err != nil {
		return fmt.Errorf("cannot check that %s is owner-only: %w", path, err)
	}
	if !ok {
		return notOwnerOnly(path, ownerOnlyDetail(path))
	}
	return nil
}

// errNotOwnerOnly is wrapped by every refusal of CheckOwnerOnly and
// ReadOwnerOnly.
var errNotOwnerOnly = errors.New("not owner-only")

func notOwnerOnly(path, detail string) error {
	return fmt.Errorf("%s is not owner-only (%s): another account could read or replace it; remove it and restart wardend: %w", path, detail, errNotOwnerOnly)
}

// readLimited reads at most max bytes and refuses a longer file.
func readLimited(r io.Reader, path string, max int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("%s is larger than the %d bytes expected of it", path, max)
	}
	return b, nil
}

// writeHooks lets tests choose the temp name and look at the temp file
// between its creation and the write. The zero value is production.
type writeHooks struct {
	tempName    func(path string) string
	afterCreate func(tmp string) error
}

// temp returns the temp file name for path: path, a dot, 16 random hex
// digits and ".tmp", in the same directory so the rename stays on one
// volume. It is unpredictable so another account cannot pre-create it.
func (h writeHooks) temp(path string) (string, error) {
	if h.tempName != nil {
		return h.tempName(path), nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return path + "." + hex.EncodeToString(b[:]) + ".tmp", nil
}

// writeSyncClose writes data to f, flushes it to disk and closes it.
func writeSyncClose(f *os.File, data []byte, h writeHooks) error {
	if h.afterCreate != nil {
		if err := h.afterCreate(f.Name()); err != nil {
			f.Close()
			return err
		}
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// OS names the running operating system for system.version.
func OS() string { return runtime.GOOS }

// DefaultSandboxLevel is L1 on Linux and macOS and L2 on Windows, where L2
// is the only level (CLAUDE.md §3–§4).
func DefaultSandboxLevel() string {
	if runtime.GOOS == "windows" {
		return "L2"
	}
	return "L1"
}

// SandboxLevels lists the levels supported on this OS.
func SandboxLevels() []string {
	if runtime.GOOS == "windows" {
		return []string{"L2"}
	}
	return []string{"L1", "L2"}
}

// Username returns the OS user name without a Windows domain prefix.
func Username() string {
	u, err := user.Current()
	if err != nil {
		return "unknown"
	}
	name := u.Username
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// LocalUser is the envelope user field, "local:<os-user>".
func LocalUser() string { return "local:" + Username() }

// ErrDaemonRunning is returned by Listen when another daemon owns the endpoint.
var ErrDaemonRunning = errors.New("another wardend is already running for this home")

// ExeName appends the executable suffix of this OS (".exe" on Windows).
func ExeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// Arch names the CPU architecture.
func Arch() string { return runtime.GOARCH }

// EndpointMissing reports a Dial failure that means no daemon: the socket
// or pipe does not exist, or a stale socket refuses the connection
// (WH-1+2: the CLI starts a daemon on this alone).
func EndpointMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

// HomeMarker is the file Ensure leaves in every Warden home it creates or
// accepts; an existing directory without it is not re-permissioned.
const HomeMarker = ".warden-home"

// errNotOwnerWritable is wrapped by every refusal of ReadOwnerWritable.
var errNotOwnerWritable = errors.New("not the user's own unmodifiable file")

// notOwnerWritable is a ReadOwnerWritable refusal: why, the file's owner and
// permissions, and how to fix it. It never says to remove the file: what it
// guards are verification keys, and removing one discards the anchor.
func notOwnerWritable(path, why, detail, fix string) error {
	return fmt.Errorf("%s %s (%s), so another account could have written it; %s: %w", path, why, detail, fix, errNotOwnerWritable)
}

// MoveAside renames a refused file at path to path.refused-<unix nanos> in
// the same directory, so a caller replacing it neither trusts nor destroys
// it (it may be a legitimate file with loose permissions; WH-4 round 1). The
// rename moves the entry itself, never a link's target. Afterwards the moved
// file is made owner-only when it is a regular file the user owns; that step
// is best effort and never follows a link. It returns the new name.
func MoveAside(path string) (string, error) {
	dst := path + ".refused-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	tightenMoved(dst)
	return dst, nil
}
