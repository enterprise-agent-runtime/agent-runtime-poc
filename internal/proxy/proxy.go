package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// A07 §10 limits. Configuration may only lower them; Config.Limits
// lowers MaxConns, ConnWait and IdleTimeout, the rest are fixed.
const (
	headerTimeout         = 10 * time.Second
	headerLimit           = 16 << 10
	helloTimeout          = 10 * time.Second
	idleTimeout           = 300 * time.Second
	dnsTimeout            = 5 * time.Second
	dnsCacheTTL           = 30 * time.Second
	dnsCacheMax           = 1024 // hosts per task; beyond that answers are not cached
	dialPerAddr           = 10 * time.Second
	dialTotal             = 20 * time.Second
	maxDialAddrs          = 4
	responseHeaderTimeout = 60 * time.Second
	refusalWriteTimeout   = 10 * time.Second
	eventTimeout          = 5 * time.Second
	maxConns              = 64
	connWait              = 10 * time.Second
	acceptBackoffMin      = 5 * time.Millisecond
	acceptBackoffMax      = time.Second
	inFlightHeadroom      = 32 // MaxInFlight = MaxConns + this, by default
	maxEventHost          = 253
)

// Resolver is the DNS lookup the proxy uses; net.DefaultResolver in
// production, a fake in tests (A07 §12).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer opens upstream connections. The proxy passes it only checked IP
// addresses ("ip:port"), never a name (A07 §7). *net.Dialer in production.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Config configures one TaskProxy.
type Config struct {
	SandboxID      string
	SandboxPurpose string // "task" (default) or "harness" (ID-12)
	Task           TaskRef
	Allow          *Allowlist // required
	Events         EventSink  // required
	Decider        Decider    // nil: StaticDecider
	Resolver       Resolver   // nil: net.DefaultResolver
	Dialer         Dialer     // nil: &net.Dialer{}
	Now            func() time.Time
	CallID         func() string // tool call running in the sandbox; nil or "" is null
	Logger         *slog.Logger  // nil: discard

	// OperatorLiteralAllow lists canonical IP-literal keys ("10.0.0.5:8443")
	// whose address may be dialed although it is in a denied range (owner
	// decision 2026-10-16, replacing A07 §7's literal-entry exception). It is
	// not an allow: the target still needs an allowlist entry or a Decider
	// allow, and a harness sandbox gets no exemption (round-2 review F2). It
	// comes only from operator configuration; the Allowlist, the Decider,
	// grants and obligations can never add to it. New refuses entries in the
	// floor or in an IPv6 transition range (floorDenied, transitionPrefixes).
	OperatorLiteralAllow []string
	// InterfaceAddrs returns the host's own addresses; a resolved address
	// among them is dropped (owner decision 2026-10-16). nil: the system's
	// (net.InterfaceAddrs).
	InterfaceAddrs func() ([]netip.Addr, error)
	// Limits lowers the A07 §10 limits; zero fields and values above the
	// A07 defaults mean the default (limits may only be lowered).
	Limits Limits
}

// Limits are the per-task resource limits of A07 §10 that tests and
// configuration may lower.
type Limits struct {
	MaxConns    int           // concurrent connections past evaluation; 64
	ConnWait    time.Duration // how long connection 65 waits for a slot; 10 s
	IdleTimeout time.Duration // tunnel idle, plain-HTTP body and response stall; 300 s
	// MaxInFlight caps every connection the proxy holds, from accept on:
	// reading its request head, waiting for a slot, or running. Default
	// MaxConns+32 (CWE-770: the MaxConns slots alone leave the header phase
	// and the slot wait unbounded, and at L1 the proxy runs in the daemon).
	MaxInFlight int
}

