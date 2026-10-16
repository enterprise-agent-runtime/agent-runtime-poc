package exec

// Output streams of a spawned process (design A06 §7.2 streams and
// chunking, §7.5 output cap and truncation marker). This is the pure part:
// the pumps in proc_unix.go read the pipes and feed write; a ticker calls
// tick; the end of the call calls finish. The package doc is in server.go.

import (
	"encoding/base64"
	"strconv"
	"sync"
	"unicode/utf8"

	"warden.dev/warden/internal/execproto"
)

// Chunking constants of A06 §7.2 and §7.5.
const (
	chunkMax   = 16 << 10 // bytes per chunk before encoding, and the pump's read buffer
	tailMax    = 64 << 10 // the tail kept for after the marker is at most this
	maxHoldUTF = 3        // an incomplete trailing UTF-8 sequence held for the next chunk
)

// output turns one handle's stdout and stderr into exec.proc.io chunks.
//
//   - A chunk goes out on a tick (every 100 ms, A06 §7.2) or as soon as 16
//     KiB of one stream are buffered; no chunk is larger than 16 KiB.
//   - seq counts per handle across both streams, the marker and the tail,
//     in the order the chunks reach sink. sink is called with the lock
//     held, so the order of seq is the order on the wire.
//   - Data is UTF-8 when valid. An incomplete trailing sequence of at most
//     3 bytes waits for the next chunk; anything else that is not valid
//     UTF-8 goes as base64, so no byte is altered before the daemon's
//     redactor sees it (the executor does not redact, §7.2).
//   - The cap (A06 §7.5): of max combined bytes, the first head = max −
//     tail go out live, where tail = min(64 KiB, max/4); the last tail
//     bytes are kept in a ring; everything between is counted as
//     discarded and dropped (the pumps keep reading, so the child never
//     blocks on a full pipe). At finish, if anything was discarded, one
//     marker chunk "\n[warden: output truncated: <n> bytes omitted]\n"
//     precedes the tail chunks (segment tail); otherwise the held bytes go
//     out as ordinary head chunks and nothing reports truncation.
//
// A sink error (the connection is gone) stops further sink calls; the
// counts go on, so the wait result still says what the process wrote.
type output struct {
	mu             sync.Mutex
	handle, callID string
	sink           func(execproto.IONotification) error
	sinkErr        error

	seq     int64
	head    int64     // live bytes still allowed
	tailCap int64     // ring capacity
	pending [2][]byte // per stream (stdout, stderr), within head, not yet sent
	ring    []segment
	ringLen int64

	stdout, stderr, forwarded, discarded int64
	finished                             bool
}

type segment struct {
	stream execproto.Stream
	b      []byte
}

func newOutput(handle, callID string, max int64, sink func(execproto.IONotification) error) *output {
	tail := min(int64(tailMax), max/4)
	return &output{handle: handle, callID: callID, sink: sink, head: max - tail, tailCap: tail}
}

func streamIndex(s execproto.Stream) int {
	if s == execproto.StreamStderr {
		return 1
	}
	return 0
}

func streamOf(i int) execproto.Stream {
	if i == 1 {
		return execproto.StreamStderr
	}
	return execproto.StreamStdout
}

// write takes bytes one pump read from stream s (stdout or stderr).
func (o *output) write(s execproto.Stream, p []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s == execproto.StreamStderr {
		o.stderr += int64(len(p))
	} else {
		o.stdout += int64(len(p))
	}
	if o.finished {
		o.discarded += int64(len(p))
		return
	}
	live := min(int64(len(p)), o.head)
	i := streamIndex(s)
	if live > 0 {
		o.pending[i] = append(o.pending[i], p[:live]...)
		o.head -= live
		o.flushFull(i)
	}
	if rest := p[live:]; len(rest) > 0 {
		o.keep(s, rest)
	}
}

// keep appends to the tail ring and drops what no longer fits, counting it
// as discarded. After a cut, continuation bytes at the new front are
// dropped too, so the tail starts on a character boundary.
func (o *output) keep(s execproto.Stream, p []byte) {
	if n := len(o.ring); n > 0 && o.ring[n-1].stream == s {
		o.ring[n-1].b = append(o.ring[n-1].b, p...)
	} else {
		o.ring = append(o.ring, segment{stream: s, b: append([]byte(nil), p...)})
	}
	o.ringLen += int64(len(p))
	cut := false
	for o.ringLen > o.tailCap && len(o.ring) > 0 {
		cut = true
		first := &o.ring[0]
		n := min(int64(len(first.b)), o.ringLen-o.tailCap)
		first.b = first.b[n:]
		o.ringLen -= n
		o.discarded += n
		if len(first.b) == 0 {
			o.ring = o.ring[1:]
		}
	}
	if cut && len(o.ring) > 0 {
		first := &o.ring[0]
		for k := 0; k < maxHoldUTF && len(first.b) > 0 && !utf8.RuneStart(first.b[0]); k++ {
			first.b = first.b[1:]
			o.ringLen--
			o.discarded++
		}
		if len(first.b) == 0 {
			o.ring = o.ring[1:]
		}
	}
}

