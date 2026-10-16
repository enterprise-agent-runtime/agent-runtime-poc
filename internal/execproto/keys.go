package execproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// checkKeys walks the JSON text alongside the Go type it will be decoded
// into and refuses what encoding/json would silently accept: a duplicate
// key in any object (json keeps the last one), and, in an object that maps
// to a struct, a key that is not exactly one of the struct's JSON names
// (json matches names case-insensitively, so "ARGV" or "Approved" would set
// the field). Objects decoded by their own UnmarshalJSON, and
// json.RawMessage, are checked for duplicates only; their decoders check the
// rest (ParseProfiles does).
// Validity of the JSON itself is left to the decoder that follows.
func checkKeys(data []byte, t reflect.Type) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	err := walkKeys(dec, t, "$")
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		return nil // reported, with position, by the real decode
	}
	return err
}

func walkKeys(dec *json.Decoder, t reflect.Type, at string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // a scalar
	}
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch delim {
	case '[':
		var elem reflect.Type
		if t != nil && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && t != reflect.TypeFor[json.RawMessage]() {
			elem = t.Elem()
		}
		for dec.More() {
			if err := walkKeys(dec, elem, at+"[]"); err != nil {
				return err
			}
		}
	case '{':
		fields, elem, strict := objectShape(t)
		seen := map[string]bool{}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := tok.(string)
			if seen[key] {
				return fmt.Errorf("duplicate key %q in %s", key, at)
			}
			seen[key] = true
			next := elem
			if strict {
				ft, ok := fields[key]
				if !ok {
					return fmt.Errorf("unknown key %q in %s (field names are exact and case-sensitive)", key, at)
				}
				next = ft
			}
			if err := walkKeys(dec, next, at+"."+key); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // the closing delimiter
	return err
}

// objectShape says how to check an object decoded into t: a struct gives
// its exact field names (strict); a map gives its element type; anything else (interface, RawMessage, a type with its own
// UnmarshalJSON) gets the duplicate check only.
func objectShape(t reflect.Type) (fields map[string]reflect.Type, elem reflect.Type, strict bool) {
	switch {
	case t == nil:
		return nil, nil, false
	case reflect.PointerTo(t).Implements(reflect.TypeFor[json.Unmarshaler]()):
		return nil, nil, false
	case t.Kind() == reflect.Map:
		return nil, t.Elem(), false
	case t.Kind() != reflect.Struct:
		return nil, nil, false
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
	return fields, nil, true
}
