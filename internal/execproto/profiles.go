package execproto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// ArgsPlaceholder is the only placeholder a profile entry may contain, and
// only as its last token: it matches zero or more remaining argv elements
// (A06 §3.2, §6 step 4).
const ArgsPlaceholder = "<args>"

// Limits of the A06 §3.2 schema and §6 step 4.
const (
	maxProfileName   = 32   // ^[a-z][a-z0-9-]{0,31}$
	maxEntryTokens   = 16   // maxItems of an entry
	maxTokenChars    = 256  // maxLength of a token, in characters as JSON Schema counts
	maxArgsElemBytes = 4096 // each element matched by <args> (A06 §6 step 4, A08 §2.4)
	maxArgsElems     = 64   // elements <args> may absorb (A08 §2.4)
	profilesVersion  = 1
	errPrefix        = "execproto: profiles file"
)

// Profile is one named command profile with its argv entries, in file order.
type Profile struct {
	Name     string
	Commands [][]string
}

// ProfileSet is the allow-profiles file of A06 §3.2: the profiles granted to
// one task (capability commands.profiles ∩ policy/platform-defaults.yaml).
// Profiles keep file order, because the first matching profile is the
// executor's match (A06 §6 step 4); that is why this is a slice and the
// JSON object is decoded and encoded by hand rather than through a map.
type ProfileSet struct {
	Version  int
	TaskKey  string
	Profiles []Profile
}

// LoadProfiles reads and parses the file given as --allow-profiles and
// returns it with the digest of its bytes, which exec.hello reports.
func LoadProfiles(path string) (*ProfileSet, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", errPrefix, err)
	}
	set, err := ParseProfiles(data)
	if err != nil {
		return nil, "", err
	}
	return set, ProfilesDigest(data), nil
}

// ProfilesDigest is "sha256:<hex>" of the file bytes (common.json Sha256).
func ProfilesDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseProfiles parses a profiles file and checks every rule of the A06
// §3.2 schema: unknown fields, missing fields and duplicate profile names
// are errors, and <args> may appear only as the last token of an entry and
// never as argv[0] (where it could only ever match the literal "<args>").
func ParseProfiles(data []byte) (*ProfileSet, error) {
	var f struct {
		Version  *int            `json:"version"`
		TaskKey  *string         `json:"task_key"`
		Profiles json.RawMessage `json:"profiles"`
	}
	if err := Decode(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	switch {
	case f.Version == nil:
		return nil, errors.New(errPrefix + ": version is required")
	case *f.Version != profilesVersion:
		return nil, fmt.Errorf("%s: version %d, want %d", errPrefix, *f.Version, profilesVersion)
	case f.TaskKey == nil:
		return nil, errors.New(errPrefix + ": task_key is required")
	case len(f.Profiles) == 0:
		return nil, errors.New(errPrefix + ": profiles is required")
	}
	profiles, err := parseProfileObject(f.Profiles)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	return &ProfileSet{Version: *f.Version, TaskKey: *f.TaskKey, Profiles: profiles}, nil
}

// parseProfileObject decodes the "profiles" object token by token so that
// its key order is kept and duplicate names are seen.
func parseProfileObject(raw json.RawMessage) ([]Profile, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("profiles must be an object")
	}
	out := []Profile{}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, _ := tok.(string)
		if !validProfileName(name) {
			return nil, fmt.Errorf("profile name %q does not match ^[a-z][a-z0-9-]{0,31}$", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate profile %q", name)
		}
		seen[name] = true
		var cmds [][]string
		if err := dec.Decode(&cmds); err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		if err := validateCommands(cmds); err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		out = append(out, Profile{Name: name, Commands: cmds})
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, err
	}
	return out, nil
}

