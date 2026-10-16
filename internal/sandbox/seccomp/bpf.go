package seccomp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Classic BPF opcodes (include/uapi/linux/bpf_common.h), seccomp return
// values (include/uapi/linux/seccomp.h) and BPF_MAXINSNS
// (include/uapi/linux/bpf_common.h).
const (
	bpfLD   = 0x00
	bpfW    = 0x00
	bpfABS  = 0x20
	bpfJMP  = 0x05
	bpfJA   = 0x00
	bpfJEQ  = 0x10
	bpfJGE  = 0x30
	bpfJSET = 0x40
	bpfK    = 0x00
	bpfRET  = 0x06

	seccompRetKillProcess = 0x80000000
	seccompRetErrno       = 0x00050000
	seccompRetAllow       = 0x7fff0000
	seccompRetData        = 0x0000ffff

	maxInstructions = 4096
	maxErrno        = 4095 // include/linux/err.h MAX_ERRNO

	// struct seccomp_data (include/uapi/linux/seccomp.h), little-endian.
	offNr   = 0
	offArch = 4
	offArgs = 16 // args[i] low word at offArgs + 8*i
)

// Instruction is one struct sock_filter (include/uapi/linux/filter.h).
type Instruction struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// Program renders the A06 §11.5 filter for a as classic BPF:
//
//	ld [arch]; jeq native ? ok : kill
//	ok: ld [nr]; [amd64: jge 0x40000000 ? kill]   ; x32 ABI
//	jeq nr_i ? action_i | filter_i                 ; one per table syscall
//	ret ALLOW
//	filter_i: ld [args[n] low]; compare ? action_i : ALLOW
//	allow: ret ALLOW; kill: ret KILL_PROCESS; one ret per errno action
//
// Syscalls the architecture lacks are skipped (A06 §11.5 "Arch notes").
func Program(a Arch) ([]Instruction, error) {
	if a.AuditArch() == 0 {
		return nil, fmt.Errorf("seccomp: unsupported architecture %v", a)
	}
	s := newAsm()
	allow, killL := s.newLabel(), s.newLabel()

	// Errno actions get one shared ret each, in first-use order.
	var actions []Action
	actionLabel := map[Action]label{}
	labelFor := func(act Action) (label, error) {
		switch {
		case act.Kind == KillProcess:
			return killL, nil
		case act.Kind == Errno && act.Errno > 0 && act.Errno <= maxErrno:
		default:
			return 0, fmt.Errorf("seccomp: invalid action %+v", act)
		}
		if l, ok := actionLabel[act]; ok {
			return l, nil
		}
		l := s.newLabel()
		actionLabel[act] = l
		actions = append(actions, act)
		return l, nil
	}

	ok := s.newLabel()
	s.ld(offArch)
	s.jeq(a.AuditArch(), ok, killL)
	s.mark(ok)
	s.ld(offNr)
	if a == AMD64 {
		next := s.newLabel()
		s.jge(x32SyscallBit, killL, next)
		s.mark(next)
	}

	type filterBlock struct {
		at     label
		filter ArgFilter
		act    Action
		actL   label
	}
	var blocks []filterBlock
	seen := map[string]bool{}
	for _, r := range Table() {
		for _, sc := range r.Syscalls {
			if seen[sc.Name] {
				return nil, fmt.Errorf("seccomp: %s in more than one row", sc.Name)
			}
			seen[sc.Name] = true
			nr, present := a.Nr(sc.Name)
			if !present {
				continue
			}
			actL, err := labelFor(r.Action)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", sc.Name, err)
			}
			target := actL
			if sc.Filter != nil {
				if err := sc.Filter.validate(); err != nil {
					return nil, fmt.Errorf("seccomp: %s: %w", sc.Name, err)
				}
				target = s.newLabel()
				blocks = append(blocks, filterBlock{target, *sc.Filter, r.Action, actL})
			}
			next := s.newLabel()
			s.jeq(nr, target, next)
			s.mark(next)
		}
	}
	s.ret(seccompRetAllow)

	for _, b := range blocks {
		s.mark(b.at)
		s.ld(uint32(offArgs + 8*b.filter.Arg))
		switch b.filter.Kind {
		case AnyBit:
			s.jset(b.filter.Mask, b.actL, allow)
		case In:
			for _, v := range b.filter.Values {
				next := s.newLabel()
				s.jeq(v, b.actL, next)
				s.mark(next)
			}
			s.ret(seccompRetAllow)
		case NotIn:
			for _, v := range b.filter.Values {
				next := s.newLabel()
				s.jeq(v, allow, next)
				s.mark(next)
			}
			s.ret(retValue(b.act))
		}
	}

	s.mark(allow)
	s.ret(seccompRetAllow)
	s.mark(killL)
	s.ret(seccompRetKillProcess)
	for _, act := range actions {
		s.mark(actionLabel[act])
		s.ret(retValue(act))
	}
	return s.assemble()
}

