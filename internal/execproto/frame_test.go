package execproto

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
)

// zeroReader yields n zero bytes without allocating, so a test can offer a
// frame body far larger than anything it wants the reader to allocate.
type zeroReader struct{ n, read int }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.read >= z.n {
		return 0, io.EOF
	}
	k := min(len(p), z.n-z.read)
	clear(p[:k])
	z.read += k
	return k, nil
}

// TestExecProtocol_FrameCap guards A06 §4.1 "Maximum frame 8 MiB; larger
// frames close the channel". The point of a cap is that the peer cannot
// make the reader allocate what the header claims: the executor is fed by
// the daemon, but the daemon is fed by an executor running next to
// agent-controlled processes, and a 9 MiB (or 2 GiB) Content-Length must be
// refused from the header alone. Allocation is measured with
// runtime.MemStats.TotalAlloc, which counts heap bytes and is deterministic
// under -race; a wall-clock budget would not be (CLAUDE.md §8.6).
func TestExecProtocol_FrameCap(t *testing.T) {
	const size = 9 << 20
	body := &zeroReader{n: size}
	fr := NewFrameReader(io.MultiReader(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n", size)), body))

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := fr.ReadFrame()
	runtime.ReadMemStats(&after)

	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadFrame of a 9 MiB frame: got %d bytes, err %v; want ErrFrameTooLarge", len(got), err)
	}
	if d := after.TotalAlloc - before.TotalAlloc; d > 1<<20 {
		t.Errorf("refusing a 9 MiB frame allocated %d bytes; the header alone must decide", d)
	}
	if body.read > 64<<10 {
		t.Errorf("refusing a 9 MiB frame read %d body bytes; the header alone must decide", body.read)
	}
}

// TestFrame_CapBoundary pins the cap at exactly MaxFrameBytes: a frame of
// that size is read, one byte more is refused, and the writer refuses to
// produce a frame the peer would refuse.
func TestFrame_CapBoundary(t *testing.T) {
	if d := len(fmt.Sprint(MaxFrameBytes)); d != maxLengthDigits {
		t.Fatalf("maxLengthDigits = %d, MaxFrameBytes has %d digits", maxLengthDigits, d)
	}
	big := bytes.Repeat([]byte{'x'}, MaxFrameBytes)
	var buf bytes.Buffer
	if err := NewFrameWriter(&buf).WriteFrame(big); err != nil {
		t.Fatalf("WriteFrame of exactly MaxFrameBytes: %v", err)
	}
	got, err := NewFrameReader(&buf).ReadFrame()
	if err != nil || len(got) != MaxFrameBytes {
		t.Fatalf("ReadFrame of exactly MaxFrameBytes: %d bytes, %v", len(got), err)
	}

	over := append(big, 'x')
	buf.Reset()
	if err := NewFrameWriter(&buf).WriteFrame(over); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("WriteFrame of MaxFrameBytes+1: %v, want ErrFrameTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a refused WriteFrame wrote %d bytes", buf.Len())
	}
	in := fmt.Sprintf("Content-Length: %d\r\n\r\n", MaxFrameBytes+1)
	if _, err := NewFrameReader(strings.NewReader(in)).ReadFrame(); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("ReadFrame of MaxFrameBytes+1: %v, want ErrFrameTooLarge", err)
	}
}

// TestFrame_RoundTrip writes several frames back to back and reads them in
// order: the reader must consume exactly Content-Length bytes per frame.
func TestFrame_RoundTrip(t *testing.T) {
	msgs := []string{`{"a":1}`, `{}`, `{"jsonrpc":"2.0","method":"exec.proc.io","params":{"data":"Content-Length: 3\r\n\r\n"}}`}
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)
	for _, m := range msgs {
		if err := w.WriteFrame([]byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(buf.String(), "Content-Length: 7\r\n\r\n{\"a\":1}") {
		t.Fatalf("wire form = %q", buf.String())
	}
	r := NewFrameReader(&buf)
	for i, m := range msgs {
		got, err := r.ReadFrame()
		if err != nil || string(got) != m {
			t.Fatalf("frame %d = %q, %v; want %q", i, got, err, m)
		}
	}
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Errorf("after the last frame: %v, want io.EOF", err)
	}
}

