// Package secrets implements the secrets broker of WRD-10 §6 and design
// A15: keychain backends (Secret Service, macOS Keychain, Windows
// Credential Manager through go-keyring; the encrypted-file backend of
// CLAUDE.md §3; an in-memory backend for tests), strict secret:// parsing
// and consumer authorization, host-side credential resolution for the
// provider adapters (model.CredentialSource) with secret.access events,
// the redaction engine, and the Ed25519 checkpoint signer.
//
// Secret values never leave this package except inside a model.Credential
// handed to an adapter's transport; they are never returned by an API
// method, serialized, logged or put into an event (INV-C).
package secrets

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Service is the keychain service name for every Warden item.
const Service = "warden"

// Ref is a parsed secret:// reference (A15 §3.1).
type Ref struct {
	Account string // keychain account, e.g. providers/anthropic/api_key
	Kind    string // provider | harness | checkpoint_key
	ID      string // provider or harness id
	Name    string // api_key | token | client_key | client_cert
}

func (r Ref) String() string { return "secret://" + r.Account }

var (
	providerRef = regexp.MustCompile(`^providers/([a-z0-9][a-z0-9-]{0,62})/(api_key|token|client_key|client_cert)$`)
	harnessRef  = regexp.MustCompile(`^harnesses/([a-z0-9][a-z0-9-]{0,62})/token$`)
)

// ErrBadRef is returned for anything that is not a valid reference.
var ErrBadRef = errors.New("invalid secret reference")

// Parse parses a reference strictly: no query, fragment, percent-encoding,
// dot segments or trailing slash (A15 §3.1).
func Parse(s string) (Ref, error) {
	acct, ok := strings.CutPrefix(s, "secret://")
	if !ok {
		return Ref{}, fmt.Errorf("%w: %q must start with secret://", ErrBadRef, s)
	}
	if m := providerRef.FindStringSubmatch(acct); m != nil {
		return Ref{Account: acct, Kind: "provider", ID: m[1], Name: m[2]}, nil
	}
	if m := harnessRef.FindStringSubmatch(acct); m != nil {
		return Ref{Account: acct, Kind: "harness", ID: m[1], Name: "token"}, nil
	}
	if acct == "keys/checkpoint/ed25519" {
		return Ref{Account: acct, Kind: "checkpoint_key"}, nil
	}
	return Ref{}, fmt.Errorf("%w: %q", ErrBadRef, s)
}

// ProviderRef builds secret://providers/<id>/<name>.
func ProviderRef(id, name string) string { return "secret://providers/" + id + "/" + name }

// ErrNotPermitted is returned when a consumer may not resolve a reference.
var ErrNotPermitted = errors.New("not_permitted")

// Authorize binds references to consumers (A15 §3.2): adapter:<id> may
// resolve only secret://providers/<id>/*; harness:<id> only its token;
// checkpoint only the checkpoint key; proxy and delivery nothing.
func Authorize(r Ref, consumer string) error {
	switch {
	case consumer == "adapter:"+r.ID && r.Kind == "provider":
		return nil
	case consumer == "harness:"+r.ID && r.Kind == "harness":
		return nil
	case consumer == "checkpoint" && r.Kind == "checkpoint_key":
		return nil
	}
	return fmt.Errorf("%w: %s may not resolve %s", ErrNotPermitted, consumer, r)
}
