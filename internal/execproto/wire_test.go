package execproto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const secretSentinel = "value-set-by-test-not-a-credential"

func spawnWithSecret() SpawnParams {
	return SpawnParams{
		CallID: "call_1", Argv: []string{"copilot", "--headless"}, TimeoutMS: 60000,
		SecretEnv: NewSecretEnv(map[string]string{"COPILOT_TOKEN": secretSentinel}),
		Authz:     Authz{DecisionID: "dec_1", Kind: AuthzHarness, Approved: true},
	}
}

// TestSecretEnv_NeverLeaks guards CLAUDE.md §9 rule 3 / INV-C for the one
// wire field that carries a secret value (HX-1b, A06 §4.3 secret_env). The
// M2-4 security review showed the first version leaking it through
// json.Marshal, through slog's JSON handler, and through %v of a struct that
// holds the params in an unexported field (fmt cannot call methods there and
// printed the raw map). Every one of those paths is a plausible log line or
// error message; only the explicit wire encoder may produce the value.
func TestSecretEnv_NeverLeaks(t *testing.T) {
	p := spawnWithSecret()
	type holder struct {
		params SpawnParams // unexported: fmt prints it by reflection
		env    SecretEnv
	}
	h := holder{params: p, env: p.SecretEnv}

	outputs := map[string]string{}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		outputs["fmt "+verb+" params"] = fmt.Sprintf(verb, p)
		outputs["fmt "+verb+" env"] = fmt.Sprintf(verb, p.SecretEnv)
		outputs["fmt "+verb+" unexported holder"] = fmt.Sprintf(verb, h)
		outputs["fmt "+verb+" pointer to holder"] = fmt.Sprintf(verb, &h)
	}
	for name, v := range map[string]any{"params": p, "&params": &p, "env": p.SecretEnv, "map of params": map[string]any{"p": p}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		outputs["json.Marshal "+name] = string(b)
	}
	for name, newHandler := range map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	} {
		var buf bytes.Buffer
		log := slog.New(newHandler(&buf))
		log.Info("spawn", "params", p, "env", p.SecretEnv, "ptr", &p, "holder", h)
		outputs["slog "+name] = buf.String()
	}
	// SpawnParams logs its shape, not its arguments (LogValue).
	var one bytes.Buffer
	slog.New(slog.NewJSONHandler(&one, nil)).Info("spawn", "params", p)
	slog.New(slog.NewJSONHandler(&one, nil)).Info("spawn", "env", p.SecretEnv)
	if s := one.String(); !strings.Contains(s, `"argv0":"copilot"`) || !strings.Contains(s, `"env":"SecretEnv{COPILOT_TOKEN}"`) || strings.Contains(s, "--headless") {
		t.Errorf("slog of SpawnParams = %s, want argv0 and argc without the arguments", s)
	}
	for name, s := range outputs {
		if strings.Contains(s, secretSentinel) || strings.Contains(s, hex.EncodeToString([]byte(secretSentinel))) {
			t.Errorf("%s printed the secret value: %s", name, s)
		}
	}
	for _, name := range []string{"fmt %v env", "json.Marshal params", "slog json", "slog text"} {
		if !strings.Contains(outputs[name], "COPILOT_TOKEN") {
			t.Errorf("%s lost the key name: %s", name, outputs[name])
		}
	}

	// The wire encoder is the one place the value appears, and it survives
	// the round trip.
	wire, err := EncodeSpawnParams(p)
	if err != nil {
		t.Fatal(err)
	}
	viaAny, err := MarshalWire(&p)
	if err != nil || !bytes.Equal(wire, viaAny) {
		t.Errorf("MarshalWire(&p) = %s, %v; want EncodeSpawnParams output %s", viaAny, err, wire)
	}
	if !bytes.Contains(wire, []byte(secretSentinel)) {
		t.Fatalf("EncodeSpawnParams dropped the value: %s", wire)
	}
	var back SpawnParams
	if err := Decode(wire, &back); err != nil {
		t.Fatal(err)
	}
	if v, ok := back.SecretEnv.Value("COPILOT_TOKEN"); !ok || v != secretSentinel || back.SecretEnv.Len() != 1 {
		t.Errorf("decoded secret_env = %v (%q, %v)", back.SecretEnv.Keys(), v, ok)
	}
	if plain, _ := MarshalWire(SpawnParams{CallID: "call_2", Argv: []string{"go"}}); bytes.Contains(plain, []byte("secret_env")) {
		t.Errorf("empty secret_env was sent: %s", plain)
	}
}

