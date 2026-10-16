package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConnect_AllowedEmitsProxyConnect is the allowed half of A07 §3.2: an
// allowlisted CONNECT is answered 200, the ClientHello is passed through
// unchanged, bytes flow both ways, and one proxy.connect with the final
// counters is written at close (A07 §9). The Host header is ignored: the
// request target is authoritative.
func TestConnect_AllowedEmitsProxyConnect(t *testing.T) {
	h := newHarness(t, "registry.npmjs.org:443")
	h.res.set("registry.npmjs.org", addrs("104.16.1.34", "104.16.2.34"))
	hello := clientHello("registry.npmjs.org")
	var got []byte
	upDone := make(chan struct{})
	h.dial.upstream = func(c net.Conn) {
		defer close(upDone)
		defer c.Close()
		buf := make([]byte, len(hello)+4)
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Errorf("upstream read: %v", err)
			return
		}
		got = buf
		_, _ = c.Write([]byte("pong"))
	}
	c, done := h.open(t)
	resp, br := send(t, c, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\nHost: evil.example:443\r\n\r\n")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	go func() { _, _ = c.Write(cat(hello, []byte("ping"))) }()
	pong := make([]byte, 4)
	if _, err := io.ReadFull(br, pong); err != nil || string(pong) != "pong" {
		t.Fatalf("client read %q, %v; want pong", pong, err)
	}
	<-upDone
	_ = c.Close()
	waitDone(t, done)

	if string(got) != string(cat(hello, []byte("ping"))) {
		t.Errorf("upstream received %q, want the ClientHello then ping", got)
	}
	if d := h.dial.dialed(); len(d) != 1 || d[0] != "tcp 104.16.1.34:443" {
		t.Errorf("dialed %v, want [tcp 104.16.1.34:443]", d)
	}
	conns, denies := h.sink.snapshot()
	if len(denies) != 0 || len(conns) != 1 {
		t.Fatalf("events: %d connect, %d denied; want 1, 0 (%+v)", len(conns), len(denies), denies)
	}
	want := ConnectEvent{SandboxID: testSandbox, Host: "registry.npmjs.org", Port: 443, Method: "CONNECT",
		Rule: RuleCapability, CallID: "call_31", IP: "104.16.1.34",
		BytesUp: int64(len(hello) + 4), BytesDown: 4}
	if conns[0] != want {
		t.Errorf("proxy.connect = %+v\nwant          %+v", conns[0], want)
	}
}

// TestConnect_DialFailedEmitsConnectError: A07 §3.2 step 4 says a dial
// failure is a proxy.connect with error dial_failed, not a proxy.denied.
func TestConnect_DialFailedEmitsConnectError(t *testing.T) {
	h := newHarness(t, "registry.npmjs.org:443")
	h.res.set("registry.npmjs.org", addrs("104.16.1.34"))
	h.dial.fail = true
	c, done := h.open(t)
	resp, _ := send(t, c, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\n\r\n")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d, want 502", resp.StatusCode)
	}
	_ = c.Close()
	waitDone(t, done)
	conns, denies := h.sink.snapshot()
	if len(denies) != 0 || len(conns) != 1 || conns[0].Error != ConnErrDialFailed || conns[0].BytesUp != 0 || conns[0].BytesDown != 0 {
		t.Errorf("events %+v %+v; want one proxy.connect{error: dial_failed, bytes 0}", conns, denies)
	}
}