func retValue(act Action) uint32 {
	if act.Kind == KillProcess {
		return seccompRetKillProcess
	}
	return seccompRetErrno | uint32(act.Errno)&seccompRetData
}

func (f ArgFilter) validate() error {
	if f.Arg < 0 || f.Arg > 5 {
		return fmt.Errorf("argument index %d outside 0..5", f.Arg)
	}
	switch f.Kind {
	case AnyBit:
		if f.Mask == 0 {
			return errors.New("AnyBit with an empty mask")
		}
	case In, NotIn:
		if len(f.Values) == 0 {
			return errors.New("value filter without values")
		}
		dup := map[uint32]bool{}
		for _, v := range f.Values {
			if dup[v] {
				return fmt.Errorf("value %#x listed twice", v)
			}
			dup[v] = true
		}
	default:
		return fmt.Errorf("unknown match kind %d", f.Kind)
	}
	return nil
}

// Encode lays out p as the raw little-endian struct sock_filter array.
func Encode(p []Instruction) []byte {
	out := make([]byte, 8*len(p))
	for i, in := range p {
		b := out[8*i:]
		binary.LittleEndian.PutUint16(b[0:], in.Code)
		b[2], b[3] = in.Jt, in.Jf
		binary.LittleEndian.PutUint32(b[4:], in.K)
	}
	return out
}

// Render is Encode(Program(a)): the bytes bwrap reads from --add-seccomp-fd.
func Render(a Arch) ([]byte, error) {
	p, err := Program(a)
	if err != nil {
		return nil, err
	}
	return Encode(p), nil
}

// The assembler. Jumps name labels; assemble lays the program out and
// resolves them. A conditional jump reaches at most 255 instructions ahead
// (8-bit jt/jf), so a branch whose target is further away is relaxed: it
// jumps 0 or 1 ahead to a BPF_JA trampoline, whose offset is 32 bits.
// Relaxing one jump can push others out of range, so layout repeats until
// nothing changes (each pass only grows the program, so it terminates).

type label int

type opKind int

const (
	opPlain opKind = iota // ld, ret
	opCond                // jeq, jge, jset with labels t and f
	opJA                  // ja to label t
)

type op struct {
	kind opKind
	code uint16
	k    uint32
	t, f label
}

type asm struct {
	ops   []op
	marks []int // label -> index in ops, -1 until placed
	err   error
}

func newAsm() *asm { return &asm{} }

func (a *asm) newLabel() label {
	a.marks = append(a.marks, -1)
	return label(len(a.marks) - 1)
}

// mark places l at the next instruction emitted.
func (a *asm) mark(l label) {
	if a.marks[l] != -1 && a.err == nil {
		a.err = fmt.Errorf("seccomp: label %d placed twice", l)
	}
	a.marks[l] = len(a.ops)
}

func (a *asm) ld(off uint32) {
	a.ops = append(a.ops, op{kind: opPlain, code: bpfLD | bpfW | bpfABS, k: off})
}
func (a *asm) ret(k uint32) { a.ops = append(a.ops, op{kind: opPlain, code: bpfRET | bpfK, k: k}) }
func (a *asm) ja(l label)   { a.ops = append(a.ops, op{kind: opJA, code: bpfJMP | bpfJA, t: l}) }
func (a *asm) jeq(k uint32, t, f label) {
	a.ops = append(a.ops, op{kind: opCond, code: bpfJMP | bpfJEQ | bpfK, k: k, t: t, f: f})
}
func (a *asm) jge(k uint32, t, f label) {
	a.ops = append(a.ops, op{kind: opCond, code: bpfJMP | bpfJGE | bpfK, k: k, t: t, f: f})
}
func (a *asm) jset(k uint32, t, f label) {
	a.ops = append(a.ops, op{kind: opCond, code: bpfJMP | bpfJSET | bpfK, k: k, t: t, f: f})
}

