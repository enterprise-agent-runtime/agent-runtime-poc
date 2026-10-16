package linux_bwrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"warden.dev/warden/internal/sandbox"
)

// readBwrap returns one captured bwrap output from testdata/bwrap.
//
// Provenance: version-0.9.0.txt and help-0.9.0.txt are the output of the
// Ubuntu 24.04 bwrap 0.9.0 in WSL2. help-0.8.0.txt and help-0.6.1.txt are
// the usage() text of upstream bubblewrap tags v0.8.0 and v0.6.1 (what
// "bwrap --help" prints there). Note the 0.6.x text: it lists the flag as
// "--add-seccomp FD" although the parser accepts only "--add-seccomp-fd"
// (fixed in 0.7), so a parser that looks for the exact flag name would wrongly
// fall back to the deprecated --seccomp on Ubuntu 22.04.
func readBwrap(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "bwrap", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestParseFeatures_RealOutputs pins the A06 §11.1 feature probe to what
// each supported bwrap actually prints: --add-seccomp-fd since 0.5 (the
// 0.6.x help misnames it), --size since 0.7, --disable-userns since 0.8.
func TestParseFeatures_RealOutputs(t *testing.T) {
	cases := []struct {
		ver  string
		want Features
	}{
		{"0.6.1", Features{Version: "0.6.1", AddSeccompFD: true, Size: false, DisableUserns: false}},
		{"0.8.0", Features{Version: "0.8.0", AddSeccompFD: true, Size: true, DisableUserns: true}},
		{"0.9.0", Features{Version: "0.9.0", AddSeccompFD: true, Size: true, DisableUserns: true}},
	}
	for _, c := range cases {
		t.Run(c.ver, func(t *testing.T) {
			got, err := ParseFeatures(readBwrap(t, "version-"+c.ver+".txt"), readBwrap(t, "help-"+c.ver+".txt"))
			if err != nil {
				t.Fatalf("ParseFeatures() = %v", err)
			}
			if got != c.want {
				t.Errorf("ParseFeatures() = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestParseFeatures_FailsClosed: below the A06 §11.1 minimum, or on output
// that cannot be read as a bwrap version, there is no L1 sandbox
// (sandbox_unavailable), never a sandbox built from guessed flags.
func TestParseFeatures_FailsClosed(t *testing.T) {
	help := readBwrap(t, "help-0.9.0.txt")
	for _, v := range []string{
		"bubblewrap 0.5.0", // below 0.6.0
		"bubblewrap 0.5.99",
		"bubblewrap 0.4.1",
		"",
		"bubblewrap",
		"bubblewrap 0.6",     // not X.Y.Z
		"bubblewrap 0.6.x",   // not numeric
		"bubblewrap -1.6.0",  // not numeric
		"bubblewrap 0.6.0.1", // not X.Y.Z
		"bwrap-fork 0.9.0",   // not bubblewrap
		"bubblewrap 99999999999999999999.0.0",
	} {
		_, err := ParseFeatures(v, help)
		if !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("ParseFeatures(%q) = %v, want %v", v, err, sandbox.ErrUnavailable)
		}
	}
	// An empty help text cannot show any flag: every optional flag is off,
	// and --add-seccomp-fd off means the --seccomp fallback.
	got, err := ParseFeatures("bubblewrap 0.6.0\n", "")
	if err != nil || got != (Features{Version: "0.6.0"}) {
		t.Errorf("ParseFeatures(0.6.0, empty help) = %+v, %v; want no optional flags", got, err)
	}
}

func TestParseFeatures_AcceptsNewerVersions(t *testing.T) {
	help := readBwrap(t, "help-0.9.0.txt")
	for _, v := range []string{"bubblewrap 0.6.0", "bubblewrap 0.10.0", "bubblewrap 1.0.0\n", "  bubblewrap 0.11.2  \n"} {
		if _, err := ParseFeatures(v, help); err != nil {
			t.Errorf("ParseFeatures(%q) = %v, want accepted", v, err)
		}
	}
}

// A flag named only inside another flag's description must not count.
func TestParseFeatures_MatchesFlagsNotProse(t *testing.T) {
	help := "usage: bwrap [OPTIONS...] [--] COMMAND [ARGS...]\n" +
		"    --tmpfs DEST                 Mount new tmpfs on DEST (see --size)\n" +
		"    --unshare-user               Needed by --disable-userns\n" +
		"    --seccomp FD                 Load rules; --add-seccomp-fd is not here\n"
	got, err := ParseFeatures("bubblewrap 0.6.1", help)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size || got.DisableUserns || got.AddSeccompFD {
		t.Errorf("ParseFeatures() = %+v, want no optional flag", got)
	}
}

func TestFeatures_Missing(t *testing.T) {
	if m := (Features{Version: "0.9.0", AddSeccompFD: true, Size: true, DisableUserns: true}).Missing(); len(m) != 0 {
		t.Errorf("Missing() = %q, want none", m)
	}
	got := strings.Join(Features{Version: "0.6.0"}.Missing(), ",")
	if got != "--add-seccomp-fd,--size,--disable-userns" {
		t.Errorf("Missing() = %q", got)
	}
}