// TestConnect_DeniedEmitsProxyDenied covers deny by default and every
// pre-dial refusal of A07 §3: each produces exactly one proxy.denied with
// the A07 §9 reason, the right status, no dial, and no proxy.connect.
func TestConnect_DeniedEmitsProxyDenied(t *testing.T) {
	cases := []struct {
		name    string
		req     string
		status  int
		reason  Reason
		host    string
		port    uint16
		rule    string
		resolve bool // whether DNS is expected to be consulted
	}{
		{"not on allowlist", "CONNECT collector.example.net:443 HTTP/1.1\r\n\r\n", 403, ReasonPolicyDenied, "collector.example.net", 443, RuleStaticDeny, false},
		{"port not on allowlist", "CONNECT registry.npmjs.org:8443 HTTP/1.1\r\n\r\n", 403, ReasonPolicyDenied, "registry.npmjs.org", 8443, RuleStaticDeny, false},
		{"metadata IP not on allowlist", "CONNECT 169.254.169.254:80 HTTP/1.1\r\n\r\n", 403, ReasonPolicyDenied, "169.254.169.254", 80, RuleStaticDeny, false},
		{"short IPv4 form", "CONNECT 127.1:443 HTTP/1.1\r\n\r\n", 400, ReasonInvalidTarget, "127.1", 443, "", false},
		{"missing port", "CONNECT registry.npmjs.org HTTP/1.1\r\n\r\n", 400, ReasonInvalidTarget, "registry.npmjs.org", 0, "", false},
		{"localhost", "CONNECT localhost:22 HTTP/1.1\r\n\r\n", 403, ReasonPrivateAddress, "localhost", 22, "", false},
		{"resolves only to private", "CONNECT private.example:443 HTTP/1.1\r\n\r\n", 403, ReasonPrivateAddress, "private.example", 443, RuleCapability, true},
		{"dns failure", "CONNECT nxdomain.example:443 HTTP/1.1\r\n\r\n", 502, ReasonDNSFailed, "nxdomain.example", 443, RuleCapability, true},
		{"https absolute form", "GET https://registry.npmjs.org/secret/path HTTP/1.1\r\nHost: registry.npmjs.org\r\n\r\n", 400, ReasonUnsupportedMethod, "registry.npmjs.org", 443, "", false},
		{"origin form", "GET /secret/path HTTP/1.1\r\nHost: registry.npmjs.org\r\n\r\n", 400, ReasonUnsupportedMethod, "", 0, "", false},
		{"upgrade", "GET http://plain.example/ws HTTP/1.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n", 501, ReasonUnsupportedMethod, "plain.example", 80, "", false},
		{"http2 preface", "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n", 400, ReasonUnsupportedMethod, "", 0, "", false},
		// Methods must match ^[A-Z]+$ (review of 7ad63b0): a lower-case or
		// odd token is not a method any client sends, and it never reaches
		// an event.
		{"lower-case method", "get http://plain.example/ HTTP/1.1\r\n\r\n", 400, ReasonUnsupportedMethod, "", 0, "", false},
		{"token method", "X-Y http://plain.example/ HTTP/1.1\r\n\r\n", 400, ReasonUnsupportedMethod, "", 0, "", false},
		{"lower-case connect", "connect registry.npmjs.org:443 HTTP/1.1\r\n\r\n", 400, ReasonUnsupportedMethod, "", 0, "", false},
		// Userinfo never reaches proxy.denied.host (INV-C hygiene), and
		// non-ASCII is escaped.
		{"userinfo in target", "CONNECT user:pw@registry.npmjs.org:443 HTTP/1.1\r\n\r\n", 400, ReasonInvalidTarget, "registry.npmjs.org", 443, "", false},
		{"non-ASCII target", "CONNECT bücher.example:443 HTTP/1.1\r\n\r\n", 400, ReasonInvalidTarget, "b" + string(rune(92)) + "u00fccher.example", 443, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "registry.npmjs.org:443", "private.example:443", "nxdomain.example:443", "plain.example:80")
			h.res.set("private.example", addrs("10.0.0.7", "127.0.0.1", "::ffff:169.254.169.254"))
			h.res.set("registry.npmjs.org", addrs("104.16.1.34"))
			h.dial.upstream = func(c net.Conn) { _ = c.Close() }
			c, done := h.open(t)
			resp, br := send(t, c, tc.req)
			if resp.StatusCode != tc.status {
				t.Errorf("status %d, want %d", resp.StatusCode, tc.status)
			}
			body, _ := io.ReadAll(io.LimitReader(br, 4096))
			if !strings.HasPrefix(string(body), "warden: ") || strings.Contains(string(body), "secret") {
				t.Errorf("body %q: want a one-line warden refusal without the request path", body)
			}
			_ = c.Close()
			waitDone(t, done)
			if d := h.dial.dialed(); len(d) != 0 {
				t.Errorf("dialed %v; a refused target must never be dialed", d)
			}
			resolved := h.res.count(tc.host) > 0
			if resolved != tc.resolve {
				t.Errorf("resolver consulted = %v, want %v", resolved, tc.resolve)
			}
			conns, denies := h.sink.snapshot()
			if len(conns) != 0 || len(denies) != 1 {
				t.Fatalf("events: %d connect, %d denied; want 0, 1", len(conns), len(denies))
			}
			d := denies[0]
			if d.Reason != tc.reason || d.Host != tc.host || d.Port != tc.port || d.Rule != tc.rule ||
				d.SandboxID != testSandbox || d.CallID != "call_31" || d.HeldMS != 0 {
				t.Errorf("proxy.denied = %+v; want reason %s host %q port %d rule %q", d, tc.reason, tc.host, tc.port, tc.rule)
			}
		})
	}
}

