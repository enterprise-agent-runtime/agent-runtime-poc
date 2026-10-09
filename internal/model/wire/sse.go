// Package wire holds the HTTP plumbing shared by the provider adapters:
// the SSE line reader (design A11 §6.2), the credential-injecting transport
// (A11 §5.3), TLS and mTLS setup (A11 §5.4), the first-byte/idle watchdog
// (A11 §7.2), retry-after parsing (A11 §7.3) and transport-error
// normalization (A11 §8 rows 1–5). It lives under internal/model so that
// both adapters can use it while importing nothing else from the daemon
// (CLAUDE.md §6; docs/DECISIONS-poc.md D-007).
package wire

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// MaxLine is the longest SSE line accepted (A11 §6.2 rule 1).
const MaxLine = 8 << 20

// ErrLineTooLong is returned when a line exceeds MaxLine.
var ErrLineTooLong = errors.New("sse line too long")

// Event is one dispatched server-sent event.
type Event struct {
	Name string // the "event:" field; "" when absent
	Data string // "data:" lines joined with "\n"
}

// SSEReader parses a text/event-stream body. OnLine is called for every
// line read, comments included, so the caller can reset an idle timer.
type SSEReader struct {
	br     *bufio.Reader
	OnLine func()
	max    int
}

// NewSSEReader returns a reader over r.
func NewSSEReader(r io.Reader, onLine func()) *SSEReader {
	return &SSEReader{br: bufio.NewReaderSize(r, 64<<10), OnLine: onLine, max: MaxLine}
}

// readLine returns one line without its terminator; "\n", "\r\n" and a
// lone "\r" all end a line. At end of input with no pending bytes it
// returns io.EOF; a final unterminated line is returned before io.EOF.
func (s *SSEReader) readLine() (string, error) {
	var sb strings.Builder
	for {
		b, err := s.br.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && sb.Len() > 0 {
				return sb.String(), nil
			}
			return "", err
		}
		switch b {
		case '\n':
			return sb.String(), nil
		case '\r':
			if nb, err := s.br.Peek(1); err == nil && nb[0] == '\n' {
				_, _ = s.br.ReadByte()
			}
			return sb.String(), nil
		}
		if sb.Len() >= s.max {
			return "", ErrLineTooLong
		}
		sb.WriteByte(b)
	}
}

// Next returns the next event with non-empty data. At the end of the body
// it returns io.EOF (an event whose data was not yet dispatched by a blank
// line is still returned first).
func (s *SSEReader) Next() (Event, error) {
	var ev Event
	var data []string
	for {
		line, err := s.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(data) > 0 {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			return Event{}, err
		}
		if s.OnLine != nil {
			s.OnLine()
		}
		if line == "" {
			if len(data) > 0 {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			ev = Event{}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keep-alive
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			ev.Name = value
		}
	}
}
