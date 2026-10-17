package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Runes for the look-alike cases are built numerically: an editor or a
// tool that rewrites \u escapes into the characters themselves would
// otherwise make the cases unreadable or silently ASCII.
var (
	fullwidthL      = string(rune(0xFF2C)) // FULLWIDTH LATIN CAPITAL LETTER L
	fullwidthTwo    = string(rune(0xFF12)) // FULLWIDTH DIGIT TWO
	romanFifty      = string(rune(0x216C)) // ROMAN NUMERAL FIFTY, looks like L
	smallCapitalL   = string(rune(0x029F)) // LATIN LETTER SMALL CAPITAL L
	subscriptTwo    = string(rune(0x2082)) // SUBSCRIPT TWO
	zeroWidthSpace  = string(rune(0x200B))
	noBreakSpace    = string(rune(0x00A0))
	cyrillicPalochk = string(rune(0x04C0)) // CYRILLIC LETTER PALOCHKA, looks like l/1

	// escFullwidthL is fullwidthL as the error shows it (%+q).
	escFullwidthL = strings.Trim(strconv.QuoteToASCII(fullwidthL), `"`)
)

var (
	unixLevels    = []string{"L1", "L2"}
	windowsLevels = []string{"L2"} // platform.SandboxLevels() on Windows
)