// tunnel runs one CONNECT that, when answered 200, sends hello and closes.
func tunnel(t *testing.T, h *harness, target string, hello []byte) int {
	t.Helper()
	c, done := h.open(t)
	resp, _ := send(t, c, "CONNECT "+target+" HTTP/1.1\r\n\r\n")
	if resp.StatusCode == 200 && hello != nil {
		if _, err := c.Write(hello); err != nil {
			t.Logf("client write: %v", err)
		}
	}
	_ = c.Close()
	waitDone(t, done)
	return resp.StatusCode
}

// discardUpstream reads until EOF and records what it received per dial.
type discardUpstream struct {
	mu  sync.Mutex
	got [][]byte
}

func (u *discardUpstream) serve(c net.Conn) {
	b, _ := io.ReadAll(c)
	_ = c.Close()
	u.mu.Lock()
	u.got = append(u.got, b)
	u.mu.Unlock()
}

// TestRebinding_DialsCheckedIP is A07 §13 "Rebinding": the resolver
// answers a public address first and 127.0.0.1 afterwards. The dial must
// go to the address that was checked (an IP, never the name), the second
// connection inside 30 s must reuse the cached answer instead of asking
// DNS again, and once the cache expires the private answer is refused
// without a dial. A proxy that resolved to check and let the dialer
// resolve again would connect to loopback on the second answer.
func TestRebinding_DialsCheckedIP(t *testing.T) {
	h := newHarness(t, "rebind.example:443")
	h.res.set("rebind.example", addrs("93.184.215.14"), addrs("127.0.0.1"))
	up := &discardUpstream{}
	h.dial.upstream = up.serve
	hello := clientHello("rebind.example")

	if s := tunnel(t, h, "rebind.example:443", hello); s != 200 {
		t.Fatalf("first CONNECT status %d", s)
	}
	h.clock.advance(29 * time.Second)
	if s := tunnel(t, h, "rebind.example:443", hello); s != 200 {
		t.Fatalf("second CONNECT status %d", s)
	}
	if n := h.res.count("rebind.example"); n != 1 {
		t.Errorf("resolver called %d times within 30 s, want 1 (cached)", n)
	}
	for _, d := range h.dial.dialed() {
		if d != "tcp 93.184.215.14:443" {
			t.Errorf("dialed %q, want the checked address tcp 93.184.215.14:443", d)
		}
	}
	h.clock.advance(2 * time.Second) // 31 s after the first lookup
	if s := tunnel(t, h, "rebind.example:443", hello); s != 403 {
		t.Errorf("third CONNECT (rebound to 127.0.0.1) status %d, want 403", s)
	}
	if n := len(h.dial.dialed()); n != 2 {
		t.Errorf("%d dials, want 2: the rebound answer must not be dialed", n)
	}
	conns, denies := h.sink.snapshot()
	if len(conns) != 2 || len(denies) != 1 || denies[0].Reason != ReasonPrivateAddress {
		t.Errorf("events %+v / %+v; want 2 proxy.connect and 1 proxy.denied(private_address)", conns, denies)
	}
	for _, c := range conns {
		if c.IP != "93.184.215.14" {
			t.Errorf("proxy.connect ip %q, want 93.184.215.14", c.IP)
		}
	}
}

