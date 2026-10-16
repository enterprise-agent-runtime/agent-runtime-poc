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
	"strings"
	"sync"
	"time"
)

// A07 §10 limits. Configuration may only lower them; M2 has no
// configuration, so they are the values used.
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
	// that the operator allows although the address is in a denied range
	// (owner decision 2026-10-16, replacing A07 §7's literal-entry
	// exception). It comes only from operator configuration: the Allowlist,
	// the Decider, grants and obligations can never add to it. It never
	// opens the floor (floorDenied).
	OperatorLiteralAllow []string
	// InterfaceAddrs returns the host's own addresses; a resolved address
	// among them is dropped (owner decision 2026-10-16). nil: the system's
	// (net.InterfaceAddrs).
	InterfaceAddrs func() ([]netip.Addr, error)
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
	return p, nil
}

// Serve accepts connections from ln, one goroutine each, until ctx ends or
// ln fails. The caller owns ln (INV-J: this package opens no listener).
// When ctx ends, Serve closes ln and every open connection, waits for the
// handlers and returns nil.
func (p *TaskProxy) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	for {
		c, err := ln.Accept()
		if err != nil {
			parentDone := ctx.Err() != nil
			cancel()
			wg.Wait()
			if parentDone {
				return nil
			}
			return fmt.Errorf("proxy: accept: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
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
// operator entry names this literal IP (the only way past AddrDenied, and
// never past the floor; owner decision 2026-10-16).
type verdict struct {
	rule, decisionID string
	operator         bool
}

// RuleOperatorLiteral is the event rule of a connection allowed by
// Config.OperatorLiteralAllow.
const RuleOperatorLiteral = "operator.literal-allow"

// admit runs A07 §3.2 steps 1 to 4 for an authority: normalise, evaluate,
// resolve and range-check, dial the checked address. On a refusal it has
// already answered the client and written proxy.denied; on a dial failure
// it has answered 502 and written proxy.connect{error: dial_failed}. ok
// reports whether the caller has an upstream connection to use.
func (p *TaskProxy) admit(ctx context.Context, c net.Conn, authority, method, callID string) (t Target, v verdict, up net.Conn, ip netip.Addr, ok bool) {
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
// one-shot grants, hold): operator literal entries, the allowlist fast
// path, then the Decider. Any effect other than allow, and any Decider
// error, denies (fail closed); approval_required is a denial until the
// hold lands in M3. An allow here is still subject to resolve's range
// checks.
func (p *TaskProxy) evaluate(ctx context.Context, t Target, method, callID string) (verdict, *DenyError) {
	if p.operator[t.Key()] {
		return verdict{rule: RuleOperatorLiteral, operator: true}, nil
	}
	if e, ok := p.cfg.Allow.Get(t.Key()); ok {
		return verdict{rule: e.Rule, decisionID: e.DecisionID}, nil
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
	return verdict{rule: d.Rule, decisionID: d.DecisionID}, nil
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

// rawHostPort splits an authority that failed normalisation for the event:
// the host capped at 253 bytes, the port when it parses.
func rawHostPort(authority string) (string, uint16) {
	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		host, portText = authority, ""
	}
	if len(host) > maxEventHost {
		host = host[:maxEventHost]
	}
	port, _ := parsePort(portText)
	return host, port
}

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
