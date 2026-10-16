// Package ids generates the prefixed ULID identifiers of design core §2
// (ses_, evt_, call_, …): "<prefix>_" + 26 Crockford base32 characters,
// 48-bit millisecond time then 80 bits of entropy, monotonic within a
// millisecond so that ids created by one generator sort in creation order.
// It replaces oklog/ulid, which is not an allowed dependency
// (docs/DECISIONS-poc.md D-009).
package ids

import (
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

// Prefixes of design core §2.
const (
	Workspace    = "wsp"
	Session      = "ses"
	Run          = "wfr"
	Task         = "tsk"
	Execution    = "exe"
	Event        = "evt"
	Artifact     = "art"
	Approval     = "apr"
	Decision     = "dec"
	Call         = "call"
	ModelCall    = "mc"
	Routing      = "rt"
	Sandbox      = "sb"
	Subscription = "sub"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Generator produces monotonic ULIDs. The zero value is not usable; use New.
type Generator struct {
	mu      sync.Mutex
	now     func() time.Time
	entropy io.Reader
	lastMS  uint64
	hasLast bool
	last    [10]byte
}

// New returns a generator using the wall clock and crypto/rand.
func New() *Generator { return &Generator{now: time.Now, entropy: rand.Reader} }

// NewWith returns a generator with an injected clock and entropy source
// (deterministic tests and fixtures).
func NewWith(now func() time.Time, entropy io.Reader) *Generator {
	return &Generator{now: now, entropy: entropy}
}

// ULID returns a 26-character ULID.
func (g *Generator) ULID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := uint64(g.now().UnixMilli())
	if g.hasLast && ms == g.lastMS {
		// Same millisecond: increment the entropy so ids stay ordered.
		for i := len(g.last) - 1; i >= 0; i-- {
			g.last[i]++
			if g.last[i] != 0 {
				break
			}
		}
	} else {
		if _, err := io.ReadFull(g.entropy, g.last[:]); err != nil {
			panic("ids: entropy source failed: " + err.Error()) // crypto/rand never fails on supported OSes
		}
		g.lastMS, g.hasLast = ms, true
	}
	return encode(ms, g.last)
}

// New returns "<prefix>_<ULID>".
func (g *Generator) New(prefix string) string { return prefix + "_" + g.ULID() }

func encode(ms uint64, e [10]byte) string {
	var b [16]byte
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	copy(b[6:], e[:])
	// 128 bits → 26 base32 characters (the first carries 3 bits).
	var out [26]byte
	var acc uint64
	bits := 0
	idx := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 && idx >= 0 {
			out[idx] = alphabet[acc&31]
			acc >>= 5
			bits -= 5
			idx--
		}
	}
	if idx >= 0 {
		out[idx] = alphabet[acc&31]
	}
	return string(out[:])
}

// Time returns the timestamp encoded in a ULID.
func Time(ulid string) (time.Time, error) {
	if len(ulid) != 26 {
		return time.Time{}, errors.New("ulid must be 26 characters")
	}
	var ms uint64
	for i := 0; i < 10; i++ {
		v := strings.IndexByte(alphabet, ulid[i])
		if v < 0 {
			return time.Time{}, errors.New("invalid ulid character")
		}
		ms = ms<<5 | uint64(v)
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}

// Valid reports whether s is "<prefix>_" + a 26-character Crockford ULID.
func Valid(prefix, s string) bool {
	u, ok := strings.CutPrefix(s, prefix+"_")
	if !ok || len(u) != 26 || u[0] > '7' {
		return false
	}
	for i := 0; i < len(u); i++ {
		if strings.IndexByte(alphabet, u[i]) < 0 {
			return false
		}
	}
	return true
}

var std = New()

// NewID returns a new "<prefix>_<ULID>" from the process-wide generator.
func NewID(prefix string) string { return std.New(prefix) }
