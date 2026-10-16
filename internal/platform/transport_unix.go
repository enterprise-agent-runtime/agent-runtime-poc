//go:build !windows

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
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

// Listen binds the daemon socket (mode 0600 in a 0700 directory the current
// user owns, ensureSocketDir). A stale socket is removed only when nothing
// answers on it; a live one means another daemon is running. Connections
// from other users are dropped (newPeerListener).
func Listen(home string) (net.Listener, error) {
	p := Endpoint(home)
	if err := ensureSocketDir(filepath.Dir(p), os.Getuid()); err != nil {
		return nil, err
	}
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
	return newPeerListener(l, os.Getuid()), nil
}

// Dial connects to the daemon socket and, before returning the connection,
// verifies that it is the current user's (verifySocket): another local user
// can bind the path first where the directory allows it, and nothing, in
// particular not the token, may be written to a socket that fails the check.
func Dial(ctx context.Context, home string) (net.Conn, error) {
	return dialVerified(ctx, home, serverUID)
}

// peerUIDFunc reports the uid of the process serving c; known is false where
// the OS does not say (peer_*.go).
type peerUIDFunc func(c *net.UnixConn) (uid int, known bool, err error)

// dialVerified is Dial with the peer credential lookup injected for tests.
func dialVerified(ctx context.Context, home string, peer peerUIDFunc) (net.Conn, error) {
	p := Endpoint(home)
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", p)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) {
			return nil, fmt.Errorf("wardend is not running (%s): %w", p, err)
		}
		return nil, err
	}
	if err := verifySocket(c, p, peer); err != nil {
		c.Close()
		return nil, fmt.Errorf("refusing %s: %w", p, err)
	}
	return c, nil
}

// verifySocket gathers socketFacts for the connected socket at p and applies
// socketVerdict. A fact that cannot be read is a refusal.
func verifySocket(c net.Conn, p string, peer peerUIDFunc) error {
	si, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEndpointNotOwned, err)
	}
	di, err := os.Lstat(filepath.Dir(p))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEndpointNotOwned, err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("%w: %T is not a Unix socket", ErrEndpointNotOwned, c)
	}
	uid, known, err := peer(uc)
	if err != nil {
		return fmt.Errorf("%w: cannot read the peer credential: %v", ErrEndpointNotOwned, err)
	}
	return socketVerdict(socketFacts{
		SockUID: ownerUID(si), SockMode: si.Mode(),
		DirUID: ownerUID(di), DirMode: di.Mode(),
		PeerUID: uid, PeerKnown: known,
	}, os.Getuid())
}

// ownerUID is the file's owner, or -1 when the OS does not report one.
func ownerUID(fi os.FileInfo) int {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(st.Uid)
}

// socketDirHint is the way forward when the socket directory is refused:
// usually the long-path fallback /tmp/warden-<uid> already exists, created
// by someone else, in a shared /tmp (Endpoint).
const socketDirHint = "set TMPDIR to a private directory or use a shorter WARDEN_HOME"

// ensureSocketDir makes dir the daemon's own socket directory or refuses.
// An existing entry must be a real directory (Lstat: a symlink is refused,
// never followed) owned by uid; a missing one is created with Mkdir, and
// losing that race to another creator (EEXIST) leads to the same ownership
// check. The mode is then tightened to 0700, and a failed chmod is an error.
// Another user's directory is never used: whoever owns it controls the
// socket entry inside it (CLAUDE.md §4 IPC, pipe squatting).
func ensureSocketDir(dir string, uid int) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("socket directory %s is not a directory (%v); %s", dir, fi.Mode().Type(), socketDirHint)
	}
	if owner := ownerUID(fi); owner != uid {
		return fmt.Errorf("socket directory %s is not owned by the current user (owner uid %d); %s", dir, owner, socketDirHint)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("socket directory %s: %w", dir, err)
	}
	return nil
}

// newPeerListener drops connections whose peer uid, where the OS reports it
// (serverUID: SO_PEERCRED on Linux, LOCAL_PEERCRED on macOS), is not uid,
// and connections whose credential cannot be read (A05 §2.1).
func newPeerListener(l net.Listener, uid int) net.Listener { return &peerListener{l, uid} }

type peerListener struct {
	net.Listener
	uid int
}

func (p *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := p.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			return c, nil
		}
		if peer, known, err := serverUID(uc); err != nil || (known && peer != p.uid) {
			slog.Warn("rejected a connection from another user on the daemon socket")
			c.Close()
			continue
		}
		return c, nil
	}
}
