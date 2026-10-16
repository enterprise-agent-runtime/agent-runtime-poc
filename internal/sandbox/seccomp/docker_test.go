package seccomp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

// goldenDocker is the L2 profile as Docker reads it (A06 §15). The bytes are
// compared exactly, because they are the file written to
// S/sandbox/<sb_id>/seccomp.json. Rewrite with UPDATE_GOLDEN=1 go test and
// review the diff like code (CLAUDE.md §8.1 L2).
var goldenDocker = filepath.Join("testdata", "golden", "docker-seccomp.json")

func TestGolden_DockerProfile(t *testing.T) {
	got, err := DockerProfile()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenDocker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenDocker, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenDocker)
	if err != nil {
		t.Fatalf("missing golden %s (run with UPDATE_GOLDEN=1): %v", goldenDocker, err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from DockerProfile()\n--- got\n%s", goldenDocker, got)
	}
}

// The Docker profile format (moby profiles/seccomp, the fields this package
// writes). Unknown fields are refused so the evaluator below never ignores a
// field Docker would act on.
type dProfile struct {
	DefaultAction string     `json:"defaultAction"`
	Architectures []string   `json:"architectures"`
	Syscalls      []dSyscall `json:"syscalls"`
}

type dSyscall struct {
	Names    []string `json:"names"`
	Action   string   `json:"action"`
	ErrnoRet *uint    `json:"errnoRet,omitempty"`
	Args     []dArg   `json:"args,omitempty"`
	Includes *struct {
		Arches []string `json:"arches"`
	} `json:"includes,omitempty"`
	Comment string `json:"comment,omitempty"`
}

type dArg struct {
	Index    uint   `json:"index"`
	Value    uint64 `json:"value"`
	ValueTwo uint64 `json:"valueTwo"`
	Op       string `json:"op"`
}

func parseDocker(t *testing.T) dProfile {
	t.Helper()
	raw, err := DockerProfile()
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p dProfile
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("DockerProfile is not the expected format: %v", err)
	}
	return p
}

func (s dSyscall) appliesTo(a Arch) bool {
	return s.Includes == nil || slices.Contains(s.Includes.Arches, a.String())
}

// eval is libseccomp's verdict for one call under the profile: a rule
// matches when every argument comparison holds (64-bit, as libseccomp
// compares), rules for a syscall are alternatives, and no match means the
// default action.
func (p dProfile) eval(t *testing.T, a Arch, name string, args [6]uint64) verdict {
	t.Helper()
	var hits []verdict
	for _, s := range p.Syscalls {
		if !s.appliesTo(a) || !slices.Contains(s.Names, name) {
			continue
		}
		match := true
		for _, c := range s.Args {
			v := args[c.Index]
			switch c.Op {
			case "SCMP_CMP_MASKED_EQ":
				match = match && v&c.Value == c.ValueTwo
			default:
				t.Fatalf("%s: op %s not understood by this evaluator", name, c.Op)
			}
		}
		if !match {
			continue
		}
		switch s.Action {
		case "SCMP_ACT_KILL_PROCESS":
			hits = append(hits, vKill)
		case "SCMP_ACT_ERRNO":
			if s.ErrnoRet == nil {
				t.Fatalf("%s: SCMP_ACT_ERRNO without errnoRet", name)
			}
			hits = append(hits, vErrno(uint16(*s.ErrnoRet)))
		default:
			t.Fatalf("%s: action %s not expected", name, s.Action)
		}
	}
	if len(hits) == 0 {
		return vAllow
	}
	for _, h := range hits[1:] {
		if h != hits[0] {
			t.Fatalf("%s: rules disagree: %v", name, hits)
		}
	}
	return hits[0]
}

// TestDockerProfile_Shape checks the fixed parts A06 §15 states.
func TestDockerProfile_Shape(t *testing.T) {
	p := parseDocker(t)
	if p.DefaultAction != "SCMP_ACT_ALLOW" {
		t.Errorf("defaultAction = %s, want SCMP_ACT_ALLOW", p.DefaultAction)
	}
	if fmt.Sprint(p.Architectures) != "[SCMP_ARCH_X86_64 SCMP_ARCH_AARCH64]" {
		t.Errorf("architectures = %v", p.Architectures)
	}
	for _, s := range p.Syscalls {
		for _, a := range Arches() {
			if !s.appliesTo(a) {
				continue
			}
			for _, n := range s.Names {
				if _, ok := a.Nr(n); !ok {
					t.Errorf("%s applies on %s, which has no such syscall (missing includes.arches)", n, a)
				}
			}
		}
	}
}

