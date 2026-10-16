package seccomp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The assembler must keep every conditional jump within the 8-bit jt/jf
// range and reach far targets through BPF_JA, which has a 32-bit offset.
// The A06 program is short today (well under 255 instructions), so these
// tests build long programs on purpose: a table that grows, or a filter with
// many values, must not silently wrap an offset and jump to the wrong verdict.

// TestAssembler_FarTrueTargets dispatches 1000 values to 1000 distinct
// returns, so most true targets are more than 255 instructions away.
func TestAssembler_FarTrueTargets(t *testing.T) {
	const n = 1000
	a := newAsm()
	targets := make([]label, n)
	for i := range targets {
		targets[i] = a.newLabel()
	}
	a.ld(0)
	for i := 0; i < n; i++ {
		next := a.newLabel()
		a.jeq(uint32(i), targets[i], next)
		a.mark(next)
	}
	a.ret(0xaaaa)
	for i := 0; i < n; i++ {
		a.mark(targets[i])
		a.ret(uint32(0x10000 + i))
	}
	p, err := a.assemble()
	if err != nil {
		t.Fatal(err)
	}
	if err := check(p); err != nil {
		t.Fatalf("kernel would refuse: %v", err)
	}
	// Every value, so every jump distance from 1 to past 1000 is crossed,
	// including exactly 255 and 256.
	for nr := uint32(0); nr < n; nr++ {
		r, err := run(p, seccompData{nr: nr})
		if err != nil {
			t.Fatal(err)
		}
		if r != 0x10000+nr {
			t.Errorf("nr %d returned %#x, want %#x", nr, r, 0x10000+nr)
		}
	}
	if r, _ := run(p, seccompData{nr: n + 5}); r != 0xaaaa {
		t.Errorf("unmatched nr returned %#x, want 0xaaaa", r)
	}
}

// TestAssembler_FarBothTargets puts both branches of one jump far away.
func TestAssembler_FarBothTargets(t *testing.T) {
	a := newAsm()
	yes, no := a.newLabel(), a.newLabel()
	a.ld(0)
	a.jset(0x4, yes, no)
	for i := 0; i < 300; i++ {
		a.ret(0xdead) // padding nothing reaches
	}
	a.mark(no)
	a.ret(1)
	for i := 0; i < 300; i++ {
		a.ret(0xdead)
	}
	a.mark(yes)
	a.ret(2)
	p, err := a.assemble()
	if err != nil {
		t.Fatal(err)
	}
	if err := check(p); err != nil {
		t.Fatalf("kernel would refuse: %v", err)
	}
	for nr, want := range map[uint32]uint32{0: 1, 4: 2, 5: 2, 3: 1} {
		if r, _ := run(p, seccompData{nr: nr}); r != want {
			t.Errorf("nr %d returned %d, want %d", nr, r, want)
		}
	}
}

// TestAssembler_FarFalseTarget puts only the false branch far away, with the
// true branch adjacent (the shape of a NotIn filter's last compare).
func TestAssembler_FarFalseTarget(t *testing.T) {
	a := newAsm()
	yes, no := a.newLabel(), a.newLabel()
	a.ld(0)
	a.jge(10, yes, no)
	a.mark(yes)
	a.ret(2)
	for i := 0; i < 400; i++ {
		a.ret(0xdead)
	}
	a.mark(no)
	a.ret(1)
	p, err := a.assemble()
	if err != nil {
		t.Fatal(err)
	}
	if err := check(p); err != nil {
		t.Fatalf("kernel would refuse: %v", err)
	}
	for nr, want := range map[uint32]uint32{9: 1, 10: 2, 11: 2, 0: 1} {
		if r, _ := run(p, seccompData{nr: nr}); r != want {
			t.Errorf("nr %d returned %d, want %d", nr, r, want)
		}
	}
}

// TestAssembler_JumpDistanceBoundary places each branch's target at every
// distance from 250 to 260 instructions: 255 is the last one jt/jf can hold,
// 256 would wrap to 0 in a uint8 and land on the wrong verdict.
func TestAssembler_JumpDistanceBoundary(t *testing.T) {
	for dist := 250; dist <= 260; dist++ {
		for _, farIsTrue := range []bool{true, false} {
			a := newAsm()
			near, far := a.newLabel(), a.newLabel()
			a.ld(0)
			if farIsTrue {
				a.jeq(1, far, near)
			} else {
				a.jeq(1, near, far)
			}
			a.mark(near)
			a.ret(7)
			for i := 0; i < dist-1; i++ {
				a.ret(0xdead)
			}
			a.mark(far)
			a.ret(9)
			p, err := a.assemble()
			if err != nil {
				t.Fatal(err)
			}
			if err := check(p); err != nil {
				t.Fatalf("distance %d: kernel would refuse: %v", dist, err)
			}
			wantFar, wantNear := uint32(1), uint32(0)
			if !farIsTrue {
				wantFar, wantNear = 0, 1
			}
			if r, _ := run(p, seccompData{nr: wantFar}); r != 9 {
				t.Errorf("distance %d (far branch true=%v): far target returned %#x, want 9", dist, farIsTrue, r)
			}
			if r, _ := run(p, seccompData{nr: wantNear}); r != 7 {
				t.Errorf("distance %d (far branch true=%v): near target returned %#x, want 7", dist, farIsTrue, r)
			}
		}
	}
}

// TestAssembler_Refuses covers the programs the kernel or the assembler must
// not accept: too long, a label used but never placed, a label placed twice,
// and a jump backwards (cBPF jumps only forward).
func TestAssembler_Refuses(t *testing.T) {
	long := newAsm()
	for i := 0; i < tMaxIns+1; i++ {
		long.ret(0)
	}
	unplaced := newAsm()
	unplaced.ld(0)
	unplaced.jeq(1, unplaced.newLabel(), unplaced.newLabel())
	unplaced.ret(0)
	twice := newAsm()
	l := twice.newLabel()
	twice.mark(l)
	twice.ret(0)
	twice.mark(l)
	twice.ret(1)
	back := newAsm()
	top := back.newLabel()
	back.mark(top)
	back.ld(0)
	back.ja(top)
	back.ret(0)
	for name, a := range map[string]*asm{"too long": long, "unplaced": unplaced, "placed twice": twice, "backwards": back} {
		if _, err := a.assemble(); err == nil {
			t.Errorf("%s: assembled without error", name)
		}
	}
}

// TestEncode_Layout pins the struct sock_filter byte layout: u16 code, u8 jt,
// u8 jf, u32 k, little-endian, 8 bytes each, no header.
func TestEncode_Layout(t *testing.T) {
	got := Encode([]Instruction{{Code: 0x15, Jt: 1, Jf: 2, K: 0xc000003e}, {Code: 0x06, K: 0x7fff0000}})
	want := []byte{0x15, 0x00, 1, 2, 0x3e, 0x00, 0x00, 0xc0, 0x06, 0x00, 0, 0, 0x00, 0x00, 0xff, 0x7f}
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode = % x\nwant     % x", got, want)
	}
	if binary.LittleEndian.Uint32(got[4:]) != 0xc000003e {
		t.Error("k is not little-endian")
	}
}
