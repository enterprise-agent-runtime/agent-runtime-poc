package proxy

import (
	"errors"
	"strings"
	"testing"
)

// normalizeCases is A07 §13's normalisation table plus the forms an
// attacker uses to reach an address the range check would refuse. It is
// also the corpus TestInjector_NeverMatches and the fuzz seeds run over.
var normalizeCases = []struct {
	in     string
	key    string // canonical key; "" when refused
	reason Reason // refusal reason when key == ""
	isIP   bool
}{
	// A07 §13 row 1: case and one trailing dot.
	{"EXAMPLE.org.:443", "example.org:443", "", false},
	{"registry.npmjs.org:443", "registry.npmjs.org:443", "", false},
	{"Proxy.Golang.Org:443", "proxy.golang.org:443", "", false},
	// Punycode is plain ASCII (D-033 candidate: no IDNA library, so a
	// Unicode name is refused, its xn-- form accepted).
	{"xn--bcher-kva.example:443", "xn--bcher-kva.example:443", "", false},
	{"XN--BCHER-KVA.example:443", "xn--bcher-kva.example:443", "", false},
	{"bücher.example:443", "", ReasonInvalidTarget, false},
	{"examрle.org:443", "", ReasonInvalidTarget, false}, // Cyrillic r
	// IP literals in canonical form.
	{"10.0.0.5:8443", "10.0.0.5:8443", "", true},
	{"93.184.215.14:80", "93.184.215.14:80", "", true},
	{"[2606:4700::1111]:443", "[2606:4700::1111]:443", "", true},
	{"[2606:4700:0:0:0:0:0:1111]:443", "[2606:4700::1111]:443", "", true},
	{"[::FFFF:127.0.0.1]:80", "[::ffff:127.0.0.1]:80", "", true},
	// A07 §13: octal, short and zoned forms refused. A name whose last
	// label is numeric is an address to getaddrinfo (inet_aton), never a
	// host name, so every such spelling is refused rather than resolved.
	{"0177.0.0.1:80", "", ReasonInvalidTarget, false},
	{"127.1:80", "", ReasonInvalidTarget, false},
	{"0x7f.1:80", "", ReasonInvalidTarget, false},
	{"0x7f000001:80", "", ReasonInvalidTarget, false},
	{"2130706433:80", "", ReasonInvalidTarget, false},
	{"017700000001:80", "", ReasonInvalidTarget, false},
	{"127.0.0.1.:80", "", ReasonInvalidTarget, false},
	{"1.2.3.04:80", "", ReasonInvalidTarget, false},
	{"[fe80::1%en0]:443", "", ReasonInvalidTarget, false},
	{"[fe80::1%25en0]:443", "", ReasonInvalidTarget, false},
	{"[127.0.0.1]:80", "", ReasonInvalidTarget, false},
	{"::1:80", "", ReasonInvalidTarget, false},
	// Ports: required, decimal, 1..65535, no sign, no leading zero.
	{"example.org", "", ReasonInvalidTarget, false},
	{"example.org:", "", ReasonInvalidTarget, false},
	{"example.org:0", "", ReasonInvalidTarget, false},
	{"example.org:65536", "", ReasonInvalidTarget, false},
	{"example.org:65535", "example.org:65535", "", false},
	{"example.org:+443", "", ReasonInvalidTarget, false},
	{"example.org:0443", "", ReasonInvalidTarget, false},
	{"example.org:https", "", ReasonInvalidTarget, false},
	{"example.org:44 3", "", ReasonInvalidTarget, false},
	// Names: LDH only, label and name lengths, one trailing dot only.
	{":443", "", ReasonInvalidTarget, false},
	{".:443", "", ReasonInvalidTarget, false},
	{"example..org:443", "", ReasonInvalidTarget, false},
	{"example.org..:443", "", ReasonInvalidTarget, false},
	{".example.org:443", "", ReasonInvalidTarget, false},
	{"-example.org:443", "", ReasonInvalidTarget, false},
	{"example-.org:443", "", ReasonInvalidTarget, false},
	{"ex_ample.org:443", "", ReasonInvalidTarget, false},
	{"ex ample.org:443", "", ReasonInvalidTarget, false},
	{"example.org/x:443", "", ReasonInvalidTarget, false},
	{"user@example.org:443", "", ReasonInvalidTarget, false},
	{"*.example.org:443", "", ReasonInvalidTarget, false},
	{"exa%6dple.org:443", "", ReasonInvalidTarget, false},
	{"example.org\x00.evil:443", "", ReasonInvalidTarget, false},
	{strings.Repeat("a", 63) + ".org:443", strings.Repeat("a", 63) + ".org:443", "", false},
	{strings.Repeat("a", 64) + ".org:443", "", ReasonInvalidTarget, false},
	{strings.Repeat("a.", 126) + "a:443", strings.Repeat("a.", 126) + "a:443", "", false},  // 253 bytes
	{strings.Repeat("a.", 126) + "a.:443", strings.Repeat("a.", 126) + "a:443", "", false}, // 253 + root dot
	{strings.Repeat("a.", 126) + "ab:443", "", ReasonInvalidTarget, false},                 // 254 bytes
	{"1a.example:443", "1a.example:443", "", false},
	{"example.123a:443", "example.123a:443", "", false},
	// Local by definition: refused as private_address without resolution.
	{"localhost:80", "", ReasonPrivateAddress, false},
	{"LOCALHOST.:80", "", ReasonPrivateAddress, false},
	{"api.localhost:80", "", ReasonPrivateAddress, false},
	{"printer.local:631", "", ReasonPrivateAddress, false},
	{"metadata.google.internal:80", "", ReasonPrivateAddress, false},
	{"internal:80", "", ReasonPrivateAddress, false},
	{"notlocalhost:80", "notlocalhost:80", "", false},
	{"localhost.example.org:80", "localhost.example.org:80", "", false},
}

