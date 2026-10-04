// Package jcs implements the JSON Canonicalization Scheme of RFC 8785, used
// for event hashing and checkpoint signatures (design A04 §6.3, §10.1):
// UTF-8, no insignificant whitespace, object members sorted by the UTF-16
// code units of their names, minimal string escaping and ECMAScript
// shortest round-trip number formatting. It replaces gowebpki/jcs, which is
// not an allowed dependency (docs/DECISIONS-poc.md D-009).
package jcs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Transform canonicalizes a JSON text.
func Transform(in []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("jcs: trailing data after JSON value")
	}
	var b bytes.Buffer
	if err := write(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Marshal JSON-encodes v and canonicalizes the result.
func Marshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Transform(raw)
}

func write(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			return fmt.Errorf("jcs: number %q: %w", x, err)
		}
		s, err := Number(f)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case string:
		writeString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := write(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			if err := write(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("jcs: unexpected type %T", v)
	}
	return nil
}

// lessUTF16 orders strings by their UTF-16 code units (RFC 8785 §3.2.3).
func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// writeString escapes per RFC 8785 §3.2.2.2: only '"', '\\' and control
// characters; \b \t \n \f \r use their short forms, other controls \u00xx
// in lowercase hex. Everything else is literal UTF-8.
func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

// Number formats f as ECMAScript Number.prototype.toString does
// (RFC 8785 §3.2.2.3). NaN and ±Inf are errors.
func Number(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.New("jcs: NaN and Infinity are not valid JSON")
	}
	if f == 0 {
		return "0", nil // also -0
	}
	neg := f < 0
	if neg {
		f = -f
	}
	// Shortest round-trip digits and decimal exponent: d.ddd e±x.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	k := len(digits) // number of significant digits
	n := exp + 1     // position of the decimal point relative to the digits
	var s string
	switch {
	case k <= n && n <= 21:
		s = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		s = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		s = "0." + strings.Repeat("0", -n) + digits
	default:
		sign := "+"
		if n-1 < 0 {
			sign = "-"
		}
		x := n - 1
		if x < 0 {
			x = -x
		}
		if k == 1 {
			s = digits + "e" + sign + strconv.Itoa(x)
		} else {
			s = digits[:1] + "." + digits[1:] + "e" + sign + strconv.Itoa(x)
		}
	}
	if neg {
		s = "-" + s
	}
	return s, nil
}
