package proxy

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Target is a normalised destination (A07 §4, §12). Host is the canonical
// text form: a lower-case ASCII name without trailing dot, or the canonical
// text of an IP address (IPv6 without brackets). IP is valid exactly when
// the authority was an IP literal.
type Target struct {
	Host string
	Port uint16
	IP   netip.Addr
}

// IsIP reports whether the target was an IP literal.
func (t Target) IsIP() bool { return t.IP.IsValid() }

// Key is the canonical "host:port" used everywhere: allowlist, events,
// caches (A07 §4 step 4). IPv6 hosts are bracketed.
func (t Target) Key() string {
	if t.IP.Is6() {
		return "[" + t.Host + "]:" + strconv.Itoa(int(t.Port))
	}
	return t.Host + ":" + strconv.Itoa(int(t.Port))
}

// maxAuthority bounds the input before any parsing: a 253-byte name, a
// colon and a five-digit port, plus one trailing dot.
const maxAuthority = 253 + 1 + 1 + 5

// Normalize parses an authority "host:port" into its canonical Target
// (A07 §4). The error is a *DenyError with reason invalid_target, or
// private_address for names that are local by definition (A07 §4 step 3).
//
//  1. The port is required: decimal digits only, no sign, no leading zero,
//     1..65535.
//  2. A bracketed host must be an IPv6 address without a zone; an
//     unbracketed one that netip parses as IPv4 is an IPv4 literal. netip
//     refuses octal, hex, integer and short IPv4 forms.
//  3. Anything else is a name: one trailing dot stripped, ASCII only
//     (non-ASCII is refused, there is no IDNA, D-033 candidate), lower-cased,
//     LDH labels of 1..63 bytes that neither start nor end with a hyphen,
//     253 bytes in total. A name whose last label is a decimal or 0x-hex
//     number is refused: the system resolver (inet_aton) would read
//     "127.1", "0177.0.0.1" or "2130706433" as an address, so such a
//     "name" would reach loopback without ever being range-checked as an
//     IP literal. localhost, *.localhost, *.local and *.internal are
//     private_address without resolution.
func Normalize(authority string) (Target, error) {
	if len(authority) > maxAuthority {
		return Target{}, deny(ReasonInvalidTarget, "authority too long")
	}
	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		return Target{}, &DenyError{Reason: ReasonInvalidTarget, Detail: "split host and port", Err: err}
	}
	port, ok := parsePort(portText)
	if !ok {
		return Target{}, deny(ReasonInvalidTarget, "port must be 1..65535 in canonical decimal")
	}
	if strings.HasPrefix(authority, "[") {
		if strings.ContainsRune(host, '%') {
			return Target{}, deny(ReasonInvalidTarget, "IPv6 zone")
		}
		a, err := netip.ParseAddr(host)
		if err != nil || !a.Is6() || a.Zone() != "" {
			return Target{}, deny(ReasonInvalidTarget, "bracketed host is not an IPv6 address")
		}
		return Target{Host: a.String(), Port: port, IP: a}, nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if !a.Is4() {
			return Target{}, deny(ReasonInvalidTarget, "unbracketed IPv6")
		}
		return Target{Host: a.String(), Port: port, IP: a}, nil
	}
	name, reason := normalizeName(host)
	if reason != "" {
		return Target{}, deny(reason, "host name")
	}
	return Target{Host: name, Port: port}, nil
}

// parsePort accepts exactly the canonical decimal spelling of 1..65535.
func parsePort(s string) (uint16, bool) {
	if len(s) == 0 || len(s) > 5 || s[0] == '0' {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	if n < 1 || n > 65535 {
		return 0, false
	}
	return uint16(n), true
}

// normalizeName returns the canonical form of a DNS name, or the refusal
// reason. It is shared with the SNI comparison so that both sides of that
// comparison are spelled the same way.
func normalizeName(s string) (string, Reason) {
	s = strings.TrimSuffix(s, ".")
	if len(s) == 0 || len(s) > 253 {
		return "", ReasonInvalidTarget
	}
	b := []byte(s)
	labelStart := 0
	for i := 0; i <= len(b); i++ {
		if i < len(b) && b[i] != '.' {
			c := b[i]
			switch {
			case c >= 'A' && c <= 'Z':
				b[i] = c + ('a' - 'A')
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			default:
				return "", ReasonInvalidTarget // non-LDH, including every non-ASCII byte
			}
			continue
		}
		label := b[labelStart:i]
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ReasonInvalidTarget
		}
		labelStart = i + 1
	}
	name := string(b)
	if numericLabel(name[strings.LastIndexByte(name, '.')+1:]) {
		return "", ReasonInvalidTarget
	}
	if name == "localhost" || name == "local" || name == "internal" ||
		strings.HasSuffix(name, ".localhost") || strings.HasSuffix(name, ".local") || strings.HasSuffix(name, ".internal") {
		return "", ReasonPrivateAddress
	}
	return name, ""
}

// numericLabel reports whether a (lower-case) label is a number in the
// sense of inet_aton and the WHATWG URL host parser: decimal digits, or
// "0x" followed by zero or more hex digits.
func numericLabel(l string) bool {
	if strings.HasPrefix(l, "0x") {
		for i := 2; i < len(l); i++ {
			c := l[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(l); i++ {
		if l[i] < '0' || l[i] > '9' {
			return false
		}
	}
	return len(l) > 0
}