// The executor and the PDP must read the same message. encoding/json
// matches object keys case-insensitively and lets the last duplicate win,
// while other parsers (the JSON Schema validator, the policy engine's CEL
// input, a log viewer) see the first or the exact-case key: a parser
// differential (CWE-436). The M2-4 review built a spawn whose validated argv
// was npm test and whose decoded argv was sh -c id, and an authz that read
// approved:false to the schema and true to Go.
func TestDecodeRejectsDuplicateKeys(t *testing.T) {
	cases := []struct {
		name, raw string
		v         any
	}{
		{"argv twice", `{"call_id":"call_1","argv":["npm","test"],"argv":["sh","-c","id"],"timeout_ms":1000,"authz":{"decision_id":"dec_1","profile":"node-test","approved":false,"kind":"tool"}}`, new(SpawnParams)},
		{"approved twice in authz", `{"call_id":"call_1","argv":["go"],"timeout_ms":1000,"authz":{"decision_id":"dec_1","profile":"","approved":false,"approved":true,"kind":"tool"}}`, new(SpawnParams)},
		{"secret_env key twice", `{"call_id":"call_1","argv":["go"],"timeout_ms":1000,"secret_env":{"K":"a","K":"b"},"authz":{"decision_id":"dec_1","profile":"","approved":true,"kind":"harness"}}`, new(SpawnParams)},
		{"in an array element", `{"resolved":{"root":"worktree","rel_path":"","canonical":"/work","requested_rel_path":"","deny_match":null},"entries":[{"rel_path":"a","rel_path":"b","kind":"file","denied":false}],"truncated":false}`, new(ListResult)},
		{"deny_match twice", `{"resolved":{"root":"worktree","rel_path":"x","canonical":"/work/x","requested_rel_path":"x","deny_match":"**/.env","deny_match":null},"exists":true}`, new(StatResult)},
	}
	for _, tc := range cases {
		if err := Decode([]byte(tc.raw), tc.v); err == nil {
			t.Errorf("%s: Decode accepted %s", tc.name, tc.raw)
		}
	}
	for _, doc := range []string{
		`{"version":1,"task_key":"k","profiles":{"t":[["go","test","./..."]]},"profiles":{"t":[["sh","<args>"]]}}`,
		`{"version":1,"version":1,"task_key":"k","profiles":{}}`,
	} {
		if _, err := ParseProfiles([]byte(doc)); err == nil {
			t.Errorf("ParseProfiles accepted %s", doc)
		}
	}
}

func TestDecodeRejectsCaseVariantKeys(t *testing.T) {
	cases := []struct {
		name, raw string
		v         any
	}{
		{"ARGV", `{"call_id":"call_1","ARGV":["sh","-c","id"],"timeout_ms":1000,"authz":{"decision_id":"dec_1","profile":"","approved":false,"kind":"tool"}}`, new(SpawnParams)},
		{"argv and Argv", `{"call_id":"call_1","argv":["npm","test"],"Argv":["sh","-c","id"],"timeout_ms":1000,"authz":{"decision_id":"dec_1","profile":"","approved":false,"kind":"tool"}}`, new(SpawnParams)},
		{"Approved in authz", `{"call_id":"call_1","argv":["go"],"timeout_ms":1000,"authz":{"decision_id":"dec_1","profile":"","approved":false,"Approved":true,"kind":"tool"}}`, new(SpawnParams)},
		{"Authz", `{"call_id":"call_1","argv":["go"],"timeout_ms":1000,"Authz":{"decision_id":"dec_1","profile":"","approved":true,"kind":"tool"}}`, new(SpawnParams)},
		{"Deny_Match", `{"resolved":{"root":"worktree","rel_path":"x","canonical":"/work/x","requested_rel_path":"x","Deny_Match":null},"exists":true}`, new(StatResult)},
	}
	for _, tc := range cases {
		if err := Decode([]byte(tc.raw), tc.v); err == nil {
			t.Errorf("%s: Decode accepted %s", tc.name, tc.raw)
		}
	}
	for _, doc := range []string{
		`{"version":1,"task_key":"k","profiles":{"t":[["go","test","./..."]]},"PROFILES":{"t":[["sh","<args>"]]}}`,
		`{"Version":1,"task_key":"k","profiles":{}}`,
		`{"version":1,"Task_Key":"k","profiles":{}}`,
	} {
		if _, err := ParseProfiles([]byte(doc)); err == nil {
			t.Errorf("ParseProfiles accepted %s", doc)
		}
	}
}

