package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLimits_OnlyLowered: A07 §10 says configuration may only lower the
// limits. A zero field and a value above the default both give the default.
func TestLimits_OnlyLowered(t *testing.T) {
	cases := []struct {
		in, want Limits
	}{
		{Limits{}, Limits{MaxConns: 64, ConnWait: 10 * time.Second, IdleTimeout: 300 * time.Second}},
		{Limits{MaxConns: 1000, ConnWait: time.Hour, IdleTimeout: time.Hour}, Limits{MaxConns: 64, ConnWait: 10 * time.Second, IdleTimeout: 300 * time.Second}},
		{Limits{MaxConns: 2, ConnWait: time.Millisecond, IdleTimeout: time.Second}, Limits{MaxConns: 2, ConnWait: time.Millisecond, IdleTimeout: time.Second}},
		{Limits{MaxConns: -1, ConnWait: -1, IdleTimeout: -1}, Limits{MaxConns: 64, ConnWait: 10 * time.Second, IdleTimeout: 300 * time.Second}},
	}
	for _, tc := range cases {
		h := newHarnessWith(t, func(c *Config) { c.Limits = tc.in })
		if h.p.limits != tc.want {
			t.Errorf("Limits %+v -> %+v, want %+v", tc.in, h.p.limits, tc.want)
		}
	}
}

// TestConnLimit is A07 §10 "Concurrent tunnels per task" (CWE-400/770):
// beyond MaxConns a connection waits ConnWait for a slot and is then
// refused with 503 and proxy.denied{too_many_connections}, without a dial;
// a slot frees when its connection ends. Without the limit a hostile
// install script could open tunnels until the daemon runs out of file
// descriptors.
func TestConnLimit(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.Limits = Limits{MaxConns: 1, ConnWait: time.Millisecond} },
		"registry.npmjs.org:443")
	h.res.set("registry.npmjs.org", addrs("104.16.1.34"))
	h.dial.upstream = func(c net.Conn) { _, _ = io.Copy(io.Discard, c); _ = c.Close() }

	first, firstDone := h.open(t)
	if resp, _ := send(t, first, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\n\r\n"); resp.StatusCode != 200 {
		t.Fatalf("first CONNECT status %d", resp.StatusCode)
	}
	second, secondDone := h.open(t)
	resp, _ := send(t, second, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\n\r\n")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("second CONNECT status %d, want 503", resp.StatusCode)
	}
	_ = second.Close()
	waitDone(t, secondDone)
	if n := len(h.dial.dialed()); n != 1 {
		t.Errorf("%d dials, want 1: the refused connection must not dial", n)
	}
	_, denies := h.sink.snapshot()
	if len(denies) != 1 || denies[0].Reason != ReasonTooManyConnections || denies[0].Host != "registry.npmjs.org" {
		t.Errorf("events %+v; want one proxy.denied(too_many_connections)", denies)
	}

	_, _ = first.Write(clientHello("registry.npmjs.org"))
	_ = first.Close()
	waitDone(t, firstDone)
	if s := tunnel(t, h, "registry.npmjs.org:443", clientHello("registry.npmjs.org")); s != 200 {
		t.Errorf("after the first closed: status %d, want 200 (slot released)", s)
	}
}

// TestConnLimit_SlotReturnedOnRefusal: a connection refused after it took
// its slot (every address private, DNS failure, dial failure) gives the
// slot back; with one slot, a leak would refuse every later connection.
func TestConnLimit_SlotReturnedOnRefusal(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.Limits = Limits{MaxConns: 1, ConnWait: time.Millisecond} },
		"registry.npmjs.org:443", "private.example:443", "nxdomain.example:443")
	h.res.set("registry.npmjs.org", addrs("104.16.1.34"))
	h.res.set("private.example", addrs("10.0.0.7"))
	up := &discardUpstream{}
	h.dial.upstream = up.serve
	for _, target := range []string{"private.example:443", "nxdomain.example:443", "private.example:443"} {
		if s := tunnel(t, h, target, nil); s == 503 {
			t.Fatalf("%s: 503, a slot leaked", target)
		}
	}
	h.dial.mu.Lock()
	h.dial.fail = true
	h.dial.mu.Unlock()
	_ = tunnel(t, h, "registry.npmjs.org:443", nil) // 502 dial_failed
	h.dial.mu.Lock()
	h.dial.fail = false
	h.dial.mu.Unlock()
	if s := tunnel(t, h, "registry.npmjs.org:443", clientHello("registry.npmjs.org")); s != 200 {
		t.Errorf("status %d after refusals, want 200 (every slot returned)", s)
	}
}