// TestRangeCheck_OperatorLiteralDialsExactAddress: the replacement for A07
// §7's literal-entry exception (owner decision 2026-10-16): an allowed
// denied-range literal that an operator entry also names is dialed at
// exactly that address without DNS, and only on that port.
func TestRangeCheck_OperatorLiteralDialsExactAddress(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.OperatorLiteralAllow = []string{"10.0.0.5:8443"} }, "10.0.0.5:8443")
	up := &discardUpstream{}
	h.dial.upstream = up.serve
	if s := tunnel(t, h, "10.0.0.5:8443", record(handshake(helloBody()))); s != 200 {
		t.Fatalf("status %d, want 200", s)
	}
	if d := h.dial.dialed(); len(d) != 1 || d[0] != "tcp 10.0.0.5:8443" {
		t.Errorf("dialed %v, want [tcp 10.0.0.5:8443]", d)
	}
	// A different port on the same address is not on the allowlist.
	if s := tunnel(t, h, "10.0.0.5:22", nil); s != 403 {
		t.Errorf("10.0.0.5:22 status %d, want 403", s)
	}
}

// TestSNIMismatch is A07 §13 "SNI" (§3.2 step 6): after 200, the first
// record must be a ClientHello whose server_name equals the CONNECT host.
// Without this check a tunnel allowed for one tenant of a CDN address can
// carry TLS to any other tenant on that address. A refused tunnel forwards
// nothing upstream and ends in proxy.denied, not proxy.connect.
func TestSNIMismatch(t *testing.T) {
	realOK := realClientHello(t, "registry.npmjs.org")
	realBad := realClientHello(t, "evil.example")
	cases := []struct {
		name   string
		target string
		hello  []byte
		reason Reason // "" = allowed
	}{
		{"matching SNI", "registry.npmjs.org:443", clientHello("registry.npmjs.org"), ""},
		{"SNI differs in case", "registry.npmjs.org:443", clientHello("Registry.NPMJS.org"), ""},
		{"real crypto/tls hello", "registry.npmjs.org:443", realOK, ""},
		{"other tenant", "registry.npmjs.org:443", clientHello("evil.example"), ReasonSNIMismatch},
		{"real hello, other tenant", "registry.npmjs.org:443", realBad, ReasonSNIMismatch},
		{"suffix of host", "registry.npmjs.org:443", clientHello("npmjs.org"), ReasonSNIMismatch},
		{"host plus suffix", "registry.npmjs.org:443", clientHello("registry.npmjs.org.evil.example"), ReasonSNIMismatch},
		{"NUL inside name", "registry.npmjs.org:443", clientHello("registry.npmjs.org\x00.evil"), ReasonSNIMismatch},
		{"no SNI to a name", "registry.npmjs.org:443", record(handshake(helloBody(ext(0x2b, []byte{2, 3, 4})))), ReasonSNIMismatch},
		{"no extensions to a name", "registry.npmjs.org:443", record(handshake(helloBody())), ReasonSNIMismatch},
		{"two host names", "registry.npmjs.org:443", record(handshake(helloBody(sniExt("registry.npmjs.org", "evil.example")))), ReasonNotTLS},
		{"two SNI extensions", "registry.npmjs.org:443", record(handshake(helloBody(sniExt("registry.npmjs.org"), sniExt("evil.example")))), ReasonNotTLS},
		{"plain HTTP in tunnel", "registry.npmjs.org:443", []byte("GET / HTTP/1.1\r\nHost: registry.npmjs.org\r\n\r\n"), ReasonNotTLS},
		{"application data record", "registry.npmjs.org:443", cat([]byte{0x17, 3, 3}, u16(4), []byte("abcd")), ReasonNotTLS},
		{"ServerHello", "registry.npmjs.org:443", record(cat([]byte{2}, handshake(helloBody(sniExt("registry.npmjs.org")))[1:])), ReasonNotTLS},
		{"fragmented hello", "registry.npmjs.org:443", func() []byte {
			hs := handshake(helloBody(sniExt("registry.npmjs.org")))
			return record(hs[:len(hs)-3])
		}(), ReasonNotTLS},
		{"truncated record", "registry.npmjs.org:443", clientHello("registry.npmjs.org")[:20], ReasonNotTLS},
		{"IP target without SNI", "93.184.215.14:443", record(handshake(helloBody())), ""},
		{"IP target, plain HTTP", "93.184.215.14:443", []byte("GET / HTTP/1.1\r\n\r\n"), ReasonNotTLS},
		// Owner decision 2026-10-16 (stricter than A07): any ClientHello with
		// encrypted_client_hello (0xfe0d) is refused, real or GREASE, because
		// the inner SNI could name any tenant behind an allowed outer name.
		{"ECH with matching outer SNI", "registry.npmjs.org:443", record(handshake(helloBody(sniExt("registry.npmjs.org"), ext(0xfe0d, []byte{0, 1, 2, 3})))), ReasonSNIMismatch},
		{"ECH to an IP target", "93.184.215.14:443", record(handshake(helloBody(ext(0xfe0d, []byte{0})))), ReasonSNIMismatch},
		{"empty ECH extension", "registry.npmjs.org:443", record(handshake(helloBody(ext(0xfe0d, nil), sniExt("registry.npmjs.org")))), ReasonSNIMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "registry.npmjs.org:443", "93.184.215.14:443")
			h.res.set("registry.npmjs.org", addrs("104.16.1.34"))
			up := &discardUpstream{}
			h.dial.upstream = up.serve
			if s := tunnel(t, h, tc.target, tc.hello); s != 200 {
				t.Fatalf("status %d, want 200 before the TLS check", s)
			}
			h.dial.wg.Wait()
			conns, denies := h.sink.snapshot()
			up.mu.Lock()
			got := up.got
			up.mu.Unlock()
			if tc.reason == "" {
				if len(conns) != 1 || len(denies) != 0 {
					t.Fatalf("events %+v / %+v; want one proxy.connect", conns, denies)
				}
				if len(got) != 1 || string(got[0]) != string(tc.hello) {
					t.Errorf("upstream got %q, want the ClientHello unchanged", got)
				}
				return
			}
			if len(conns) != 0 || len(denies) != 1 || denies[0].Reason != tc.reason {
				t.Fatalf("events %+v / %+v; want one proxy.denied(%s)", conns, denies, tc.reason)
			}
			for _, b := range got {
				if len(b) != 0 {
					t.Errorf("upstream received %q from a refused tunnel", b)
				}
			}
		})
	}
}

