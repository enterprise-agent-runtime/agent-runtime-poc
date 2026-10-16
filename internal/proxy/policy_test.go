package proxy

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
)

// allowAll is a Decider that allows every destination it is asked about,
// the worst case a buggy policy or a careless approval can produce.
func allowAll() *fakeDecider {
	return &fakeDecider{d: Decision{Effect: EffectAllow, Rule: "grant.apr_12", DecisionID: "dec_01"}}
}

// TestFloor_NoAllowOpens: no allow from any source opens the floor (owner
// decision 2026-10-16, contradicts A07 §7). Each target is on the
// allowlist and the Decider allows everything: all are refused with
// private_address and never dialed. IPv4-mapped, NAT64 and 6to4 spellings
// are covered because each reaches the same IPv4 address through a
// translator. (An operator entry in the floor is refused by New, see
// TestOperatorLiteral.)
func TestFloor_NoAllowOpens(t *testing.T) {
	targets := []string{
		"169.254.169.254:80", "[::ffff:169.254.169.254]:80", "[::ffff:127.0.0.1]:80", "127.0.0.1:80",
		"0.0.0.0:80", "[::1]:80", "[::]:80", "[fe80::1]:80", "[fd00:ec2::254]:80",
		"[64:ff9b::a9fe:a9fe]:80", "[2002:7f00:1::]:80", "127.0.0.1:8080",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			tg, err := Normalize(target)
			if err != nil {
				t.Fatal(err)
			}
			h := newHarnessWith(t, func(c *Config) {
				c.Decider = allowAll()
			}, tg.Key())
			h.dial.upstream = func(c net.Conn) { _ = c.Close() }
			if s := tunnel(t, h, target, nil); s != 403 {
				t.Errorf("status %d, want 403", s)
			}
			if d := h.dial.dialed(); len(d) != 0 {
				t.Errorf("dialed %v", d)
			}
			_, denies := h.sink.snapshot()
			if len(denies) != 1 || denies[0].Reason != ReasonPrivateAddress {
				t.Errorf("events %+v; want one proxy.denied(private_address)", denies)
			}
		})
	}
}

