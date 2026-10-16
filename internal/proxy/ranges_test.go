package proxy

import (
	"net/netip"
	"testing"
)

// TestRangeCheck is A07 §13 "Range table": one address inside every denied
// prefix of A07 §7 (first, last or a typical member), the IPv4-mapped,
// NAT64 (64:ff9b::/96) and 6to4 (2002::/16) embeddings of denied IPv4
// addresses, and public addresses that must stay reachable. It exists
// because SSRF to the cloud metadata endpoint (169.254.169.254) or to a
// service on the host's loopback is the attack the proxy is there to stop,
// and IPv6 spellings of an IPv4 address are the classic way past a check
// that only looks at IPv4.
func TestRangeCheck(t *testing.T) {
	denied := []string{
		// unspecified and loopback
		"0.0.0.0", "0.255.255.255", "127.0.0.1", "127.255.255.254", "::", "::1",
		// RFC 1918
		"10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.0.1", "192.168.255.255",
		// CGNAT
		"100.64.0.1", "100.127.255.255",
		// link-local and metadata
		"169.254.169.254", "169.254.0.1", "fe80::1", "febf:ffff::1",
		// IPv6 ULA (fd00:ec2::254 is the AWS IMDS IPv6 address)
		"fc00::1", "fd00:ec2::254", "fdff:ffff::1",
		// special purpose, benchmarking, documentation, discard
		"192.0.0.1", "192.0.0.255", "192.0.2.1", "198.18.0.1", "198.19.255.255", "198.51.100.7",
		"203.0.113.9", "2001:db8::1", "2001:db8:ffff::1", "100::1", "100::ffff:ffff:ffff:ffff",
		// multicast, reserved, broadcast
		"224.0.0.1", "239.255.255.255", "240.0.0.1", "254.255.255.255", "255.255.255.255", "ff02::1", "ff0e::1",
		// IPv4-mapped IPv6 of denied IPv4 addresses
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1", "::ffff:0.0.0.0",
		// NAT64 well-known prefix embedding a denied IPv4 address
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::10.1.2.3", "64:ff9b::192.168.1.1",
		// 6to4 embedding a denied IPv4 address (bytes 2..5)
		"2002:7f00:1::", "2002:a9fe:a9fe::1", "2002:0a00:0001:ffff::1", "2002:c0a8:0101::",
		// Owner decision 2026-10-16, stricter than A07 §7: whole prefixes of
		// IPv4-compatible (::/96), SIIT (::ffff:0:0:0/96), local-use NAT64
		// (64:ff9b:1::/48), Teredo (2001::/32), 6to4 relay anycast
		// (192.88.99.0/24), deprecated site-local (fec0::/10), benchmarking
		// (2001:2::/48) and ORCHID (2001:10::/28), public payload or not.
		"::5db8:d70e", "::1.1.1.1", "::ffff:0:5db8:d70e", "::ffff:0:1.1.1.1", "64:ff9b:1::5db8:d70e", "64:ff9b:1:ffff::1",
		"2001::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "192.88.99.1", "192.88.99.255",
		"fec0::1", "feff::1", "2001:2::1", "2001:2:0:ffff::1", "2001:10::1", "2001:1f:ffff::1",
	}
	allowed := []string{
		"93.184.215.14", "1.1.1.1", "8.8.8.8", "104.16.0.1", "172.15.255.255", "172.32.0.0",
		"100.63.255.255", "100.128.0.0", "169.253.255.255", "192.0.1.0", "198.17.255.255",
		"198.20.0.0", "223.255.255.255", "11.0.0.0", "126.255.255.255", "128.0.0.0",
		"2606:4700::1111", "2a00:1450:4001::200e", "2001:db9::1", "100:0:0:1::1", "fe7f::1",
		"2001:20::1", "2001:3::1", "2001:1::1", "192.88.98.255", "192.88.100.0", "64:ff9b:2::1",
		"::ffff:93.184.215.14",
		"64:ff9b::5db8:d70e", // NAT64 of a public address
		"2002:5db8:d70e::1",  // 6to4 of a public address
	}
	for _, s := range denied {
		if !AddrDenied(netip.MustParseAddr(s)) {
			t.Errorf("AddrDenied(%s) = false, want true", s)
		}
	}
	for _, s := range allowed {
		if AddrDenied(netip.MustParseAddr(s)) {
			t.Errorf("AddrDenied(%s) = true, want false", s)
		}
	}
	// A resolver may return a zoned link-local address; netip.Prefix never
	// contains a zoned address, so the zone must not hide it from the check.
	for _, s := range []string{"fe80::1%eth0", "::1%lo", "fd00::1%1"} {
		if !AddrDenied(netip.MustParseAddr(s)) {
			t.Errorf("AddrDenied(%s) = false, want true (zone must be ignored)", s)
		}
	}
	if !AddrDenied(netip.Addr{}) {
		t.Error("AddrDenied(zero Addr) = false; an invalid address must be refused")
	}
}

// TestFloorDenied: the unconditional floor of owner decision 2026-10-16
// (contradicts A07 §7, where a literal allowlist entry opened any denied
// address). Loopback, unspecified, link-local and the cloud metadata
// addresses are never reachable, in any spelling, whatever the allowlist,
// the Decider, a grant or an operator entry says: one approval click must
// not be able to hand an agent the instance credentials at
// 169.254.169.254. Other denied ranges are not floor.
func TestFloorDenied(t *testing.T) {
	floor := []string{
		"0.0.0.0", "0.1.2.3", "127.0.0.1", "127.255.255.255", "::", "::1",
		"169.254.169.254", "169.254.0.1", "fe80::1", "febf::1", "fe80::1%eth0", "fd00:ec2::254",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:0.0.0.0",
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::0.0.0.1",
		"2002:7f00:1::", "2002:a9fe:a9fe::1", "2002:0000:0001::",
	}
	notFloor := []string{
		"10.0.0.5", "192.168.1.1", "172.16.0.1", "100.64.0.1", "fd00:ec2::253", "fc00::1",
		"93.184.215.14", "2606:4700::1111", "64:ff9b::a00:5", "2002:0a00:0005::",
	}
	for _, s := range floor {
		if !floorDenied(netip.MustParseAddr(s)) {
			t.Errorf("floorDenied(%s) = false, want true", s)
		}
		if !AddrDenied(netip.MustParseAddr(s)) {
			t.Errorf("AddrDenied(%s) = false: the floor must be inside the denied set", s)
		}
	}
	for _, s := range notFloor {
		if floorDenied(netip.MustParseAddr(s)) {
			t.Errorf("floorDenied(%s) = true, want false", s)
		}
	}
	if !floorDenied(netip.Addr{}) {
		t.Error("floorDenied(zero Addr) = false")
	}
}
