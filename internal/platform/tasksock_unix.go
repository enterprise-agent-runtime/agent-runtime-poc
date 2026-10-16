//go:build linux || darwin

package platform

// The per-task Unix socket of the L1 egress proxy (design A07 §2, A06 §2:
// S/scratch/px-<n>.sock, bind-mounted into the sandbox at
// /run/warden/proxy.sock). L1 exists only on Linux and macOS; on other
// systems ListenTaskSocket returns ErrTaskSocketUnsupported
// (tasksock_other.go).
//
// Owner-only from the first moment. A07 §2 binds and then chmods the socket
// 0600. Between the two the socket has the umask's mode (typically 0755),
// and connect(2) needs only write permission on the socket, so the chmod
// alone leaves a window. It is closed by binding only into a directory no
// other account can search: the socket directory must be the user's with no
// group or other bits, and every directory from the Warden home down to it
// must be the user's and writable by nobody else, so that it cannot be
// renamed away and replaced between the check and the bind. Each one is
// checked with Lstat, so a link is refused, never followed. The socket
// directory is checked again after the bind (same inode, still owner-only),
// and the socket itself must then be a socket of the user's, 0600. The
// process umask is not touched: it is process-wide, and every other
// goroutine of the daemon would create files under it.
//
// INV-J: the socket is opened with a literal net.Listen("unix", ...);
// nothing here binds a raw socket.

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// reTaskSocket is the socket's base name (A07 §2; the same pattern as the
// sandbox Spec's, D-056). Only this name is opened and only this name is
// swept, so a socket can never outlive a crash under another name.
var reTaskSocket = regexp.MustCompile(`^px-[0-9]{1,9}\.sock$`)

// taskSockOpts are the seams of listenTaskSocket and sweepTaskSockets: the
// uid the files and the peers must belong to, the owner lookup (a test
// cannot chown without root, so it makes chosen entries "another
// account's" here), the peer-credential lookup, and a hook that runs
// between bind and chmod (tests only).
type taskSockOpts struct {
	uid       int
	owner     func(os.FileInfo) int // nil: ownerUID
	peer      peerUIDFunc
	afterBind func(path string)
}

func (o taskSockOpts) ownerOf(fi os.FileInfo) int {
	if o.owner != nil {
		return o.owner(fi)
	}
	return ownerUID(fi)
}

// TaskSocket is one sandbox's proxy listener. It is a net.Listener whose
// Accept returns only connections from processes of the daemon's uid; the
// others are closed before a byte is sent (A07 §2).
//
// User namespaces: bwrap runs the sandbox in a user namespace whose uid maps
// to the daemon's uid outside. The kernel reports SO_PEERCRED translated
// into the namespace of the reader, and the daemon is in the initial one, so
// the forwarder in the sandbox is seen as the daemon's own uid and passes.
// The check therefore keeps out other accounts on the host; it does not tell
// one sandbox from another or from other processes of the same user. That
// separation comes from the mount namespace: each sandbox has only its own
// socket bound in, and S/scratch itself is not mounted (A06 §2). On macOS
// (Seatbelt, no user namespace) the sandboxed processes simply run as the
// user.
type TaskSocket struct {
	ln   *net.UnixListener
	path string
	fi   os.FileInfo // the socket as bound: Remove compares the entry with it
	uid  int
	peer peerUIDFunc

	closeOnce  sync.Once
	closeErr   error
	removeOnce sync.Once
	removeErr  error
}

// ListenTaskSocket opens the per-task socket at path, which must be
// <root>/sessions/<session>/scratch/px-<n>.sock, root being the Warden home.
// The scratch directory is created 0700 if it is missing. Anything already
// at path, of any type, is refused and left as it is; stale sockets of
// earlier runs are removed only by SweepTaskSockets at daemon start.
func ListenTaskSocket(root, path string) (*TaskSocket, error) {
	return listenTaskSocket(root, path, taskSockOpts{uid: os.Getuid(), peer: serverUID})
}

func refused(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrTaskSocketRefused, fmt.Sprintf(format, a...))
}