// TaskProxy is the proxy of one sandbox (A07 §12). There is deliberately
// no way to set its credential injector: the PoC has only noInjector
// (A07 §11).
type TaskProxy struct {
	cfg      Config
	decider  Decider
	resolver Resolver
	dialer   Dialer
	now      func() time.Time
	log      *slog.Logger
	injector CredentialInjector
	operator map[string]bool // OperatorLiteralAllow, fixed at New
	ifaddrs  func() ([]netip.Addr, error)
	limits   Limits
	sem      chan struct{} // one token per admitted connection (A07 §10)
	inflight chan struct{} // one token per connection Serve holds (Limits.MaxInFlight)

	dnsMu sync.Mutex
	dns   map[string]dnsEntry
}

type dnsEntry struct {
	addrs []netip.Addr
	at    time.Time
}

// New returns a TaskProxy.
func New(cfg Config) (*TaskProxy, error) {
	if cfg.Allow == nil || cfg.Events == nil {
		return nil, errors.New("proxy: Config.Allow and Config.Events are required")
	}
	if cfg.SandboxPurpose == "" {
		cfg.SandboxPurpose = "task"
	}
	p := &TaskProxy{cfg: cfg, decider: cfg.Decider, resolver: cfg.Resolver, dialer: cfg.Dialer,
		now: cfg.Now, log: cfg.Logger, injector: noInjector{}, dns: make(map[string]dnsEntry),
		operator: make(map[string]bool), ifaddrs: cfg.InterfaceAddrs}
	for _, k := range cfg.OperatorLiteralAllow {
		t, err := Normalize(k)
		if err != nil || !t.IsIP() || t.Key() != k {
			return nil, fmt.Errorf("proxy: operator literal entry %q: want a canonical IP-literal host:port", k)
		}
		if floorDenied(t.IP) || in(t.IP, transitionPrefixes()) {
			return nil, fmt.Errorf("proxy: operator literal entry %q: in the floor or a transition range, never reachable", k)
		}
		p.operator[k] = true
	}
	if p.ifaddrs == nil {
		p.ifaddrs = systemInterfaceAddrs
	}
	if p.decider == nil {
		p.decider = StaticDecider{}
	}
	if p.resolver == nil {
		p.resolver = net.DefaultResolver
	}
	if p.dialer == nil {
		p.dialer = &net.Dialer{}
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.log == nil {
		p.log = slog.New(slog.DiscardHandler)
	}
	p.limits = lowerOnly(cfg.Limits)
	p.sem = make(chan struct{}, p.limits.MaxConns)
	p.inflight = make(chan struct{}, p.limits.MaxInFlight)
	return p, nil
}

// lowerOnly applies A07 §10's rule that limits may only be lowered: a
// field that is not positive, or above the A07 default, is the default.
func lowerOnly(l Limits) Limits {
	if l.MaxConns <= 0 || l.MaxConns > maxConns {
		l.MaxConns = maxConns
	}
	if l.ConnWait <= 0 || l.ConnWait > connWait {
		l.ConnWait = connWait
	}
	if l.IdleTimeout <= 0 || l.IdleTimeout > idleTimeout {
		l.IdleTimeout = idleTimeout
	}
	if d := l.MaxConns + inFlightHeadroom; l.MaxInFlight <= 0 || l.MaxInFlight > d {
		l.MaxInFlight = d
	}
	return l
}

// Serve accepts connections from ln, one goroutine each, until ctx ends or
// ln fails permanently. The caller owns ln (INV-J: this package opens no
// listener). A temporary accept error (EMFILE, ENFILE, ECONNABORTED and the
// like) is retried with backoff from 5 ms to 1 s, so a moment without file
// descriptors does not end the task's egress for good. When ctx ends,
// Serve closes ln and every open connection, waits for the handlers and
// writes any pending too_many_connections summary, then
// returns nil. At most Limits.MaxInFlight connections are held at once;
// the rest are closed as soon as they are accepted.
func (p *TaskProxy) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	var backoff time.Duration
	// Connections refused for MaxInFlight are not written one by one: a
	// flood would fill the store. Each burst (refusals with no accepted
	// connection between them) becomes one proxy.denied{too_many_connections,
	// count}, written when the next connection is accepted or Serve ends.
	dropped := 0
	flush := func() {
		if dropped > 0 {
			p.emitDenied(ctx, DeniedEvent{Reason: ReasonTooManyConnections, Count: dropped})
			dropped = 0
		}
	}
	for {
		c, err := ln.Accept()
		if err != nil && ctx.Err() == nil && isTemporary(err) {
			backoff = min(max(2*backoff, acceptBackoffMin), acceptBackoffMax)
			p.log.Warn("proxy accept failed; retrying", "err", err, "backoff", backoff)
			t := time.NewTimer(backoff)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
			}
			continue
		}
		backoff = 0
		if err != nil {
			parentDone := ctx.Err() != nil
			cancel()
			wg.Wait()
			flush()
			if parentDone {
				return nil
			}
			return fmt.Errorf("proxy: accept: %w", err)
		}
		select {
		case p.inflight <- struct{}{}:
		default:
			// Over MaxInFlight: close at once, before any read or
			// goroutine, and count it for the burst's summary.
			_ = c.Close()
			dropped++
			continue
		}
		flush() // an accepted connection ends the burst
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-p.inflight }()
			p.ServeConn(ctx, c)
		}()
	}
}

