package execproto

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wrd16Profiles is the WRD-16 §9 table (CF-30: verbatim), as the daemon
// would write it for a task granted every profile.
const wrd16Profiles = `{"version":1,"task_key":"implement","profiles":{
 "node-build":[["npm","run","build"],["pnpm","build"],["npx","tsc","--noEmit"],["npx","tsc","-p","."]],
 "node-test":[["npm","test"],["npm","test","--","<args>"],["pnpm","test"],["npx","vitest","run"],["npx","vitest","run","<args>"]],
 "go-build":[["go","build","./..."],["go","vet","./..."]],
 "go-test":[["go","test","./..."],["go","test","-race","./..."],["go","test","-json","./..."]],
 "python-test":[["pytest"],["python","-m","pytest"],["pytest","-q","<args>"]],
 "lint":[["npx","eslint","."],["golangci-lint","run"],["ruff","check","."]],
 "install":[["npm","ci"],["npm","install"],["pnpm","install"],["go","mod","download"],["pip","install","-r","requirements.txt"]]}}`

func mustParse(t *testing.T, s string) *ProfileSet {
	t.Helper()
	p, err := ParseProfiles([]byte(s))
	if err != nil {
		t.Fatalf("ParseProfiles: %v", err)
	}
	return p
}

// The profile match decides whether a command runs as R2 without asking or
// falls to R3 approval (WRD-16 §9, §10.6), and the executor re-checks it
// against the PDP's answer (A06 §6 steps 4-5). A loose match is a privilege
// escalation: if `npm test` also matched `npm test --silent`, any flag the
// model appends (including ones that load a config file it just wrote)
// would run unapproved. A06 §6 step 4 is therefore exact: token by token,
// case-sensitive, no reordering, and only a trailing <args> admits more.
func TestProfileMatch(t *testing.T) {
	set := mustParse(t, wrd16Profiles)
	long := strings.Repeat("x", 4097)
	cases := []struct {
		name string
		argv []string
		want string // "" = no profile
	}{
		{"exact", []string{"npm", "test"}, "node-test"},
		{"extra flag without <args>", []string{"npm", "test", "--silent"}, ""},
		{"extra flag after --", []string{"npm", "test", "--", "--silent"}, "node-test"},
		{"A06 example", []string{"npm", "test", "--", "--reporter=json", "--outputFile=/scratch/vitest.json"}, "node-test"},
		{"<args> matches zero elements", []string{"npm", "test", "--"}, "node-test"},
		{"<args> element over 4096 bytes", []string{"npm", "test", "--", long}, ""},
		{"<args> element at 4096 bytes", []string{"npm", "test", "--", long[:4096]}, "node-test"},
		{"trailing <args> after literals", []string{"npx", "vitest", "run", "src/users.test.ts"}, "node-test"},
		{"prefix of an entry", []string{"npx", "vitest"}, ""},
		{"prefix of argv0 only", []string{"npm"}, ""},
		{"nil argv", nil, ""},
		{"empty argv", []string{}, ""},
		{"empty argv0", []string{""}, ""},
		{"case-sensitive argv0", []string{"NPM", "test"}, ""},
		{"case-sensitive argument", []string{"npm", "Test"}, ""},
		{"no option reordering", []string{"go", "test", "./...", "-race"}, ""},
		{"no extra option", []string{"go", "test", "-v", "./..."}, ""},
		{"exact multi-token", []string{"go", "test", "-race", "./..."}, "go-test"},
		{"argv0 with a slash", []string{"/usr/bin/npm", "test"}, ""},
		{"argv0 relative path", []string{"./npm", "test"}, ""},
		{"literal <args> is not a wildcard", []string{"npm", "<args>"}, ""},
		{"placeholder after literal", []string{"pytest", "-q", "-k", "users"}, "python-test"},
		{"placeholder needs its literal", []string{"pytest", "-k", "users"}, ""},
		{"single token", []string{"pytest"}, "python-test"},
		{"different file", []string{"pip", "install", "-r", "dev-requirements.txt"}, ""},
		{"install", []string{"pip", "install", "-r", "requirements.txt"}, "install"},
		{"no globbing", []string{"npx", "tsc", "-p", "*"}, ""},
	}
	for _, tc := range cases {
		got, ok := set.Match(tc.argv)
		if ok != (tc.want != "") || got != tc.want {
			t.Errorf("%s: Match(%q) = %q, %v; want %q", tc.name, tc.argv, got, ok, tc.want)
		}
	}
}

