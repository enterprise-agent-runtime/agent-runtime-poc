//go:build !windows

package platform

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The Unix half of the pipe-squatting fix (security review of FX-3;
// CLAUDE.md §4 IPC): Dial checks the socket it reached before handing the
// connection to the client that will send the token. Each test serves the
// socket with platform.Listen, loosens one property, and requires Dial to
// refuse without a byte reaching the server.

func TestDial_RefusesSocketMode0644(t *testing.T) {
	h, l := listenShort(t)
	if err := os.Chmod(Endpoint(h), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRefused(t, l, func() (net.Conn, error) { return Dial(unixCtx(t), h) })
}

func TestDial_RefusesSocketDir0755(t *testing.T) {
	h, l := listenShort(t)
	if err := os.Chmod(filepath.Dir(Endpoint(h)), 0o755); err != nil {
		t.Fatal(err)
	}
	assertRefused(t, l, func() (net.Conn, error) { return Dial(unixCtx(t), h) })
}

// TestDial_RefusesPeerOfAnotherUser injects the peer credential: a test
// cannot run a server as another uid, so the function the OS check feeds
// is replaced by one reporting a different uid.
func TestDial_RefusesPeerOfAnotherUser(t *testing.T) {
	h, l := listenShort(t)
	other := func(*net.UnixConn) (int, bool, error) { return os.Getuid() + 1, true, nil }
	assertRefused(t, l, func() (net.Conn, error) { return dialVerified(unixCtx(t), h, other) })
}

func TestDial_AcceptsOwnSocket(t *testing.T) {
	h, l := listenShort(t)
	go drain(l, nil)
	c, err := Dial(unixCtx(t), h)
	if err != nil {
		t.Fatalf("Dial refused the socket Listen created: %v", err)
	}
	c.Close()
}

// TestServerUID_ReportsThePeer: on Linux (SO_PEERCRED) and macOS
// (LOCAL_PEERCRED) the real credential function must report the serving
// process, which in this test is the test itself.
func TestServerUID_ReportsThePeer(t *testing.T) {
	h, l := listenShort(t)
	go drain(l, nil)
	var d net.Dialer
	c, err := d.DialContext(unixCtx(t), "unix", Endpoint(h))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uid, known, err := serverUID(c.(*net.UnixConn))
	if err != nil {
		t.Fatal(err)
	}
	if (runtime.GOOS == "linux" || runtime.GOOS == "darwin") && !known {
		t.Fatal("peer uid not reported")
	}
	if known && uid != os.Getuid() {
		t.Fatalf("peer uid = %d, want %d", uid, os.Getuid())
	}
}

// listenShort serves platform.Listen on a home short enough that the socket
// sits in <home>/run rather than the shared per-user temp directory, so
// loosening its directory cannot disturb other tests.
func listenShort(t *testing.T) (string, net.Listener) {
	t.Helper()
	h, err := os.MkdirTemp("/tmp", "wh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(h) })
	t.Setenv(EnvHome, h)
	if Endpoint(h) != filepath.Join(h, "run", "wardend.sock") {
		t.Fatalf("endpoint %s is not under the home", Endpoint(h))
	}
	l, err := Listen(h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return h, l
}

func assertRefused(t *testing.T, l net.Listener, dial func() (net.Conn, error)) {
	t.Helper()
	got := make(chan int, 1)
	go drain(l, got)
	c, err := dial()
	if err == nil {
		c.Write([]byte("token"))
		c.Close()
		t.Fatal("Dial accepted the endpoint")
	}
	if !errors.Is(err, ErrEndpointNotOwned) {
		t.Fatalf("err = %v, want ErrEndpointNotOwned", err)
	}
	if n := <-got; n != 0 {
		t.Fatalf("%d bytes reached the server", n)
	}
}

func drain(l net.Listener, got chan<- int) {
	c, err := l.Accept()
	if err != nil {
		if got != nil {
			got <- 0
		}
		return
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := io.Copy(io.Discard, c)
	if got != nil {
		got <- int(n)
	}
}

func unixCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The daemon half of the same squatting class: Listen used to MkdirAll the
// socket directory and ignore the chmod (`_ = os.Chmod`), so a directory
// another user had created first (the long-path fallback lives in a shared
// /tmp as /tmp/warden-<uid> on Linux) or a symlink in its place was used as
// is, and the daemon bound its socket where that user controls the entry.
// ensureSocketDir takes the uid to compare against, which is the seam for
// "owned by someone else" without a real chown.

func TestEnsureSocketDir_CreatesMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if err := ensureSocketDir(dir, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, 0o700)
}

func TestEnsureSocketDir_TightensExistingMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureSocketDir(dir, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, 0o700)
}

func TestEnsureSocketDir_RefusesSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "run")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if err := ensureSocketDir(dir, os.Getuid()); err == nil || !strings.Contains(err.Error(), "socket directory") || !strings.Contains(err.Error(), "set TMPDIR to a private directory or use a shorter WARDEN_HOME") {
		t.Fatalf("err = %v, want a socket directory refusal with the way forward", err)
	}
	assertMode(t, target, 0o755) // the symlink's target was not touched
}

func TestEnsureSocketDir_RefusesOtherOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := ensureSocketDir(dir, os.Getuid()+1)
	if err == nil || !strings.Contains(err.Error(), "is not owned by the current user") || !strings.Contains(err.Error(), "set TMPDIR to a private directory or use a shorter WARDEN_HOME") {
		t.Fatalf("err = %v, want an ownership refusal with the way forward", err)
	}
	assertMode(t, dir, 0o755) // someone else's directory is not chmodded
}

