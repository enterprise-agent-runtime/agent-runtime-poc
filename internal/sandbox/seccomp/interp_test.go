package seccomp

import (
	"encoding/binary"
	"fmt"
)

// This file is a classic-BPF interpreter for seccomp programs, written for
// the tests only. It reads the raw bytes (so Encode is under test too),
// checks them the way the kernel does before it accepts a filter
// (kernel/bpf/core.c bpf_check_classic and kernel/seccomp.c
// seccomp_check_filter), and runs them over a synthetic struct seccomp_data.
// It accepts only the opcodes a seccomp program may use and that the
// assembler emits; anything else is an error, never a silent allow.

// cBPF opcodes (include/uapi/linux/bpf_common.h).
const (
	tLdWAbs = 0x20 // BPF_LD (0) | BPF_W (0) | BPF_ABS (0x20)
	tJA     = 0x05 // BPF_JMP (5) | BPF_JA (0)
	tJEQK   = 0x15 // BPF_JMP | BPF_JEQ (0x10) | BPF_K (0)
	tJGTK   = 0x25 // BPF_JMP | BPF_JGT (0x20) | BPF_K
	tJGEK   = 0x35 // BPF_JMP | BPF_JGE (0x30) | BPF_K
	tJSETK  = 0x45 // BPF_JMP | BPF_JSET (0x40) | BPF_K
	tRetK   = 0x06 // BPF_RET (6) | BPF_K
	tMaxIns = 4096 // BPF_MAXINSNS
)

// Return values (include/uapi/linux/seccomp.h), spelled out again here so a
// wrong constant in the package is caught.
const (
	tRetKillProcess = 0x80000000
	tRetErrno       = 0x00050000
	tRetAllow       = 0x7fff0000
)

// seccompData is struct seccomp_data (include/uapi/linux/seccomp.h).
type seccompData struct {
	nr   uint32
	arch uint32
	ip   uint64
	args [6]uint64
}

func (d seccompData) bytes() []byte {
	b := make([]byte, 64)
	binary.LittleEndian.PutUint32(b[0:], d.nr)
	binary.LittleEndian.PutUint32(b[4:], d.arch)
	binary.LittleEndian.PutUint64(b[8:], d.ip)
	for i, a := range d.args {
		binary.LittleEndian.PutUint64(b[16+8*i:], a)
	}
	return b
}

// decode splits raw sock_filter bytes into instructions.
func decode(raw []byte) ([]Instruction, error) {
	if len(raw) == 0 || len(raw)%8 != 0 {
		return nil, fmt.Errorf("program length %d is not a positive multiple of 8", len(raw))
	}
	p := make([]Instruction, len(raw)/8)
	for i := range p {
		b := raw[8*i:]
		p[i] = Instruction{
			Code: binary.LittleEndian.Uint16(b[0:]),
			Jt:   b[2],
			Jf:   b[3],
			K:    binary.LittleEndian.Uint32(b[4:]),
		}
	}
	return p, nil
}

// check is the kernel's acceptance test, restricted to what seccomp allows.
func check(p []Instruction) error {
	if len(p) == 0 || len(p) > tMaxIns {
		return fmt.Errorf("program length %d outside 1..%d", len(p), tMaxIns)
	}
	for pc, in := range p {
		switch in.Code {
		case tLdWAbs:
			if in.K >= 64 || in.K%4 != 0 {
				return fmt.Errorf("pc %d: load offset %d outside seccomp_data or unaligned", pc, in.K)
			}
		case tJA:
			if uint64(pc)+1+uint64(in.K) >= uint64(len(p)) {
				return fmt.Errorf("pc %d: ja %d jumps past the end", pc, in.K)
			}
		case tJEQK, tJGTK, tJGEK, tJSETK:
			if pc+1+int(in.Jt) >= len(p) || pc+1+int(in.Jf) >= len(p) {
				return fmt.Errorf("pc %d: conditional jump (%d, %d) past the end", pc, in.Jt, in.Jf)
			}
		case tRetK:
		default:
			return fmt.Errorf("pc %d: opcode %#x not allowed", pc, in.Code)
		}
	}
	if p[len(p)-1].Code != tRetK {
		return fmt.Errorf("last instruction %#x is not ret", p[len(p)-1].Code)
	}
	return nil
}

// run executes a checked program and returns its seccomp return value.
func run(p []Instruction, d seccompData) (uint32, error) {
	if err := check(p); err != nil {
		return 0, err
	}
	data := d.bytes()
	var acc uint32
	for pc := 0; pc < len(p); {
		in := p[pc]
		switch in.Code {
		case tLdWAbs:
			acc = binary.LittleEndian.Uint32(data[in.K:])
			pc++
		case tJA:
			pc += 1 + int(in.K)
		case tJEQK, tJGTK, tJGEK, tJSETK:
			var cond bool
			switch in.Code {
			case tJEQK:
				cond = acc == in.K
			case tJGTK:
				cond = acc > in.K
			case tJGEK:
				cond = acc >= in.K
			case tJSETK:
				cond = acc&in.K != 0
			}
			if cond {
				pc += 1 + int(in.Jt)
			} else {
				pc += 1 + int(in.Jf)
			}
		case tRetK:
			return in.K, nil
		}
	}
	return 0, fmt.Errorf("fell off the end")
}

// verdict is a decoded return value, for readable failures.
type verdict struct {
	kind  string // "allow", "kill", "errno"
	errno uint16
}

func (v verdict) String() string {
	if v.kind == "errno" {
		return fmt.Sprintf("errno(%d)", v.errno)
	}
	return v.kind
}

var (
	vAllow = verdict{kind: "allow"}
	vKill  = verdict{kind: "kill"}
)

func vErrno(e uint16) verdict { return verdict{kind: "errno", errno: e} }

func decodeRet(r uint32) verdict {
	switch {
	case r == tRetAllow:
		return vAllow
	case r == tRetKillProcess:
		return vKill
	case r&0xffff0000 == tRetErrno:
		return vErrno(uint16(r & 0xffff))
	}
	return verdict{kind: fmt.Sprintf("unexpected %#x", r)}
}
