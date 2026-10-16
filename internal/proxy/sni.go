package proxy

import (
	"encoding/binary"
	"errors"
	"io"
)

// A07 §3.2 step 6 and §10: one record, at most 16 KiB of payload.
const (
	recordHeaderLen  = 5
	maxRecordPayload = 16384
)

// errHello is every structural defect of a ClientHello. The caller turns
// it into not_tls; the detail never reaches an event.
var errHello = errors.New("malformed ClientHello")

// readTLSRecord reads exactly one TLS record (header and payload) from r.
// It refuses, before reading the payload, anything that is not a
// handshake record of TLS major version 3 with 1..16384 payload bytes, so
// a client cannot make the proxy buffer more than one bounded record.
func readTLSRecord(r io.Reader) ([]byte, error) {
	hdr := make([]byte, recordHeaderLen)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, &DenyError{Reason: ReasonNotTLS, Detail: "short record header", Err: err}
	}
	if hdr[0] != 0x16 || hdr[1] != 3 {
		return nil, deny(ReasonNotTLS, "not a TLS handshake record")
	}
	n := int(binary.BigEndian.Uint16(hdr[3:5]))
	if n == 0 || n > maxRecordPayload {
		return nil, deny(ReasonNotTLS, "record length out of range")
	}
	rec := make([]byte, recordHeaderLen+n)
	copy(rec, hdr)
	if _, err := io.ReadFull(r, rec[recordHeaderLen:]); err != nil {
		return nil, &DenyError{Reason: ReasonNotTLS, Detail: "short record", Err: err}
	}
	return rec, nil
}

// cursor is a bounds-checked reader over a byte slice. Every read checks
// the remaining length first; a failed read leaves ok false and every later
// read fails too, so the parser below needs one check per structure, not
// one per field.
type cursor struct {
	b  []byte
	ok bool
}

func (c *cursor) bytes(n int) []byte {
	if !c.ok || n < 0 || n > len(c.b) {
		c.ok = false
		return nil
	}
	out := c.b[:n]
	c.b = c.b[n:]
	return out
}

func (c *cursor) u8() int {
	b := c.bytes(1)
	if b == nil {
		return 0
	}
	return int(b[0])
}

func (c *cursor) u16() int {
	b := c.bytes(2)
	if b == nil {
		return 0
	}
	return int(binary.BigEndian.Uint16(b))
}

func (c *cursor) u24() int {
	b := c.bytes(3)
	if b == nil {
		return 0
	}
	return int(b[0])<<16 | int(b[1])<<8 | int(b[2])
}

// sub returns a cursor over the next n bytes and advances past them.
func (c *cursor) sub(n int) cursor {
	b := c.bytes(n)
	return cursor{b: b, ok: c.ok}
}

func (c *cursor) empty() bool { return c.ok && len(c.b) == 0 }

// parseClientHelloSNI parses one TLS record holding exactly one complete
// ClientHello (RFC 8446 §4.1.2, RFC 6066 §3) and returns its host_name.
// It is deliberately strict: the handshake must fill the record exactly
// (a ClientHello split over records is refused, A07 §3.2 step 6 reads one
// record), every length must match its contents, an extension type may
// appear once, and server_name must hold exactly one host_name of
// 1..255 bytes. A ClientHello without server_name returns hasSNI false.
func parseClientHelloSNI(rec []byte) (sni string, hasSNI bool, err error) {
	c := cursor{b: rec, ok: true}
	if c.u8() != 0x16 || c.u8() != 3 {
		return "", false, errHello
	}
	c.u8() // record minor version: any
	payload := c.sub(c.u16())
	if !c.empty() || !payload.ok || len(payload.b) == 0 {
		return "", false, errHello
	}
	if payload.u8() != 1 { // handshake type client_hello
		return "", false, errHello
	}
	body := payload.sub(payload.u24())
	if !payload.empty() || !body.ok {
		return "", false, errHello
	}
	if body.u8() != 3 { // legacy_version major
		return "", false, errHello
	}
	body.u8()
	body.bytes(32) // random
	sid := body.u8()
	body.bytes(sid)
	cs := body.u16()
	body.bytes(cs)
	cm := body.u8()
	body.bytes(cm)
	if !body.ok || sid > 32 || cs < 2 || cs%2 != 0 || cm < 1 {
		return "", false, errHello
	}
	if body.empty() {
		return "", false, nil // no extensions block at all
	}
	exts := body.sub(body.u16())
	if !body.empty() || !exts.ok {
		return "", false, errHello
	}
	seen := make(map[int]bool)
	for !exts.empty() {
		typ := exts.u16()
		data := exts.sub(exts.u16())
		if !exts.ok {
			return "", false, errHello
		}
		if seen[typ] {
			return "", false, errHello
		}
		seen[typ] = true
		if typ != 0 { // server_name
			continue
		}
		name, err := parseServerName(data)
		if err != nil {
			return "", false, err
		}
		sni, hasSNI = name, true
	}
	return sni, hasSNI, nil
}

// parseServerName parses the server_name extension body: a list holding
// exactly one entry, of type host_name (0), 1..255 bytes long.
func parseServerName(data cursor) (string, error) {
	list := data.sub(data.u16())
	if !data.empty() || !list.ok {
		return "", errHello
	}
	if list.u8() != 0 {
		return "", errHello
	}
	name := list.bytes(list.u16())
	if !list.empty() || len(name) == 0 || len(name) > 255 {
		return "", errHello
	}
	return string(name), nil
}