// TestPlainHTTP_HopByHopStripped is A07 §13 "Plain HTTP" (§3.3): the
// outbound request is in origin form with Host from the URI (blocks
// Host-header fronting), hop-by-hop headers and Proxy-Authorization are
// removed in both directions, no Via/X-Forwarded-For/Accept-Encoding/
// User-Agent is added, and a redirect is returned, never followed.
func TestPlainHTTP_HopByHopStripped(t *testing.T) {
	h := newHarness(t, "plain.example:80")
	h.res.set("plain.example", addrs("93.184.215.14"))
	type seen struct {
		uri, host string
		header    http.Header
		body      string
	}
	upSeen := make(chan seen, 1)
	h.dial.upstream = func(c net.Conn) {
		defer c.Close()
		req, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			t.Errorf("upstream ReadRequest: %v", err)
			return
		}
		b, _ := io.ReadAll(req.Body)
		upSeen <- seen{req.RequestURI, req.Host, req.Header, string(b)}
		_, _ = io.WriteString(c, "HTTP/1.1 302 Found\r\n"+
			"Location: http://plain.example/elsewhere\r\n"+
			"Connection: X-Down-Hop\r\n"+
			"X-Down-Hop: 1\r\n"+
			"Keep-Alive: timeout=5\r\n"+
			"Proxy-Authenticate: Basic\r\n"+
			"X-Down-Keep: yes\r\n"+
			"Content-Length: 3\r\n\r\nabc")
	}
	c, done := h.open(t)
	resp, _ := send(t, c, "POST http://plain.example/upload?q=1 HTTP/1.1\r\n"+
		"Host: evil.example\r\n"+
		"Connection: keep-alive, X-Hop\r\n"+
		"X-Hop: secret\r\n"+
		"Keep-Alive: timeout=5\r\n"+
		"Proxy-Connection: keep-alive\r\n"+
		"Proxy-Authorization: Basic dXNlcjpwYXNz\r\n"+
		"TE: trailers\r\n"+
		"Trailer: X-T\r\n"+
		"X-Keep: yes\r\n"+
		"Content-Length: 5\r\n\r\nhello")
	body, _ := io.ReadAll(resp.Body)
	_ = c.Close()
	waitDone(t, done)

	var up seen
	select {
	case up = <-upSeen:
	default:
		t.Fatal("upstream saw no request")
	}
	if up.uri != "/upload?q=1" || up.host != "plain.example" || up.body != "hello" {
		t.Errorf("upstream got uri %q host %q body %q; want /upload?q=1, plain.example, hello", up.uri, up.host, up.body)
	}
	for _, k := range []string{"X-Hop", "Keep-Alive", "Proxy-Connection", "Proxy-Authorization",
		"Te", "Trailer", "Upgrade", "Via", "X-Forwarded-For", "Accept-Encoding", "User-Agent", "Authorization"} {
		if v, ok := up.header[k]; ok {
			t.Errorf("upstream received %s: %q", k, v)
		}
	}
	// The proxy's own hop is one request per connection, so the only
	// Connection value upstream may see is the proxy's "close".
	if v := up.header.Values("Connection"); len(v) > 1 || len(v) == 1 && v[0] != "close" {
		t.Errorf("upstream Connection header %q, want only close", v)
	}
	if up.header.Get("X-Keep") != "yes" {
		t.Errorf("end-to-end header X-Keep lost: %v", up.header)
	}
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "http://plain.example/elsewhere" || string(body) != "abc" {
		t.Errorf("client got %d Location %q body %q; want the 302 passed back", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	for _, k := range []string{"X-Down-Hop", "Keep-Alive", "Proxy-Authenticate"} {
		if v, ok := resp.Header[k]; ok {
			t.Errorf("client received %s: %q", k, v)
		}
	}
	if v := resp.Header.Values("Connection"); len(v) > 1 || len(v) == 1 && v[0] != "close" {
		t.Errorf("client Connection header %q, want only close", v)
	}
	if resp.Header.Get("X-Down-Keep") != "yes" {
		t.Errorf("end-to-end response header lost: %v", resp.Header)
	}
	if d := h.dial.dialed(); len(d) != 1 || d[0] != "tcp 93.184.215.14:80" {
		t.Errorf("dialed %v; want one dial to the checked IP (redirect not followed)", d)
	}
	conns, denies := h.sink.snapshot()
	want := ConnectEvent{SandboxID: testSandbox, Host: "plain.example", Port: 80, Method: "POST",
		Rule: RuleCapability, CallID: "call_31", IP: "93.184.215.14", BytesUp: 5, BytesDown: 3}
	if len(denies) != 0 || len(conns) != 1 || conns[0] != want {
		t.Errorf("events %+v / %+v\nwant one %+v", conns, denies, want)
	}
}

// TestInjector_NeverMatches is A07 §11's test: the credential-injection
// hook exists but is never enabled in the PoC. Match is false for every
// target in the normalisation corpus, and no Config field can select
// another injector (BI-3: no credential enters the proxy path).
func TestInjector_NeverMatches(t *testing.T) {
	h := newHarness(t)
	if _, ok := h.p.injector.(noInjector); !ok {
		t.Fatalf("injector is %T, want noInjector", h.p.injector)
	}
	n := 0
	for _, c := range normalizeCases {
		tg, err := Normalize(c.in)
		if err != nil {
			continue
		}
		n++
		if h.p.injector.Match(tg) {
			t.Errorf("injector matches %s", tg.Key())
		}
	}
	if n == 0 {
		t.Fatal("corpus has no accepted target")
	}
	ifc := reflect.TypeOf((*CredentialInjector)(nil)).Elem()
	cfg := reflect.TypeOf(Config{})
	for i := 0; i < cfg.NumField(); i++ {
		if ft := cfg.Field(i).Type; ft.Kind() == reflect.Interface && ft.Implements(ifc) || ft == ifc {
			t.Errorf("Config.%s can select a credential injector", cfg.Field(i).Name)
		}
	}
}

// TestAllowlist_ExactOnly: A07 §5.1 has no wildcard, suffix or CIDR
// entries, and a key must already be canonical, so an entry can never
// match more than the one host:port it names.
func TestAllowlist_ExactOnly(t *testing.T) {
	a, err := NewAllowlist(Entry{Key: "registry.npmjs.org:443", Source: SourceCapability})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"*.npmjs.org:443", "*:443", "Registry.npmjs.org:443", "registry.npmjs.org", "registry.npmjs.org.:443", "10.0.0.0/8:443"} {
		if err := a.Add(Entry{Key: k, Source: SourceCapability}); err == nil {
			t.Errorf("Add(%q) accepted", k)
		}
	}
	if err := a.Add(Entry{Key: "pypi.org:443", Source: "manifest"}); err == nil {
		t.Error("Add with an unknown source accepted")
	}
	for _, k := range []string{"registry.npmjs.org:80", "npmjs.org:443", "x.registry.npmjs.org:443"} {
		if _, ok := a.Get(k); ok {
			t.Errorf("Get(%q) matched", k)
		}
	}
	if e, ok := a.Get("registry.npmjs.org:443"); !ok || e.Rule != RuleCapability {
		t.Errorf("Get exact = %+v, %v", e, ok)
	}
	_ = a.Add(Entry{Key: "pypi.org:443", Source: SourceObligation, Rule: "user.package-install", DecisionID: "dec_1"})
	if k := a.Keys(); !reflect.DeepEqual(k, []string{"pypi.org:443", "registry.npmjs.org:443"}) {
		t.Errorf("Keys() = %v", k)
	}
}