// tick sends what is pending on both streams (A06 §7.2: at most every
// 100 ms), holding back an incomplete trailing UTF-8 sequence.
func (o *output) tick() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return
	}
	o.flush(0, false)
	o.flush(1, false)
}

// finish sends everything left once both pipes are closed: the pending
// head bytes, then the marker and the tail when bytes were discarded, or
// the held bytes as head chunks when none were.
func (o *output) finish() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return
	}
	o.flush(0, true)
	o.flush(1, true)
	o.finished = true
	seg := execproto.SegmentHead
	if o.discarded > 0 {
		marker := "\n[warden: output truncated: " + strconv.FormatInt(o.discarded, 10) + " bytes omitted]\n"
		o.emit(execproto.StreamMarker, []byte(marker), execproto.SegmentTail, false)
		seg = execproto.SegmentTail
	}
	for _, sg := range o.ring {
		pieces, _ := splitChunks(sg.b, true)
		for _, piece := range pieces {
			o.emit(sg.stream, piece, seg, true)
		}
	}
	o.ring, o.ringLen = nil, 0
}

// flush sends stream i's pending bytes in chunks of at most 16 KiB. Unless
// final, an incomplete trailing UTF-8 sequence stays pending.
func (o *output) flush(i int, final bool) {
	pieces, rest := splitChunks(o.pending[i], final)
	for _, piece := range pieces {
		o.emit(streamOf(i), piece, execproto.SegmentHead, true)
	}
	o.pending[i] = append(o.pending[i][:0], rest...)
}

// flushFull sends stream i's whole 16 KiB chunks and keeps the rest
// pending for the next tick (A06 §7.2 "or when 16 KiB are buffered").
func (o *output) flushFull(i int) {
	buf := o.pending[i]
	sent := 0
	for len(buf)-sent >= chunkMax {
		piece := buf[sent : sent+chunkMax]
		if k := incompleteTail(piece); k > 0 && utf8.Valid(piece[:len(piece)-k]) {
			piece = piece[:len(piece)-k]
		}
		o.emit(streamOf(i), piece, execproto.SegmentHead, true)
		sent += len(piece)
	}
	if sent > 0 {
		o.pending[i] = append(o.pending[i][:0], buf[sent:]...)
	}
}

// splitChunks cuts buf into chunks of at most chunkMax bytes. A chunk that
// ends in an incomplete UTF-8 sequence after valid text is cut before the
// sequence, which then starts the next chunk; unless final, a remainder
// that is only such a sequence is returned as rest.
func splitChunks(buf []byte, final bool) (pieces [][]byte, rest []byte) {
	for len(buf) > 0 {
		n := min(len(buf), chunkMax)
		piece := buf[:n]
		if !final || n < len(buf) {
			if k := incompleteTail(piece); k > 0 && (k == n || utf8.Valid(piece[:n-k])) {
				piece = piece[:n-k]
			}
		}
		if len(piece) == 0 {
			if final {
				piece = buf[:n] // cannot happen with n < len(buf): a held tail is at most 3 bytes
			} else {
				return pieces, buf
			}
		}
		pieces = append(pieces, piece)
		buf = buf[len(piece):]
	}
	return pieces, nil
}

// incompleteTail is the length of a trailing UTF-8 sequence that has a
// valid start but not all its continuation bytes, 0 when there is none.
func incompleteTail(b []byte) int {
	for k := 1; k <= maxHoldUTF && k <= len(b); k++ {
		c := b[len(b)-k]
		if !utf8.RuneStart(c) {
			continue
		}
		if c < utf8.RuneSelf || utf8.FullRune(b[len(b)-k:]) {
			return 0
		}
		return k
	}
	return 0
}

// emit sends one chunk. counted says whether its bytes are process output
// (the marker is not).
func (o *output) emit(s execproto.Stream, b []byte, seg execproto.Segment, counted bool) {
	n := execproto.IONotification{Handle: o.handle, CallID: o.callID, Seq: o.seq, Stream: s, Segment: seg, Encoding: execproto.EncodingUTF8}
	if utf8.Valid(b) {
		n.Data = string(b)
	} else {
		n.Data = base64.StdEncoding.EncodeToString(b)
		n.Encoding = execproto.EncodingBase64
	}
	o.seq++
	if counted {
		o.forwarded += int64(len(b))
	}
	if o.sinkErr == nil {
		o.sinkErr = o.sink(n)
	}
}

// stats returns the byte counts of exec.proc.wait (all four always) and
// whether anything was discarded.
func (o *output) stats() (execproto.WaitBytes, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return execproto.WaitBytes{Stdout: o.stdout, Stderr: o.stderr, Forwarded: o.forwarded, Discarded: o.discarded}, o.discarded > 0
}