// TestListen_RefusesSymlinkedSocketDir: the check is wired into Listen.
func TestListen_RefusesSymlinkedSocketDir(t *testing.T) {
	h, err := os.MkdirTemp("/tmp", "wh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(h) })
	t.Setenv(EnvHome, h)
	target := filepath.Join(h, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(h, "run")); err != nil {
		t.Fatal(err)
	}
	if l, err := Listen(h); err == nil {
		l.Close()
		t.Fatal("Listen bound a socket in a symlinked directory")
	}
	if _, err := os.Lstat(filepath.Join(target, "wardend.sock")); err == nil {
		t.Fatal("socket created behind the symlink")
	}
}

// TestPeerChecked_RejectsOtherUser: the daemon drops a connection whose
// peer uid differs from its own. Linux had this (SO_PEERCRED); macOS used a
// no-op and now reads LOCAL_PEERCRED through the same serverUID. The uid
// the listener compares against is the seam: a real other-user client is
// not available in a test.
func TestPeerChecked_RejectsOtherUser(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("peer credentials are read on Linux and macOS only")
	}
	l := rawUnixListener(t)
	pl := newPeerListener(l, os.Getuid()+1)
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := pl.Accept(); err == nil {
			accepted <- c
		}
		close(accepted)
	}()
	var d net.Dialer
	c, err := d.DialContext(unixCtx(t), "unix", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read = %v, want EOF: the connection was not dropped", err)
	}
	l.Close()
	if c, ok := <-accepted; ok {
		c.Close()
		t.Fatal("Accept returned a connection from another uid")
	}
}

func TestPeerChecked_AcceptsSameUser(t *testing.T) {
	l := rawUnixListener(t)
	pl := newPeerListener(l, os.Getuid())
	go func() {
		var d net.Dialer
		if c, err := d.DialContext(context.Background(), "unix", l.Addr().String()); err == nil {
			defer c.Close()
			time.Sleep(time.Second)
		}
	}()
	c, err := pl.Accept()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func rawUnixListener(t *testing.T) net.Listener {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "wh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	l, err := net.Listen("unix", filepath.Join(d, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func assertMode(t *testing.T, p string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != want {
		t.Fatalf("%s mode = %#o, want %#o", p, fi.Mode().Perm(), want)
	}
}