// TestEvents_JSON: the payloads match A07 §9, with null for absent rule,
// decision_id, call_id and error.
func TestEvents_JSON(t *testing.T) {
	b, _ := json.Marshal(ConnectEvent{SandboxID: testSandbox, Host: "a.example", Port: 443, Method: "CONNECT", BytesUp: 1})
	want := `{"sandbox_id":"` + testSandbox + `","host":"a.example","port":443,"method":"CONNECT","rule":null,"decision_id":null,"call_id":null,"bytes_up":1,"bytes_down":0,"duration_ms":0,"error":null}`
	if string(b) != want {
		t.Errorf("connect JSON\n got %s\nwant %s", b, want)
	}
	b, _ = json.Marshal(DeniedEvent{SandboxID: testSandbox, Host: "a.example", Port: 443, Method: "CONNECT", Rule: RuleStaticDeny, Reason: ReasonPolicyDenied})
	want = `{"sandbox_id":"` + testSandbox + `","host":"a.example","port":443,"method":"CONNECT","rule":"` + RuleStaticDeny + `","decision_id":null,"call_id":null,"reason":"policy_denied","held_ms":0}`
	if string(b) != want {
		t.Errorf("denied JSON\n got %s\nwant %s", b, want)
	}
}

// pipeListener is an in-memory net.Listener, so Serve is tested without a
// socket.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "pipe", Net: "unix"}
}

