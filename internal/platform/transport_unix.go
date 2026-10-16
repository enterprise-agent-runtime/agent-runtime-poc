//go:build !windows

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// maxSocketPath keeps below sun_path (104 bytes on macOS, 108 on Linux).
const maxSocketPath = 100

// Endpoint returns the socket path for a home: <home>/run/wardend.sock, or a
// hashed path in a private temp directory when that would be too long
// (macOS test temp dirs).
func Endpoint(home string) string {
	p := filepath.Join(home, "run", "wardend.sock")
	if len(p) < maxSocketPath {
		return p
	}
	h := sha256.Sum256([]byte(filepath.Clean(home)))
	return filepath.Join(os.TempDir(), "warden-"+strconv.Itoa(os.Getuid()), hex.EncodeToString(h[:])[:16]+".sock")
}

// Listen binds the daemon socket (mode 0600 in a 0700 directory). A stale
// socket is removed only when nothing answers on it; a live one means
// another daemon is running.
func Listen(home string) (net.Listener, error) {
	p := Endpoint(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(p), 0o700)
	if fi, err := os.Lstat(p); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlinked socket path %s", p)
		}
		if c, err := net.DialTimeout("unix", p, 500*time.Millisecond); err == nil {
			c.Close()
			return nil, ErrDaemonRunning
		}
		if err := os.Remove(p); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return peerChecked(l), nil
}

// Dial connects to the daemon socket.
func Dial(ctx context.Context, home string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", Endpoint(home))
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) {
			return nil, fmt.Errorf("wardend is not running (%s): %w", Endpoint(home), err)
		}
		return nil, err
	}
	return c, nil
}