// ServeConn handles one client connection: one request, then close. It
// returns when the request, tunnel or refusal is finished, or ctx ends.
func (p *TaskProxy) ServeConn(ctx context.Context, c net.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	defer c.Close()

	callID := ""
	if p.cfg.CallID != nil {
		callID = p.cfg.CallID()
	}
	// The header limit applies to what is read from the connection until
	// the request head is parsed; the body and the tunnel are unlimited.
	lr := &limitReader{r: c, n: headerLimit}
	br := bufio.NewReader(lr)
	_ = c.SetReadDeadline(time.Now().Add(headerTimeout))
	req, err := http.ReadRequest(br)
	if err != nil {
		// A connection closed before its first byte (io.EOF exactly), or
		// cut by shutdown, attempted nothing: no event.
		if err == io.EOF || ctx.Err() != nil {
			return
		}
		p.refuse(ctx, c, http.StatusBadRequest, "", DeniedEvent{Reason: ReasonUnsupportedMethod, CallID: callID})
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	lr.unlimit()
	if !validMethod(req.Method) {
		// net/http accepts any token; no client sends one that is not
		// upper-case letters, and the raw token never reaches an event.
		p.refuse(ctx, c, http.StatusBadRequest, "", DeniedEvent{Reason: ReasonUnsupportedMethod, CallID: callID})
		return
	}

	switch {
	case req.Method == http.MethodConnect:
		p.connect(ctx, c, br, req.RequestURI, callID)
	case req.Header.Get("Upgrade") != "":
		host, port := urlHostPort(req)
		p.refuse(ctx, c, http.StatusNotImplemented, "", DeniedEvent{Host: host, Port: port, Method: req.Method, Reason: ReasonUnsupportedMethod, CallID: callID})
	case req.URL.IsAbs() && req.URL.Scheme == "http":
		p.plain(ctx, c, req, callID)
	default:
		// https:// absolute form (clients must CONNECT), origin form,
		// asterisk form and the HTTP/2 preface (A07 §3.1).
		host, port := urlHostPort(req)
		p.refuse(ctx, c, http.StatusBadRequest, "", DeniedEvent{Host: host, Port: port, Method: req.Method, Reason: ReasonUnsupportedMethod, CallID: callID})
	}
}

// verdict is an allow: the event rule and decision id, and whether an
// operator entry lifts the denied-range check for this literal (the only
// way past AddrDenied, never past the floor; owner decision 2026-10-16).
// operator is set only on an allow that came from the allowlist or the
// Decider, never in a harness sandbox (round-2 review F2).
type verdict struct {
	rule, decisionID string
	operator         bool
}

// admit runs A07 §3.2 steps 1 to 4 for an authority: normalise, evaluate,
// resolve and range-check, dial the checked address. On a refusal it has
// already answered the client and written proxy.denied; on a dial failure
// it has answered 502 and written proxy.connect{error: dial_failed}. ok
// reports whether the caller has an upstream connection to use; it must
// then call release when the connection ends.
//
// Between evaluation and resolution the connection takes one of the task's
// MaxConns slots (A07 §10), waiting at most ConnWait; a refusal from
// policy never uses a slot, so denials cannot starve allowed traffic.
func (p *TaskProxy) admit(ctx context.Context, c net.Conn, authority, method, callID string) (t Target, v verdict, up net.Conn, ip netip.Addr, release func(), ok bool) {
	t, v, up, ip, ok = p.admitSlot(ctx, c, authority, method, callID)
	if !ok {
		return t, v, nil, ip, nil, false
	}
	return t, v, up, ip, func() { <-p.sem }, true
}

func (p *TaskProxy) admitSlot(ctx context.Context, c net.Conn, authority, method, callID string) (t Target, v verdict, up net.Conn, ip netip.Addr, ok bool) {
	t, err := Normalize(authority)
	if err != nil {
		var de *DenyError
		if !errors.As(err, &de) {
			de = deny(ReasonInvalidTarget, err.Error())
		}
		host, port := rawHostPort(authority)
		status := http.StatusBadRequest
		if de.Reason == ReasonPrivateAddress {
			status = http.StatusForbidden
		}
		p.refuse(ctx, c, status, "", DeniedEvent{Host: host, Port: port, Method: method, Reason: de.Reason, CallID: callID})
		return t, v, nil, ip, false
	}
	ev := DeniedEvent{Host: t.Host, Port: t.Port, Method: method, CallID: callID}
	v, de := p.evaluate(ctx, t, method, callID)
	ev.Rule, ev.DecisionID = v.rule, v.decisionID
	if de != nil {
		ev.Reason = de.Reason
		p.refuse(ctx, c, http.StatusForbidden, t.Key(), ev)
		return t, v, nil, ip, false
	}
	if !p.acquire(ctx) {
		ev.Reason = ReasonTooManyConnections
		p.refuse(ctx, c, http.StatusServiceUnavailable, t.Key(), ev)
		return t, v, nil, ip, false
	}
	defer func() {
		if !ok { // refused or failed after taking the slot: give it back
			<-p.sem
		}
	}()
	addrs, de := p.resolve(ctx, t, v)
	if de != nil {
		ev.Reason = de.Reason
		status := http.StatusForbidden
		if de.Reason == ReasonDNSFailed {
			status = http.StatusBadGateway
		}
		p.refuse(ctx, c, status, t.Key(), ev)
		return t, v, nil, ip, false
	}
	up, ip, err = p.dial(ctx, t, addrs)
	if err != nil {
		p.log.Warn("proxy dial failed", "target", t.Key(), "err", err)
		p.writeStatus(c, http.StatusBadGateway, "warden: egress to "+t.Key()+" failed: dial_failed\n")
		p.emitConnect(ctx, ConnectEvent{Host: t.Host, Port: t.Port, Method: method, Rule: v.rule,
			DecisionID: v.decisionID, CallID: callID, Error: ConnErrDialFailed})
		return t, v, nil, ip, false
	}
	return t, v, up, ip, true
}

// evaluate is A07 §5.2 without the M3 steps (deny cache, harness rule,
// one-shot grants, hold): the allowlist fast path, then the Decider. Any
// effect other than allow, and any Decider error, denies (fail closed);
// approval_required is a denial until the hold lands in M3. An allow here
// is still subject to resolve's range checks. An operator entry is not an
// allow: it only marks an allow of this exact literal as exempt from the
// denied ranges, and never in a harness sandbox (ID-12: harness egress is
// the vendor list only).
func (p *TaskProxy) evaluate(ctx context.Context, t Target, method, callID string) (verdict, *DenyError) {
	exempt := p.operator[t.Key()] && p.cfg.SandboxPurpose != "harness"
	if e, ok := p.cfg.Allow.Get(t.Key()); ok {
		return verdict{rule: e.Rule, decisionID: e.DecisionID, operator: exempt}, nil
	}
	d, err := p.decider.DecideEgress(ctx, EgressRequest{
		Task: p.cfg.Task, SandboxID: p.cfg.SandboxID, SandboxPurpose: p.cfg.SandboxPurpose,
		CallID: callID, Method: method, Target: t, Allowlist: p.cfg.Allow.Keys(),
	})
	if err != nil {
		return verdict{}, &DenyError{Reason: ReasonPolicyDenied, Detail: "decider", Err: err}
	}
	if d.Effect != EffectAllow {
		return verdict{rule: d.Rule, decisionID: d.DecisionID}, deny(ReasonPolicyDenied, d.Reason)
	}
	// A07 §5.2 step 4: cache the allow for the task.
	if err := p.cfg.Allow.Add(Entry{Key: t.Key(), Source: SourceGrant, Rule: d.Rule, DecisionID: d.DecisionID}); err != nil {
		return verdict{}, &DenyError{Reason: ReasonPolicyDenied, Detail: "caching allow", Err: err}
	}
	return verdict{rule: d.Rule, decisionID: d.DecisionID, operator: exempt}, nil
}

// resolve returns the addresses to dial (A07 §7, as changed by the owner's
// decisions of 2026-10-16). Whatever allowed the target:
//
//   - an address in the floor is never dialed;
//   - an address of the host's own interfaces is never dialed, and if the
//     interface list cannot be read nothing is (fail closed);
//   - any other denied address is dialed only as an IP literal named by an
//     operator entry; a resolved name gets no exception.
//
// A name's answer keeps resolver order, without duplicates, at most four.
func (p *TaskProxy) resolve(ctx context.Context, t Target, v verdict) ([]netip.Addr, *DenyError) {
	own, err := p.ifaddrs()
	if err != nil {
		return nil, &DenyError{Reason: ReasonPrivateAddress, Detail: "host interface addresses unavailable", Err: err}
	}
	isOwn := func(a netip.Addr) bool {
		for _, o := range own {
			if o.WithZone("").Unmap() == a {
				return true
			}
		}
		return false
	}
	if t.IsIP() {
		a := t.IP.WithZone("").Unmap()
		switch {
		case floorDenied(a):
			return nil, deny(ReasonPrivateAddress, "literal address in the floor")
		case isOwn(a):
			return nil, deny(ReasonPrivateAddress, "literal address of this host")
		case AddrDenied(a) && !v.operator:
			return nil, deny(ReasonPrivateAddress, "literal address in a denied range")
		}
		return []netip.Addr{a}, nil
	}
	answer, err := p.lookup(ctx, t.Host)
	if err != nil {
		return nil, &DenyError{Reason: ReasonDNSFailed, Err: err}
	}
	var out []netip.Addr
	for _, a := range answer {
		a = a.WithZone("").Unmap()
		if AddrDenied(a) || isOwn(a) || containsAddr(out, a) {
			continue
		}
		out = append(out, a)
		if len(out) == maxDialAddrs {
			break
		}
	}
	if len(out) == 0 {
		if len(answer) == 0 {
			return nil, deny(ReasonDNSFailed, "empty answer")
		}
		return nil, deny(ReasonPrivateAddress, "every address is in a denied range")
	}
	return out, nil
}

// lookup resolves host once per dnsCacheTTL for this task, so all
// connections in that window see the same answer (A07 §7).
func (p *TaskProxy) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	now := p.now()
	p.dnsMu.Lock()
	if e, ok := p.dns[host]; ok {
		if now.Sub(e.at) < dnsCacheTTL {
			p.dnsMu.Unlock()
			return e.addrs, nil
		}
		delete(p.dns, host)
	}
	p.dnsMu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	addrs, err := p.resolver.LookupNetIP(lctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("proxy: resolve %s: %w", host, err)
	}
	addrs = append([]netip.Addr(nil), addrs...)

	p.dnsMu.Lock()
	defer p.dnsMu.Unlock()
	if len(p.dns) >= dnsCacheMax {
		for h, e := range p.dns {
			if now.Sub(e.at) >= dnsCacheTTL {
				delete(p.dns, h)
			}
		}
	}
	if len(p.dns) < dnsCacheMax {
		p.dns[host] = dnsEntry{addrs: addrs, at: now}
	}
	return addrs, nil
}