// TestConnect_IdleTimeout: a tunnel with no bytes either way for
// IdleTimeout is closed and its proxy.connect says idle_timeout (A07 §10).
// The timeout is lowered through Limits, so the test waits milliseconds.
func TestConnect_IdleTimeout(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.Limits = Limits{IdleTimeout: 30 * time.Millisecond} },
		"registry.npmjs.org:443")
	h.res.set("registry.npmjs.org", addrs("104.16.1.34"))
	h.dial.upstream = func(c net.Conn) { _, _ = io.Copy(io.Discard, c); _ = c.Close() }
	c, done := h.open(t)
	if resp, _ := send(t, c, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\n\r\n"); resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	hello := clientHello("registry.npmjs.org")
	if _, err := c.Write(hello); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done) // returns only because the idle timer closed the tunnel
	conns, _ := h.sink.snapshot()
	if len(conns) != 1 || conns[0].Error != ConnErrIdleTimeout || conns[0].BytesUp != int64(len(hello)) {
		t.Errorf("events %+v; want one proxy.connect{error: idle_timeout}", conns)
	}
}

// TestPlainHTTP_StallTimesOut (CWE-400): a client that stops sending its
// request body, or an upstream that stops sending its response body, no
// longer holds the connection (and its slot) forever; after IdleTimeout
// without progress the request ends with proxy.connect{error: idle_timeout}.
func TestPlainHTTP_StallTimesOut(t *testing.T) {
	t.Run("client body", func(t *testing.T) {
		h := newHarnessWith(t, func(c *Config) { c.Limits = Limits{IdleTimeout: 30 * time.Millisecond} }, "plain.example:80")
		h.res.set("plain.example", addrs("93.184.215.14"))
		h.dial.upstream = func(c net.Conn) {
			defer c.Close()
			if req, err := http.ReadRequest(bufio.NewReader(c)); err == nil {
				_, _ = io.Copy(io.Discard, req.Body)
			}
		}
		c, done := h.open(t)
		// Bound the test's own wait, so a proxy without the stall timeout
		// fails here instead of hanging the suite.
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		// Five of the ten announced body bytes, then silence.
		resp, _ := send(t, c, "POST http://plain.example/ HTTP/1.1\r\nContent-Length: 10\r\n\r\nhello")
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("status %d, want 502", resp.StatusCode)
		}
		waitDone(t, done)
		conns, _ := h.sink.snapshot()
		if len(conns) != 1 || conns[0].Error != ConnErrIdleTimeout {
			t.Errorf("events %+v; want one proxy.connect{error: idle_timeout}", conns)
		}
	})
	t.Run("upstream body", func(t *testing.T) {
		h := newHarnessWith(t, func(c *Config) { c.Limits = Limits{IdleTimeout: 30 * time.Millisecond} }, "plain.example:80")
		h.res.set("plain.example", addrs("93.184.215.14"))
		release := make(chan struct{})
		h.dial.upstream = func(c net.Conn) {
			defer c.Close()
			if _, err := http.ReadRequest(bufio.NewReader(c)); err != nil {
				return
			}
			_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nabc")
			select { // stall, then let the test end
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}
		defer close(release)
		c, done := h.open(t)
		resp, _ := send(t, c, "GET http://plain.example/ HTTP/1.1\r\n\r\n")
		_, _ = io.ReadAll(resp.Body)
		waitDone(t, done)
		conns, _ := h.sink.snapshot()
		if len(conns) != 1 || conns[0].Error != ConnErrIdleTimeout || conns[0].BytesDown != 3 {
			t.Errorf("events %+v; want one proxy.connect{error: idle_timeout, bytes_down: 3}", conns)
		}
	})
}

// tempErr is an accept error that says it is temporary, like EMFILE.
type tempErr struct{}

func (tempErr) Error() string   { return "accept: too many open files" }
func (tempErr) Temporary() bool { return true }
func (tempErr) Timeout() bool   { return false }

// permErr has the method but says it is not temporary (EBADF, EINVAL).
type permErr struct{}

func (permErr) Error() string   { return "accept: bad file descriptor" }
func (permErr) Temporary() bool { return false }
func (permErr) Timeout() bool   { return false }

// flakyListener fails Accept with errs first, then serves inner.
type flakyListener struct {
	*pipeListener
	mu   sync.Mutex
	errs []error
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if len(l.errs) > 0 {
		err := l.errs[0]
		l.errs = l.errs[1:]
		l.mu.Unlock()
		return nil, err
	}
	l.mu.Unlock()
	return l.pipeListener.Accept()
}

// TestServe_RetriesTemporaryAcceptErrors (CWE-400): running out of file
// descriptors for a moment must not stop the task's proxy for good, which
// is what returning from Serve did; a permanent error still ends Serve.
func TestServe_RetriesTemporaryAcceptErrors(t *testing.T) {
	h := newHarness(t)
	ln := &flakyListener{pipeListener: newPipeListener(), errs: []error{tempErr{}, tempErr{}, tempErr{}}}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- h.p.Serve(ctx, ln) }()
	c := ln.dial(t)
	resp, _ := send(t, c, "CONNECT collector.example.net:443 HTTP/1.1\r\n\r\n")
	if resp.StatusCode != 403 {
		t.Errorf("status %d after temporary accept errors, want 403 (still serving)", resp.StatusCode)
	}
	_ = c.Close()
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}

	for _, e := range []error{errors.New("listener broken"), permErr{}} {
		perm := &flakyListener{pipeListener: newPipeListener(), errs: []error{e}}
		ctx, cancel := context.WithCancel(context.Background())
		res := make(chan error, 1)
		go func() { res <- h.p.Serve(ctx, perm) }()
		select {
		case err := <-res:
			if err == nil {
				t.Errorf("Serve returned nil on the permanent accept error %q", e)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("Serve kept retrying the permanent accept error %q", e)
		}
		cancel()
	}
}

