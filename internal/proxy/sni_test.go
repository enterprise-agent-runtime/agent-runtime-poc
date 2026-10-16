package proxy

import (
	"bytes"
	"strings"
	"testing"
)

// TestParseClientHelloSNI checks the hand-rolled parser on well-formed
// hellos (minimal and real crypto/tls output) and on every length field
// lying about its size. The parser reads attacker bytes before any
// decision, so each length is checked against what remains.
func TestParseClientHelloSNI(t *testing.T) {
	long := strings.Repeat("a", 256)
	cases := []struct {
		name   string
		rec    []byte
		sni    string
		hasSNI bool
		ok     bool
	}{
		{"minimal", clientHello("a.example"), "a.example", true, true},
		{"real", realClientHello(t, "registry.npmjs.org"), "registry.npmjs.org", true, true},
		{"no extensions", record(handshake(helloBody())), "", false, true},
		{"empty extension block", record(handshake(cat(helloBody(), u16(0)))), "", false, true},
		{"other extension only", record(handshake(helloBody(ext(0x2b, []byte{2, 3, 4})))), "", false, true},
		{"empty name", record(handshake(helloBody(sniExt("")))), "", false, false},
		{"name over 255", record(handshake(helloBody(sniExt(long)))), "", false, false},
		{"two names", record(handshake(helloBody(sniExt("a.example", "b.example")))), "", false, false},
		{"duplicate extension", record(handshake(helloBody(ext(0x2b, []byte{2, 3, 4}), ext(0x2b, []byte{2, 3, 4})))), "", false, false},
		{"unknown name type", record(handshake(helloBody(ext(0, cat(u16(4), []byte{1}, u16(1), []byte("a")))))), "", false, false},
		{"list length lies", record(handshake(helloBody(ext(0, cat(u16(40), []byte{0}, u16(1), []byte("a")))))), "", false, false},
		{"name length lies", record(handshake(helloBody(ext(0, cat(u16(4), []byte{0}, u16(9), []byte("a")))))), "", false, false},
		{"extension length lies", record(handshake(cat(helloBody(), u16(8), u16(0), u16(60)))), "", false, false},
		{"extensions block lies", record(handshake(cat(helloBody(), u16(60)))), "", false, false},
		{"trailing byte after extensions", record(handshake(cat(helloBody(sniExt("a.example")), []byte{0}))), "", false, false},
		{"trailing byte after handshake", record(cat(handshake(helloBody(sniExt("a.example"))), []byte{0})), "", false, false},
		{"session id over 32", record(handshake(cat([]byte{3, 3}, make([]byte, 32), []byte{33}, make([]byte, 33), u16(2), []byte{0x13, 1}, []byte{1, 0}))), "", false, false},
		{"odd cipher suites", record(handshake(cat([]byte{3, 3}, make([]byte, 32), []byte{0}, u16(3), []byte{0x13, 1, 0}, []byte{1, 0}))), "", false, false},
		{"no compression", record(handshake(cat([]byte{3, 3}, make([]byte, 32), []byte{0}, u16(2), []byte{0x13, 1}, []byte{0}))), "", false, false},
		{"handshake longer than record", record(handshake(helloBody())[:20]), "", false, false},
		{"not a handshake record", cat([]byte{0x17, 3, 3}, u16(3), []byte{1, 0, 0}), "", false, false},
		{"SSLv2-style first byte", []byte{0x80, 0x2e, 0x01, 0x03, 0x01}, "", false, false},
		{"empty", nil, "", false, false},
		{"header only", []byte{0x16, 3, 1, 0, 0}, "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sni, has, err := parseClientHelloSNI(tc.rec)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if err == nil && (sni != tc.sni || has != tc.hasSNI) {
				t.Errorf("got %q, %v; want %q, %v", sni, has, tc.sni, tc.hasSNI)
			}
		})
	}
}

// TestReadTLSRecord_Bounds: one record of at most 16 KiB is read; a
// larger declared length or a non-handshake type is refused before the
// body is read, so a client cannot make the proxy buffer more.
func TestReadTLSRecord_Bounds(t *testing.T) {
	ok := clientHello("a.example")
	got, err := readTLSRecord(bytes.NewReader(cat(ok, []byte("rest"))))
	if err != nil || !bytes.Equal(got, ok) {
		t.Fatalf("readTLSRecord = %q, %v; want the first record only", got, err)
	}
	for name, in := range map[string][]byte{
		"too long":  cat([]byte{0x16, 3, 1}, u16(16385), make([]byte, 16385)),
		"zero":      []byte{0x16, 3, 1, 0, 0},
		"not tls":   []byte("GET / HTTP/1.1\r\n\r\n"),
		"bad major": cat([]byte{0x16, 4, 1}, u16(1), []byte{1}),
		"short":     []byte{0x16, 3},
	} {
		if _, err := readTLSRecord(bytes.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// FuzzParseClientHelloSNI: the parser never panics on any input, and a
// name it returns is a non-empty substring of the input of at most 255
// bytes.
func FuzzParseClientHelloSNI(f *testing.F) {
	f.Add(clientHello("registry.npmjs.org"))
	f.Add(realClientHello(f, "registry.npmjs.org"))
	f.Add(record(handshake(helloBody())))
	f.Add(record(handshake(helloBody(sniExt("a.example", "b.example")))))
	f.Add([]byte("GET / HTTP/1.1\r\n\r\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		sni, has, err := parseClientHelloSNI(in)
		if err != nil {
			return
		}
		if has && (sni == "" || len(sni) > 255 || !bytes.Contains(in, []byte(sni))) {
			t.Fatalf("parse returned %q from %x", sni, in)
		}
		if !has && sni != "" {
			t.Fatalf("hasSNI false with name %q", sni)
		}
	})
}