// dial connects to the checked addresses in order: 10 s each, 20 s in
// total (A07 §10). The dialer sees "ip:port" only.
func (p *TaskProxy) dial(ctx context.Context, t Target, addrs []netip.Addr) (net.Conn, netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTotal)
	defer cancel()
	var lastErr error
	for _, a := range addrs {
		dctx, dcancel := context.WithTimeout(ctx, dialPerAddr)
		c, err := p.dialer.DialContext(dctx, "tcp", netip.AddrPortFrom(a, t.Port).String())
		dcancel()
		if err == nil {
			return c, a, nil
		}
		lastErr = err
	}
	return nil, netip.Addr{}, fmt.Errorf("proxy: dial %s: %w", t.Key(), lastErr)
}

// refuse answers the client with a one-line refusal and writes
// proxy.denied. key is the canonical target for the body, "" when there is
// none; the body never echoes a path or a raw target.
func (p *TaskProxy) refuse(ctx context.Context, c net.Conn, status int, key string, ev DeniedEvent) {
	p.emitDenied(ctx, ev)
	body := "warden: request refused: " + string(ev.Reason) + "\n"
	if key != "" {
		body = "warden: egress to " + key + " denied: " + string(ev.Reason) + "\n"
	}
	p.writeStatus(c, status, body)
}

