package execproto

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
)

// RedactedValue replaces every secret_env value outside the wire encoder.
// It starts with the A15 §6.4 marker, so a redacted copy that is ever
// written back is refused by the executor (ErrRedactionMarker).
const RedactedValue = "[REDACTED:secret_env]"

// SecretEnv is exec.proc.spawn secret_env: at most one variable whose value
// is a vendor token for a harness engine (HX-1b, A06 §4.3, A15 §5). The
// value is a secret (CLAUDE.md §9 rule 3, INV-C), so SecretEnv is built to
// keep it out of every generic output path:
//
//   - fmt (all verbs), slog (LogValuer) and String print the keys only;
//   - json.Marshal writes RedactedValue in place of each value;
//   - the values are reachable only through a func. fmt cannot call methods
//     on a value in an unexported struct field and prints it by reflection,
//     following maps and pointers (a pointer under %s or %q prints its
//     target); a func value prints as an address under every verb.
//
// The value reaches the wire only through EncodeSpawnParams or MarshalWire,
// and code reads it only through Value. Decoding (json.Unmarshal, Decode)
// sets the real values.
type SecretEnv struct {
	vals func() map[string]string
}

// NewSecretEnv copies m into a SecretEnv; an empty m gives the zero value.
func NewSecretEnv(m map[string]string) SecretEnv {
	if len(m) == 0 {
		return SecretEnv{}
	}
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return SecretEnv{vals: func() map[string]string { return c }}
}

// Len is the number of variables.
func (e SecretEnv) Len() int {
	if e.vals == nil {
		return 0
	}
	return len(e.vals())
}

// IsZero reports an empty SecretEnv (for the omitzero tag).
func (e SecretEnv) IsZero() bool { return e.Len() == 0 }

// Keys returns the variable names, sorted.
func (e SecretEnv) Keys() []string {
	keys := make([]string, 0, e.Len())
	if e.vals != nil {
		for k := range e.vals() {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Value returns the secret value of key. It is the one accessor for the
// value; the executor calls it when it builds the engine's environment.
func (e SecretEnv) Value(key string) (string, bool) {
	if e.vals == nil {
		return "", false
	}
	v, ok := e.vals()[key]
	return v, ok
}

// String lists the keys, never the values.
func (e SecretEnv) String() string { return "SecretEnv{" + strings.Join(e.Keys(), ",") + "}" }

// GoString is String, so %#v does not print values either.
func (e SecretEnv) GoString() string { return e.String() }

// Format makes every fmt verb print String.
func (e SecretEnv) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, e.String()) }

// LogValue makes slog print the keys only.
func (e SecretEnv) LogValue() slog.Value { return slog.StringValue(e.String()) }

// MarshalJSON writes each key with RedactedValue. Use EncodeSpawnParams or
// MarshalWire to send the real value.
func (e SecretEnv) MarshalJSON() ([]byte, error) {
	red := make(map[string]string, e.Len())
	for _, k := range e.Keys() {
		red[k] = RedactedValue
	}
	return json.Marshal(red)
}

// reveal is the real map, for the wire encoder only.
func (e SecretEnv) reveal() map[string]string {
	if e.vals == nil {
		return nil
	}
	return e.vals()
}

// UnmarshalJSON reads an object of strings; null is empty. Duplicate and
// case-variant keys are refused earlier, by Decode's key check.
func (e *SecretEnv) UnmarshalJSON(data []byte) error {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*e = NewSecretEnv(m)
	return nil
}

// spawnPlain is SpawnParams without its methods, for encoding.
type spawnPlain SpawnParams

// MarshalJSON encodes the params with secret_env redacted and a nil argv as
// []. It is what json.Marshal, and anything built on it, produces.
func (p SpawnParams) MarshalJSON() ([]byte, error) { return p.encode(false) }

// EncodeSpawnParams encodes exec.proc.spawn params for the wire, with the
// real secret_env value. The transport must use this (or MarshalWire) for
// the params of exec.proc.spawn: putting a SpawnParams inside another value
// and calling json.Marshal on that sends the redacted placeholder instead.
func EncodeSpawnParams(p SpawnParams) ([]byte, error) { return p.encode(true) }

func (p SpawnParams) encode(reveal bool) ([]byte, error) {
	plain := spawnPlain(p)
	if plain.Argv == nil {
		plain.Argv = []string{}
	}
	plain.SecretEnv = SecretEnv{}
	b, err := json.Marshal(plain)
	if err != nil || p.SecretEnv.IsZero() {
		return b, err
	}
	var env []byte
	if reveal {
		env, err = json.Marshal(p.SecretEnv.reveal())
	} else {
		env, err = p.SecretEnv.MarshalJSON()
	}
	if err != nil {
		return nil, err
	}
	// b is a non-empty object (call_id, argv, timeout_ms, authz are always
	// written), so the field can be appended before its closing brace.
	out := make([]byte, 0, len(b)+len(env)+16)
	out = append(out, b[:len(b)-1]...)
	out = append(out, `,"secret_env":`...)
	out = append(out, env...)
	out = append(out, '}')
	return out, nil
}

// LogValue logs a spawn without its arguments or secret values: argv may
// carry file content or tokens the PDP has not yet scanned (A06 §6 step 7).
func (p SpawnParams) LogValue() slog.Value {
	exe := ""
	if len(p.Argv) > 0 {
		exe = p.Argv[0]
	}
	return slog.GroupValue(
		slog.String("call_id", p.CallID),
		slog.String("argv0", exe),
		slog.Int("argc", len(p.Argv)),
		slog.String("cwd", p.Cwd),
		slog.Int64("timeout_ms", p.TimeoutMS),
		slog.String("decision_id", p.Authz.DecisionID),
		slog.String("profile", p.Authz.Profile),
		slog.String("secret_env", p.SecretEnv.String()),
	)
}

// MarshalWire encodes any message for the wire: EncodeSpawnParams for
// spawn params, json.Marshal for everything else. A transport that encodes
// params through MarshalWire into a json.RawMessage before building the
// JSON-RPC envelope cannot lose or leak secret_env.
func MarshalWire(v any) ([]byte, error) {
	switch p := v.(type) {
	case SpawnParams:
		return EncodeSpawnParams(p)
	case *SpawnParams:
		if p != nil {
			return EncodeSpawnParams(*p)
		}
	}
	return json.Marshal(v)
}
