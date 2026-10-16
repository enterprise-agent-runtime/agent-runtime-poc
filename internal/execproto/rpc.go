package execproto

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// MaxRelPathBytes is the maxLength of RelPath (common.json).
const MaxRelPathBytes = 4096

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

// Error implements error. A standard or reserved JSON-RPC code gets fixed
// text; for any other code the peer's message is neutralised and cut to
// MaxDetailBytes.
func (e *RPCError) Error() string {
	msg := rpcText(e.Code)
	if msg == "" {
		msg = neutraliseCut(e.Message, MaxDetailBytes)
	}
	return fmt.Sprintf("execproto: rpc error %d: %s", e.Code, msg)
}

// rpcText is the fixed text of a JSON-RPC code in the range the
// specification reserves (-32768 … -32000), "" outside it.
func rpcText(code int) string {
	switch code {
	case RPCParseError:
		return "parse error"
	case RPCInvalidRequest:
		return "invalid request"
	case RPCMethodNotFound:
		return "method not found"
	case RPCInvalidParams:
		return "invalid params"
	case RPCInternalError:
		return "internal error"
	}
	if n := Code(code).Name(); n != "" {
		return n
	}
	switch {
	case code >= -32099 && code <= -32000:
		return "server error"
	case code >= -32768 && code <= -32000:
		return "reserved error code"
	}
	return ""
}

// Neutralise replaces every character that can forge a log line, drive a
// terminal or reorder displayed text (CWE-117, CWE-150) with U+FFFD: C0
// controls, DEL and C1 controls, the bidi embedding, override and isolate
// controls (U+202A–U+202E, U+2066–U+2069), the bidi marks (U+200E, U+200F,
// U+061C), the zero-width characters (U+200B–U+200D, U+FEFF), the line and
// paragraph separators (U+2028, U+2029), and invalid UTF-8. It is applied at the
// boundary to text the executor sends; internal/termsafe cannot be used
// here (execproto is a stdlib-only leaf, A02 §5 R6).
func Neutralise(s string) string {
	clean := true
	for _, r := range s {
		if unsafeRune(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unsafeRune(r) {
			r = utf8.RuneError
		}
		b.WriteRune(r)
	}
	return b.String()
}

func unsafeRune(r rune) bool {
	switch {
	case r < 0x20, r >= 0x7f && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x2028, r == 0x2029:
		return true
	case r >= 0x200b && r <= 0x200f, r == 0x061c, r == 0xfeff:
		return true // zero-width space, joiners, LRM, RLM, ALM, BOM
	}
	return r == utf8.RuneError // invalid UTF-8 decodes to it; writing it back is the same character
}

// neutraliseCut bounds peer text to n bytes. It cuts first, so the work and
// the allocation are bounded by n whatever the input (an 8 MiB detail of
// control bytes would otherwise become 24 MiB of U+FFFD), then neutralises,
// which can grow the text up to threefold, then cuts again on a rune
// boundary.
func neutraliseCut(s string, n int) string {
	return cutUTF8(Neutralise(cutUTF8(s, n)), n)
}

// cutUTF8 cuts s to at most n bytes on a rune boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Err returns the error a caller should see: an executor *Error for a code
// in the executor range (so errors.Is(err, ErrDenyList) works), the
// RPCError itself otherwise. For an executor code the code is
// authoritative: data is kept only when it decodes strictly and names the
// same code, and the violation classification is always the A06 §4.4
// table's, so a peer cannot mark a deny_list error as no violation. The
// peer's detail and rel_path are neutralised (Neutralise) and cut to their
// schema lengths; its message is not used.
func (e *RPCError) Err() error {
	c := Code(e.Code)
	if c.Name() == "" {
		return e
	}
	out := NewError(c, "")
	var d ErrorData
	if e.Data != nil && Decode(e.Data, &d) == nil && d.Code == c.Name() {
		out = NewError(c, neutraliseCut(d.Detail, MaxDetailBytes))
		out.Data.RelPath = neutraliseCut(d.RelPath, MaxRelPathBytes)
	}
	return out
}
