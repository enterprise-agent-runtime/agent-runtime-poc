package execproto

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The executor protocol is spoken by two binaries built from one tree but
// deployed separately (the daemon on the host, warden-exec inside the
// sandbox image), and M2-5..M2-10 and M3 build on it in parallel. A renamed
// JSON tag or a required field turned omitempty would not fail to compile;
// it would fail at the first real call, or worse, silently drop a field the
// other side relies on (deny_match, authz.profile). These tests pin the Go
// structs to the A06 §4.3 schemas, which are kept verbatim under
// testdata/schema: the goldens must validate against the schema, survive a
// decode/encode round trip unchanged, and every struct field must be a
// schema property (and the reverse), with required properties never
// omitempty. The goldens are inputs authored from A06, not outputs of the
// code, so there is no UPDATE_GOLDEN mode here: the structs follow the
// documents, never the other way round.

const schemaBase = "https://schemas.warden.dev/poc/exec/"

func compileSchemas(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	for _, name := range []string{"common", "hello", "fs", "proc", "git", "profiles"} {
		b, err := os.ReadFile(filepath.Join("testdata", "schema", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("schema %s: %v", name, err)
		}
		if err := c.AddResource(schemaBase+name+".json", doc); err != nil {
			t.Fatalf("schema %s: %v", name, err)
		}
	}
	return c
}

type goldenCase struct {
	golden string // testdata/golden/<golden>.json
	def    string // schema file#/$defs/<Def>
	newT   func() any
}

var goldenCases = []goldenCase{
	{"hello.params", "hello.json#/$defs/Params", func() any { return new(HelloParams) }},
	{"hello.result", "hello.json#/$defs/Result", func() any { return new(HelloResult) }},
	{"hello.result.noroots", "hello.json#/$defs/Result", func() any { return new(HelloResult) }},
	{"fs.read.params", "fs.json#/$defs/ReadParams", func() any { return new(ReadParams) }},
	{"fs.read.result", "fs.json#/$defs/ReadResult", func() any { return new(ReadResult) }},
	{"fs.list.params", "fs.json#/$defs/ListParams", func() any { return new(ListParams) }},
	{"fs.list.result", "fs.json#/$defs/ListResult", func() any { return new(ListResult) }},
	{"fs.list.result.empty", "fs.json#/$defs/ListResult", func() any { return new(ListResult) }},
	{"fs.search.params", "fs.json#/$defs/SearchParams", func() any { return new(SearchParams) }},
	{"fs.search.result", "fs.json#/$defs/SearchResult", func() any { return new(SearchResult) }},
	{"fs.search.result.empty", "fs.json#/$defs/SearchResult", func() any { return new(SearchResult) }},
	{"fs.write.params", "fs.json#/$defs/WriteParams", func() any { return new(WriteParams) }},
	{"fs.write.result", "fs.json#/$defs/WriteResult", func() any { return new(WriteResult) }},
	{"fs.patch.params", "fs.json#/$defs/PatchParams", func() any { return new(PatchParams) }},
	{"fs.patch.result", "fs.json#/$defs/PatchResult", func() any { return new(PatchResult) }},
	{"fs.patch.result.empty", "fs.json#/$defs/PatchResult", func() any { return new(PatchResult) }},
	{"fs.stat.params", "fs.json#/$defs/StatParams", func() any { return new(StatParams) }},
	{"fs.stat.result", "fs.json#/$defs/StatResult", func() any { return new(StatResult) }},
	{"fs.stat.result.denied", "fs.json#/$defs/StatResult", func() any { return new(StatResult) }},
	{"proc.spawn.params", "proc.json#/$defs/SpawnParams", func() any { return new(SpawnParams) }},
	{"proc.spawn.params.harness", "proc.json#/$defs/SpawnParams", func() any { return new(SpawnParams) }},
	{"proc.spawn.result", "proc.json#/$defs/SpawnResult", func() any { return new(SpawnResult) }},
	{"proc.io.notification", "proc.json#/$defs/IoNotification", func() any { return new(IONotification) }},
	{"proc.wait.params", "proc.json#/$defs/WaitParams", func() any { return new(WaitParams) }},
	{"proc.wait.result", "proc.json#/$defs/WaitResult", func() any { return new(WaitResult) }},
	{"proc.wait.result.signal", "proc.json#/$defs/WaitResult", func() any { return new(WaitResult) }},
	{"proc.wait.result.running", "proc.json#/$defs/WaitResult", func() any { return new(WaitResult) }},
	{"proc.signal.params", "proc.json#/$defs/SignalParams", func() any { return new(SignalParams) }},
	{"proc.signal.result", "proc.json#/$defs/SignalResult", func() any { return new(SignalResult) }},
	{"shutdown.params", "proc.json#/$defs/ShutdownParams", func() any { return new(ShutdownParams) }},
	{"shutdown.result", "proc.json#/$defs/ShutdownResult", func() any { return new(ShutdownResult) }},
	{"git.run.params.diff", "git.json#/$defs/RunParams", func() any { return new(GitRunParams) }},
	{"git.run.params.commit", "git.json#/$defs/RunParams", func() any { return new(GitRunParams) }},
	{"git.run.result", "git.json#/$defs/RunResult", func() any { return new(GitRunResult) }},
	{"error.data", "common.json#/$defs/ErrorData", func() any { return new(ErrorData) }},
	{"profiles", "profiles.json", func() any { return new(ProfileSet) }},
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func instance(t *testing.T, raw []byte) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// canonical re-encodes JSON with sorted keys. The profiles file is the one
// place where object key order is meaning (first profile in file order wins,
// A06 §6 step 4), so it is compared compacted, order intact.
func canonical(t *testing.T, name string, raw []byte) string {
	t.Helper()
	if name == "profiles" {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func TestGolden_ExecSchemas(t *testing.T) {
	c := compileSchemas(t)
	seen := map[string]bool{}
	for _, tc := range goldenCases {
		t.Run(tc.golden, func(t *testing.T) {
			s, err := c.Compile(schemaBase + tc.def)
			if err != nil {
				t.Fatalf("compile %s: %v", tc.def, err)
			}
			raw := readGolden(t, tc.golden)
			if err := s.Validate(instance(t, raw)); err != nil {
				t.Fatalf("golden does not match A06 %s: %v", tc.def, err)
			}

			v := tc.newT()
			if err := Decode(raw, v); err != nil {
				t.Fatalf("decode: %v", err)
			}
			var out []byte
			if ps, ok := v.(*ProfileSet); ok {
				out, err = ps.MarshalJSON() // exact bytes, as the daemon writes them
			} else {
				out, err = MarshalWire(v) // what the transport sends, secret_env included
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := canonical(t, tc.golden, out), canonical(t, tc.golden, raw); got != want {
				t.Errorf("round trip changed the message\n--- got\n%s\n--- want\n%s", got, want)
			}
			if err := s.Validate(instance(t, out)); err != nil {
				t.Errorf("re-encoded message violates A06 %s: %v", tc.def, err)
			}

			// A06 sets additionalProperties: false on every object, so an
			// unknown top-level field must be refused by both the schema and
			// Decode.
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				t.Fatal(err)
			}
			obj["x_unknown"] = json.RawMessage(`1`)
			bad, _ := json.Marshal(obj)
			if s.Validate(instance(t, bad)) == nil {
				t.Errorf("schema accepted an unknown field (test setup is wrong)")
			}
			if err := Decode(bad, tc.newT()); err == nil {
				t.Errorf("Decode accepted an unknown field")
			}

			if tc.golden != "profiles" && !seen[tc.def] {
				seen[tc.def] = true
				checkFields(t, reflect.TypeOf(v).Elem(), s, tc.def)
			}
		})
	}
}

// TestGolden_NestedUnknownFieldsRejected covers the objects inside a
// message: authz is what binds a spawn to its policy decision, and resolved
// is what the PDP decides on, so a stray field there must not pass silently.
func TestGolden_NestedUnknownFieldsRejected(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		v    any
	}{
		{"authz", `{"call_id":"call_1","argv":["go"],"timeout_ms":1000,"authz":{"decision_id":"dec_1","profile":"","approved":false,"kind":"tool","scope":"session"}}`, new(SpawnParams)},
		{"resolved", `{"resolved":{"root":"worktree","rel_path":"","canonical":"/work","requested_rel_path":"","deny_match":null,"host_path":"/x"},"exists":true}`, new(StatResult)},
		{"list entry", `{"resolved":{"root":"worktree","rel_path":"","canonical":"/work","requested_rel_path":"","deny_match":null},"entries":[{"rel_path":"a","kind":"file","denied":false,"owner":"root"}],"truncated":false}`, new(ListResult)},
		{"git diff", `{"op":"diff","diff":{"against":"base","color":true}}`, new(GitRunParams)},
		{"trailing data", `{"handle":"ph_1","signal":"TERM"} {}`, new(SignalParams)},
	}
	for _, tc := range cases {
		if err := Decode([]byte(tc.raw), tc.v); err == nil {
			t.Errorf("%s: Decode accepted %s", tc.name, tc.raw)
		}
	}
}

// opaqueTypes encode themselves and have no schema-shaped fields to walk.
var opaqueTypes = map[reflect.Type]bool{reflect.TypeOf(time.Time{}): true, reflect.TypeOf(SecretEnv{}): true}

// checkFields walks a struct type alongside its schema: every JSON field is
// a schema property, every property is a field, and a required property is
// never omitempty.
func checkFields(t *testing.T, typ reflect.Type, s *jsonschema.Schema, at string) {
	t.Helper()
	for s.Ref != nil {
		s = s.Ref
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch {
	case typ.Kind() == reflect.Slice:
		if s.Items2020 != nil {
			checkFields(t, typ.Elem(), s.Items2020, at+"[]")
		}
		return
	case typ.Kind() != reflect.Struct || opaqueTypes[typ]:
		return
	}
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	fields := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			t.Errorf("%s: field %s has no JSON name", at, f.Name)
			continue
		}
		fields[name] = true
		prop, ok := s.Properties[name]
		if !ok {
			t.Errorf("%s: field %s (%q) is not a property in A06", at, f.Name, name)
			continue
		}
		if required[name] && (strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")) {
			t.Errorf("%s: required property %q is omitempty", at, name)
		}
		checkFields(t, f.Type, prop, at+"."+name)
	}
	var missing []string
	for name := range s.Properties {
		if !fields[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("%s: A06 property %q has no struct field", at, name)
	}
}