func listenTaskSocket(root, path string, o taskSockOpts) (*TaskSocket, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return nil, refused("%q and %q must be absolute", root, path)
	}
	root, path = filepath.Clean(root), filepath.Clean(path)
	dir := filepath.Dir(path)
	if !reTaskSocket.MatchString(filepath.Base(path)) || filepath.Base(dir) != "scratch" ||
		filepath.Dir(filepath.Dir(filepath.Dir(dir))) != root || filepath.Base(filepath.Dir(filepath.Dir(dir))) != "sessions" {
		return nil, refused("%s is not <warden home>/sessions/<session>/scratch/px-<n>.sock (A07 §2)", path)
	}
	// sun_path is 104 bytes on macOS, 108 on Linux. A07 §2's fallback to
	// $TMPDIR/warden-<uid> is not taken: the Spec pins this path (D-056).
	if len(path) >= maxSocketPath {
		return nil, refused("%s is %d bytes, over the %d-byte socket path limit; use a shorter WARDEN_HOME", path, len(path), maxSocketPath)
	}
	// The home, sessions/ and the session: the user's, writable by nobody
	// else, not links.
	session := filepath.Dir(dir)
	for _, d := range []string{root, filepath.Dir(session), session} {
		if err := o.checkAncestor(d); err != nil {
			return nil, err
		}
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("%w: %v", ErrTaskSocketRefused, err)
	}
	di, err := o.checkSocketDir(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, refused("%s already exists; it is left as it is", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %v", ErrTaskSocketRefused, err)
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTaskSocketRefused, err)
	}
	ln := l.(*net.UnixListener)
	// Close must never unlink: by then the path may hold something else.
	// Remove deletes the entry only after comparing it with the socket.
	ln.SetUnlinkOnClose(false)
	s := &TaskSocket{ln: ln, path: path, uid: o.uid, peer: o.peer}
	if o.afterBind != nil {
		o.afterBind(path)
	}
	fail := func(err error) (*TaskSocket, error) {
		_ = ln.Close()
		if s.fi != nil {
			_ = s.removeIfOurs()
		}
		return nil, err
	}
	// Re-check after bind. First the directory must still be the one
	// checked before (same inode, Lstat on both sides of reading the
	// entry); only then is the entry at the path recorded as ours, so that
	// a refusal removes it. After a swap the path leads somewhere else, and
	// nothing is removed by path: the listener is closed and its file, in
	// the moved directory, is left dead (review of 2aa3972, CWE-367: the
	// path could hold another session's live socket).
	sameDir := func() bool {
		d, err := os.Lstat(dir)
		return err == nil && os.SameFile(di, d)
	}
	if !sameDir() {
		return fail(refused("socket directory %s was replaced during bind", dir))
	}
	fi, ferr := os.Lstat(path)
	if !sameDir() {
		return fail(refused("socket directory %s was replaced during bind", dir))
	}
	if ferr == nil && fi.Mode().Type() == os.ModeSocket && o.ownerOf(fi) == o.uid {
		s.fi = fi
	}
	// The same directory, still owner-only and the user's.
	if _, err := o.checkSocketDir(dir); err != nil {
		return fail(err)
	}
	if ferr != nil {
		return fail(fmt.Errorf("%w: %v", ErrTaskSocketRefused, ferr))
	}
	if s.fi == nil {
		return fail(refused("%s is %v owned by uid %d after bind, not a socket of uid %d", path, fi.Mode().Type(), o.ownerOf(fi), o.uid))
	}
	// chmod follows a link; only the user can change entries in the
	// owner-only directory, so a link here would be the user's own doing,
	// and the Lstat below refuses the result.
	if err := os.Chmod(path, 0o600); err != nil {
		return fail(fmt.Errorf("%w: %v", ErrTaskSocketRefused, err))
	}
	fi2, err := os.Lstat(path)
	if err != nil {
		return fail(fmt.Errorf("%w: %v", ErrTaskSocketRefused, err))
	}
	if !os.SameFile(s.fi, fi2) || fi2.Mode().Type() != os.ModeSocket || fi2.Mode().Perm() != 0o600 || o.ownerOf(fi2) != o.uid {
		return fail(refused("%s is %v owned by uid %d after chmod, want this socket, 0600", path, fi2.Mode(), o.ownerOf(fi2)))
	}
	return s, nil
}

// checkAncestor: a real directory of uid that no other account can write.
func (o taskSockOpts) checkAncestor(d string) error {
	fi, err := os.Lstat(d)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTaskSocketRefused, err)
	}
	if !fi.IsDir() {
		return refused("%s is not a directory (%v)", d, fi.Mode().Type())
	}
	if owner := o.ownerOf(fi); owner != o.uid {
		return refused("%s is not owned by the current user (owner uid %d)", d, owner)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return refused("%s is writable by another account (%v)", d, fi.Mode().Perm())
	}
	return nil
}

