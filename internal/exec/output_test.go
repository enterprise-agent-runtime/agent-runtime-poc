package exec

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"warden.dev/warden/internal/execproto"
)

type sink struct{ got []execproto.IONotification }

func (s *sink) put(n execproto.IONotification) error { s.got = append(s.got, n); return nil }

// bytes decodes a chunk's data.
func chunkBytes(t *testing.T, n execproto.IONotification) string {
	t.Helper()
	if n.Encoding == execproto.EncodingBase64 {
		b, err := base64.StdEncoding.DecodeString(n.Data)
		if err != nil {
			t.Fatalf("chunk %d: bad base64: %v", n.Seq, err)
		}
		return string(b)
	}
	return n.Data
}

func checkSeq(t *testing.T, got []execproto.IONotification) {
	t.Helper()
	for i, n := range got {
		if n.Seq != int64(i) || n.Handle != "ph_1" || n.CallID != "call_1" {
			t.Fatalf("chunk %d: seq %d handle %q call %q; seq must count 0,1,2… per handle across streams", i, n.Seq, n.Handle, n.CallID)
		}
	}
}

// TestOutput_ChunksAtTickOr16KiB guards design A06 §7.2: chunks go out at
// most every 100 ms (the tick) or once 16 KiB are buffered, never larger
// than 16 KiB, with seq increasing per handle across stdout and stderr so
// the daemon can interleave the two in order. The pump decides nothing by
// wall clock: the test is the tick.
func TestOutput_ChunksAtTickOr16KiB(t *testing.T) {
	var s sink
	o := newOutput("ph_1", "call_1", 1<<20, s.put)
	o.write(execproto.StreamStdout, []byte("hello "))
	o.write(execproto.StreamStderr, []byte("warn"))
	if len(s.got) != 0 {
		t.Fatalf("chunk sent before the tick and under 16 KiB: %+v", s.got)
	}
	o.tick()
	if len(s.got) != 2 || s.got[0].Stream != execproto.StreamStdout || s.got[0].Data != "hello " ||
		s.got[1].Stream != execproto.StreamStderr || s.got[1].Data != "warn" {
		t.Fatalf("after the tick: %+v", s.got)
	}
	o.tick()
	if len(s.got) != 2 {
		t.Fatalf("an empty tick sent a chunk: %+v", s.got[2:])
	}
	big := strings.Repeat("x", 40000)
	o.write(execproto.StreamStdout, []byte(big))
	if len(s.got) != 4 {
		t.Fatalf("40000 bytes: %d chunks before the tick, want 2 full 16 KiB chunks", len(s.got)-2)
	}
	o.finish()
	total := ""
	for _, n := range s.got[2:] {
		if len(chunkBytes(t, n)) > chunkMax {
			t.Errorf("chunk %d has %d bytes, over 16 KiB", n.Seq, len(chunkBytes(t, n)))
		}
		if n.Segment != execproto.SegmentHead || n.Encoding != execproto.EncodingUTF8 {
			t.Errorf("chunk %d: segment %q encoding %q", n.Seq, n.Segment, n.Encoding)
		}
		total += chunkBytes(t, n)
	}
	if total != big {
		t.Errorf("stdout lost or reordered bytes: %d of %d", len(total), len(big))
	}
	checkSeq(t, s.got)
	b, truncated := o.stats()
	if truncated || b.Stdout != 40006 || b.Stderr != 4 || b.Forwarded != 40010 || b.Discarded != 0 {
		t.Errorf("stats = %+v truncated %v", b, truncated)
	}
}

// TestOutput_UTF8 guards A06 §7.2's encoding rule: a chunk is UTF-8 when
// valid; an incomplete trailing sequence (at most 3 bytes) waits for the
// next chunk instead of turning both into base64; invalid data goes as
// base64, so no byte is lost or altered on the way to the redactor.
func TestOutput_UTF8(t *testing.T) {
	var s sink
	o := newOutput("ph_1", "call_1", 1<<20, s.put)
	euro := "\xe2\x82\xac" // U+20AC, built from bytes
	o.write(execproto.StreamStdout, []byte("a"+euro[:2]))
	o.tick()
	o.write(execproto.StreamStdout, []byte(euro[2:]+"b"))
	o.tick()
	o.write(execproto.StreamStdout, []byte("\xff\xfe"))
	o.tick()
	o.write(execproto.StreamStdout, []byte(euro[:1]))
	o.finish() // the end of the stream: a held partial sequence goes as base64
	want := []struct {
		data string
		enc  execproto.Encoding
	}{
		{"a", execproto.EncodingUTF8},
		{euro + "b", execproto.EncodingUTF8},
		{"\xff\xfe", execproto.EncodingBase64},
		{euro[:1], execproto.EncodingBase64},
	}
	if len(s.got) != len(want) {
		t.Fatalf("chunks %+v", s.got)
	}
	for i, w := range want {
		if chunkBytes(t, s.got[i]) != w.data || s.got[i].Encoding != w.enc {
			t.Errorf("chunk %d = %q (%s), want %q (%s)", i, chunkBytes(t, s.got[i]), s.got[i].Encoding, w.data, w.enc)
		}
	}
	// A rune split by the 16 KiB boundary is not split between chunks.
	var s2 sink
	o2 := newOutput("ph_1", "call_1", 1<<20, s2.put)
	o2.write(execproto.StreamStdout, []byte(strings.Repeat("x", chunkMax-1)+euro+"y"))
	o2.finish()
	if len(s2.got) != 2 || s2.got[0].Encoding != execproto.EncodingUTF8 || s2.got[1].Encoding != execproto.EncodingUTF8 ||
		chunkBytes(t, s2.got[1]) != euro+"y" {
		t.Errorf("rune across the 16 KiB boundary: %d chunks, second %q", len(s2.got), chunkBytes(t, s2.got[len(s2.got)-1]))
	}
}

