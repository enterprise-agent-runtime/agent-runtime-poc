package proxy

import (
	"context"
	"net/http"
)

// CredentialInjector adds credentials host-side (A07 §11, WRD-10 §7). The
// PoC has only noInjector: WRD-16 §10.4 keeps the hook so that the MVP does
// not change the proxy's structure, and no configuration selects another
// implementation.
type CredentialInjector interface {
	// Match reports whether this target would receive injected credentials.
	Match(t Target) bool
	// Inject mutates outbound headers of a plain-HTTP request (or, in a
	// future TLS-terminating mode, of the decrypted request). It would
	// resolve secret:// references through internal/secrets and emit
	// secret.access{consumer: "proxy"}.
	Inject(ctx context.Context, t Target, h http.Header) error
}

type noInjector struct{}

func (noInjector) Match(Target) bool                                 { return false }
func (noInjector) Inject(context.Context, Target, http.Header) error { return nil }
