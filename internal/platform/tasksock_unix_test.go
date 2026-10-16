//go:build linux || darwin

package platform

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The per-task proxy socket of design A07 §2 (M2-12a). The sandbox's
// forwarder reaches the daemon's egress proxy through S/scratch/px-<n>.sock;
// another local account that could connect to it would get the task's
// egress allowlist, or could hold its connection slots (CWE-276, CWE-668).
// Each test names the property it pins.

// taskTree is a Warden home with one session directory, all 0700, on a
// short path under /tmp (sun_path is 104 bytes on macOS).
type taskTree struct{ home, session, scratch, sock string }

func newTaskTree(t *testing.T) taskTree {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "px")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	home := filepath.Join(base, "wh")
	session := filepath.Join(home, "sessions", "01jaxr8q7m2v9ktc3f6yh5n0pb")
	scratch := filepath.Join(session, "scratch")
	for _, d := range []string{home, filepath.Join(home, "sessions"), session, scratch} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return taskTree{home: home, session: session, scratch: scratch, sock: filepath.Join(scratch, "px-1.sock")}
}

func defaultTaskSockOpts() taskSockOpts {
	return taskSockOpts{uid: os.Getuid(), peer: serverUID}
}

// TestTaskSocket_OwnerOnlyFromFirstConnectableMoment: A07 §2 binds and then
// chmods 0600, which alone leaves a window in which the socket has the
// umask's mode (0755 or 0775 with the usual umasks, and connect needs only
// write permission on the socket). The window is closed by binding only into
// a directory no other account can search: the hook runs between bind and
// chmod, the first moment the socket is connectable, and requires that
// directory to be 0700 and the user's, and every directory from the home
// down to be writable by nobody else (so it cannot be swapped). The result
// is a socket, the user's, 0600.
func TestTaskSocket_OwnerOnlyFromFirstConnectableMoment(t *testing.T) {
	tr := newTaskTree(t)
	o := defaultTaskSockOpts()
	hooked := false
	o.afterBind = func(p string) {
		hooked = true
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode().Type() != os.ModeSocket {
			t.Errorf("at bind: %s is %v (%v), want the bound socket", p, fi, err)
		}
		di, err := os.Lstat(filepath.Dir(p))
		if err != nil {
			t.Fatal(err)
		}
		if di.Mode().Perm() != 0o700 || ownerUID(di) != os.Getuid() || !di.IsDir() {
			t.Errorf("at bind: socket directory is %v owned by %d; another account could reach the socket", di.Mode(), ownerUID(di))
		}
		for d := filepath.Dir(filepath.Dir(p)); ; d = filepath.Dir(d) {
			fi, err := os.Lstat(d)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm()&0o022 != 0 || ownerUID(fi) != os.Getuid() {
				t.Errorf("at bind: ancestor %s is %v owned by %d; it could be swapped", d, fi.Mode(), ownerUID(fi))
			}
			if d == tr.home {
				break
			}
		}
	}
	s, err := listenTaskSocket(tr.home, tr.sock, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	if !hooked {
		t.Fatal("the bind hook did not run")
	}
	fi, err := os.Lstat(tr.sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Type() != os.ModeSocket || fi.Mode().Perm() != 0o600 || ownerUID(fi) != os.Getuid() {
		t.Fatalf("socket is %v owned by %d, want a 0600 socket owned by %d", fi.Mode(), ownerUID(fi), os.Getuid())
	}
	if s.Path() != tr.sock {
		t.Errorf("Path() = %q, want %q", s.Path(), tr.sock)
	}
}

// TestTaskSocket_CreatesMissingScratch0700: the scratch directory may not
// exist yet when the first sandbox of a session is created; it is created
// owner-only, never with the umask's mode.
func TestTaskSocket_CreatesMissingScratch0700(t *testing.T) {
	tr := newTaskTree(t)
	if err := os.Remove(tr.scratch); err != nil {
		t.Fatal(err)
	}
	s, err := listenTaskSocket(tr.home, tr.sock, defaultTaskSockOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	assertMode(t, tr.scratch, 0o700)
}

// TestTaskSocket_RefusesUnsafeDirectories: a socket directory another
// account can search, an ancestor another account can write (it could
// rename the directory away and put its own in place between the check and
// the bind), a symlinked directory, and a directory of another account are
// all refused before bind, and nothing is created or re-permissioned.
func TestTaskSocket_RefusesUnsafeDirectories(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, tr taskTree) (check string, mode os.FileMode)
		opts  func(o *taskSockOpts)
		want  string
	}{
		{"scratch searchable by others", func(t *testing.T, tr taskTree) (string, os.FileMode) {
			chmod(t, tr.scratch, 0o711)
			return tr.scratch, 0o711
		}, nil, "socket directory"},
		{"session writable by group", func(t *testing.T, tr taskTree) (string, os.FileMode) {
			chmod(t, tr.session, 0o770)
			return tr.session, 0o770
		}, nil, "writable by another account"},
		{"home writable by others", func(t *testing.T, tr taskTree) (string, os.FileMode) {
			chmod(t, tr.home, 0o703)
			return tr.home, 0o703
		}, nil, "writable by another account"},
		{"scratch is a symlink", func(t *testing.T, tr taskTree) (string, os.FileMode) {
			if err := os.Remove(tr.scratch); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(tr.session, "elsewhere")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, tr.scratch); err != nil {
				t.Fatal(err)
			}
			return target, 0o700
		}, nil, "not a directory"},
		{"session is a symlink", func(t *testing.T, tr taskTree) (string, os.FileMode) {
			target := filepath.Join(tr.home, "other")
			if err := os.Rename(tr.session, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, tr.session); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(target, "scratch"), 0o700
		}, nil, "not a directory"},
		// A second account is not available to a non-root test, so the
		// expected uid is the seam: every directory then belongs to
		// "another account".
		{"directories of another account", func(t *testing.T, tr taskTree) (string, os.FileMode) {
			return tr.scratch, 0o700
		}, func(o *taskSockOpts) { o.uid = os.Getuid() + 1 }, "not owned by the current user"},
		// The owner seam makes one directory another account's, so each
		// owner check is tested on its own.
		{"scratch of another account", func(t *testing.T, tr taskTree) (string, os.FileMode) {
			return tr.scratch, 0o700
		}, func(o *taskSockOpts) { o.owner = foreignNamed("scratch") }, "socket directory"},
		{"session of another account", func(t *testing.T, tr taskTree) (string, os.FileMode) {
			return tr.scratch, 0o700
		}, func(o *taskSockOpts) { o.owner = foreignNamed("01jaxr8q7m2v9ktc3f6yh5n0pb") }, "not owned by the current user"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := newTaskTree(t)
			check, mode := c.setup(t, tr)
			o := defaultTaskSockOpts()
			o.afterBind = func(string) { t.Error("bound a socket in an unsafe directory") }
			if c.opts != nil {
				c.opts(&o)
			}
			s, err := listenTaskSocket(tr.home, tr.sock, o)
			if err == nil {
				s.Remove()
				t.Fatal("listen succeeded")
			}
			if !errors.Is(err, ErrTaskSocketRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want ErrTaskSocketRefused naming %q", err, c.want)
			}
			assertMode(t, check, mode)
			if _, err := os.Lstat(filepath.Join(check, "px-1.sock")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a socket entry exists in %s: %v", check, err)
			}
		})
	}
}

