package config

// config.yaml: the daemon's own settings (WRD-13 §3, CLAUDE.md §4
// "Sandbox": sandbox.default_level). Today the daemon reads one key; the
// struct grows with the keys it reads, and anything else is refused at
// load (CLAUDE.md §7) rather than accepted and ignored.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Daemon is config.yaml after validation.
type Daemon struct {
	Sandbox DaemonSandbox
}

// DaemonSandbox is the sandbox section.
type DaemonSandbox struct {
	// DefaultLevel is sandbox.default_level in its canonical spelling, one
	// of platform.SandboxLevels() (CONFLICTS C-06).
	DefaultLevel string
}

// daemonKeys is every key config.yaml may hold, by section. A key outside
// it is refused at load and named, with its line (CLAUDE.md §7).
var daemonKeys = map[string][]string{
	"sandbox": {"default_level"},
}

// LoadDaemon reads config.yaml at path. A missing file or an absent
// sandbox.default_level gives def (platform.DefaultSandboxLevel()); a
// present value is normalised against levels (platform.SandboxLevels())
// by SandboxLevel, and anything it refuses stops the load.
//
// The file is walked as YAML nodes rather than decoded into a struct, so
// that a key the daemon does not read is named as the user wrote it
// ("sandbox.backend", not a Go type), a merge key cannot bring keys in
// unseen, and a key that is present but null, empty or not a string is
// told apart from an absent one: only the absent key takes the default.
// The file must hold exactly one YAML document: a second one, or a first
// one that is empty, would otherwise go unread (CWE-178, review of M2-17).
func LoadDaemon(path string, levels []string, def string) (Daemon, error) {
	d := Daemon{Sandbox: DaemonSandbox{DefaultLevel: def}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return Daemon{}, fmt.Errorf("config.yaml: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) { // an empty file sets nothing
			return d, nil
		}
		return Daemon{}, fmt.Errorf("config.yaml: %w", err)
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return Daemon{}, errors.New("config.yaml must contain exactly one YAML document")
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return d, nil
		}
		root = root.Content[0]
	}
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" { // "---" or "~" alone
		return d, nil
	}
	values, err := daemonValues(root)
	if err != nil {
		return Daemon{}, err
	}
	n, ok := values["sandbox.default_level"]
	if !ok { // the key is absent
		return d, nil
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return Daemon{}, fmt.Errorf("config.yaml line %d: sandbox.default_level must be one of %s, as a string (got %s)",
			n.Line, strings.Join(levels, ", "), describeNode(*n))
	}
	lvl, err := SandboxLevel(n.Value, levels)
	if err != nil {
		return Daemon{}, fmt.Errorf("config.yaml line %d: %w", n.Line, err)
	}
	d.Sandbox.DefaultLevel = lvl
	return d, nil
}

// daemonValues walks the top-level mapping and returns the value node of
// every known key by its dotted name, refusing unknown, repeated and
// non-scalar keys and sections that are not mappings.
func daemonValues(root *yaml.Node) (map[string]*yaml.Node, error) {
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config.yaml line %d: the top level must be a mapping of sections (got %s)", root.Line, describeNode(*root))
	}
	out := map[string]*yaml.Node{}
	err := eachKey(root, "", func(section string, k, v *yaml.Node) error {
		keys, known := daemonKeys[section]
		if !known {
			return notRead(k, section)
		}
		if v.Kind == yaml.ScalarNode && v.Tag == "!!null" { // "sandbox:" with nothing under it
			return nil
		}
		if v.Kind != yaml.MappingNode {
			return fmt.Errorf("config.yaml line %d: %+q must be a mapping (got %s)", v.Line, section, describeNode(*v))
		}
		return eachKey(v, section+".", func(name string, k, v *yaml.Node) error {
			if !slices.Contains(keys, name) {
				return notRead(k, section+"."+name)
			}
			out[section+"."+name] = v
			return nil
		})
	})
	return out, err
}

// eachKey calls f for every key of mapping m, refusing a key that is not
// a plain string or that appears twice. prefix is only for messages.
func eachKey(m *yaml.Node, prefix string, f func(name string, k, v *yaml.Node) error) error {
	seen := map[string]bool{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if k.Kind != yaml.ScalarNode || (k.Tag != "!!str" && k.Tag != "!!merge") {
			return fmt.Errorf("config.yaml line %d: keys must be plain names (got %s)", k.Line, describeNode(*k))
		}
		if seen[k.Value] {
			return fmt.Errorf("config.yaml line %d: key %+q appears twice", k.Line, prefix+k.Value)
		}
		seen[k.Value] = true
		if err := f(k.Value, k, v); err != nil {
			return err
		}
	}
	return nil
}

func notRead(k *yaml.Node, name string) error {
	return fmt.Errorf("config.yaml line %d: key %+q is not read by this version of wardend; remove it", k.Line, name)
}

func describeNode(n yaml.Node) string {
	switch {
	case n.Kind == yaml.ScalarNode && n.Tag == "!!null":
		return "no value"
	case n.Kind == yaml.ScalarNode:
		return "a value of type " + strings.TrimPrefix(n.Tag, "!!")
	case n.Kind == yaml.SequenceNode:
		return "a list"
	case n.Kind == yaml.MappingNode:
		return "a mapping"
	}
	return "something else"
}

// SandboxLevel normalises a sandbox.default_level value (CWE-178). Leading
// and trailing ASCII white space is trimmed; what is left must be ASCII and
// equal one of levels ignoring ASCII case, and comes back in that level's
// spelling ("l2" is "L2"). Anything else is an error, never a default: an
// empty value, a level this OS does not offer (L1 on Windows, C-06), and
// any non-ASCII character, so a look-alike such as a fullwidth L is not a
// level. The error shows the value Go-escaped, so a look-alike is visible
// as one.
func SandboxLevel(raw string, levels []string) (string, error) {
	v := strings.Trim(raw, " \t\r\n")
	ascii := true
	for i := 0; i < len(v); i++ {
		if v[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		for _, l := range levels {
			if asciiEqualFold(v, l) {
				return l, nil
			}
		}
	}
	why := "is not a sandbox level"
	if ascii && (asciiEqualFold(v, "L1") || asciiEqualFold(v, "L2")) {
		why = "is not available on this OS"
	}
	return "", fmt.Errorf("sandbox.default_level %+q %s; use one of %s", raw, why, strings.Join(levels, ", "))
}

// asciiEqualFold is strings.EqualFold restricted to ASCII: Unicode simple
// folding maps some non-ASCII letters onto ASCII ones (the Kelvin sign
// onto k), which a level name must never match.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'a' <= x && x <= 'z' {
			x -= 'a' - 'A'
		}
		if 'a' <= y && y <= 'z' {
			y -= 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
