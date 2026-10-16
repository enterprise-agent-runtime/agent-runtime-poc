// Package schema embeds the runtime API contract of design A05 (method
// params/results, notifications, shared types) together with the A04
// identifiers, event envelope and payload schemas it references, all JSON
// Schema draft 2020-12 under https://schemas.warden.dev/poc/. The files are
// extracted verbatim from the design documents; deviations are limited to
// the Windows path patterns and the "windows" OS value (CONFLICTS C-47).
// The UI's TypeScript types are generated from these files (CLAUDE.md §7).
package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed common.json approval.json api events artifacts
var files embed.FS

// Base is the URL prefix of every schema $id.
const Base = "https://schemas.warden.dev/poc/"

// Registry holds the compiled params and result schemas of every method.
type Registry struct {
	params, results map[string]*jsonschema.Schema
	notifications   map[string]*jsonschema.Schema
	compiler        *jsonschema.Compiler
}

var (
	once    sync.Once
	reg     *Registry
	loadErr error
)

// Load compiles the embedded schemas once.
func Load() (*Registry, error) {
	once.Do(func() { reg, loadErr = compile() })
	return reg, loadErr
}

// Files lists the embedded schema paths (relative to Base).
func Files() ([]string, error) {
	var out []string
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			out = append(out, p)
		}
		return err
	})
	sort.Strings(out)
	return out, err
}

func compile() (*Registry, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	paths, err := Files()
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		b, _ := files.ReadFile(p)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", p, err)
		}
		if err := c.AddResource(Base+p, doc); err != nil {
			return nil, fmt.Errorf("schema %s: %w", p, err)
		}
	}
	r := &Registry{params: map[string]*jsonschema.Schema{}, results: map[string]*jsonschema.Schema{},
		notifications: map[string]*jsonschema.Schema{}, compiler: c}
	for _, p := range paths {
		name, ok := strings.CutPrefix(p, "api/")
		if !ok || name == "types.json" {
			continue
		}
		method := strings.TrimSuffix(name, ".json")
		if n, ok := strings.CutPrefix(method, "notification."); ok {
			s, err := c.Compile(Base + p + "#/$defs/params")
			if err != nil {
				return nil, fmt.Errorf("%s params: %w", p, err)
			}
			r.notifications[n] = s
			continue
		}
		ps, err := c.Compile(Base + p + "#/$defs/params")
		if err != nil {
			return nil, fmt.Errorf("%s params: %w", p, err)
		}
		rs, err := c.Compile(Base + p + "#/$defs/result")
		if err != nil {
			return nil, fmt.Errorf("%s result: %w", p, err)
		}
		r.params[method], r.results[method] = ps, rs
	}
	return r, nil
}

// Methods lists every method with a schema.
func (r *Registry) Methods() []string {
	var out []string
	for m := range r.params {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Has reports whether method has a schema.
func (r *Registry) Has(method string) bool { _, ok := r.params[method]; return ok }

func instance(raw []byte) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

// ValidateParams checks request params (strict: unknown params rejected).
func (r *Registry) ValidateParams(method string, raw []byte) error {
	s, ok := r.params[method]
	if !ok {
		return fmt.Errorf("no schema for %s", method)
	}
	v, err := instance(raw)
	if err != nil {
		return err
	}
	return s.Validate(v)
}

// ValidateResult checks a result value (open: extra fields allowed).
func (r *Registry) ValidateResult(method string, result any) error {
	s, ok := r.results[method]
	if !ok {
		return fmt.Errorf("no schema for %s", method)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	v, err := instance(b)
	if err != nil {
		return err
	}
	return s.Validate(v)
}

// ValidateNotification checks notification params (event, stream.delta, event.gap).
func (r *Registry) ValidateNotification(name string, params any) error {
	s, ok := r.notifications[name]
	if !ok {
		return fmt.Errorf("no schema for notification %s", name)
	}
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	v, err := instance(b)
	if err != nil {
		return err
	}
	return s.Validate(v)
}

// ValidateEnvelope checks a stored event envelope against events/envelope.json.
func (r *Registry) ValidateEnvelope(raw []byte) error {
	s, err := r.compiler.Compile(Base + "events/envelope.json")
	if err != nil {
		return err
	}
	v, err := instance(raw)
	if err != nil {
		return err
	}
	return s.Validate(v)
}
