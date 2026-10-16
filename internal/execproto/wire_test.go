package execproto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
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
