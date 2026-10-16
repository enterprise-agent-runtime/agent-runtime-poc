package execproto

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLimits pins every exec.fs limit constant to the number in the A06
// §4.3 fs.json schema kept verbatim under testdata/schema. The executor
// enforces these constants (Decode checks no ranges), so a constant that
// drifted from the schema would let the executor accept what the daemon's
// schema forbids, or refuse what it allows, without anything failing to
// compile. MaxWriteBytes and MaxPatchFiles have no schema number (the 4 MiB
// write cap is in the content description and §4.4; the file cap is D-048)
// and are pinned to their sources here instead.
func TestLimits(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "schema", "fs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		pointer string
		want    int
	}{
		{"ReadParams/properties/length/maximum", MaxReadBytes},
		{"ReadParams/properties/length/default", MaxReadBytes},
		{"ListParams/properties/depth/default", DefaultListDepth},
		{"ListParams/properties/depth/maximum", MaxListDepth},
		{"ListParams/properties/max_entries/default", DefaultListEntries},
		{"ListParams/properties/max_entries/maximum", MaxListEntries},
		{"SearchParams/properties/query/maxLength", MaxSearchQueryBytes},
		{"SearchParams/properties/max_matches/maximum", MaxSearchMatches},
		{"SearchParams/properties/max_matches/default", MaxSearchMatches},
		{"SearchResult/properties/matches/maxItems", MaxSearchMatches},
		{"SearchParams/properties/context_lines/maximum", MaxSearchContextLines},
		{"SearchParams/properties/max_file_bytes/maximum", MaxSearchFileBytes},
		{"SearchParams/properties/max_file_bytes/default", MaxSearchFileBytes},
		{"SearchParams/properties/include/maxItems", MaxSearchGlobs},
		{"SearchParams/properties/include/items/maxLength", MaxSearchGlobBytes},
		{"SearchParams/properties/exclude/maxItems", MaxSearchGlobs},
		{"SearchParams/properties/exclude/items/maxLength", MaxSearchGlobBytes},
		{"SearchResult/properties/matches/items/properties/text/maxLength", MaxSearchTextBytes},
		{"SearchResult/properties/matches/items/properties/before/items/maxLength", MaxSearchTextBytes},
		{"PatchParams/properties/patch/maxLength", MaxPatchBytes},
		{"PatchParams/properties/strip/default", DefaultPatchStrip},
		{"PatchParams/properties/strip/maximum", MaxPatchStrip},
	}
	for _, c := range cases {
		v := schema.(map[string]any)["$defs"]
		for _, k := range strings.Split(c.pointer, "/") {
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%s: %q is not an object", c.pointer, k)
			}
			v = m[k]
		}
		n, ok := v.(float64)
		if !ok || int(n) != c.want {
			t.Errorf("%s = %v, constant is %d", c.pointer, v, c.want)
		}
	}
	// "≤ 4 MiB after decoding" (WriteParams.content) and A06 §4.4 too_large.
	if MaxWriteBytes != 4<<20 {
		t.Errorf("MaxWriteBytes = %d, want 4 MiB", MaxWriteBytes)
	}
	// D-048: refused before applying, exactly where Decode stops accepting.
	if MaxPatchFiles != MaxArrayLen {
		t.Errorf("MaxPatchFiles = %d, want MaxArrayLen %d", MaxPatchFiles, MaxArrayLen)
	}
}