// TestOperatorLiteral: an operator entry (Config.OperatorLiteralAllow) only
// lifts the denied-range check for that exact literal; it is not an allow
// (round-2 review F2, CWE-863; coordinator decision, the stricter reading
// of A07 §5.1/§5.2 and ID-12). The request still needs an allow from the
// task allowlist or the Decider, so the policy is asked and a
// policy.decision is written, and a harness sandbox gets no exemption.
// Without an operator entry, an allowlist entry or grant for a denied
// literal is refused (owner decision 2026-10-16, contradicts A07 §7).
func TestOperatorLiteral(t *testing.T) {
	cases := []struct {
		name     string
		target   string
		operator []string
		allow    []string
		decider  *fakeDecider
		purpose  string
		status   int
		reason   Reason // for refusals
		asked    bool   // whether the Decider must have been asked
	}{
		{"allowlist entry + operator entry", "10.0.0.5:443", []string{"10.0.0.5:443"}, []string{"10.0.0.5:443"}, nil, "", 200, "", false},
		{"grant + operator entry", "10.0.0.5:443", []string{"10.0.0.5:443"}, nil, allowAll(), "", 200, "", true},
		{"mapped spelling, allowlist + operator", "[::ffff:10.0.0.5]:443", []string{"10.0.0.5:443"}, []string{"10.0.0.5:443"}, nil, "", 200, "", false},
		{"operator entry alone", "10.0.0.5:443", []string{"10.0.0.5:443"}, nil, &fakeDecider{d: Decision{Effect: EffectDeny, Rule: "user.egress-deny", DecisionID: "dec_09"}}, "", 403, ReasonPolicyDenied, true},
		{"operator entry, other port", "10.0.0.5:22", []string{"10.0.0.5:443"}, []string{"10.0.0.5:22"}, nil, "", 403, ReasonPrivateAddress, false},
		{"allowlist entry only", "10.0.0.6:443", nil, []string{"10.0.0.6:443"}, nil, "", 403, ReasonPrivateAddress, false},
		{"grant only", "192.168.1.1:443", nil, nil, allowAll(), "", 403, ReasonPrivateAddress, true},
		{"harness sandbox, allowlist + operator", "10.0.0.5:443", []string{"10.0.0.5:443"}, []string{"10.0.0.5:443"}, nil, "harness", 403, ReasonPrivateAddress, false},
		{"public literal on allowlist", "93.184.215.14:443", nil, []string{"93.184.215.14:443"}, nil, "", 200, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, func(c *Config) {
				c.OperatorLiteralAllow = tc.operator
				if tc.decider != nil {
					c.Decider = tc.decider
				}
				c.SandboxPurpose = tc.purpose
			}, tc.allow...)
			up := &discardUpstream{}
			h.dial.upstream = up.serve
			if s := tunnel(t, h, tc.target, record(handshake(helloBody()))); s != tc.status {
				t.Errorf("status %d, want %d", s, tc.status)
			}
			if tc.status != 200 {
				if len(h.dial.dialed()) != 0 {
					t.Errorf("dialed %v", h.dial.dialed())
				}
				_, denies := h.sink.snapshot()
				if len(denies) != 1 || denies[0].Reason != tc.reason {
					t.Errorf("events %+v; want one proxy.denied(%s)", denies, tc.reason)
				}
			}
			if tc.decider != nil && (len(tc.decider.calls()) > 0) != tc.asked {
				t.Errorf("Decider asked %d times, want asked=%v", len(tc.decider.calls()), tc.asked)
			}
			if tc.status == 200 {
				conns, _ := h.sink.snapshot()
				if len(conns) != 1 || conns[0].Rule == "operator.literal-allow" {
					t.Errorf("proxy.connect %+v; want the allow's own rule, not an operator rule", conns)
				}
			}
		})
	}

	// New refuses operator entries that are not canonical IP literals, that
	// are in the floor, or that are in a transition range whose embedded
	// IPv4 address the floor cannot see (round-2 review F3, CWE-918):
	// IPv4-compatible, SIIT, local-use NAT64, Teredo, NAT64 and 6to4.
	for _, bad := range []string{
		"registry.npmjs.org:443", "[::ffff:10.0.0.5]:443", "10.0.0.5", "10.0.0.0/8:443",
		"127.0.0.1:8080", "169.254.169.254:80", "0.0.0.0:80", "[::1]:80", "[fe80::1]:80", "[fd00:ec2::254]:80",
		"[::7f00:1]:80", "[::a00:5]:443", "[::ffff:0:a9fe:a9fe]:80", "[64:ff9b:1::a9fe:a9fe]:80",
		"[2001:0:4136:e378:8000:63bf:3fff:fdd2]:80", "[64:ff9b::a00:5]:443", "[2002:a00:5::]:443",
	} {
		allow, _ := NewAllowlist()
		if _, err := New(Config{Allow: allow, Events: &fakeSink{}, OperatorLiteralAllow: []string{bad}}); err == nil {
			t.Errorf("New accepted operator entry %q", bad)
		}
	}
	for _, good := range []string{"10.0.0.5:443", "[fd12::1]:443", "192.168.1.1:8443"} {
		allow, _ := NewAllowlist()
		if _, err := New(Config{Allow: allow, Events: &fakeSink{}, OperatorLiteralAllow: []string{good}}); err != nil {
			t.Errorf("New refused operator entry %q: %v", good, err)
		}
	}
}

// TestInterfaceAddrs_Dropped: an address of the host's own interfaces is
// never dialed (owner decision 2026-10-16): a public DNS name pointed at
// the daemon host's public address would otherwise reach services bound to
// that address. If the interface list cannot be read, the proxy refuses
// rather than guessing.
func TestInterfaceAddrs_Dropped(t *testing.T) {
	own := func() ([]netip.Addr, error) { return addrs("93.184.215.99", "2606:4700::99"), nil }
	h := newHarnessWith(t, func(c *Config) { c.InterfaceAddrs = own },
		"mixed.example:443", "self.example:443", "93.184.215.99:443")
	h.res.set("mixed.example", addrs("93.184.215.99", "2606:4700::99", "93.184.215.14"))
	h.res.set("self.example", addrs("93.184.215.99", "::ffff:93.184.215.99"))
	up := &discardUpstream{}
	h.dial.upstream = up.serve
	if s := tunnel(t, h, "mixed.example:443", clientHello("mixed.example")); s != 200 {
		t.Errorf("mixed.example status %d", s)
	}
	if d := h.dial.dialed(); len(d) != 1 || d[0] != "tcp 93.184.215.14:443" {
		t.Errorf("dialed %v; want only the address that is not the host's", d)
	}
	if s := tunnel(t, h, "self.example:443", clientHello("self.example")); s != 403 {
		t.Errorf("self.example status %d, want 403", s)
	}
	if s := tunnel(t, h, "93.184.215.99:443", nil); s != 403 {
		t.Errorf("literal own address status %d, want 403", s)
	}
	if n := len(h.dial.dialed()); n != 1 {
		t.Errorf("%d dials, want 1", n)
	}

	broken := newHarnessWith(t, func(c *Config) {
		c.InterfaceAddrs = func() ([]netip.Addr, error) { return nil, errors.New("netlink: permission denied") }
	}, "mixed.example:443")
	broken.res.set("mixed.example", addrs("93.184.215.14"))
	broken.dial.upstream = func(c net.Conn) { _, _ = io.Copy(io.Discard, c); _ = c.Close() }
	if s := tunnel(t, broken, "mixed.example:443", clientHello("mixed.example")); s != 403 {
		t.Errorf("unreadable interface list: status %d, want 403 (fail closed)", s)
	}
}

