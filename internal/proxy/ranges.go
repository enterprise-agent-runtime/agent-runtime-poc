package proxy

import "net/netip"

// deniedPrefixes is the A07 §7 table, in its order, without its last row
// (NAT64 and 6to4, which inAny handles by extraction), followed by the
// owner's 2026-10-16 additions. It is
// a function rather than a package variable so that there is no global
// mutable state (CLAUDE.md §7); it runs once per resolved address.
func deniedPrefixes() []netip.Prefix {
	return []netip.Prefix{
		// unspecified and loopback (host services)
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::/128"),
		netip.MustParsePrefix("::1/128"),
		// RFC 1918
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		// CGNAT
		netip.MustParsePrefix("100.64.0.0/10"),
		// link-local (cloud metadata endpoints)
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("fe80::/10"),
		// IPv6 ULA
		netip.MustParsePrefix("fc00::/7"),
		// special purpose, benchmarking, documentation, discard
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("100::/64"),
		// multicast, reserved, broadcast
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("255.255.255.255/32"),
		netip.MustParsePrefix("ff00::/8"),
		// Owner decision 2026-10-16, stricter than A07 §7: transition and
		// legacy ranges denied whole, whatever address they embed.
		netip.MustParsePrefix("::/96"),           // IPv4-compatible (deprecated)
		netip.MustParsePrefix("::ffff:0:0:0/96"), // SIIT IPv4-translated (RFC 6145)
		netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64 (RFC 8215)
		netip.MustParsePrefix("2001::/32"),       // Teredo
		netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast
		netip.MustParsePrefix("fec0::/10"),       // site-local (deprecated)
		netip.MustParsePrefix("2001:2::/48"),     // benchmarking
		netip.MustParsePrefix("2001:10::/28"),    // ORCHID
	}
}

// floorPrefixes is the unconditional floor (owner decision 2026-10-16,
// contradicting A07 §7's literal-entry exception): loopback, unspecified,
// link-local and the cloud metadata addresses. No allowlist entry, Decider
// allow, grant, obligation or operator entry reaches them.
func floorPrefixes() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("::/128"),
		netip.MustParsePrefix("169.254.0.0/16"), // includes 169.254.169.254
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("fd00:ec2::254/128"), // AWS IMDS over IPv6
	}
}

// AddrDenied reports whether a is in a destination range the proxy never
// connects to (A07 §7 plus the owner's 2026-10-16 additions), unless an
// operator entry names that literal IP and it is not in the floor. The
// address is unmapped first (::ffff:a.b.c.d is a.b.c.d). For NAT64
// (64:ff9b::/96) and 6to4 (2002::/16) addresses the embedded IPv4 address
// is extracted and checked against the same table: a NAT64 gateway or a
// 6to4 relay would otherwise carry the connection to that IPv4 address.
// A zone is ignored. The zero Addr is denied.
func AddrDenied(a netip.Addr) bool { return inAny(a, deniedPrefixes()) }

// floorDenied reports whether a, or the IPv4 address it embeds, is in the
// floor. The zero Addr is in the floor.
func floorDenied(a netip.Addr) bool { return inAny(a, floorPrefixes()) }

// inAny checks a and every IPv4 address it stands for against prefixes.
func inAny(a netip.Addr, prefixes []netip.Prefix) bool {
	if !a.IsValid() {
		return true
	}
	// netip.Prefix.Contains never contains a zoned address, so fe80::1%eth0
	// would pass every prefix; the zone does not change the destination.
	a = a.WithZone("").Unmap()
	if in(a, prefixes) {
		return true
	}
	if a.Is6() {
		b := a.As16()
		if b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b && allZero(b[4:12]) { // 64:ff9b::/96
			return in(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), prefixes)
		}
		if b[0] == 0x20 && b[1] == 0x02 { // 2002::/16
			return in(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), prefixes)
		}
	}
	return false
}

func in(a netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