// TestOutput_CapKeepsHeadAndTail guards design A06 §7.5: max_output_bytes
// caps the combined stdout and stderr the daemon receives. The first
// max − tail bytes go live, the last tail = min(64 KiB, max/4) bytes are
// kept and sent after one marker chunk at exit, everything between is
// drained and counted, never forwarded. Test runners print their summary
// last, so the summary survives truncation (H4: verification reads it).
func TestOutput_CapKeepsHeadAndTail(t *testing.T) {
	var s sink
	const max = 4096 // tail 1024, head 3072
	o := newOutput("ph_1", "call_1", max, s.put)
	o.write(execproto.StreamStdout, []byte(strings.Repeat("h", 3000)))
	o.write(execproto.StreamStderr, []byte(strings.Repeat("e", 100))) // 3100: head ends at 3072
	for i := 0; i < 50; i++ {
		o.write(execproto.StreamStdout, []byte(strings.Repeat("m", 1000)))
	}
	summary := "Tests: 12 passed, 1 failed\n"
	o.write(execproto.StreamStdout, []byte(strings.Repeat("t", 1024-len(summary))))
	o.write(execproto.StreamStdout, []byte(summary))
	o.tick()
	o.finish()

	var head, tail strings.Builder
	var marker *execproto.IONotification
	for i, n := range s.got {
		switch {
		case n.Stream == execproto.StreamMarker:
			if marker != nil {
				t.Fatal("two markers")
			}
			marker = &s.got[i]
		case marker == nil:
			if n.Segment != execproto.SegmentHead {
				t.Errorf("chunk %d before the marker has segment %q", n.Seq, n.Segment)
			}
			head.WriteString(chunkBytes(t, n))
		default:
			if n.Segment != execproto.SegmentTail {
				t.Errorf("chunk %d after the marker has segment %q", n.Seq, n.Segment)
			}
			tail.WriteString(chunkBytes(t, n))
		}
	}
	if marker == nil {
		t.Fatal("no marker chunk")
	}
	discarded := int64(3100+50000+1024) - max
	if want := fmt.Sprintf("\n[warden: output truncated: %d bytes omitted]\n", discarded); marker.Data != want || marker.Encoding != execproto.EncodingUTF8 {
		t.Errorf("marker = %q, want %q", marker.Data, want)
	}
	if want := strings.Repeat("h", 3000) + strings.Repeat("e", 72); head.String() != want {
		t.Errorf("head has %d bytes, want the first 3072", head.Len())
	}
	if !strings.HasSuffix(tail.String(), summary) || tail.Len() != 1024 {
		t.Errorf("tail has %d bytes ending %q; want the last 1024 with the summary", tail.Len(), tail.String()[max0(tail.Len()-40):])
	}
	checkSeq(t, s.got)
	b, truncated := o.stats()
	if !truncated || b.Discarded != discarded || b.Forwarded != max || b.Stdout != 3000+50000+1024 || b.Stderr != 100 {
		t.Errorf("stats = %+v truncated %v", b, truncated)
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// TestOutput_UnderCapNoMarker: output above head but within the cap loses
// nothing and gets no marker; the held bytes go out at exit, still in the
// head segment, and nothing reports truncation.
func TestOutput_UnderCapNoMarker(t *testing.T) {
	var s sink
	o := newOutput("ph_1", "call_1", 4096, s.put)
	all := strings.Repeat("a", 3500) + strings.Repeat("b", 596) // exactly the cap
	o.write(execproto.StreamStdout, []byte(all))
	o.finish()
	got := ""
	for _, n := range s.got {
		if n.Stream == execproto.StreamMarker || n.Segment != execproto.SegmentHead {
			t.Errorf("chunk %d: stream %q segment %q", n.Seq, n.Stream, n.Segment)
		}
		got += chunkBytes(t, n)
	}
	if got != all {
		t.Errorf("forwarded %d of %d bytes", len(got), len(all))
	}
	if b, truncated := o.stats(); truncated || b.Discarded != 0 || b.Forwarded != 4096 {
		t.Errorf("stats = %+v truncated %v", b, truncated)
	}
}