// TestTaskSocket_RefusesExistingPath: an entry at the socket path, of any
// type and whoever made it, is refused and left exactly as it was. Binding
// over it is not possible anyway (EADDRINUSE), but removing it first, as the
// daemon socket does for a stale socket, would delete whatever a link
// points at or another task's live socket (CWE-59, CWE-367). Stale sockets
// of earlier runs are removed only by the startup sweep. The owner of the
// entry is not read: anything that exists is refused, so the case of a file
// of another account is the same branch.
func TestTaskSocket_RefusesExistingPath(t *testing.T) {
	cases := []struct {
		name  string
		make  func(t *testing.T, tr taskTree)
		check func(t *testing.T, tr taskTree)
	}{
		{"regular file", func(t *testing.T, tr taskTree) {
			if err := os.WriteFile(tr.sock, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, tr taskTree) {
			if b, err := os.ReadFile(tr.sock); err != nil || string(b) != "keep" {
				t.Errorf("file now %q, %v", b, err)
			}
		}},
		{"symlink to a file", func(t *testing.T, tr taskTree) {
			target := filepath.Join(tr.session, "victim")
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, tr.sock); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, tr taskTree) {
			if l, err := os.Readlink(tr.sock); err != nil || l != filepath.Join(tr.session, "victim") {
				t.Errorf("link now %q, %v", l, err)
			}
			if b, err := os.ReadFile(filepath.Join(tr.session, "victim")); err != nil || string(b) != "keep" {
				t.Errorf("link target now %q, %v", b, err)
			}
		}},
		{"dangling symlink", func(t *testing.T, tr taskTree) {
			if err := os.Symlink(filepath.Join(tr.session, "nothing"), tr.sock); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, tr taskTree) {
			if _, err := os.Readlink(tr.sock); err != nil {
				t.Errorf("link gone: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(tr.session, "nothing")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("something was created behind the link: %v", err)
			}
		}},
		{"FIFO", func(t *testing.T, tr taskTree) {
			if err := syscall.Mkfifo(tr.sock, 0o600); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, tr taskTree) {
			if fi, err := os.Lstat(tr.sock); err != nil || fi.Mode().Type() != os.ModeNamedPipe {
				t.Errorf("FIFO now %v, %v", fi, err)
			}
		}},
		{"directory", func(t *testing.T, tr taskTree) {
			if err := os.Mkdir(tr.sock, 0o700); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, tr taskTree) {
			if fi, err := os.Lstat(tr.sock); err != nil || !fi.IsDir() {
				t.Errorf("directory now %v, %v", fi, err)
			}
		}},
		{"socket of an earlier run", func(t *testing.T, tr taskTree) {
			staleSocket(t, tr.sock)
		}, func(t *testing.T, tr taskTree) {
			if fi, err := os.Lstat(tr.sock); err != nil || fi.Mode().Type() != os.ModeSocket {
				t.Errorf("socket now %v, %v", fi, err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := newTaskTree(t)
			c.make(t, tr)
			before, err := os.Lstat(tr.sock)
			if err != nil {
				t.Fatal(err)
			}
			o := defaultTaskSockOpts()
			o.afterBind = func(string) { t.Error("bound over an existing entry") }
			s, err := listenTaskSocket(tr.home, tr.sock, o)
			if err == nil {
				s.Remove()
				t.Fatal("listen succeeded over an existing entry")
			}
			if !errors.Is(err, ErrTaskSocketRefused) || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("err = %v, want ErrTaskSocketRefused (already exists)", err)
			}
			after, err := os.Lstat(tr.sock)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("entry changed: before %v, after %v (%v)", before.Mode(), after, err)
			}
			c.check(t, tr)
		})
	}
}

// TestTaskSocket_RefusesBadPaths: the path is S/scratch/px-<n>.sock inside
// the given home (D-056; the sweep removes only that name, so a socket
// under any other name would outlive a crash), and short enough for
// sun_path.
func TestTaskSocket_RefusesBadPaths(t *testing.T) {
	tr := newTaskTree(t)
	// A session directory long enough to push a well-formed path past the
	// limit, so that row is refused for its length alone.
	long := filepath.Join(tr.home, "sessions", strings.Repeat("0", 80), "scratch")
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	const layout, length, abs = "is not <warden home>/sessions/<session>/scratch/px-<n>.sock", "socket path limit", "must be absolute"
	for _, c := range []struct{ p, want string }{
		{filepath.Join(tr.scratch, "proxy.sock"), layout},
		{filepath.Join(tr.scratch, "px-1.sock.tmp"), layout},
		{filepath.Join(tr.scratch, "px-1234567890.sock"), layout},
		{"px-1.sock", abs},
		{filepath.Join(filepath.Dir(tr.home), "px-1.sock"), layout},
		{filepath.Join(tr.home, "px-1.sock"), layout},
		{filepath.Join(long, "px-1.sock"), length},
		{filepath.Join(tr.home, "sessions", "px-1.sock"), layout},
		{filepath.Join(tr.home, "other", "s", "scratch", "px-1.sock"), layout},
	} {
		p := c.p
		s, err := listenTaskSocket(tr.home, p, defaultTaskSockOpts())
		if err == nil {
			s.Remove()
			t.Errorf("%s: listen succeeded", p)
			continue
		}
		if !errors.Is(err, ErrTaskSocketRefused) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrTaskSocketRefused naming %q", p, err, c.want)
		}
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: created (%v)", p, err)
		}
	}
}

