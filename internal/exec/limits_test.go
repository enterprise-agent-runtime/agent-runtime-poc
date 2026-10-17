package exec

import (
	"testing"
	"time"
)

func u64(v uint64) *uint64 { return &v }

// TestParseChildLimits guards the --child-limits flag of design A06 §3.1
// ("cpu=3600,fsize=1073741824,nofile=4096,core=0[,data=…][,nproc=<n>]"),
// the rlimits every child gets through the shim (§7.3). The flag comes
// from the daemon, but a parser that accepted a typo, a duplicate or a
// negative value would silently launch children without the limit the
// daemon meant, so it fails closed on anything it does not fully
// understand.
func TestParseChildLimits(t *testing.T) {
	ok := []struct {
		in   string
		want ChildLimits
	}{
		{"cpu=3600,fsize=1073741824,nofile=4096,core=0", ChildLimits{CPU: 3600, Fsize: 1073741824, Nofile: 4096, Core: 0}},
		{"core=0,nofile=4096,fsize=1,cpu=1,data=3221225472,nproc=600", ChildLimits{CPU: 1, Fsize: 1, Nofile: 4096, Core: 0, Data: u64(3221225472), Nproc: u64(600)}},
	}
	for _, c := range ok {
		got, err := ParseChildLimits(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.CPU != c.want.CPU || got.Fsize != c.want.Fsize || got.Nofile != c.want.Nofile || got.Core != c.want.Core ||
			!eqPtr(got.Data, c.want.Data) || !eqPtr(got.Nproc, c.want.Nproc) {
			t.Errorf("%q = %+v, want %+v", c.in, got, c.want)
		}
	}
	bad := []string{
		"",
		"cpu=3600,fsize=1,nofile=4096", // core missing
		"cpu=3600,fsize=1,nofile=4096,core=0,as=1",         // unknown key (RLIMIT_AS is deliberately not used, §7.3)
		"cpu=3600,cpu=1,fsize=1,nofile=4096,core=0",        // duplicate
		"cpu=-1,fsize=1,nofile=4096,core=0",                // sign
		"cpu=+1,fsize=1,nofile=4096,core=0",                // sign
		"cpu=01,fsize=1,nofile=4096,core=0",                // leading zero
		"cpu=0,fsize=1,nofile=4096,core=0",                 // no CPU at all is not a limit
		"cpu=1,fsize=1,nofile=0,core=0",                    // no descriptor at all: nothing could run
		"cpu=1,fsize=1,nofile=4096,core=0,",                // empty entry
		"cpu=1,fsize=1,nofile=4096,core",                   // no value
		"cpu=1,fsize=1,nofile=4096,core=0x0",               // not decimal
		"cpu=1,fsize=1,nofile=4096,core=0,nproc=0",         // no process at all
		"cpu=1,fsize=1,nofile=4096,core=0 ",                // trailing space
		"cpu=99999999999999999999,fsize=1,nofile=1,core=0", // overflow
	}
	for _, in := range bad {
		if _, err := ParseChildLimits(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func eqPtr(a, b *uint64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// TestChildLimits_ForCall guards A06 §7.3's CPU row ("RLIMIT_CPU = 4 ×
// call timeout, runaway guard only") together with the launch flag's
// cpu value: the child gets the smaller of the two, rounded up to whole
// seconds, and every other limit is the launch value unchanged.
func TestChildLimits_ForCall(t *testing.T) {
	base := ChildLimits{CPU: 3600, Fsize: 10, Nofile: 20, Core: 0, Data: u64(30)}
	cases := []struct {
		timeout time.Duration
		cpu     uint64
	}{
		{time.Second, 4},
		{1500 * time.Millisecond, 8}, // ceil(1.5) = 2 seconds, times 4
		{10 * time.Minute, 2400},
		{time.Hour, 3600}, // 4 h capped by the launch value
	}
	for _, c := range cases {
		got := base.ForCall(c.timeout)
		if got.CPU != c.cpu || got.Fsize != 10 || got.Nofile != 20 || got.Core != 0 || !eqPtr(got.Data, u64(30)) || got.Nproc != nil {
			t.Errorf("ForCall(%v) = %+v, want cpu %d and the rest unchanged", c.timeout, got, c.cpu)
		}
	}
}
