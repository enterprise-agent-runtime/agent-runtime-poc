package seccomp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// The Docker seccomp profile format (moby profiles/seccomp), limited to the
// fields this package writes.
type dockerProfile struct {
	DefaultAction string          `json:"defaultAction"`
	Architectures []string        `json:"architectures"`
	Syscalls      []dockerSyscall `json:"syscalls"`
}

type dockerSyscall struct {
	Names    []string        `json:"names"`
	Action   string          `json:"action"`
	ErrnoRet *uint           `json:"errnoRet,omitempty"`
	Args     []dockerArg     `json:"args,omitempty"`
	Includes *dockerIncludes `json:"includes,omitempty"`
	Comment  string          `json:"comment,omitempty"`
}

type dockerArg struct {
	Index    uint   `json:"index"`
	Value    uint64 `json:"value"`
	ValueTwo uint64 `json:"valueTwo"`
	Op       string `json:"op"`
}

type dockerIncludes struct {
	Arches []string `json:"arches"`
}

// DockerProfile renders the L2 Docker seccomp JSON from Table (A06 §15):
// defaultAction SCMP_ACT_ALLOW, the native architectures only, and each
// listed syscall with SCMP_ACT_KILL_PROCESS or SCMP_ACT_ERRNO and its
// errno. A syscall one architecture lacks carries includes.arches.
//
// Argument filters become SCMP_CMP_MASKED_EQ comparisons, the only operator
// used, with masks of at most 32 bits: libseccomp compares all 64 bits of an
// argument while the kernel (and the BPF program) use only the low 32, so an
// unmasked comparison could be bypassed with junk in the upper half. Rules
// for one syscall are alternatives (any match applies the action):
//   - AnyBit: one rule per bit, (arg & bit) == bit;
//   - In: one rule per value, (arg & 0xffffffff) == value;
//   - NotIn: the complement of the allowed set as prefix patterns
//     (complementCubes), since a single libseccomp rule cannot say "none of".
func DockerProfile() ([]byte, error) {
	p := dockerProfile{DefaultAction: "SCMP_ACT_ALLOW", Syscalls: []dockerSyscall{}}
	for _, a := range Arches() {
		p.Architectures = append(p.Architectures, a.dockerArch())
	}
	seen := map[string]bool{}
	for _, r := range Table() {
		base := dockerSyscall{Comment: r.Reason}
		switch {
		case r.Action.Kind == KillProcess:
			base.Action = "SCMP_ACT_KILL_PROCESS"
		case r.Action.Kind == Errno && r.Action.Errno > 0 && r.Action.Errno <= maxErrno:
			e := uint(r.Action.Errno)
			base.Action, base.ErrnoRet = "SCMP_ACT_ERRNO", &e
		default:
			return nil, fmt.Errorf("seccomp: invalid action %+v", r.Action)
		}

		// Unconditional syscalls, grouped by the architectures that have them.
		var groups []dockerSyscall
		for _, sc := range r.Syscalls {
			if seen[sc.Name] {
				return nil, fmt.Errorf("seccomp: %s in more than one row", sc.Name)
			}
			seen[sc.Name] = true
			inc, err := includesFor(sc.Name)
			if err != nil {
				return nil, err
			}
			if sc.Filter != nil {
				continue
			}
			placed := false
			for i := range groups {
				if sameIncludes(groups[i].Includes, inc) {
					groups[i].Names = append(groups[i].Names, sc.Name)
					placed = true
					break
				}
			}
			if !placed {
				g := base
				g.Names, g.Includes = []string{sc.Name}, inc
				groups = append(groups, g)
			}
		}
		p.Syscalls = append(p.Syscalls, groups...)

		// Argument-filtered syscalls: one entry per comparison.
		for _, sc := range r.Syscalls {
			if sc.Filter == nil {
				continue
			}
			f := *sc.Filter
			if err := f.validate(); err != nil {
				return nil, fmt.Errorf("seccomp: %s: %w", sc.Name, err)
			}
			inc, _ := includesFor(sc.Name)
			for _, c := range dockerComparisons(f) {
				e := base
				e.Names, e.Includes, e.Args = []string{sc.Name}, inc, []dockerArg{c}
				p.Syscalls = append(p.Syscalls, e)
			}
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// includesFor is nil when every architecture has the syscall, otherwise the
// ones that do. A syscall no architecture has is an error (a misspelling).
func includesFor(name string) (*dockerIncludes, error) {
	var have []string
	for _, a := range Arches() {
		if _, ok := a.Nr(name); ok {
			have = append(have, a.String())
		}
	}
	switch len(have) {
	case 0:
		return nil, fmt.Errorf("seccomp: %s exists on no supported architecture", name)
	case len(Arches()):
		return nil, nil
	}
	return &dockerIncludes{Arches: have}, nil
}

func sameIncludes(a, b *dockerIncludes) bool {
	if a == nil || b == nil {
		return a == b
	}
	return fmt.Sprint(a.Arches) == fmt.Sprint(b.Arches)
}

func dockerComparisons(f ArgFilter) []dockerArg {
	masked := func(mask, value uint32) dockerArg {
		return dockerArg{Index: uint(f.Arg), Value: uint64(mask), ValueTwo: uint64(value), Op: "SCMP_CMP_MASKED_EQ"}
	}
	var out []dockerArg
	switch f.Kind {
	case AnyBit:
		for bit := uint32(1); bit != 0; bit <<= 1 {
			if f.Mask&bit != 0 {
				out = append(out, masked(bit, bit))
			}
		}
	case In:
		for _, v := range f.Values {
			out = append(out, masked(0xffffffff, v))
		}
	case NotIn:
		for _, c := range complementCubes(f.Values) {
			out = append(out, masked(c.mask, c.value))
		}
	}
	return out
}

// cube is the set of 32-bit x with x & mask == value, where mask is a run of
// leading one bits.
type cube struct{ mask, value uint32 }

// complementCubes covers every 32-bit value not in allowed with disjoint
// prefix cubes: walk the binary trie of allowed from the top bit and emit
// each subtree that holds no allowed value. At most 32 cubes per allowed
// value; output order is ascending.
func complementCubes(allowed []uint32) []cube {
	var out []cube
	var walk func(prefix uint32, depth int, set []uint32)
	walk = func(prefix uint32, depth int, set []uint32) {
		if len(set) == 0 {
			var mask uint32
			if depth > 0 {
				mask = ^uint32(0) << (32 - depth)
			}
			out = append(out, cube{mask, prefix})
			return
		}
		if depth == 32 {
			return // exactly an allowed value
		}
		bit := uint32(1) << (31 - depth)
		var zeros, ones []uint32
		for _, v := range set {
			if v&bit == 0 {
				zeros = append(zeros, v)
			} else {
				ones = append(ones, v)
			}
		}
		walk(prefix, depth+1, zeros)
		walk(prefix|bit, depth+1, ones)
	}
	walk(0, 0, allowed)
	return out
}
