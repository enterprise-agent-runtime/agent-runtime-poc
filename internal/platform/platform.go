// Package platform confines every operating-system difference of the PoC
// (CLAUDE.md §3, §6; WRD-02 §3): the Warden home directory, the daemon
// transport (Unix socket on Linux/macOS, named pipe with an owner-only DACL
// on Windows), owner-only files, the default sandbox level, detached daemon
// start, process-group termination and terminal input without echo.
// Nothing above this package (and internal/sandbox, internal/secrets) may
// branch on runtime.GOOS.
package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
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

// PID is the daemon pid file.
func (l Layout) PID() string { return l.path("run", "wardend.pid") }

// Ensure creates the home and its directories owner-only. The home and run
// directories get an explicit owner-only descriptor on Windows, inherited by
// what is created in them (WH-1+2); the others are 0700 and, on Windows,
// inherit the home's.
func (l Layout) Ensure() error {
	for _, d := range []string{l.Home, l.Run()} {
		if err := ensureOwnerOnlyDir(d); err != nil {
			return err
		}
	}
	for _, d := range []string{l.DB(), l.Blobs(), l.Keys(), l.Exports(), l.Logs(), l.Cache()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// CheckOwnerOnly returns an error unless path is owner-only (OwnerOnly):
// wardend checks the token file right after writing it and refuses to serve,
// and the client checks it before reading the token (WH-1+2, security review
// of FX-3).
func CheckOwnerOnly(path string) error {
	ok, err := OwnerOnly(path)
	if err != nil {
		return fmt.Errorf("cannot check that %s is owner-only: %w", path, err)
	}
	if !ok {
		return fmt.Errorf("%s is not owner-only (%s): another account could read or replace it; remove it and restart wardend", path, ownerOnlyDetail(path))
	}
	return nil
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