// TestTaskSocket_RefusesChangesDuringBind: what is checked before bind is
// checked again after it. If the socket directory was replaced while
// binding (the bind then went somewhere unchecked), or the entry at the
// path is not the user's socket, the listener is closed and refused, and
// an entry that is not the one bound is left in place.
func TestTaskSocket_RefusesChangesDuringBind(t *testing.T) {
	cases := []struct {
		name  string
		swap  func(t *testing.T, tr taskTree)
		want  string
		check func(t *testing.T, tr taskTree)
	}{
		{"socket directory replaced", func(t *testing.T, tr taskTree) {
			if err := os.Rename(tr.scratch, filepath.Join(tr.session, "moved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(tr.scratch, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "replaced during bind", func(t *testing.T, tr taskTree) {
			// The bound socket moved with its directory and can no longer
			// be found by path; what matters is that nothing listens on it.
			if c, err := net.Dial("unix", filepath.Join(tr.session, "moved", "px-1.sock")); err == nil {
				c.Close()
				t.Error("the listener bound in the moved directory still accepts")
			}
		}},
		// Review of 2aa3972 (CWE-367): the entry at the path was recorded as
		// "ours" before the directory identity was checked, so a refusal
		// after a swap unlinked whatever socket of the user now sat at the
		// path, such as another session's live px-1.sock (on Linux that
		// detaches the bind in the other sandbox). After a directory swap
		// nothing may be removed by path.
		{"directory swapped for one holding another socket", func(t *testing.T, tr taskTree) {
			if err := os.Rename(tr.scratch, filepath.Join(tr.session, "moved")); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(tr.session, "other")
			if err := os.Mkdir(other, 0o700); err != nil {
				t.Fatal(err)
			}
			l, err := net.Listen("unix", filepath.Join(other, "px-1.sock"))
			if err != nil {
				t.Fatal(err)
			}
			l.(*net.UnixListener).SetUnlinkOnClose(false)
			t.Cleanup(func() { l.Close() })
			if err := os.Rename(other, tr.scratch); err != nil {
				t.Fatal(err)
			}
		}, "replaced during bind", func(t *testing.T, tr taskTree) {
			fi, err := os.Lstat(tr.sock)
			if err != nil || fi.Mode().Type() != os.ModeSocket {
				t.Fatalf("the other socket at the path was removed: %v, %v", fi, err)
			}
			c, err := net.Dial("unix", tr.sock)
			if err != nil {
				t.Fatalf("the other socket no longer accepts: %v", err)
			}
			c.Close()
		}},
		{"socket directory loosened", func(t *testing.T, tr taskTree) {
			chmod(t, tr.scratch, 0o755)
		}, "not owner-only", func(t *testing.T, tr taskTree) {
			if _, err := os.Lstat(tr.sock); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the refused socket was not removed: %v", err)
			}
		}},
		{"socket replaced by a file", func(t *testing.T, tr taskTree) {
			if err := os.Remove(tr.sock); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tr.sock, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "not a socket", func(t *testing.T, tr taskTree) {
			if b, err := os.ReadFile(tr.sock); err != nil || string(b) != "keep" {
				t.Errorf("the swapped-in file did not survive: %q, %v", b, err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := newTaskTree(t)
			o := defaultTaskSockOpts()
			o.afterBind = func(string) { c.swap(t, tr) }
			s, err := listenTaskSocket(tr.home, tr.sock, o)
			if err == nil {
				s.Remove()
				t.Fatal("listen succeeded")
			}
			if !errors.Is(err, ErrTaskSocketRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want ErrTaskSocketRefused naming %q", err, c.want)
			}
			c.check(t, tr)
		})
	}
}

// TestTaskSocket_RemoveDeletesOnlyOurSocket: at destroy the socket is
// removed only if the entry at the path is still the one this listener
// bound (same device and inode). Something put there since, here a file
// and another socket, survives and the refusal is reported (CWE-367).
func TestTaskSocket_RemoveDeletesOnlyOurSocket(t *testing.T) {
	t.Run("ours", func(t *testing.T) {
		tr := newTaskTree(t)
		s, err := listenTaskSocket(tr.home, tr.sock, defaultTaskSockOpts())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Remove(); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
		if _, err := os.Lstat(tr.sock); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("socket still there: %v", err)
		}
		if err := s.Remove(); err != nil {
			t.Fatalf("second Remove() = %v, want nil", err)
		}
	})
	swaps := map[string]func(t *testing.T, p string){
		"file": func(t *testing.T, p string) {
			if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"socket": func(t *testing.T, p string) { staleSocket(t, p) },
	}
	for name, swap := range swaps {
		t.Run("swapped "+name, func(t *testing.T) {
			tr := newTaskTree(t)
			s, err := listenTaskSocket(tr.home, tr.sock, defaultTaskSockOpts())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(tr.sock); err != nil {
				t.Fatal(err)
			}
			swap(t, tr.sock)
			before, err := os.Lstat(tr.sock)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Remove(); err == nil || !strings.Contains(err.Error(), "left in place") {
				t.Fatalf("Remove() = %v, want a refusal to remove a replaced entry", err)
			}
			after, err := os.Lstat(tr.sock)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("the swapped-in %s did not survive: %v", name, err)
			}
		})
	}
}

// TestTaskSocket_CloseKeepsTheFile: Close (called by proxy.Serve when its
// context ends) stops accepting but does not unlink; only Remove does,
// after its identity check. net.UnixListener unlinks its path on Close by
// default, which would delete whatever is at the path by then.
func TestTaskSocket_CloseKeepsTheFile(t *testing.T) {
	tr := newTaskTree(t)
	s, err := listenTaskSocket(tr.home, tr.sock, defaultTaskSockOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	if err := os.Remove(tr.sock); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tr.sock, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(tr.sock); err != nil || string(b) != "keep" {
		t.Fatalf("Close removed or changed the entry at the path: %q, %v", b, err)
	}
	if _, err := s.Accept(); err == nil {
		t.Fatal("Accept after Close succeeded")
	}
}

// TestTaskSocket_RefusesPeerOfAnotherUid: a connection whose peer uid
// (SO_PEERCRED, LOCAL_PEERCRED) is not the daemon's, or whose credential
// cannot be read or is not reported, is closed before a byte is sent and
// never returned by Accept (A07 §2). A second account is not available to a
// non-root test, so the peer-credential function is the seam, as in
// TestPeerChecked_RejectsOtherUser; the real function is exercised by the
// same-uid case.
func TestTaskSocket_RefusesPeerOfAnotherUid(t *testing.T) {
	peers := map[string]peerUIDFunc{
		"other uid":  func(*net.UnixConn) (int, bool, error) { return os.Getuid() + 1, true, nil },
		"root":       func(*net.UnixConn) (int, bool, error) { return 0, true, nil },
		"unreadable": func(*net.UnixConn) (int, bool, error) { return os.Getuid(), true, errors.New("EINVAL") },
		"unknown":    func(*net.UnixConn) (int, bool, error) { return os.Getuid(), false, nil },
	}
	if os.Getuid() == 0 {
		delete(peers, "root")
	}
	for name, peer := range peers {
		t.Run(name, func(t *testing.T) {
			tr := newTaskTree(t)
			o := defaultTaskSockOpts()
			o.peer = peer
			s, err := listenTaskSocket(tr.home, tr.sock, o)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Remove()
			accepted := make(chan net.Conn, 1)
			go func() {
				if c, err := s.Accept(); err == nil {
					accepted <- c
				}
				close(accepted)
			}()
			c, err := net.Dial("unix", tr.sock)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			// The deadline only stops a hung test; nothing asserts on time.
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			n, err := c.Read(make([]byte, 1))
			if n != 0 || err != io.EOF {
				t.Fatalf("read = %d, %v; want 0 bytes and EOF", n, err)
			}
			s.Close()
			if c, ok := <-accepted; ok {
				c.Close()
				t.Fatal("Accept returned a refused connection")
			}
		})
	}
	t.Run("same uid", func(t *testing.T) {
		tr := newTaskTree(t)
		s, err := listenTaskSocket(tr.home, tr.sock, defaultTaskSockOpts())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Remove()
		go func() {
			if c, err := net.Dial("unix", tr.sock); err == nil {
				_, _ = c.Write([]byte("x"))
				_, _ = c.Read(make([]byte, 1))
				c.Close()
			}
		}()
		c, err := s.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		b := make([]byte, 1)
		if _, err := io.ReadFull(c, b); err != nil || b[0] != 'x' {
			t.Fatalf("read %q, %v", b, err)
		}
	})
}

// TestSweepTaskSockets: at daemon start the sockets of earlier runs are
// removed (A07 §2 startup cleanup), but only px-<n>.sock entries that are
// sockets of the user in a real <home>/sessions/<s>/scratch directory of
// the user. A link (even to one of the user's sockets), a regular file, a
// FIFO, another name, a symlinked session directory and everything of
// another account are left alone (CWE-59).
func TestSweepTaskSockets(t *testing.T) {
	tr := newTaskTree(t)
	staleSocket(t, filepath.Join(tr.scratch, "px-1.sock"))
	staleSocket(t, filepath.Join(tr.scratch, "px-22.sock"))
	elsewhere := filepath.Join(tr.home, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	staleSocket(t, filepath.Join(elsewhere, "real.sock"))
	if err := os.Symlink(filepath.Join(elsewhere, "real.sock"), filepath.Join(tr.scratch, "px-2.sock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tr.scratch, "px-3.sock"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(tr.scratch, "px-4.sock"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleSocket(t, filepath.Join(tr.scratch, "hook-1.sock"))
	// A socket of another account in the user's own scratch directory
	// (owner seam: foreignNamed).
	staleSocket(t, filepath.Join(tr.scratch, "px-7.sock"))
	if err := os.Mkdir(filepath.Join(tr.scratch, "implement"), 0o700); err != nil {
		t.Fatal(err)
	}
	staleSocket(t, filepath.Join(tr.scratch, "implement", "px-5.sock"))
	// A session directory that is a link to a scratch tree elsewhere.
	other := filepath.Join(elsewhere, "s2")
	if err := os.MkdirAll(filepath.Join(other, "scratch"), 0o700); err != nil {
		t.Fatal(err)
	}
	staleSocket(t, filepath.Join(other, "scratch", "px-1.sock"))
	if err := os.Symlink(other, filepath.Join(tr.home, "sessions", "01jaxr8q7m2v9ktc3f6yh5n0pc")); err != nil {
		t.Fatal(err)
	}
	// A session whose scratch is a link.
	s3 := filepath.Join(tr.home, "sessions", "01jaxr8q7m2v9ktc3f6yh5n0pd")
	if err := os.Mkdir(s3, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "scratch"), filepath.Join(s3, "scratch")); err != nil {
		t.Fatal(err)
	}

	kept := []string{
		filepath.Join(tr.scratch, "px-2.sock"),
		filepath.Join(elsewhere, "real.sock"),
		filepath.Join(tr.scratch, "px-3.sock"),
		filepath.Join(tr.scratch, "px-4.sock"),
		filepath.Join(tr.scratch, "hook-1.sock"),
		filepath.Join(tr.scratch, "px-7.sock"),
		filepath.Join(tr.scratch, "implement", "px-5.sock"),
		filepath.Join(other, "scratch", "px-1.sock"),
	}
	removable := []string{filepath.Join(tr.scratch, "px-1.sock"), filepath.Join(tr.scratch, "px-22.sock")}

	// Another account's tree (the uid is the seam): nothing is removed.
	removed, err := sweepTaskSockets(tr.home, taskSockOpts{uid: os.Getuid() + 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("swept another account's tree: %v", removed)
	}
	for _, p := range append(kept, removable...) {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("%s gone after a sweep for another uid: %v", p, err)
		}
	}

	removed, err = sweepTaskSockets(tr.home, taskSockOpts{uid: os.Getuid(), owner: foreignNamed("px-7.sock")})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(removed)
	if !slices.Equal(removed, removable) {
		t.Fatalf("removed %v, want %v", removed, removable)
	}
	for _, p := range removable {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s not removed: %v", p, err)
		}
	}
	for _, p := range kept {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(tr.scratch, "px-3.sock")); err != nil || string(b) != "keep" {
		t.Errorf("regular file changed: %q, %v", b, err)
	}
}

// TestSweepTaskSockets_SymlinkedSessionsDir: a sessions/ that is a link is
// not followed, so sockets in the tree it points at survive.
func TestSweepTaskSockets_SymlinkedSessionsDir(t *testing.T) {
	tr := newTaskTree(t)
	staleSocket(t, tr.sock)
	moved := filepath.Join(tr.home, "real-sessions")
	if err := os.Rename(filepath.Join(tr.home, "sessions"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, filepath.Join(tr.home, "sessions")); err != nil {
		t.Fatal(err)
	}
	removed, err := SweepTaskSockets(tr.home)
	if err != nil || len(removed) != 0 {
		t.Fatalf("SweepTaskSockets = %v, %v; want nothing", removed, err)
	}
	if _, err := os.Lstat(filepath.Join(moved, filepath.Base(tr.session), "scratch", "px-1.sock")); err != nil {
		t.Fatalf("socket behind the link removed: %v", err)
	}
}

// TestSweepTaskSockets_SymlinkedHome: a home that is a link is not swept
// (Ensure refuses such a home too, D-052).
func TestSweepTaskSockets_SymlinkedHome(t *testing.T) {
	tr := newTaskTree(t)
	staleSocket(t, tr.sock)
	link := filepath.Join(filepath.Dir(tr.home), "link")
	if err := os.Symlink(tr.home, link); err != nil {
		t.Fatal(err)
	}
	removed, err := SweepTaskSockets(link)
	if err != nil || len(removed) != 0 {
		t.Fatalf("SweepTaskSockets = %v, %v; want nothing", removed, err)
	}
	if _, err := os.Lstat(tr.sock); err != nil {
		t.Fatalf("socket behind the link removed: %v", err)
	}
}

// foreignNamed is an owner seam: entries with this base name belong to
// "another account", the rest to their real owner.
func foreignNamed(name string) func(os.FileInfo) int {
	return func(fi os.FileInfo) int {
		if fi.Name() == name {
			return os.Getuid() + 1
		}
		return ownerUID(fi)
	}
}

// TestSweepTaskSockets_NoSessions: a home without sessions/ is not an error.
func TestSweepTaskSockets_NoSessions(t *testing.T) {
	removed, err := SweepTaskSockets(t.TempDir())
	if err != nil || len(removed) != 0 {
		t.Fatalf("SweepTaskSockets = %v, %v; want nothing", removed, err)
	}
}

// staleSocket leaves a socket file at p, as a crashed daemon would.
func staleSocket(t *testing.T, p string) {
	t.Helper()
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
}

func chmod(t *testing.T, p string, m os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, m); err != nil {
		t.Fatal(err)
	}
}

// TestPeerRefusal_NamesTheReason: Accept logs why it refused a peer
// (review of 2aa3972: every refusal was logged as "another user", which
// misleads whoever debugs an unreadable or unreported credential). The
// decision and its reason come from one pure function.
func TestPeerRefusal_NamesTheReason(t *testing.T) {
	me := os.Getuid()
	cases := []struct {
		name  string
		uid   int
		known bool
		err   error
		want  string
	}{
		{"same uid", me, true, nil, ""},
		{"another uid", me + 1, true, nil, "peer is another user"},
		{"unreadable", me, true, errors.New("EINVAL"), "peer credential unreadable"},
		{"not reported", me, false, nil, "peer credential not reported"},
	}
	for _, c := range cases {
		if got := peerRefusal(c.uid, c.known, c.err, me); got != c.want {
			t.Errorf("%s: peerRefusal = %q, want %q", c.name, got, c.want)
		}
	}
}