// TestNormalize is A07 §13 "Normalization table". It exists because every
// later check (allowlist, range, SNI, events) compares canonical strings:
// two spellings of one destination that normalise differently are an
// allowlist bypass, and a numeric spelling that slips through as a "name"
// reaches the system resolver, which reads 127.1 as loopback.
func TestNormalize(t *testing.T) {
	for _, c := range normalizeCases {
		got, err := Normalize(c.in)
		if c.key == "" {
			var de *DenyError
			if !errors.As(err, &de) {
				t.Errorf("Normalize(%q) = %q, %v; want refusal %s", c.in, got.Key(), err, c.reason)
				continue
			}
			if de.Reason != c.reason {
				t.Errorf("Normalize(%q) reason = %s, want %s", c.in, de.Reason, c.reason)
			}
			continue
		}
		if err != nil {
			t.Errorf("Normalize(%q) error %v, want %q", c.in, err, c.key)
			continue
		}
		if got.Key() != c.key {
			t.Errorf("Normalize(%q).Key() = %q, want %q", c.in, got.Key(), c.key)
		}
		if got.IsIP() != c.isIP {
			t.Errorf("Normalize(%q).IsIP() = %v, want %v", c.in, got.IsIP(), c.isIP)
		}
		// The canonical key is a fixed point: normalising it again changes
		// nothing, so a key stored in the allowlist matches itself.
		again, err := Normalize(got.Key())
		if err != nil || again != got {
			t.Errorf("Normalize(%q) is not idempotent: %+v, %v", got.Key(), again, err)
		}
	}
}

// FuzzNormalize checks that the normaliser never panics, that whatever it
// accepts is a fixed point, and that an accepted name is pure lower-case
// LDH (the property the allowlist and the SNI comparison rely on).
func FuzzNormalize(f *testing.F) {
	for _, c := range normalizeCases {
		f.Add(c.in)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got, err := Normalize(in)
		if err != nil {
			var de *DenyError
			if !errors.As(err, &de) {
				t.Fatalf("Normalize(%q) error %T is not a *DenyError", in, err)
			}
			return
		}
		if got.Port == 0 {
			t.Fatalf("Normalize(%q) accepted port 0", in)
		}
		again, err := Normalize(got.Key())
		if err != nil || again != got {
			t.Fatalf("Normalize(%q) = %+v not a fixed point: %+v, %v", in, got, again, err)
		}
		if !got.IsIP() {
			for i := 0; i < len(got.Host); i++ {
				b := got.Host[i]
				if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '.') {
					t.Fatalf("Normalize(%q) host %q has byte %q", in, got.Host, b)
				}
			}
		}
	})
}
