package execproto

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// The JSON-RPC 2.0 envelope of warden.exec/1 (A06 §4.1). Both sides decode
// every frame with DecodeMessage, which inherits Decode's strictness: an
// envelope with "ID" next to "id", or two "method" members, would otherwise
// mean one call to the daemon and another to the executor.

// JSONRPCVersion is the jsonrpc member of every message.
const JSONRPCVersion = "2.0"

// Standard JSON-RPC error codes, used where no executor code (A06 §4.4)
// applies: the message is not JSON-RPC at all, or names no method.
const (
	RPCParseError     = -32700
	RPCInvalidRequest = -32600
	RPCMethodNotFound = -32601
	RPCInvalidParams  = -32602
	RPCInternalError  = -32603
)

// Message is one JSON-RPC 2.0 message. Absent members are nil; a JSON null
// result or id is the four bytes "null".
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// MessageKind classifies a Message.
type MessageKind int

// Message kinds.
const (
	KindInvalid MessageKind = iota
	KindRequest
	KindNotification
	KindResponse
)

// Kind classifies m by its members, as DecodeMessage validated them.
func (m Message) Kind() MessageKind {
	switch {
	case m.Method != "" && m.ID != nil:
		return KindRequest
	case m.Method != "":
		return KindNotification
	case m.ID != nil:
		return KindResponse
	}
	return KindInvalid
}

// DecodeMessage decodes one frame body strictly (Decode) and checks that it
// is exactly one of: a request (method and a number or string id), a
// notification (method, no id) or a response (id and exactly one of result
// and error, no method).
func DecodeMessage(b []byte) (Message, error) {
	var m Message
	if err := Decode(b, &m); err != nil {
		return Message{}, err
	}
	if m.JSONRPC != JSONRPCVersion {
		return Message{}, errors.New("execproto: message: jsonrpc must be \"2.0\"")
	}
	switch m.Kind() {
	case KindRequest, KindNotification:
		if m.Result != nil || m.Error != nil {
			return Message{}, errors.New("execproto: message: a request carries no result or error")
		}
		if m.ID != nil && !scalarID(m.ID) {
			return Message{}, errors.New("execproto: message: request id must be a number or a string")
		}
	case KindResponse:
		if (m.Result == nil) == (m.Error == nil) {
			return Message{}, errors.New("execproto: message: a response carries exactly one of result and error")
		}
		if m.Params != nil {
			return Message{}, errors.New("execproto: message: a response carries no params")
		}
	default:
		return Message{}, errors.New("execproto: message: neither a request nor a response")
	}
	return m, nil
}

// scalarID reports a JSON number or string (already known to be valid JSON).
func scalarID(id json.RawMessage) bool {
	c := id[0]
	return c == '"' || c == '-' || (c >= '0' && c <= '9')
}

// RPCError is a JSON-RPC error object. For executor errors Code is an
// executor Code and Data its ErrorData (A06 §4.4).
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// RPCErrorOf turns an executor error into its JSON-RPC error object. The
// message is the fixed code name; the sanitized detail travels in data.
func RPCErrorOf(e *Error) *RPCError {
	data, _ := json.Marshal(e.Data) // ErrorData has only strings and bools
	return &RPCError{Code: int(e.Code), Message: e.Code.Name(), Data: data}
}

// Error implements error. The peer's message is cut to MaxDetailBytes.
func (e *RPCError) Error() string {
	msg := e.Message
	if len(msg) > MaxDetailBytes {
		cut := MaxDetailBytes
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut]
	}
	return fmt.Sprintf("execproto: rpc error %d: %s", e.Code, msg)
}

// Err returns the error a caller should see: an executor *Error for a code
// in the executor range (so errors.Is(err, ErrDenyList) works), the
// RPCError itself otherwise. For an executor code the code is
// authoritative: data is kept only when it decodes strictly and names the
// same code, and the violation classification is always the A06 §4.4
// table's, so a peer cannot mark a deny_list error as no violation.
func (e *RPCError) Err() error {
	c := Code(e.Code)
	if c.Name() == "" {
		return e
	}
	out := NewError(c, "")
	var d ErrorData
	if e.Data != nil && Decode(e.Data, &d) == nil && d.Code == c.Name() {
		out = NewError(c, d.Detail)
		out.Data.RelPath = d.RelPath
	}
	return out
}