// TestEncodeRequiredNeverNull: a Go nil slice encodes as null, and A06
// requires arrays for roots, entries, matches, files and argv; a peer that
// validates (or a TypeScript client that maps over the field) fails on
// null. The zero value of every message type must encode with required
// properties present and typed as the schema says; null only where the
// schema allows null (deny_match).
func TestEncodeRequiredNeverNull(t *testing.T) {
	c := compileSchemas(t)
	for _, tc := range goldenCases {
		if tc.golden == "profiles" {
			continue
		}
		s, err := c.Compile(schemaBase + tc.def)
		if err != nil {
			t.Fatal(err)
		}
		out, err := MarshalWire(tc.newT())
		if err != nil {
			t.Fatalf("%s: %v", tc.golden, err)
		}
		var v any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatal(err)
		}
		checkRequired(t, tc.golden, v, s)
	}
}

func checkRequired(t *testing.T, at string, v any, s *jsonschema.Schema) {
	t.Helper()
	for s.Ref != nil {
		s = s.Ref
	}
	switch v := v.(type) {
	case map[string]any:
		for _, name := range s.Required {
			pv, ok := v[name]
			ps := s.Properties[name]
			if !ok {
				t.Errorf("%s: required %q missing from the zero value", at, name)
				continue
			}
			if pv == nil && !schemaAllows(ps, "null") {
				t.Errorf("%s: required %q encodes as null", at, name)
			}
		}
		for name, pv := range v {
			if ps := s.Properties[name]; ps != nil && pv != nil {
				checkRequired(t, at+"."+name, pv, ps)
			}
		}
	case []any:
		if s.Items2020 != nil {
			for _, e := range v {
				checkRequired(t, at+"[]", e, s.Items2020)
			}
		}
	}
}

func schemaAllows(s *jsonschema.Schema, typ string) bool {
	for s.Ref != nil {
		s = s.Ref
	}
	if s.Types == nil {
		return s.Enum != nil && enumHasNull(s.Enum.Values)
	}
	for _, t := range s.Types.ToStrings() {
		if t == typ {
			return true
		}
	}
	return false
}

func enumHasNull(vals []any) bool {
	for _, v := range vals {
		if v == nil {
			return true
		}
	}
	return false
}

