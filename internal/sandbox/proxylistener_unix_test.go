//go:build linux || darwin

package sandbox

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"warden.dev/warden/internal/proxy"
)

// The L1 proxy listener end to end (design A07 §2, M2-12a): the Spec's
// socket path, opened owner-only by internal/platform, served by a real
// internal/proxy TaskProxy, reached by a client over the real Unix socket as
// the in-sandbox forwarder would. The upstream is in memory: the proxy's
// Dialer is injected, so no TCP socket is opened anywhere (INV-J).

// diskLayout is the test layout on disk: the Warden home and the session
// directory exist, 0700, on a short path (sun_path).
func diskLayout(t *testing.T) testLayout {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "sbpx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	l := layoutIn(base, true)
	for _, d := range []string{l.home, filepath.Join(l.home, "sessions"), l.session} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// recSink records events. When entered and release are set, ProxyConnect
// signals entered and then waits for release, so a test can hold an event
// write in progress.
type recSink struct {
	mu       sync.Mutex
	connects []proxy.ConnectEvent
	denies   []proxy.DeniedEvent

	entered, release chan struct{}
}

func (s *recSink) ProxyConnect(ctx context.Context, ev proxy.ConnectEvent) error {
	if s.entered != nil {
		close(s.entered)
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err // like the store: an event with a dead context is lost
	}
	s.connects = append(s.connects, ev)
	return nil
}

func (s *recSink) ProxyDenied(ctx context.Context, ev proxy.DeniedEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.denies = append(s.denies, ev)
	return nil
}

func (s *recSink) snapshot() ([]proxy.ConnectEvent, []proxy.DeniedEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]proxy.ConnectEvent(nil), s.connects...), append([]proxy.DeniedEvent(nil), s.denies...)
}

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := r[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// pipeDialer connects every dial to upstream over net.Pipe.
type pipeDialer struct {
	mu       sync.Mutex
	dialed   []string
	upstream func(net.Conn)
}

func (d *pipeDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, network+" "+address)
	d.mu.Unlock()
	near, far := net.Pipe()
	go d.upstream(far)
	return near, nil
}

// clientHello is the first record crypto/tls sends for ServerName sni.
func clientHello(t *testing.T, sni string) []byte {
	t.Helper()
	c, s := net.Pipe()
	defer s.Close()
	go func() {
		_ = tls.Client(c, &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12}).Handshake()
		c.Close()
	}()
	br := bufio.NewReader(s)
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(br, hdr); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, int(hdr[3])<<8|int(hdr[4]))
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatal(err)
	}
	return append(hdr, body...)
}

type proxyFixture struct {
	spec *Spec
	sink *recSink
	dial *pipeDialer
	cfg  proxy.Config
}

func newProxyFixture(t *testing.T, allow ...string) proxyFixture {
	t.Helper()
	l := diskLayout(t)
	var entries []proxy.Entry
	for _, k := range allow {
		entries = append(entries, proxy.Entry{Key: k, Source: proxy.SourceCapability})
	}
	al, err := proxy.NewAllowlist(entries...)
	if err != nil {
		t.Fatal(err)
	}
	f := proxyFixture{spec: validSpec(l, LevelL1), sink: &recSink{}, dial: &pipeDialer{}}
	f.cfg = proxy.Config{
		Allow: al, Events: f.sink, Dialer: f.dial,
		Resolver:       staticResolver{"registry.npmjs.org": {netip.MustParseAddr("104.16.1.34")}},
		InterfaceAddrs: func() ([]netip.Addr, error) { return nil, nil },
		// Decider nil: StaticDecider, deny everything not on the allowlist (D-058).
	}
	return f
}

// connect sends a CONNECT over the real socket and returns the response.
func connect(t *testing.T, path, target string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	// Only stops a hung test; nothing asserts on time.
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, br, resp
}