func validProfileName(s string) bool {
	if len(s) == 0 || len(s) > maxProfileName || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func validateCommands(cmds [][]string) error {
	if len(cmds) == 0 {
		return errors.New("needs at least one entry")
	}
	for i, cmd := range cmds {
		if len(cmd) == 0 || len(cmd) > maxEntryTokens {
			return fmt.Errorf("entry %d has %d tokens, want 1..%d", i, len(cmd), maxEntryTokens)
		}
		for j, tok := range cmd {
			if !utf8.ValidString(tok) {
				// A set built in Go can hold bytes JSON cannot carry; the
				// encoder would write U+FFFD and the file would no longer
				// say what the daemon matched against.
				return fmt.Errorf("entry %d token %d is not valid UTF-8", i, j)
			}
			if n := utf8.RuneCountInString(tok); n == 0 || n > maxTokenChars {
				return fmt.Errorf("entry %d token %d has %d characters, want 1..%d", i, j, n, maxTokenChars)
			}
			if tok == ArgsPlaceholder && (j == 0 || j != len(cmd)-1) {
				return fmt.Errorf("entry %d: %s may only be the last token, after argv[0]", i, ArgsPlaceholder)
			}
		}
	}
	return nil
}

// UnmarshalJSON is ParseProfiles.
func (p *ProfileSet) UnmarshalJSON(data []byte) error {
	set, err := ParseProfiles(data)
	if err != nil {
		return err
	}
	*p = *set
	return nil
}

// EncodeProfiles is how the daemon writes --allow-profiles: it validates
// the set against the same rules ParseProfiles applies and returns the file
// bytes together with their digest, which exec.hello must report back
// (A06 §3.1, §4.3). Write exactly these bytes.
func EncodeProfiles(set *ProfileSet) (data []byte, digest string, err error) {
	if set == nil {
		return nil, "", errors.New(errPrefix + ": nil profile set")
	}
	data, err = set.MarshalJSON()
	if err != nil {
		return nil, "", err
	}
	return data, ProfilesDigest(data), nil
}

// MarshalJSON validates the set and writes it with the profiles in their
// order. It uses encoding/json's default escaping ("<args>" is written as
// <args>), because json.Marshal re-escapes a Marshaler's output:
// any other choice would make json.Marshal(set) and set.MarshalJSON()
// produce different bytes, hence different digests.
func (p ProfileSet) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	taskKey, err := json.Marshal(p.TaskKey)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, `{"version":%d,"task_key":%s,"profiles":{`, p.Version, taskKey)
	for i, pr := range p.Profiles {
		if i > 0 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(pr.Name)
		if err != nil {
			return nil, err
		}
		cmds, err := json.Marshal(pr.Commands)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(cmds)
	}
	b.WriteString("}}")
	return b.Bytes(), nil
}

// validate applies the A06 §3.2 rules to a set built in Go.
func (p ProfileSet) validate() error {
	if p.Version != profilesVersion {
		return fmt.Errorf("%s: version %d, want %d", errPrefix, p.Version, profilesVersion)
	}
	seen := map[string]bool{}
	for _, pr := range p.Profiles {
		if !validProfileName(pr.Name) {
			return fmt.Errorf("%s: profile name %q does not match ^[a-z][a-z0-9-]{0,31}$", errPrefix, pr.Name)
		}
		if seen[pr.Name] {
			return fmt.Errorf("%s: duplicate profile %q", errPrefix, pr.Name)
		}
		seen[pr.Name] = true
		if err := validateCommands(pr.Commands); err != nil {
			return fmt.Errorf("%s: profile %q: %w", errPrefix, pr.Name, err)
		}
	}
	return nil
}

// Match returns the first profile, in file order, with an entry matching
// argv exactly (A06 §6 step 4): argv[0] contains no '/' and equals the
// first token, every other literal token equals the argv element at its
// position, and argv has exactly as many elements as the entry unless the
// entry ends in <args>, which matches zero or more remaining elements.
// Case-sensitive; no globbing; no option reordering. What <args> may absorb
// follows A08 §2.4 so the executor never accepts what the PDP refuses: at
// most 64 elements, each 1 to 4096 bytes, none containing a newline or NUL.
// A nil set matches nothing.
func (p *ProfileSet) Match(argv []string) (string, bool) {
	if p == nil {
		return "", false
	}
	for _, pr := range p.Profiles {
		for _, cmd := range pr.Commands {
			if matchEntry(cmd, argv) {
				return pr.Name, true
			}
		}
	}
	return "", false
}

func matchEntry(cmd, argv []string) bool {
	if len(argv) == 0 || len(cmd) == 0 || strings.Contains(argv[0], "/") {
		return false
	}
	fixed := cmd
	open := cmd[len(cmd)-1] == ArgsPlaceholder
	if open {
		fixed = cmd[:len(cmd)-1]
		if len(argv) < len(fixed) {
			return false
		}
	} else if len(argv) != len(cmd) {
		return false
	}
	for i, tok := range fixed {
		if argv[i] != tok {
			return false
		}
	}
	rest := argv[len(fixed):]
	if len(rest) > maxArgsElems {
		return false
	}
	for _, a := range rest {
		if len(a) == 0 || len(a) > maxArgsElemBytes || strings.ContainsAny(a, "\n\x00") {
			return false
		}
	}
	return true
}