// TestProfileMatch_FirstInFileOrder: A06 §6 step 4 says the first profile in
// file order is the executor's profile, and step 5 compares it with
// authz.profile. Decoding into a Go map would make that order random and
// produce spurious profile_mismatch failures (or hide real ones), so the
// order must survive parsing and re-encoding.
func TestProfileMatch_FirstInFileOrder(t *testing.T) {
	set := mustParse(t, `{"version":1,"task_key":"verify","profiles":{
		"zz-broad":[["make","<args>"]],
		"aa-narrow":[["make","test"]]}}`)
	for i := 0; i < 20; i++ {
		if got, _ := set.Match([]string{"make", "test"}); got != "zz-broad" {
			t.Fatalf("Match = %q, want the first profile in file order (zz-broad)", got)
		}
	}
	if set.Profiles[0].Name != "zz-broad" || set.Profiles[1].Name != "aa-narrow" {
		t.Errorf("profile order = %v", set.Profiles)
	}
	// A06 §6 step 4: argv[0] must contain no '/', even when a profile spells
	// one (WRD-04 §9 lists ./gradlew). A path argv[0] is resolved under the
	// worktree (§6 step 3), so the repository decides what runs: it can never
	// be a granted profile, only an approval.
	pathSet := mustParse(t, `{"version":1,"task_key":"verify","profiles":{"java-test":[["./gradlew","test"]]}}`)
	if got, ok := pathSet.Match([]string{"./gradlew", "test"}); ok {
		t.Errorf("Match of a path argv[0] = %q, want no profile", got)
	}
	var nilSet *ProfileSet
	if _, ok := nilSet.Match([]string{"make", "test"}); ok {
		t.Error("a nil ProfileSet matched")
	}
}

