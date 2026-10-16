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