// TestDial_CapAndDedup (review: surviving mutants): at most four checked
// addresses are tried, in resolver order, each once even when the answer
// repeats it (also as an IPv4-mapped spelling).
func TestDial_CapAndDedup(t *testing.T) {
	cases := []struct {
		name   string
		answer []string
		want   []string
	}{
		{"cap at four", []string{"93.184.215.1", "93.184.215.2", "93.184.215.3", "93.184.215.4", "93.184.215.5", "93.184.215.6"},
			[]string{"93.184.215.1", "93.184.215.2", "93.184.215.3", "93.184.215.4"}},
		{"duplicates", []string{"93.184.215.1", "93.184.215.1", "::ffff:93.184.215.1", "93.184.215.2"},
			[]string{"93.184.215.1", "93.184.215.2"}},
		{"denied ones do not count", []string{"10.0.0.1", "93.184.215.1", "127.0.0.1", "93.184.215.2", "93.184.215.3", "93.184.215.4", "93.184.215.5"},
			[]string{"93.184.215.1", "93.184.215.2", "93.184.215.3", "93.184.215.4"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "many.example:443")
			h.res.set("many.example", addrs(tc.answer...))
			h.dial.fail = true
			if s := tunnel(t, h, "many.example:443", nil); s != 502 {
				t.Errorf("status %d, want 502", s)
			}
			var want []string
			for _, a := range tc.want {
				want = append(want, "tcp "+a+":443")
			}
			if got := h.dial.dialed(); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("dialed %v, want %v", got, want)
			}
		})
	}
}

// TestLookup_CacheBounded (review: surviving mutant): the per-task DNS
// cache holds at most dnsCacheMax hosts, so a script resolving random
// names cannot grow daemon memory without bound; expired entries are
// evicted to make room, live ones are kept.
func TestLookup_CacheBounded(t *testing.T) {
	h := newHarness(t)
	h.res.def = addrs("93.184.215.14")
	ctx := context.Background()
	for i := 0; i < dnsCacheMax; i++ {
		if _, err := h.p.lookup(ctx, fmt.Sprintf("h%d.example", i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		_, _ = h.p.lookup(ctx, "extra.example")
		_, _ = h.p.lookup(ctx, "h0.example")
	}
	if n := h.res.count("extra.example"); n != 2 {
		t.Errorf("extra.example resolved %d times, want 2 (cache full, not cached)", n)
	}
	if n := h.res.count("h0.example"); n != 1 {
		t.Errorf("h0.example resolved %d times, want 1 (still cached)", n)
	}
	if n := len(h.p.dns); n > dnsCacheMax {
		t.Errorf("cache holds %d hosts, more than %d", n, dnsCacheMax)
	}
	h.clock.advance(dnsCacheTTL + time.Second)
	for i := 0; i < 2; i++ {
		_, _ = h.p.lookup(ctx, "later.example")
	}
	if n := h.res.count("later.example"); n != 1 {
		t.Errorf("later.example resolved %d times, want 1 (expired entries evicted to make room)", n)
	}
}

// TestEventHost: proxy.denied.host for a target that did not normalise is
// attacker text. It loses any userinfo (a credential in "user:pw@host"
// must not reach the audit chain, INV-C), has every byte outside printable
// ASCII escaped, and is capped at 253 bytes without splitting an escape.
func TestEventHost(t *testing.T) {
	bs := string(rune(92)) // a backslash, so the expected escapes are spelled out below
	cases := []struct{ in, want string }{
		{"evil.example", "evil.example"},
		{"user:secret@evil.example", "evil.example"},
		{"a@b@evil.example", "evil.example"},
		{"évil.example", bs + "u00e9vil.example"},
		{"a\x01b\x7fc", `a\x01b\x7fc`},
		{"bad\xffutf8", `bad\xffutf8`},
		{"back\\slash", `back\\slash`},
		{string(rune(0x202e)) + "gnp.exe", bs + "u202egnp.exe"}, // RIGHT-TO-LEFT OVERRIDE
	}
	for _, tc := range cases {
		if got := eventHost(tc.in); got != tc.want {
			t.Errorf("eventHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := eventHost(strings.Repeat("é", 200))
	if len(long) > maxEventHost || !strings.HasSuffix(long, bs+"u00e9") || strings.Count(long, bs+"u00e9")*6 != len(long) {
		t.Errorf("long host %d bytes %q: want at most %d bytes of whole escapes", len(long), long[len(long)-8:], maxEventHost)
	}
}
