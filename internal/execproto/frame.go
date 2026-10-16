package execproto

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// The executor protocol frames each JSON-RPC message LSP-style (A06 §4.1:
// "Content-Length framing, same codec as the runtime API, A05"):
//
//	Content-Length: <decimal byte count>\r\n
//	\r\n
//	<body>
//
// Both sides need the codec and the executor may import nothing but this
// package (A02 §5 R3), so it lives here, in the standard library only.
//
// The header is parsed strictly. Exactly one form is accepted: the single
// header "Content-Length: " (that case, one space) followed by a decimal
// without sign or leading zero, each line ended by CRLF, then an empty line.
// No other header, no duplicate, no bare CR or LF. Two readers that are
// lenient in different ways disagree about where a frame ends, which is
// how a peer smuggles a message past one of them (CWE-444). LSP's optional
// Content-Type header is refused too: neither jrpc2 (the runtime API's
// codec) nor this writer sends it, and a header nobody sends is only an
// attack surface.

// FrameError is a framing failure. It is a constant type, like Code, so the
// sentinels below are not global mutable state (CLAUDE.md §7). Errors
// wrapping them carry a fixed description and never quote the peer's
// bytes, which may be anything.
type FrameError string

func (e FrameError) Error() string { return string(e) }

// Framing errors. After either one the byte stream cannot be resynchronised:
// the channel must be closed (A06 §4.1 "larger frames close the channel").
const (
	ErrFrameTooLarge FrameError = "execproto: frame larger than MaxFrameBytes"
	ErrFrameHeader   FrameError = "execproto: malformed frame header"
)

// MaxHeaderBytes bounds the header block: the bytes of all its lines
// together, CRLFs not counted. The one valid header line is at most 23
// bytes ("Content-Length: 8388608").
const MaxHeaderBytes = 256

// maxLengthDigits is the number of digits in MaxFrameBytes (8388608); a
// longer decimal is over the cap whatever its value, and is refused before
// it is converted, so no length can overflow. TestFrame_CapBoundary pins it.
const maxLengthDigits = 7

const lengthPrefix = "Content-Length: "

// FrameReader reads frames from one byte stream. It is not safe for
// concurrent use; each connection has one read loop.
type FrameReader struct {
	br  *bufio.Reader
	hdr []byte // header scratch, MaxHeaderBytes, reused across frames
}

// NewFrameReader returns a FrameReader on r.
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{br: bufio.NewReader(r), hdr: make([]byte, 0, MaxHeaderBytes)}
}

// ReadFrame reads one frame and returns its body. It returns io.EOF itself
// when the stream ends cleanly between frames, io.ErrUnexpectedEOF (wrapped)
// when it ends inside one, ErrFrameTooLarge when the header announces more
// than MaxFrameBytes (decided from the header alone: nothing of that size
// is allocated or read), and ErrFrameHeader for any other header.
func (f *FrameReader) ReadFrame() ([]byte, error) {
	n, err := f.readHeader()
	if err != nil {
		return nil, err
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(f.br, body); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("execproto: frame body: %w", err)
	}
	return body, nil
}

func (f *FrameReader) readHeader() (int, error) {
	f.hdr = f.hdr[:0]
	length := -1
	for {
		start := len(f.hdr)
		if err := f.readLine(); err != nil {
			if err == io.EOF && start == 0 {
				return 0, io.EOF // between frames: the peer closed
			}
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return 0, fmt.Errorf("execproto: frame header: %w", err)
		}
		line := f.hdr[start:]
		if len(line) == 0 {
			if length < 0 {
				return 0, fmt.Errorf("%w: no Content-Length", ErrFrameHeader)
			}
			return length, nil
		}
		if length >= 0 {
			return 0, fmt.Errorf("%w: more than one header line", ErrFrameHeader)
		}
		v, ok := bytes.CutPrefix(line, []byte(lengthPrefix))
		if !ok {
			return 0, fmt.Errorf("%w: the only header allowed is Content-Length", ErrFrameHeader)
		}
		n, err := parseLength(v)
		if err != nil {
			return 0, err
		}
		length = n
	}
}

// readLine appends one header line, without its CRLF, to f.hdr. The whole
// header block shares one MaxHeaderBytes budget.
func (f *FrameReader) readLine() error {
	for {
		b, err := f.br.ReadByte()
		if err != nil {
			return err
		}
		switch b {
		case '\r':
			next, err := f.br.ReadByte()
			if err != nil {
				return err
			}
			if next != '\n' {
				return fmt.Errorf("%w: CR not followed by LF", ErrFrameHeader)
			}
			return nil
		case '\n':
			return fmt.Errorf("%w: LF without CR", ErrFrameHeader)
		}
		if len(f.hdr) >= MaxHeaderBytes {
			return fmt.Errorf("%w: header longer than %d bytes", ErrFrameHeader, MaxHeaderBytes)
		}
		f.hdr = append(f.hdr, b)
	}
}

// parseLength accepts a positive decimal without sign or leading zero. A
// zero-length frame is refused: no JSON-RPC message is empty.
func parseLength(v []byte) (int, error) {
	if len(v) == 0 || v[0] == '0' {
		return 0, fmt.Errorf("%w: Content-Length must be a positive decimal without leading zeros", ErrFrameHeader)
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%w: Content-Length must be a positive decimal without leading zeros", ErrFrameHeader)
		}
	}
	if len(v) > maxLengthDigits {
		return 0, ErrFrameTooLarge
	}
	n := 0
	for _, c := range v {
		n = n*10 + int(c-'0')
	}
	if n > MaxFrameBytes {
		return 0, ErrFrameTooLarge
	}
	return n, nil
}

// FrameWriter writes frames to one byte stream. It is safe for concurrent
// use: frames are written whole and one at a time, which is what makes
// "responses and notifications serialised through one writer" true (A06
// §4.1 ordering). After a failed write the stream is corrupt, so every
// later write fails with the first error.
type FrameWriter struct {
	mu  sync.Mutex
	w   io.Writer
	err error
	hdr []byte
}

// NewFrameWriter returns a FrameWriter on w.
func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: w, hdr: make([]byte, 0, MaxHeaderBytes)}
}

// WriteFrame writes body as one frame. It refuses, without writing
// anything, an empty body or one over MaxFrameBytes: the peer would refuse
// that frame and close the channel.
func (f *FrameWriter) WriteFrame(body []byte) error {
	if len(body) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	if len(body) == 0 {
		return fmt.Errorf("%w: empty frame", ErrFrameHeader)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.hdr = append(f.hdr[:0], lengthPrefix...)
	f.hdr = strconv.AppendInt(f.hdr, int64(len(body)), 10)
	f.hdr = append(f.hdr, "\r\n\r\n"...)
	if _, err := f.w.Write(f.hdr); err != nil {
		f.err = fmt.Errorf("execproto: write frame: %w", err)
		return f.err
	}
	if _, err := f.w.Write(body); err != nil {
		f.err = fmt.Errorf("execproto: write frame: %w", err)
		return f.err
	}
	return nil
}
