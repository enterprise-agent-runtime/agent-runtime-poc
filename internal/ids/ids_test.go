package ids

import (
	"bytes"
	"sort"
	"testing"
	"time"
)

func TestULID_KnownEncoding(t *testing.T) {
	// ULID spec example: 1469922850259 ms → 01ARZ3NDEK…, here with all-zero entropy.
	ts := time.UnixMilli(1469922850259)
	g := NewWith(func() time.Time { return ts }, bytes.NewReader(make([]byte, 10)))
	if got := g.ULID(); got != "01ARZ3NDEK0000000000000000" {
		t.Fatalf("ULID = %s", got)
	}
	g = NewWith(func() time.Time { return time.UnixMilli(0) }, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	if got := g.ULID(); got != "0000000000ZZZZZZZZZZZZZZZZ" {
		t.Fatalf("max entropy = %s", got)
	}
}

func TestULID_MonotonicWithinMillisecond(t *testing.T) {
	ts := time.UnixMilli(1700000000000)
	g := NewWith(func() time.Time { return ts }, bytes.NewReader(make([]byte, 100)))
	var got []string
	for i := 0; i < 50; i++ {
		got = append(got, g.New(Event))
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("not sorted: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Fatal("duplicate id")
		}
	}
}

func TestValidAndTime(t *testing.T) {
	id := NewID(Session)
	if !Valid(Session, id) || len(id) != 30 {
		t.Fatalf("id %s invalid", id)
	}
	if Valid(Event, id) || Valid(Session, "ses_01ARYZ6S41000000000000000I") || Valid(Session, "ses_short") || Valid(Session, "ses_81ARYZ6S410000000000000000") {
		t.Fatal("invalid id accepted")
	}
	if c := NewID(Call); len(c) != 31 || !Valid(Call, c) {
		t.Fatalf("call id %s", c)
	}
	ts, err := Time("01ARZ3NDEK0000000000000000")
	if err != nil || ts.UnixMilli() != 1469922850259 {
		t.Fatalf("time = %v %v", ts, err)
	}
	if _, err := Time("short"); err == nil {
		t.Fatal("short accepted")
	}
	if _, err := Time("01ARYZ6S4U0000000000000000"); err == nil {
		t.Fatal("bad character accepted")
	}
}