func (l *pipeListener) dial(t *testing.T) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	select {
	case l.conns <- b:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve is not accepting")
	}
	return a
}

// TestServe_StopsOnCancel: cancelling the context closes the listener and
// every open client connection, and Serve returns nil after its handlers.
func TestServe_StopsOnCancel(t *testing.T) {
	h := newHarness(t, "registry.npmjs.org:443")
	ln := newPipeListener()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- h.p.Serve(ctx, ln) }()
	c := ln.dial(t) // a client that never sends its request
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Error("client connection still open after Serve returned")
	}
}

// TestServeConn_EventPerAttempt: a connection that closes before sending a
// byte attempted nothing and writes no event (otherwise every port probe
// fills the audit chain); a malformed request and a request head over the
// 16 KiB limit (A07 §10) are refused with one proxy.denied each.
func TestServeConn_EventPerAttempt(t *testing.T) {
	h := newHarness(t, "registry.npmjs.org:443")
	c, done := h.open(t)
	_ = c.Close()
	waitDone(t, done)
	if conns, denies := h.sink.snapshot(); len(conns)+len(denies) != 0 {
		t.Errorf("empty connection wrote events %+v %+v", conns, denies)
	}

	for name, raw := range map[string]string{
		"garbage":    "hello there\r\n\r\n",
		"head 16KiB": "CONNECT registry.npmjs.org:443 HTTP/1.1\r\nX-Pad: " + strings.Repeat("a", 16<<10) + "\r\n\r\n",
	} {
		h := newHarness(t, "registry.npmjs.org:443")
		c, done := h.open(t)
		resp, _ := send(t, c, raw)
		_ = c.Close()
		waitDone(t, done)
		conns, denies := h.sink.snapshot()
		if resp.StatusCode != 400 || len(conns) != 0 || len(denies) != 1 || denies[0].Reason != ReasonUnsupportedMethod {
			t.Errorf("%s: status %d, events %+v %+v; want 400 and one proxy.denied(unsupported_method)", name, resp.StatusCode, conns, denies)
		}
		if d := h.dial.dialed(); len(d) != 0 {
			t.Errorf("%s: dialed %v", name, d)
		}
	}
}