func (p *TaskProxy) writeStatus(c net.Conn, status int, body string) {
	_ = c.SetWriteDeadline(time.Now().Add(refusalWriteTimeout))
	_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
	_ = c.SetWriteDeadline(time.Time{})
}

// eventContext detaches event writing from the connection's context. The
// final proxy.connect of a tunnel is written when the tunnel closes, and
// at sandbox.destroy that close is caused by cancelling Serve's context; a
// write with that context is refused by the store and the event, with its
// byte counts, is lost (CWE-778; A07 §2 "Close" requires every proxy event
// to be written before sandbox.destroy). The values (ids, deadlines of
// callers) are kept; only cancellation is dropped, and eventTimeout bounds
// the write instead.
func eventContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), eventTimeout)
}

func (p *TaskProxy) emitDenied(ctx context.Context, ev DeniedEvent) {
	ev.SandboxID = p.cfg.SandboxID
	ctx, cancel := eventContext(ctx)
	defer cancel()
	if err := p.cfg.Events.ProxyDenied(ctx, ev); err != nil {
		p.log.Error("proxy.denied not written", "err", err)
	}
}

func (p *TaskProxy) emitConnect(ctx context.Context, ev ConnectEvent) {
	ev.SandboxID = p.cfg.SandboxID
	ctx, cancel := eventContext(ctx)
	defer cancel()
	if err := p.cfg.Events.ProxyConnect(ctx, ev); err != nil {
		p.log.Error("proxy.connect not written", "err", err)
	}
}