// TestDockerProfile_SameRowsAsProgram proves the L1 program and the L2
// profile come from the same rows (A06 §11.5 "The same table generates the
// Docker JSON"): the same syscall names, and for both architectures the same
// verdict for every listed syscall over the filter values, every namespace
// bit, junk in the upper 32 bits and a deterministic sweep of other values.
func TestDockerProfile_SameRowsAsProgram(t *testing.T) {
	p := parseDocker(t)

	var tableNames, jsonNames []string
	for _, r := range Table() {
		for _, s := range r.Syscalls {
			tableNames = append(tableNames, s.Name)
		}
	}
	seen := map[string]bool{}
	for _, s := range p.Syscalls {
		for _, n := range s.Names {
			if !seen[n] {
				seen[n] = true
				jsonNames = append(jsonNames, n)
			}
		}
	}
	sort.Strings(tableNames)
	sort.Strings(jsonNames)
	if fmt.Sprint(tableNames) != fmt.Sprint(jsonNames) {
		t.Errorf("syscalls differ\ntable %v\n json %v", tableNames, jsonNames)
	}

	rng := rand.New(rand.NewPCG(14, 2026)) // fixed seed: deterministic
	for _, a := range Arches() {
		prog := programFor(t, a)
		names := append(slices.Clone(tableNames), "read", "write", "openat", "execve", "getpid", "pidfd_open")
		for _, name := range names {
			nr, ok := a.Nr(name)
			if !ok {
				continue
			}
			var probes [][6]uint64
			probes = append(probes, [6]uint64{}, [6]uint64{0xdeadbeef, 0x5412, 0x40000, 17, ^uint64(0), 1})
			for _, c := range a06ArgCases() {
				if c.name == name {
					probes = append(probes, c.args)
				}
			}
			probes = append(probes, edgeProbes(name, rng.Uint32())...)
			for i := 0; i < 300; i++ {
				var args [6]uint64
				for j := range args {
					switch i % 3 {
					case 0:
						args[j] = rng.Uint64()
					case 1:
						args[j] = uint64(rng.Uint32() & 0x7f) // small: hits the socket and errno ranges
					default:
						args[j] = uint64(1)<<rng.IntN(32) | uint64(rng.Uint32())<<32
					}
				}
				probes = append(probes, args)
			}
			for _, args := range probes {
				bpf := runFor(t, prog, a, nr, args)
				js := p.eval(t, a, name, args)
				if bpf != js {
					t.Errorf("%s %s(%#x): program %v, Docker profile %v", a, name, args, bpf, js)
				}
			}
		}
	}
}

// edgeProbes are the values next to every filter boundary (from the M2-14
// review): for each listed value v (each mask bit for AnyBit), v-3..v+3 and
// v with each single bit flipped, in the filtered argument, each with the
// upper half 0, all ones, and one fixed pseudo-random value. This is where a
// complement cube or a jump that is off by one would show.
func edgeProbes(name string, upper uint32) [][6]uint64 {
	var out [][6]uint64
	for _, r := range Table() {
		for _, sc := range r.Syscalls {
			if sc.Name != name || sc.Filter == nil {
				continue
			}
			f := sc.Filter
			vals := f.Values
			if f.Kind == AnyBit {
				for b := 0; b < 32; b++ {
					if f.Mask&(1<<b) != 0 {
						vals = append(vals, 1<<b)
					}
				}
			}
			var lows []uint32
			for _, v := range vals {
				for d := -3; d <= 3; d++ {
					lows = append(lows, v+uint32(d))
				}
				for b := 0; b < 32; b++ {
					lows = append(lows, v^(1<<b))
				}
			}
			for _, lo := range lows {
				for _, hi := range []uint32{0, 0xffffffff, upper} {
					var args [6]uint64
					args[f.Arg] = uint64(hi)<<32 | uint64(lo)
					out = append(out, args)
				}
			}
		}
	}
	return out
}
