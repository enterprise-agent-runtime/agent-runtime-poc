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

// rawDaemon is config.yaml as written. default_level is a node, so a key
// that is present but null, empty or not a string is told apart from an
// absent key: only the absent key takes the platform default.
type rawDaemon struct {
	Sandbox struct {
		DefaultLevel yaml.Node `yaml:"default_level"`
	} `yaml:"sandbox"`
}

// LoadDaemon reads config.yaml at path. A missing file or an absent
// sandbox.default_level gives def (platform.DefaultSandboxLevel()); a
// present value is normalised against levels (platform.SandboxLevels())
// by SandboxLevel, and anything it refuses stops the load.
func LoadDaemon(path string, levels []string, def string) (Daemon, error) {
	d := Daemon{Sandbox: DaemonSandbox{DefaultLevel: def}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return Daemon{}, fmt.Errorf("config.yaml: %w", err)
	}
	var raw rawDaemon
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) { // an empty file sets nothing
		return Daemon{}, fmt.Errorf("config.yaml: %w", err)
	}
	n := raw.Sandbox.DefaultLevel
	if n.Kind == 0 { // the key is absent
		return d, nil
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return Daemon{}, fmt.Errorf("config.yaml line %d: sandbox.default_level must be one of %s, as a string (got %s)",
			n.Line, strings.Join(levels, ", "), describeNode(n))
	}
	lvl, err := SandboxLevel(n.Value, levels)
	if err != nil {
		return Daemon{}, fmt.Errorf("config.yaml line %d: %w", n.Line, err)
	}
	d.Sandbox.DefaultLevel = lvl
	return d, nil
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