// target resolves l for the jump at index i: placed, inside the program and
// strictly ahead (classic BPF has no backward jumps).
func (a *asm) target(i int, l label) (int, error) {
	if int(l) < 0 || int(l) >= len(a.marks) || a.marks[l] < 0 {
		return 0, fmt.Errorf("seccomp: instruction %d jumps to unplaced label %d", i, l)
	}
	at := a.marks[l]
	if at >= len(a.ops) {
		return 0, fmt.Errorf("seccomp: label %d placed after the last instruction", l)
	}
	if at <= i {
		return 0, fmt.Errorf("seccomp: instruction %d jumps backwards to %d", i, at)
	}
	return at, nil
}

func (a *asm) assemble() ([]Instruction, error) {
	if a.err != nil {
		return nil, a.err
	}
	if len(a.ops) == 0 || a.ops[len(a.ops)-1].code != bpfRET|bpfK {
		return nil, errors.New("seccomp: program must end with ret")
	}
	tgtT := make([]int, len(a.ops))
	tgtF := make([]int, len(a.ops))
	for i, o := range a.ops {
		var err error
		switch o.kind {
		case opJA:
			tgtT[i], err = a.target(i, o.t)
		case opCond:
			if tgtT[i], err = a.target(i, o.t); err == nil {
				tgtF[i], err = a.target(i, o.f)
			}
		}
		if err != nil {
			return nil, err
		}
	}

	farT := make([]bool, len(a.ops))
	farF := make([]bool, len(a.ops))
	pos := make([]int, len(a.ops)+1)
	for {
		for i, o := range a.ops {
			size := 1
			if o.kind == opCond {
				if farT[i] {
					size++
				}
				if farF[i] {
					size++
				}
			}
			pos[i+1] = pos[i] + size
		}
		if pos[len(a.ops)] > maxInstructions {
			return nil, fmt.Errorf("seccomp: program has %d instructions, the kernel accepts %d", pos[len(a.ops)], maxInstructions)
		}
		changed := false
		for i, o := range a.ops {
			if o.kind != opCond {
				continue
			}
			if !farT[i] && pos[tgtT[i]]-(pos[i]+1) > 255 {
				farT[i], changed = true, true
			}
			if !farF[i] && pos[tgtF[i]]-(pos[i]+1) > 255 {
				farF[i], changed = true, true
			}
		}
		if !changed {
			break
		}
	}

	out := make([]Instruction, 0, pos[len(a.ops)])
	for i, o := range a.ops {
		here := pos[i]
		switch o.kind {
		case opPlain:
			out = append(out, Instruction{Code: o.code, K: o.k})
		case opJA:
			out = append(out, Instruction{Code: o.code, K: uint32(pos[tgtT[i]] - (here + 1))})
		case opCond:
			in := Instruction{Code: o.code, K: o.k}
			slot := here + 1 // first trampoline, if any
			if farT[i] {
				in.Jt = 0
			} else {
				in.Jt = uint8(pos[tgtT[i]] - (here + 1))
			}
			if farF[i] {
				in.Jf = 0
				if farT[i] {
					in.Jf = 1
				}
			} else {
				in.Jf = uint8(pos[tgtF[i]] - (here + 1))
			}
			out = append(out, in)
			if farT[i] {
				out = append(out, Instruction{Code: bpfJMP | bpfJA, K: uint32(pos[tgtT[i]] - (slot + 1))})
				slot++
			}
			if farF[i] {
				out = append(out, Instruction{Code: bpfJMP | bpfJA, K: uint32(pos[tgtF[i]] - (slot + 1))})
			}
		}
	}
	return out, nil
}