// TestParseProfiles_Rejects: the profiles file is the executor's own copy of
// what the task may run. CLAUDE.md §7 requires unknown fields to be
// rejected; a typo'd key that was silently ignored, or an <args> in the
// middle that the matcher treated as a literal, would make the executor and
// the PDP disagree about what is allowed. Each rule of the A06 §3.2 schema
// has a case.
func TestParseProfiles_Rejects(t *testing.T) {
	cases := []struct{ name, doc string }{
		{"not JSON", `{`},
		{"not an object", `[]`},
		{"null", `null`},
		{"unknown top-level field", `{"version":1,"task_key":"k","profiles":{},"extra":true}`},
		{"$schema is not a property", `{"$schema":"x","version":1,"task_key":"k","profiles":{}}`},
		{"version missing", `{"task_key":"k","profiles":{}}`},
		{"version 2", `{"version":2,"task_key":"k","profiles":{}}`},
		{"version string", `{"version":"1","task_key":"k","profiles":{}}`},
		{"task_key missing", `{"version":1,"profiles":{}}`},
		{"task_key not a string", `{"version":1,"task_key":3,"profiles":{}}`},
		{"profiles missing", `{"version":1,"task_key":"k"}`},
		{"profiles null", `{"version":1,"task_key":"k","profiles":null}`},
		{"profiles an array", `{"version":1,"task_key":"k","profiles":[]}`},
		{"name upper-case", `{"version":1,"task_key":"k","profiles":{"Node-test":[["npm","test"]]}}`},
		{"name starts with a digit", `{"version":1,"task_key":"k","profiles":{"1test":[["npm","test"]]}}`},
		{"name empty", `{"version":1,"task_key":"k","profiles":{"":[["npm","test"]]}}`},
		{"name 33 chars", `{"version":1,"task_key":"k","profiles":{"` + "a" + strings.Repeat("b", 32) + `":[["npm","test"]]}}`},
		{"duplicate name", `{"version":1,"task_key":"k","profiles":{"t":[["a"]],"t":[["b"]]}}`},
		{"profile with no entries", `{"version":1,"task_key":"k","profiles":{"t":[]}}`},
		{"profile null", `{"version":1,"task_key":"k","profiles":{"t":null}}`},
		{"empty entry", `{"version":1,"task_key":"k","profiles":{"t":[[]]}}`},
		{"entry of 17 tokens", `{"version":1,"task_key":"k","profiles":{"t":[["a"` + strings.Repeat(`,"a"`, 16) + `]]}}`},
		{"empty token", `{"version":1,"task_key":"k","profiles":{"t":[["npm",""]]}}`},
		{"token over 256 chars", `{"version":1,"task_key":"k","profiles":{"t":[["npm","` + strings.Repeat("x", 257) + `"]]}}`},
		{"token not a string", `{"version":1,"task_key":"k","profiles":{"t":[["npm",1]]}}`},
		{"<args> not last", `{"version":1,"task_key":"k","profiles":{"t":[["npm","<args>","x"]]}}`},
		{"<args> as argv0", `{"version":1,"task_key":"k","profiles":{"t":[["<args>"]]}}`},
		{"trailing data", `{"version":1,"task_key":"k","profiles":{}} {}`},
	}
	for _, tc := range cases {
		if _, err := ParseProfiles([]byte(tc.doc)); err == nil {
			t.Errorf("%s: ParseProfiles accepted %s", tc.name, tc.doc)
		}
	}
}

// TestParseProfiles_Accepts pins the edges the schema allows, so a fix to a
// rejection above cannot quietly tighten them.
func TestParseProfiles_Accepts(t *testing.T) {
	cases := []struct{ name, doc string }{
		{"no profiles", `{"version":1,"task_key":"","profiles":{}}`},
		{"field order free", `{"profiles":{},"task_key":"k","version":1}`},
		{"name 32 chars", `{"version":1,"task_key":"k","profiles":{"a` + strings.Repeat("b", 31) + `":[["npm","test"]]}}`},
		{"entry of 16 tokens", `{"version":1,"task_key":"k","profiles":{"t":[["a"` + strings.Repeat(`,"a"`, 15) + `]]}}`},
		{"256 multi-byte chars", `{"version":1,"task_key":"k","profiles":{"t":[["npm","` + strings.Repeat("é", 256) + `"]]}}`},
	}
	for _, tc := range cases {
		if _, err := ParseProfiles([]byte(tc.doc)); err != nil {
			t.Errorf("%s: ParseProfiles rejected %s: %v", tc.name, tc.doc, err)
		}
	}
}

// TestLoadProfiles_Digest: exec.hello reports the sha256 of the file the
// executor loaded, and the daemon destroys the sandbox when it differs from
// the file it generated (A06 §3.1, §4.3). The digest is of the bytes on
// disk, not of a re-encoding, or a correct file could fail the check.
func TestLoadProfiles_Digest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(wrd16Profiles), 0o600); err != nil {
		t.Fatal(err)
	}
	set, digest, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(wrd16Profiles))
	if want := "sha256:" + hex.EncodeToString(sum[:]); digest != want || ProfilesDigest([]byte(wrd16Profiles)) != want {
		t.Errorf("digest = %s, want %s", digest, want)
	}
	if set.TaskKey != "implement" || len(set.Profiles) != 7 || set.Profiles[0].Name != "node-build" {
		t.Errorf("loaded %+v", set)
	}
	if _, _, err := LoadProfiles(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("LoadProfiles of a missing file succeeded")
	}
}