// limitReader fails with errHeaderTooLarge once n bytes have been read,
// until unlimit is called.
type limitReader struct {
	r         io.Reader
	n         int
	unlimited bool
}

var errHeaderTooLarge = errors.New("proxy: request head exceeds 16 KiB")

func (l *limitReader) Read(b []byte) (int, error) {
	if l.unlimited {
		return l.r.Read(b)
	}
	if l.n <= 0 {
		return 0, errHeaderTooLarge
	}
	if len(b) > l.n {
		b = b[:l.n]
	}
	k, err := l.r.Read(b)
	l.n -= k
	return k, err
}

func (l *limitReader) unlimit() { l.unlimited = true }

// urlHostPort is the target of an absolute-form request that is refused
// before evaluation, canonical when it normalises.
func urlHostPort(req *http.Request) (string, uint16) {
	if !req.URL.IsAbs() || req.URL.Host == "" {
		return "", 0
	}
	port := req.URL.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(req.URL.Scheme, "https") {
			port = "443"
		}
	}
	authority := net.JoinHostPort(req.URL.Hostname(), port)
	if t, err := Normalize(authority); err == nil {
		return t.Host, t.Port
	}
	return rawHostPort(authority)
}

func containsAddr(list []netip.Addr, a netip.Addr) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

// systemInterfaceAddrs is the production Config.InterfaceAddrs: every
// address configured on the host's interfaces.
func systemInterfaceAddrs() ([]netip.Addr, error) {
	ifas, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("proxy: interface addresses: %w", err)
	}
	out := make([]netip.Addr, 0, len(ifas))
	for _, ia := range ifas {
		var ip net.IP
		switch v := ia.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if a, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, a.Unmap())
		}
	}
	return out, nil
}

