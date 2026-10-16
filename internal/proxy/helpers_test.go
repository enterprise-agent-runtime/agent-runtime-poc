package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// fakeSink records events in order. Like the real store's Append, it
// refuses to write with a context that is already done, so a test fails if
// the proxy emits an event with a cancelled context (CWE-778: the event
// would be lost in production).
type fakeSink struct {
	mu       sync.Mutex
	connects []ConnectEvent
	denies   []DeniedEvent
	refused  int
}

func (s *fakeSink) ProxyConnect(ctx context.Context, ev ConnectEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		s.refused++
		return err
	}
	s.connects = append(s.connects, ev)
	return nil
}

func (s *fakeSink) ProxyDenied(ctx context.Context, ev DeniedEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		s.refused++
		return err
	}
	s.denies = append(s.denies, ev)
	return nil
}

func (s *fakeSink) snapshot() ([]ConnectEvent, []DeniedEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ConnectEvent(nil), s.connects...), append([]DeniedEvent(nil), s.denies...)
}

// fakeResolver answers from a per-host queue; the last answer repeats.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][][]netip.Addr
	fail    map[string]error
	calls   map[string]int
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{answers: map[string][][]netip.Addr{}, fail: map[string]error{}, calls: map[string]int{}}
}

func (r *fakeResolver) set(host string, answers ...[]netip.Addr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[host] = answers
}

func (r *fakeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[host]++
	if err := r.fail[host]; err != nil {
		return nil, err
	}
	q := r.answers[host]
	if len(q) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	i := r.calls[host] - 1
	if i >= len(q) {
		i = len(q) - 1
	}
	return q[i], nil
}

func (r *fakeResolver) count(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[host]
}

// fakeDialer records every address it is asked for and connects each dial
// to an in-test upstream over net.Pipe; no socket is opened.
type fakeDialer struct {
	mu       sync.Mutex
	addrs    []string
	upstream func(net.Conn) // runs in its own goroutine; must close the conn
	fail     bool
	wg       sync.WaitGroup // one per running upstream
}

func (d *fakeDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.addrs = append(d.addrs, network+" "+address)
	fail, up := d.fail, d.upstream
	d.mu.Unlock()
	if fail || up == nil {
		return nil, errors.New("connection refused")
	}
	near, far := net.Pipe()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		up(far)
	}()
	return near, nil
}

func (d *fakeDialer) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	p     *TaskProxy
	sink  *fakeSink
	res   *fakeResolver
	dial  *fakeDialer
	clock *fakeClock
}

const testSandbox = "sb_01JAX0000000000000000000AA"

func newHarness(t *testing.T, keys ...string) *harness {
	t.Helper()
	return newHarnessWith(t, nil, keys...)
}

// newHarnessWith is newHarness with a hook to adjust the Config before New.
// The host's interface addresses default to none, so no test depends on
// the machine it runs on.
func newHarnessWith(t *testing.T, mod func(*Config), keys ...string) *harness {
	t.Helper()
	var entries []Entry
	for _, k := range keys {
		entries = append(entries, Entry{Key: k, Source: SourceCapability})
	}
	allow, err := NewAllowlist(entries...)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{sink: &fakeSink{}, res: newFakeResolver(), dial: &fakeDialer{},
		clock: &fakeClock{t: time.Date(2026, 9, 26, 10, 14, 3, 0, time.UTC)}}
	cfg := Config{
		SandboxID: testSandbox, Allow: allow, Events: h.sink,
		Resolver: h.res, Dialer: h.dial, Now: h.clock.Now,
		CallID:         func() string { return "call_31" },
		InterfaceAddrs: func() ([]netip.Addr, error) { return nil, nil },
	}
	if mod != nil {
		mod(&cfg)
	}
	h.p, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// fakeDecider answers every DecideEgress with one fixed decision or error
// and records the requests.
type fakeDecider struct {
	mu   sync.Mutex
	d    Decision
	err  error
	reqs []EgressRequest
}

func (f *fakeDecider) DecideEgress(_ context.Context, req EgressRequest) (Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return f.d, f.err
}

func (f *fakeDecider) WaitApproval(context.Context, string) (Decision, error) {
	return Decision{Effect: EffectDeny}, errors.New("fake: no approvals")
}

func (f *fakeDecider) calls() []EgressRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]EgressRequest(nil), f.reqs...)
}

// open starts ServeConn on one end of a pipe and returns the client end
// and a channel closed when ServeConn returns.
func (h *harness) open(t *testing.T) (net.Conn, <-chan struct{}) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.p.ServeConn(context.Background(), server)
	}()
	t.Cleanup(func() { _ = client.Close() })
	return client, done
}

// waitDone fails the test if ServeConn does not return. The bound only
// stops a hung test; nothing asserts on time.
func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ServeConn did not return")
	}
}

// send writes a request in a goroutine (net.Pipe writes block until read)
// and returns the response the proxy sends back.
func send(t *testing.T, c net.Conn, raw string) (*http.Response, *bufio.Reader) {
	t.Helper()
	go func() { _, _ = io.WriteString(c, raw) }()
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading proxy response to %q: %v", raw, err)
	}
	return resp, br
}

func addrs(ss ...string) []netip.Addr {
	out := make([]netip.Addr, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

// ---- ClientHello construction ------------------------------------------

func u16(n int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(n)) }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func ext(typ int, data []byte) []byte { return cat(u16(typ), u16(len(data)), data) }

// sniExt builds a server_name extension holding one host_name per name.
func sniExt(names ...string) []byte {
	var list []byte
	for _, n := range names {
		list = cat(list, []byte{0}, u16(len(n)), []byte(n))
	}
	return ext(0, cat(u16(len(list)), list))
}

// helloBody builds a ClientHello body (after the handshake header).
func helloBody(exts ...[]byte) []byte {
	body := cat([]byte{3, 3}, make([]byte, 32), []byte{0}, u16(2), []byte{0x13, 0x01}, []byte{1, 0})
	if exts == nil {
		return body
	}
	all := cat(exts...)
	return cat(body, u16(len(all)), all)
}

func handshake(body []byte) []byte {
	return cat([]byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body)
}

func record(payload []byte) []byte { return cat([]byte{0x16, 3, 1}, u16(len(payload)), payload) }

// clientHello is a minimal well-formed ClientHello record with an SNI.
func clientHello(sni string) []byte {
	return record(handshake(helloBody(sniExt(sni), ext(0x2b, []byte{2, 3, 4}))))
}

// realClientHello returns the first TLS record crypto/tls writes for a
// handshake with ServerName sni, so the parser is tested against what real
// clients send (GREASE-free but with key shares, ALPN and so on).
func realClientHello(t testing.TB, sni string) []byte {
	t.Helper()
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	go func() {
		c := tls.Client(near, &tls.Config{ServerName: sni, NextProtos: []string{"h2", "http/1.1"}})
		_ = c.Handshake()
	}()
	rec, err := readTLSRecordForTest(far)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func readTLSRecordForTest(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	body := make([]byte, binary.BigEndian.Uint16(hdr[3:5]))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return cat(hdr, body), nil
}
