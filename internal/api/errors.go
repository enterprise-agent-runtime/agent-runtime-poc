package api

import (
	"encoding/json"
	"errors"

	"github.com/creachadair/jrpc2"
)

// Error codes of design A05 §5 and core §6; error.data.code carries the
// string name.
const (
	CodeParse                = -32700
	CodeInvalidRequest       = -32600
	CodeMethodNotFound       = -32601
	CodeInvalidParams        = -32602
	CodeInternal             = -32603
	CodeCancelled            = -32800
	CodeUnauthorized         = -32001
	CodeNotFound             = -32002
	CodeInvalidState         = -32003
	CodePolicyDenied         = -32004
	CodeConfirmationRequired = -32005
	CodeSandboxUnavailable   = -32006
	CodeNoAdmissibleModel    = -32007
	CodeBudgetExhausted      = -32008
	CodeStoreUnavailable     = -32009
	CodeUnsupportedInPoC     = -32010
	CodeProtocolMismatch     = -32011
	CodeVendorTerms          = -32012
)

var codeNames = map[int]string{
	CodeParse: "parse_error", CodeInvalidRequest: "invalid_request", CodeMethodNotFound: "method_not_found",
	CodeInvalidParams: "invalid_params", CodeInternal: "internal_error", CodeCancelled: "request_cancelled",
	CodeUnauthorized: "unauthorized", CodeNotFound: "not_found", CodeInvalidState: "invalid_state",
	CodePolicyDenied: "policy_denied", CodeConfirmationRequired: "confirmation_required",
	CodeSandboxUnavailable: "sandbox_unavailable", CodeNoAdmissibleModel: "no_admissible_model",
	CodeBudgetExhausted: "budget_exhausted", CodeStoreUnavailable: "store_unavailable",
	CodeUnsupportedInPoC: "unsupported_in_poc", CodeProtocolMismatch: "protocol_mismatch", CodeVendorTerms: "vendor_terms",
}

// ErrorData is error.data (A05 types.json errorData).
type ErrorData struct {
	Code      string         `json:"code"`
	Reason    string         `json:"reason,omitempty"`
	Detail    string         `json:"detail,omitempty"`
	Errors    []FieldError   `json:"errors,omitempty"`
	Supported []string       `json:"supported,omitempty"`
	Extra     map[string]any `json:"-"`
}

// FieldError is one schema violation of invalid params.
type FieldError struct {
	InstancePath string `json:"instance_path"`
	Message      string `json:"message"`
}

// Fail builds a JSON-RPC error with structured data. detail must never
// contain a secret.
func Fail(code int, reason, msg string) *jrpc2.Error {
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	d := ErrorData{Code: codeNames[code], Reason: reason, Detail: msg}
	b, _ := json.Marshal(d)
	return &jrpc2.Error{Code: jrpc2.Code(code), Message: msg, Data: b}
}

// FailData builds an error with explicit data.
func FailData(code int, msg string, d ErrorData) *jrpc2.Error {
	d.Code = codeNames[code]
	b, _ := json.Marshal(d)
	return &jrpc2.Error{Code: jrpc2.Code(code), Message: msg, Data: b}
}

// DataOf extracts ErrorData from an error returned by a client call.
func DataOf(err error) (int, ErrorData, bool) {
	var je *jrpc2.Error
	if !errors.As(err, &je) {
		return 0, ErrorData{}, false
	}
	var d ErrorData
	_ = json.Unmarshal(je.Data, &d)
	return int(je.Code), d, true
}
