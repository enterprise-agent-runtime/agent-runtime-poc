package proxy

import (
	"context"
	"encoding/json"
)

// The A07 §9 proxy.connect error values.
const (
	ConnErrDialFailed    = "dial_failed"
	ConnErrIdleTimeout   = "idle_timeout"
	ConnErrUpstreamReset = "upstream_reset"
)

// ConnectEvent is the proxy.connect payload (A07 §9). Empty Rule,
// DecisionID, CallID and Error encode as JSON null; the task and execution
// ids belong to the event envelope, which the sink writes.
type ConnectEvent struct {
	SandboxID  string
	Host       string
	Port       uint16
	Method     string
	Rule       string
	DecisionID string
	CallID     string
	IP         string // address actually dialed; "" before a dial
	BytesUp    int64
	BytesDown  int64
	DurationMS int64
	Error      string
}

// DeniedEvent is the proxy.denied payload (A07 §9). For a target that could
// not be normalised, Host is the raw text capped at 253 bytes and Port is
// whatever parsed (possibly 0).
type DeniedEvent struct {
	SandboxID  string
	Host       string
	Port       uint16
	Method     string
	Rule       string
	DecisionID string
	CallID     string
	Reason     Reason
	HeldMS     int64
	Count      int // > 1 only on rate-limit summaries (M3); 0 omits it
}

// EventSink writes proxy events to the session chain (A07 §12). The proxy
// calls it from connection goroutines, concurrently.
type EventSink interface {
	ProxyConnect(ctx context.Context, ev ConnectEvent) error
	ProxyDenied(ctx context.Context, ev DeniedEvent) error
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// MarshalJSON writes the A07 §9 ProxyConnect object.
func (e ConnectEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SandboxID  string  `json:"sandbox_id"`
		Host       string  `json:"host"`
		Port       uint16  `json:"port"`
		Method     string  `json:"method"`
		Rule       *string `json:"rule"`
		DecisionID *string `json:"decision_id"`
		CallID     *string `json:"call_id"`
		IP         string  `json:"ip,omitempty"`
		BytesUp    int64   `json:"bytes_up"`
		BytesDown  int64   `json:"bytes_down"`
		DurationMS int64   `json:"duration_ms"`
		Error      *string `json:"error"`
	}{e.SandboxID, e.Host, e.Port, e.Method, nullable(e.Rule), nullable(e.DecisionID), nullable(e.CallID),
		e.IP, e.BytesUp, e.BytesDown, e.DurationMS, nullable(e.Error)})
}

// MarshalJSON writes the A07 §9 ProxyDenied object.
func (e DeniedEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SandboxID  string  `json:"sandbox_id"`
		Host       string  `json:"host"`
		Port       uint16  `json:"port"`
		Method     string  `json:"method"`
		Rule       *string `json:"rule"`
		DecisionID *string `json:"decision_id"`
		CallID     *string `json:"call_id"`
		Reason     Reason  `json:"reason"`
		HeldMS     int64   `json:"held_ms"`
		Count      int     `json:"count,omitempty"`
	}{e.SandboxID, e.Host, e.Port, e.Method, nullable(e.Rule), nullable(e.DecisionID), nullable(e.CallID),
		e.Reason, e.HeldMS, e.Count})
}