// checkSocketDir: a real directory of uid with no group or other bits, so
// no other account can search it and reach the socket inside.
func (o taskSockOpts) checkSocketDir(d string) (os.FileInfo, error) {
	fi, err := os.Lstat(d)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTaskSocketRefused, err)
	}
	if !fi.IsDir() {
		return nil, refused("socket directory %s is not a directory (%v)", d, fi.Mode().Type())
	}
	if owner := o.ownerOf(fi); owner != o.uid {
		return nil, refused("socket directory %s is not owned by the current user (owner uid %d)", d, owner)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, refused("socket directory %s is %v, not owner-only", d, fi.Mode().Perm())
	}
	return fi, nil
}

// Path is the socket's host path.
func (s *TaskSocket) Path() string { return s.path }

// Addr is the listener's address.
func (s *TaskSocket) Addr() net.Addr { return s.ln.Addr() }

// Accept returns the next connection whose peer runs as the daemon's uid.
// A connection of another uid, or one whose credential cannot be read or is
// not reported, is closed at once, before a byte is sent, and is not
// returned. No event is written (A07 §2).
func (s *TaskSocket) Accept() (net.Conn, error) {
	for {
		c, err := s.ln.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, known, err := s.peer(c)
		if reason := peerRefusal(uid, known, err, s.uid); reason != "" {
			slog.Warn("refused a connection on a proxy socket", "path", s.path, "reason", reason)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

// peerRefusal is why Accept refuses a peer, or "" to accept it. Each
// refusal names its actual cause, so the log does not blame "another user"
// for a credential that could not be read.
func peerRefusal(uid int, known bool, err error, want int) string {
	switch {
	case err != nil:
		return "peer credential unreadable"
	case !known:
		return "peer credential not reported"
	case uid != want:
		return "peer is another user"
	}
	return ""
}

// Close stops accepting. It does not remove the socket file; Remove does.
// It is safe to call more than once (proxy.Serve closes the listener when
// its context ends).
func (s *TaskSocket) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.ln.Close() })
	return s.closeErr
}

// Remove closes the listener and deletes the socket file, but only if the
// entry at the path is still the socket this listener bound (same device
// and inode, CWE-367). Anything else found there is left in place and
// reported. A missing entry is not an error.
func (s *TaskSocket) Remove() error {
	_ = s.Close()
	s.removeOnce.Do(func() { s.removeErr = s.removeIfOurs() })
	return s.removeErr
}

func (s *TaskSocket) removeIfOurs() error {
	fi, err := os.Lstat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(fi, s.fi) || fi.Mode().Type() != os.ModeSocket {
		return fmt.Errorf("%s is no longer this proxy's socket; left in place", s.path)
	}
	return os.Remove(s.path)
}

// SweepTaskSockets removes the per-task sockets earlier runs left under
// <root>/sessions/*/scratch/ (A07 §2 startup cleanup) and returns their
// paths. It must run only once this daemon holds the home's endpoint
// (Listen, D-053), before any sandbox is created: from then on every such
// socket belongs to this daemon. Only entries named px-<n>.sock that are
// sockets owned by the user are removed, and only in session and scratch
// directories that are real directories of the user writable by no one
// else. Links, other file types, other names and anything of another
// account are left alone (never followed). Per-entry failures are joined
// into the error; the sweep goes on.
func SweepTaskSockets(root string) ([]string, error) {
	return sweepTaskSockets(root, taskSockOpts{uid: os.Getuid()})
}

func sweepTaskSockets(root string, o taskSockOpts) ([]string, error) {
	sessions := filepath.Join(root, "sessions")
	ents, err := os.ReadDir(sessions)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if o.checkAncestor(root) != nil || o.checkAncestor(sessions) != nil {
		return nil, nil
	}
	var removed []string
	var errs []error
	for _, e := range ents {
		s := filepath.Join(sessions, e.Name())
		scratch := filepath.Join(s, "scratch")
		if o.checkAncestor(s) != nil || o.checkAncestor(scratch) != nil {
			continue
		}
		socks, err := os.ReadDir(scratch)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, se := range socks {
			if !reTaskSocket.MatchString(se.Name()) {
				continue
			}
			p := filepath.Join(scratch, se.Name())
			fi, err := os.Lstat(p)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if fi.Mode().Type() != os.ModeSocket || o.ownerOf(fi) != o.uid {
				continue
			}
			if err := os.Remove(p); err != nil {
				errs = append(errs, err)
				continue
			}
			removed = append(removed, p)
		}
	}
	return removed, errors.Join(errs...)
}