// rawHostPort splits an authority that failed normalisation for the event.
// Userinfo is cut at the last "@" first, so "user:pw@host:443" gives
// "host" and 443 and no credential reaches the audit chain (INV-C); the
// host is then escaped and capped by eventHost; the port is kept when it
// parses.
func rawHostPort(authority string) (string, uint16) {
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		authority = authority[i+1:]
	}
	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		host, portText = authority, ""
	}
	port, _ := parsePort(portText)
	return eventHost(host), port
}

// eventHost makes attacker-supplied host text safe for proxy.denied.host:
// it drops everything up to the last "@", writes printable ASCII as is,
// escapes a backslash as \\, a byte of invalid UTF-8 as \xNN and every
// other rune as a Go escape (x-hex for control bytes, or backslash-u and four hex digits for
// e-acute or RIGHT-TO-LEFT OVERRIDE), and stops before the escape
// that would exceed maxEventHost bytes, so an escape is never split.
func eventHost(s string) string {
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	var b strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		var piece string
		switch {
		case r == utf8.RuneError && size == 1:
			piece = fmt.Sprintf(`\x%02x`, s[0])
		case r == '\\':
			piece = `\\`
		case r >= 0x20 && r < 0x7f:
			piece = string(r)
		default:
			q := strconv.QuoteRuneToASCII(r) // 'x' with escapes inside
			piece = q[1 : len(q)-1]
		}
		if b.Len()+len(piece) > maxEventHost {
			break
		}
		b.WriteString(piece)
		s = s[size:]
	}
	return b.String()
}

// validMethod reports whether m matches ^[A-Z]+$.
func validMethod(m string) bool {
	if m == "" {
		return false
	}
	for i := 0; i < len(m); i++ {
		if m[i] < 'A' || m[i] > 'Z' {
			return false
		}
	}
	return true
}

// acquire takes one of the task's connection slots, waiting at most
// ConnWait (A07 §10: the 65th connection waits up to 10 s, then 503).
func (p *TaskProxy) acquire(ctx context.Context) bool {
	select {
	case p.sem <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(p.limits.ConnWait)
	defer t.Stop()
	select {
	case p.sem <- struct{}{}:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// isTemporary reports whether an accept error is worth retrying: it says
// so itself (syscall.Errno does for EMFILE, ENFILE, ECONNABORTED, EAGAIN,
// EINTR and others) or is a timeout. net.ErrClosed is never temporary.
func isTemporary(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return false
	}
	var t interface{ Temporary() bool }
	if errors.As(err, &t) && t.Temporary() {
		return true
	}
	var to interface{ Timeout() bool }
	return errors.As(err, &to) && to.Timeout()
}

// inFlight is the number of connections Serve currently holds.
func (p *TaskProxy) inFlight() int { return len(p.inflight) }