// TestDecodeDeepNestingBounded: the key check of Decode must cost no more
// than the decode it guards. The first version built a path string per
// level and kept every one alive, so memory grew with depth²: the re-review
// measured 9.7 GiB for a 2 MiB spawn frame of nested objects with long keys
// under argv. A frame of about 1 MiB must now be refused within a small,
// fixed allocation budget, both when the nesting sits under a field of the
// wrong kind (argv is an array of strings) and when it goes into `any`.
func TestDecodeDeepNestingBounded(t *testing.T) {
	key := strings.Repeat("a", 2000)
	const depth = 500 // ~1 MiB with these keys
	nested := strings.Repeat(`{"`+key+`":`, depth) + "1" + strings.Repeat("}", depth)
	frames := map[string]struct {
		raw string
		v   func() any
	}{
		"under argv":       {`{"call_id":"call_1","argv":` + nested + `,"timeout_ms":1000,"authz":{"decision_id":"dec_1","profile":"","approved":false,"kind":"tool"}}`, func() any { return new(SpawnParams) }},
		"into any":         {nested, func() any { return new(any) }},
		"under secret_env": {`{"call_id":"call_1","argv":["go"],"timeout_ms":1000,"secret_env":` + nested + `,"authz":{"decision_id":"dec_1","profile":"","approved":true,"kind":"harness"}}`, func() any { return new(SpawnParams) }},
	}
	const budget = 64 << 20
	for name, f := range frames {
		if len(f.raw) < 900<<10 {
			t.Fatalf("%s: frame is %d bytes, want about 1 MiB", name, len(f.raw))
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		err := Decode([]byte(f.raw), f.v())
		runtime.ReadMemStats(&after)
		if err == nil {
			t.Errorf("%s: Decode accepted a %d-deep frame", name, depth)
		}
		t.Logf("%s: %d bytes in, %d KiB allocated", name, len(f.raw), (after.TotalAlloc-before.TotalAlloc)>>10)
		if got := after.TotalAlloc - before.TotalAlloc; got > budget {
			t.Errorf("%s: Decode allocated %d MiB, budget %d MiB", name, got>>20, budget>>20)
		}
	}
}

// TestDecodeDepthCap: no A06 message nests deeper than about 6 levels;
// anything past 64 is refused with an error, never silently passed.
func TestDecodeDepthCap(t *testing.T) {
	for depth, ok := range map[int]bool{1: true, 64: true, 65: false, 10000: false} {
		raw := strings.Repeat("[", depth) + strings.Repeat("]", depth)
		err := Decode([]byte(raw), new(any))
		if (err == nil) != ok {
			t.Errorf("depth %d: Decode error = %v, want ok=%v", depth, err, ok)
		}
	}
}

// TestDecodeRejectsSecretEnvCaseVariants: environment variable names are
// case-insensitive on Windows, so {"K":..,"k":..} would set one variable
// with whichever value the platform picks.
func TestDecodeRejectsSecretEnvCaseVariants(t *testing.T) {
	raw := `{"call_id":"call_1","argv":["go"],"timeout_ms":1000,"secret_env":{"TOKEN":"a","token":"b"},"authz":{"decision_id":"dec_1","profile":"","approved":true,"kind":"harness"}}`
	if err := Decode([]byte(raw), new(SpawnParams)); err == nil {
		t.Errorf("Decode accepted case-variant secret_env keys")
	}
}

// TestSecretEnvEqual: SecretEnv hides its values behind a func, which
// reflect.DeepEqual and go-cmp cannot compare; Equal compares contents.
func TestSecretEnvEqual(t *testing.T) {
	a := NewSecretEnv(map[string]string{"K": "v"})
	cases := []struct {
		name string
		x, y SecretEnv
		want bool
	}{
		{"same contents", a, NewSecretEnv(map[string]string{"K": "v"}), true},
		{"both empty", SecretEnv{}, NewSecretEnv(nil), true},
		{"other value", a, NewSecretEnv(map[string]string{"K": "w"}), false},
		{"other key", a, NewSecretEnv(map[string]string{"L": "v"}), false},
		{"empty vs set", SecretEnv{}, a, false},
		{"extra key", a, NewSecretEnv(map[string]string{"K": "v", "L": "v"}), false},
	}
	for _, tc := range cases {
		if got := tc.x.Equal(tc.y); got != tc.want {
			t.Errorf("%s: Equal = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestWireTypesHaveNoEmbeddedFields: the key check (objectShape) maps each
// exported field to its JSON name and does not implement encoding/json's
// promotion of embedded fields or the `json:"-,"` spelling of a field named
// "-". A wire type that gained either would make the check refuse valid
// keys or accept invalid ones; this pins the assumption.
func TestWireTypesHaveNoEmbeddedFields(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, at string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] || opaqueTypes[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Anonymous {
				t.Errorf("%s.%s is an embedded field", at, f.Name)
			}
			if strings.HasPrefix(f.Tag.Get("json"), "-,") {
				t.Errorf("%s.%s uses the json:\"-,\" spelling", at, f.Name)
			}
			walk(f.Type, at+"."+f.Name)
		}
	}
	for _, tc := range goldenCases {
		v := tc.newT()
		walk(reflect.TypeOf(v), reflect.TypeOf(v).Elem().Name())
	}
	if len(seen) < 20 {
		t.Fatalf("walked only %d types", len(seen))
	}
}