// TestDecider_FailClosed (review of 7ad63b0: surviving mutants). A Decider
// error, approval_required (no hold in M2), deny and an unknown effect all
// give 403 policy_denied with no resolution and no dial; an allow is
// cached in the allowlist as a grant, so the second connection does not
// ask the Decider again.
func TestDecider_FailClosed(t *testing.T) {
	cases := []struct {
		name string
		dec  *fakeDecider
		rule string
	}{
		{"error", &fakeDecider{err: errors.New("pdp unavailable"), d: Decision{Effect: EffectAllow}}, ""},
		{"approval required", &fakeDecider{d: Decision{Effect: EffectApprovalRequired, Rule: "user.egress-other", DecisionID: "dec_07", ApprovalID: "apr_12"}}, "user.egress-other"},
		{"deny", &fakeDecider{d: Decision{Effect: EffectDeny, Rule: "user.egress-deny", DecisionID: "dec_08"}}, "user.egress-deny"},
		{"unknown effect", &fakeDecider{d: Decision{Effect: "maybe", Rule: "x"}}, "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, func(c *Config) { c.Decider = tc.dec }, "registry.npmjs.org:443")
			h.res.set("collector.example.net", addrs("93.184.215.14"))
			h.dial.upstream = func(c net.Conn) { _ = c.Close() }
			if s := tunnel(t, h, "collector.example.net:443", nil); s != 403 {
				t.Errorf("status %d, want 403", s)
			}
			if d := h.dial.dialed(); len(d) != 0 {
				t.Errorf("dialed %v", d)
			}
			if h.res.count("collector.example.net") != 0 {
				t.Error("a denied name was resolved")
			}
			_, denies := h.sink.snapshot()
			if len(denies) != 1 || denies[0].Reason != ReasonPolicyDenied || denies[0].Rule != tc.rule {
				t.Errorf("events %+v; want one proxy.denied(policy_denied, rule %q)", denies, tc.rule)
			}
			if _, ok := h.p.cfg.Allow.Get("collector.example.net:443"); ok {
				t.Error("a non-allow was cached in the allowlist")
			}
		})
	}

	dec := allowAll()
	h := newHarnessWith(t, func(c *Config) { c.Decider = dec }, "registry.npmjs.org:443")
	h.res.set("collector.example.net", addrs("93.184.215.14"))
	up := &discardUpstream{}
	h.dial.upstream = up.serve
	for i := 0; i < 2; i++ {
		if s := tunnel(t, h, "collector.example.net:443", clientHello("collector.example.net")); s != 200 {
			t.Fatalf("allow #%d: status %d", i+1, s)
		}
	}
	reqs := dec.calls()
	if len(reqs) != 1 {
		t.Fatalf("Decider asked %d times, want 1 (the allow is cached)", len(reqs))
	}
	r := reqs[0]
	if r.Target.Key() != "collector.example.net:443" || r.Method != "CONNECT" || r.CallID != "call_31" ||
		r.SandboxID != testSandbox || r.SandboxPurpose != "task" || len(r.Allowlist) != 1 || r.Allowlist[0] != "registry.npmjs.org:443" {
		t.Errorf("EgressRequest = %+v", r)
	}
	e, ok := h.p.cfg.Allow.Get("collector.example.net:443")
	if !ok || e.Source != SourceGrant || e.Rule != "grant.apr_12" || e.DecisionID != "dec_01" {
		t.Errorf("cached entry %+v, %v; want source grant with the decision's rule and id", e, ok)
	}
	conns, _ := h.sink.snapshot()
	if len(conns) != 2 || conns[0].Rule != "grant.apr_12" || conns[0].DecisionID != "dec_01" {
		t.Errorf("proxy.connect events %+v; want the grant's rule and decision id", conns)
	}
}