// TestSandboxLevel_Normalises guards CWE-178 (improper handling of case
// sensitivity) on sandbox.default_level: doctor's sandbox.l2 check is
// blocking exactly when the level is "L2" (D-026), so "l2" in config.yaml
// made L2 the level in the user's mind while doctor treated the engine as
// optional. The value is matched case-insensitively after trimming, comes
// back in its canonical spelling, and anything else is refused, never
// defaulted: a look-alike is not a level. On Windows only L2 exists, so L1
// is refused there (CONFLICTS C-06).
func TestSandboxLevel_Normalises(t *testing.T) {
	cases := []struct {
		raw    string
		levels []string
		want   string // "" = refused
	}{
		{"L2", unixLevels, "L2"},
		{"l2", unixLevels, "L2"},
		{" L2 ", unixLevels, "L2"},
		{"\tl1\n", unixLevels, "L1"},
		{"L1", unixLevels, "L1"},
		{"l1", unixLevels, "L1"},
		{"L2", windowsLevels, "L2"},
		{"l2", windowsLevels, "L2"},
		{"L1", windowsLevels, ""},
		{"l1", windowsLevels, ""},
		{"L 2", unixLevels, ""},
		{"L3", unixLevels, ""},
		{"L0", unixLevels, ""},
		{"", unixLevels, ""},
		{"   ", unixLevels, ""},
		{"L", unixLevels, ""},
		{"2", unixLevels, ""},
		{"L2L2", unixLevels, ""},
		{"L2,L1", unixLevels, ""},
		{"L2\x00", unixLevels, ""},
		{fullwidthL + "2", unixLevels, ""},
		{"L" + fullwidthTwo, unixLevels, ""},
		{fullwidthL + fullwidthTwo, unixLevels, ""},
		{romanFifty + "2", unixLevels, ""},
		{smallCapitalL + "2", unixLevels, ""},
		{"L" + subscriptTwo, unixLevels, ""},
		{"L" + zeroWidthSpace + "2", unixLevels, ""},
		{noBreakSpace + "L2", unixLevels, ""},
		{"L2" + noBreakSpace, unixLevels, ""},
		{cyrillicPalochk + "2", unixLevels, ""},
	}
	for _, c := range cases {
		got, err := SandboxLevel(c.raw, c.levels)
		if c.want == "" {
			if err == nil {
				t.Errorf("SandboxLevel(%+q, %v) = %q, want refused", c.raw, c.levels, got)
				continue
			}
			if got != "" {
				t.Errorf("SandboxLevel(%+q) refused but returned %q", c.raw, got)
			}
			if !strings.Contains(err.Error(), "sandbox.default_level") || !strings.Contains(err.Error(), strings.Join(c.levels, ", ")) {
				t.Errorf("SandboxLevel(%+q): error %q does not name the key and the allowed levels", c.raw, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("SandboxLevel(%+q, %v) = %q, %v; want %q", c.raw, c.levels, got, err, c.want)
		}
	}
	// The value is shown escaped, so a look-alike is visible as one.
	_, err := SandboxLevel(fullwidthL+"2", unixLevels)
	if err == nil || !strings.Contains(err.Error(), escFullwidthL) {
		t.Errorf("error %v does not show the look-alike escaped", err)
	}
	// On Windows the refusal of L1 says why.
	_, err = SandboxLevel("L1", windowsLevels)
	if err == nil || !strings.Contains(err.Error(), "not available on this OS") {
		t.Errorf("L1 on Windows: error %v", err)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadDaemon_DefaultLevel: config.yaml's sandbox.default_level reaches
// the daemon normalised, its default comes from internal/platform when the
// key is absent (C-06), and a bad value stops the load instead of falling
// back to the default; so do a misspelt key and a non-string value, which
// would otherwise default silently in the same way.
func TestLoadDaemon_DefaultLevel(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.yaml")
	cases := []struct {
		name   string
		path   string
		levels []string
		def    string
		want   string // "" = refused
		errHas string
	}{
		{"no file", missing, unixLevels, "L1", "L1", ""},
		{"no file, Windows", missing, windowsLevels, "L2", "L2", ""},
		{"empty file", writeConfig(t, ""), unixLevels, "L1", "L1", ""},
		{"comments only", writeConfig(t, "# nothing yet\n"), unixLevels, "L1", "L1", ""},
		// Unknown fields are rejected (CLAUDE.md §7): config.yaml holds only
		// what the daemon reads, so a section it would ignore is refused
		// rather than silently doing nothing.
		{"section the daemon does not read", writeConfig(t, "runtime: {}\n"), unixLevels, "L1", "", "runtime"},
		{"sandbox without the key", writeConfig(t, "sandbox: {}\n"), unixLevels, "L1", "L1", ""},
		{"canonical", writeConfig(t, "sandbox:\n  default_level: L2\n"), unixLevels, "L1", "L2", ""},
		{"lower case", writeConfig(t, "sandbox:\n  default_level: l2\n"), unixLevels, "L1", "L2", ""},
		{"quoted with spaces", writeConfig(t, "sandbox:\n  default_level: \" L2 \"\n"), unixLevels, "L1", "L2", ""},
		{"L1 on Windows", writeConfig(t, "sandbox:\n  default_level: L1\n"), windowsLevels, "L2", "", "not available on this OS"},
		{"empty string", writeConfig(t, "sandbox:\n  default_level: \"\"\n"), unixLevels, "L1", "", "sandbox.default_level"},
		{"null", writeConfig(t, "sandbox:\n  default_level: null\n"), unixLevels, "L1", "", "as a string (got no value)"},
		{"tilde", writeConfig(t, "sandbox:\n  default_level: ~\n"), unixLevels, "L1", "", "as a string (got no value)"},
		{"bare key", writeConfig(t, "sandbox:\n  default_level:\n"), unixLevels, "L1", "", "as a string (got no value)"},
		{"number", writeConfig(t, "sandbox:\n  default_level: 2\n"), unixLevels, "L1", "", "as a string (got a value of type int)"},
		{"list", writeConfig(t, "sandbox:\n  default_level: [L2]\n"), unixLevels, "L1", "", "as a string (got a list)"},
		{"L3", writeConfig(t, "sandbox:\n  default_level: L3\n"), unixLevels, "L1", "", "sandbox.default_level"},
		{"fullwidth", writeConfig(t, "sandbox:\n  default_level: "+fullwidthL+"2\n"), unixLevels, "L1", "", escFullwidthL},
		{"misspelt key", writeConfig(t, "sandbox:\n  default_levle: L2\n"), unixLevels, "L1", "", "default_levle"},
		{"key in another case", writeConfig(t, "sandbox:\n  Default_Level: L2\n"), unixLevels, "L1", "", "Default_Level"},
		{"duplicate key", writeConfig(t, "sandbox:\n  default_level: L1\n  default_level: L2\n"), unixLevels, "L1", "", "default_level"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := LoadDaemon(c.path, c.levels, c.def)
			if c.want == "" {
				if err == nil {
					t.Fatalf("loaded %+v, want refused", d)
				}
				if !strings.Contains(err.Error(), c.errHas) {
					t.Errorf("error %q lacks %q", err, c.errHas)
				}
				if !strings.Contains(err.Error(), "config.yaml") {
					t.Errorf("error %q does not name the file", err)
				}
				return
			}
			if err != nil || d.Sandbox.DefaultLevel != c.want {
				t.Fatalf("LoadDaemon = %+v, %v; want default level %q", d, err, c.want)
			}
		})
	}
}
