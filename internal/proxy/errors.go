package proxy

// Reason is a proxy.denied reason (A07 §9, the ProxyDenied.reason enum).
type Reason string

// The A07 §9 reasons. M2 emits the ones it can produce; the hold, flood and
// rate-limit reasons arrive with A07 §6 in M3.
const (
	ReasonPolicyDenied       Reason = "policy_denied"
	ReasonApprovalRejected   Reason = "approval_rejected"
	ReasonApprovalTimeout    Reason = "approval_timeout"
	ReasonClientClosed       Reason = "client_closed"
	ReasonSandboxDestroyed   Reason = "sandbox_destroyed"
	ReasonInvalidTarget      Reason = "invalid_target"
	ReasonUnsupportedMethod  Reason = "unsupported_method"
	ReasonPrivateAddress     Reason = "private_address"
	ReasonDNSFailed          Reason = "dns_failed"
	ReasonSNIMismatch        Reason = "sni_mismatch"
	ReasonNotTLS             Reason = "not_tls"
	ReasonTooManyConnections Reason = "too_many_connections"
	ReasonTooManyPending     Reason = "too_many_pending"
	ReasonRateLimited        Reason = "rate_limited"
)

// DenyError is a refusal with its proxy.denied reason. Detail is for the
// daemon log only; it never reaches an event (A07 §9).
type DenyError struct {
	Reason Reason
	Detail string
	Err    error
}

func (e *DenyError) Error() string {
	s := "proxy: " + string(e.Reason)
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *DenyError) Unwrap() error { return e.Err }

func deny(r Reason, detail string) *DenyError { return &DenyError{Reason: r, Detail: detail} }