// TestServe_CancelDuringTunnelKeepsProxyConnect guards a confirmed bug
// (review of 7ad63b0, CWE-778): the final proxy.connect of a tunnel still
// open at sandbox.destroy was emitted with the cancelled Serve context, so
// the store refused it and the tunnel's byte counts were lost (A07 §2
// "Close": every proxy event is written before sandbox.destroy). The sink
// here refuses a done context exactly like the store.
func TestServe_CancelDuringTunnelKeepsProxyConnect(t *testing.T) {
	h := newHarness(t, "registry.npmjs.org:443")
	h.res.set("registry.npmjs.org", addrs("104.16.1.34"))
	hello := clientHello("registry.npmjs.org")
	got := make(chan struct{})
	h.dial.upstream = func(c net.Conn) {
		defer c.Close()
		buf := make([]byte, len(hello))
		if _, err := io.ReadFull(c, buf); err == nil {
			close(got)
		}
		_, _ = io.Copy(io.Discard, c) // hold the tunnel open until closed
	}
	ln := newPipeListener()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- h.p.Serve(ctx, ln) }()
	c := ln.dial(t)
	defer c.Close()
	resp, _ := send(t, c, "CONNECT registry.npmjs.org:443 HTTP/1.1\r\n\r\n")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	go func() { _, _ = c.Write(hello) }()
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("upstream never received the ClientHello")
	}
	cancel() // sandbox.destroy while the tunnel is open
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	conns, denies := h.sink.snapshot()
	if len(conns) != 1 || len(denies) != 0 || h.sink.refused != 0 {
		t.Fatalf("events %+v / %+v, %d refused for a done context; want exactly one proxy.connect", conns, denies, h.sink.refused)
	}
	if conns[0].BytesUp != int64(len(hello)) || conns[0].Error != "" {
		t.Errorf("proxy.connect = %+v; want bytes_up %d and no error (shutdown is not an upstream reset)", conns[0], len(hello))
	}
}
