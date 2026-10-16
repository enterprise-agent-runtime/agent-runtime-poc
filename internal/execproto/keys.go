package execproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// MaxDepth is the deepest nesting of arrays and objects Decode accepts. The
// A06 schemas nest about 6 levels; 64 leaves room and bounds the work of
// the key check on hostile input.
const MaxDepth = 64

// MaxArrayLen is the most elements Decode accepts in one array. The largest
// A06 array holds 2000 (exec.fs.list max_entries); argv holds at most 1024.
const MaxArrayLen = 4096

// checkKeys walks the JSON text alongside the Go type it will be decoded
// into and refuses what encoding/json would silently accept: a duplicate
// key in any object (json keeps the last one), and, in an object that maps
// to a struct, a key that is not exactly one of the struct's JSON names
// (json matches names case-insensitively, so "ARGV" or "Approved" would set
// the field). Objects decoded by their own UnmarshalJSON, maps, and
// json.RawMessage get the duplicate check only; their decoders check the
// rest (ParseProfiles, SecretEnv.UnmarshalJSON). It also refuses nesting
// deeper than MaxDepth, arrays longer than MaxArrayLen, and an array or
// object where the Go type cannot hold one (the decoder would refuse it
// too, but only after allocating its way through all of it).
//
// The cost is linear in the input: the path to the current value is a
// stack of keys, joined into a string only for an error message (the first
// version built a path string per level and kept them all, which grew with
// depth² and reached gigabytes on a 2 MiB frame). Validity of the JSON
// itself is left to the decoder that follows.
func checkKeys(data []byte, t reflect.Type) error {
	w := keyWalker{dec: json.NewDecoder(bytes.NewReader(data))}
	w.dec.UseNumber()
	err := w.walk(t)
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		return nil // reported, with position, by the real decode
	}
	return err
}

type keyWalker struct {
	dec  *json.Decoder
	path []string // keys from the root to the current value; "[]" for array elements
}

func (w *keyWalker) at() string {
	if len(w.path) == 0 {
		return "$"
	}
	return "$." + strings.Join(w.path, ".")
}

func (w *keyWalker) walk(t reflect.Type) error {
	tok, err := w.dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // a scalar
	}
	if len(w.path) >= MaxDepth {
		return fmt.Errorf("nesting deeper than %d at %s", MaxDepth, w.at())
	}
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != nil && t.Kind() == reflect.Interface {
		t = nil
	}
	switch delim {
	case '[':
		elem, ok := arrayShape(t)
		if !ok {
			return fmt.Errorf("an array where %s is expected, at %s", t, w.at())
		}
		w.path = append(w.path, "[]")
		for count := 0; w.dec.More(); count++ {
			if count == MaxArrayLen {
				return fmt.Errorf("array longer than %d at %s", MaxArrayLen, w.at())
			}
			if err := w.walk(elem); err != nil {
				return err
			}
		}
		w.path = w.path[:len(w.path)-1]
	case '{':
		fields, elem, strict, ok := objectShape(t)
		if !ok {
			return fmt.Errorf("an object where %s is expected, at %s", t, w.at())
		}
		limit := maxObjectKeys(t)
		seen := map[string]bool{}
		for count := 0; w.dec.More(); count++ {
			if limit > 0 && count == limit {
				// Refuse at the first key too many, before reading the rest:
				// a 100k-key secret_env is one key over, not 100k keys of work.
				return fmt.Errorf("more than %d keys in %s", limit, w.at())
			}
			tok, err := w.dec.Token()
			if err != nil {
				return err
			}
			key, _ := tok.(string)
			if seen[key] {
				return fmt.Errorf("duplicate key %q in %s", key, w.at())
			}
			seen[key] = true
			next := elem
			if strict {
				ft, ok := fields[key]
				if !ok {
					return fmt.Errorf("unknown key %q in %s (field names are exact and case-sensitive)", key, w.at())
				}
				next = ft
			}
			w.path = append(w.path, key)
			if err := w.walk(next); err != nil {
				return err
			}
			w.path = w.path[:len(w.path)-1]
		}
	}
	_, err = w.dec.Token() // the closing delimiter
	return err
}

// maxObjectKeys is the most keys an object decoded into t may have, 0 for no
// limit beyond the input size. Only SecretEnv has one (maxProperties 1);
// SecretEnv.UnmarshalJSON checks it again.
func maxObjectKeys(t reflect.Type) int {
	if t == reflect.TypeFor[SecretEnv]() {
		return MaxSecretEnvKeys
	}
	return 0
}

// arrayShape says how to check an array decoded into t: its element type,
// or nil for an untyped target. ok is false when t cannot hold an array.
func arrayShape(t reflect.Type) (elem reflect.Type, ok bool) {
	switch {
	case t == nil:
		return nil, true
	case t == reflect.TypeFor[json.RawMessage]() || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]()):
		return nil, true
	case t.Kind() == reflect.Slice || t.Kind() == reflect.Array:
		return t.Elem(), true
	}
	return nil, false
}

// objectShape says how to check an object decoded into t: a struct gives
// its exact field names (strict); a map gives its element type; an untyped
// target, json.RawMessage or a type with its own UnmarshalJSON gets the
// duplicate check only. ok is false when t cannot hold an object.
//
// It assumes what the wire types guarantee and TestWireTypesHaveNoEmbedded
// Fields pins: no embedded (promoted) fields and no `json:"-,"` field
// named "-". encoding/json handles both; this function does not.
func objectShape(t reflect.Type) (fields map[string]reflect.Type, elem reflect.Type, strict, ok bool) {
	switch {
	case t == nil:
		return nil, nil, false, true
	case reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]()):
		return nil, nil, false, true
	case t.Kind() == reflect.Map:
		return nil, t.Elem(), false, true
	case t.Kind() != reflect.Struct:
		return nil, nil, false, false
	}
	fields = map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		fields[name] = f.Type
	}
	return fields, nil, true, true
}