// TestOpenProxy_ConnectThroughTheSocket: an allowlisted CONNECT through the
// socket reaches the (in-memory) upstream at the checked address, bytes
// flow both ways and one proxy.connect is written with the Spec's sandbox
// id; an unlisted host is refused 403 with proxy.denied (StaticDecider).
// Close then leaves no socket behind.
func TestOpenProxy_ConnectThroughTheSocket(t *testing.T) {
	f := newProxyFixture(t, "registry.npmjs.org:443")
	hello := clientHello(t, "registry.npmjs.org")
	f.dial.upstream = func(c net.Conn) {
		defer c.Close()
		buf := make([]byte, len(hello)+4)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf[len(hello):]) != "ping" {
			t.Errorf("upstream read %q, %v", buf, err)
			return
		}
		_, _ = c.Write([]byte("pong"))
	}
	pl, err := OpenProxy(context.Background(), f.spec, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Path() != f.spec.Proxy.Socket {
		t.Fatalf("Path() = %q, want the Spec's %q", pl.Path(), f.spec.Proxy.Socket)
	}
	fi, err := os.Lstat(f.spec.Proxy.Socket)
	if err != nil || fi.Mode().Type() != os.ModeSocket || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v (%v), want a 0600 socket", fi, err)
	}

	c, br, resp := connect(t, f.spec.Proxy.Socket, "registry.npmjs.org:443")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status %d, want 200", resp.StatusCode)
	}
	if _, err := c.Write(append(hello, "ping"...)); err != nil {
		t.Fatal(err)
	}
	pong := make([]byte, 4)
	if _, err := io.ReadFull(br, pong); err != nil || string(pong) != "pong" {
		t.Fatalf("client read %q, %v; want pong", pong, err)
	}
	c.Close()

	_, _, resp = connect(t, f.spec.Proxy.Socket, "evil.example:443")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unlisted CONNECT status %d, want 403", resp.StatusCode)
	}

	if err := pl.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if _, err := os.Lstat(f.spec.Proxy.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left after Close: %v", err)
	}
	conns, denies := f.sink.snapshot()
	if len(conns) != 1 || conns[0].SandboxID != f.spec.ID || conns[0].Host != "registry.npmjs.org" || conns[0].IP != "104.16.1.34" {
		t.Errorf("proxy.connect events %+v, want one for registry.npmjs.org from %s", conns, f.spec.ID)
	}
	if len(denies) != 1 || denies[0].Host != "evil.example" || denies[0].SandboxID != f.spec.ID {
		t.Errorf("proxy.denied events %+v, want one for evil.example", denies)
	}
	if f.dial.dialed[0] != "tcp 104.16.1.34:443" || len(f.dial.dialed) != 1 {
		t.Errorf("dialed %v, want only the checked address", f.dial.dialed)
	}
}

// TestOpenProxy_CloseFlushesBeforeReturning: Close during an open tunnel
// returns only after the proxy has ended the tunnel and written its
// proxy.connect, so every proxy event of a sandbox is written before its
// sandbox.destroy (A07 §2 Close, A06 §13.3 step 4). The socket is gone.
func TestOpenProxy_CloseFlushesBeforeReturning(t *testing.T) {
	f := newProxyFixture(t, "registry.npmjs.org:443")
	f.sink.entered, f.sink.release = make(chan struct{}), make(chan struct{})
	hello := clientHello(t, "registry.npmjs.org")
	upstreamIn := make(chan struct{})
	f.dial.upstream = func(c net.Conn) {
		defer c.Close()
		buf := make([]byte, len(hello))
		if _, err := io.ReadFull(c, buf); err == nil {
			close(upstreamIn)
		}
		_, _ = io.Copy(io.Discard, c) // hold the tunnel open until it is closed
	}
	pl, err := OpenProxy(context.Background(), f.spec, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, _, resp := connect(t, f.spec.Proxy.Socket, "registry.npmjs.org:443")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status %d, want 200", resp.StatusCode)
	}
	if _, err := c.Write(hello); err != nil {
		t.Fatal(err)
	}
	<-upstreamIn
	if conns, _ := f.sink.snapshot(); len(conns) != 0 {
		t.Fatalf("proxy.connect before the tunnel ended: %+v", conns)
	}
	// Close runs while the event write is held at the gate: it must still be
	// waiting when the write has started, and return only after it is done.
	closed := make(chan error, 1)
	go func() { closed <- pl.Close() }()
	<-f.sink.entered
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while the tunnel's proxy.connect was still being written", err)
	default:
	}
	close(f.sink.release)
	if err := <-closed; err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if conns, _ := f.sink.snapshot(); len(conns) != 1 {
		t.Fatalf("after Close: %d proxy.connect events, want 1 (written before Close returned)", len(conns))
	}
	if _, err := os.Lstat(f.spec.Proxy.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left after Close: %v", err)
	}
	if err := pl.Close(); err != nil {
		t.Fatalf("second Close() = %v, want nil", err)
	}
}

// TestOpenProxy_RefusesExistingSocketPath: an entry already at the Spec's
// socket path is refused (internal/platform) and untouched, and no proxy is
// served.
func TestOpenProxy_RefusesExistingSocketPath(t *testing.T) {
	f := newProxyFixture(t)
	if err := os.Mkdir(filepath.Dir(f.spec.Proxy.Socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.spec.Proxy.Socket, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pl, err := OpenProxy(context.Background(), f.spec, f.cfg); err == nil {
		pl.Close()
		t.Fatal("OpenProxy succeeded over an existing file")
	} else if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if b, err := os.ReadFile(f.spec.Proxy.Socket); err != nil || string(b) != "keep" {
		t.Fatalf("existing file now %q, %v", b, err)
	}
}