// TestFrame_MalformedHeaders: the header is parsed strictly. A lenient
// parser is how two readers of one byte stream come to disagree about
// where a frame ends (request smuggling, CWE-444): one that accepts a bare
// LF, a duplicate length or a lower-case name would split the stream
// differently from one that does not. There is exactly one accepted form,
// "Content-Length: <decimal>\r\n\r\n" (LSP base protocol; A05 framing as
// jrpc2 writes it), and everything else closes the channel.
func TestFrame_MalformedHeaders(t *testing.T) {
	cases := []struct {
		name, in string
		want     error
	}{
		{"empty header block", "\r\n{}", ErrFrameHeader},
		{"lower-case name", "content-length: 2\r\n\r\n{}", ErrFrameHeader},
		{"no space after colon", "Content-Length:2\r\n\r\n{}", ErrFrameHeader},
		{"two spaces after colon", "Content-Length:  2\r\n\r\n{}", ErrFrameHeader},
		{"trailing space", "Content-Length: 2 \r\n\r\n{}", ErrFrameHeader},
		{"LF only", "Content-Length: 2\n\n{}", ErrFrameHeader},
		{"LF after CRLF line", "Content-Length: 2\r\n\n{}", ErrFrameHeader},
		{"bare CR inside", "Content-Length: 2\r\r\n\r\n{}", ErrFrameHeader},
		{"duplicate length", "Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}", ErrFrameHeader},
		{"conflicting length", "Content-Length: 2\r\nContent-Length: 9\r\n\r\n{}", ErrFrameHeader},
		{"other header", "Content-Length: 2\r\nContent-Type: application/json\r\n\r\n{}", ErrFrameHeader},
		{"other header first", "Content-Type: application/json\r\nContent-Length: 2\r\n\r\n{}", ErrFrameHeader},
		{"negative", "Content-Length: -2\r\n\r\n{}", ErrFrameHeader},
		{"plus sign", "Content-Length: +2\r\n\r\n{}", ErrFrameHeader},
		{"hex", "Content-Length: 0x2\r\n\r\n{}", ErrFrameHeader},
		{"leading zero", "Content-Length: 02\r\n\r\n{}", ErrFrameHeader},
		{"zero", "Content-Length: 0\r\n\r\n", ErrFrameHeader},
		{"no digits", "Content-Length: \r\n\r\n{}", ErrFrameHeader},
		{"digits then junk", "Content-Length: 2x\r\n\r\n{}", ErrFrameHeader},
		{"NUL in header", "Content-Length: 2\x00\r\n\r\n{}", ErrFrameHeader},
		{"overflowing length", "Content-Length: 99999999999999999999999\r\n\r\n{}", ErrFrameTooLarge},
		{"over the cap", "Content-Length: 8388609\r\n\r\n", ErrFrameTooLarge},
		{"endless header line", "Content-Length: 2" + strings.Repeat("0", 1<<20), ErrFrameHeader},
		{"endless header block", strings.Repeat("X: y\r\n", 1<<16), ErrFrameHeader},
		{"EOF inside header", "Content-Length: 2\r\n", io.ErrUnexpectedEOF},
		{"EOF inside body", "Content-Length: 5\r\n\r\n{}", io.ErrUnexpectedEOF},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NewFrameReader(strings.NewReader(c.in)).ReadFrame()
			if !errors.Is(err, c.want) {
				t.Fatalf("ReadFrame(%.60q) = %q, %v; want %v", c.in, got, err, c.want)
			}
		})
	}
}

// TestFrame_HeaderReadIsBounded: an endless header line is refused after
// at most MaxHeaderBytes, not after reading (or buffering) the whole line.
func TestFrame_HeaderReadIsBounded(t *testing.T) {
	src := &zeroReader{n: 64 << 20}
	if _, err := NewFrameReader(src).ReadFrame(); !errors.Is(err, ErrFrameHeader) {
		t.Fatalf("ReadFrame of 64 MiB of NULs: %v, want ErrFrameHeader", err)
	}
	if src.read > 64<<10 {
		t.Errorf("read %d bytes of an endless header", src.read)
	}
}

// TestFrame_CleanEOF: EOF before the first header byte is the peer closing
// between frames, reported as io.EOF itself so a serve loop can end
// quietly; EOF anywhere else is an error.
func TestFrame_CleanEOF(t *testing.T) {
	if _, err := NewFrameReader(strings.NewReader("")).ReadFrame(); err != io.EOF {
		t.Errorf("ReadFrame at EOF = %v, want io.EOF", err)
	}
}

// TestFrame_ErrorsDoNotEchoInput: frame errors end up in logs and in the
// error a pending call fails with. The header bytes come from the peer and
// may be anything, including a secret a broken peer wrote by mistake, so
// the error text is fixed and never quotes them.
func TestFrame_ErrorsDoNotEchoInput(t *testing.T) {
	const marker = "SECRETMARKER"
	for _, in := range []string{
		"Content-Length: " + marker + "\r\n\r\n",
		marker + ": 1\r\n\r\n",
		"Content-Length: 1\r\n" + marker + "\r\n\r\n",
	} {
		_, err := NewFrameReader(strings.NewReader(in)).ReadFrame()
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Errorf("ReadFrame(%q) error = %v, want a fixed text", in, err)
		}
	}
}
