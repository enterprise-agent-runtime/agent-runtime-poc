package execproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestDecodeMessage_Kinds: every frame is classified as exactly one of
// request, notification or response, and anything that is none of them,
// or more than one, is refused before a handler or a pending call sees it.
func TestDecodeMessage_Kinds(t *testing.T) {
	cases := []struct {
		name, in string
		want     MessageKind
		bad      bool
	}{
		{"request", `{"jsonrpc":"2.0","id":1,"method":"exec.hello","params":{}}`, KindRequest, false},
		{"request with string id", `{"jsonrpc":"2.0","id":"a","method":"exec.hello"}`, KindRequest, false},
		{"notification", `{"jsonrpc":"2.0","method":"exec.proc.io","params":{}}`, KindNotification, false},
		{"result", `{"jsonrpc":"2.0","id":1,"result":{}}`, KindResponse, false},
		{"null result", `{"jsonrpc":"2.0","id":1,"result":null}`, KindResponse, false},
		{"error", `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"x"}}`, KindResponse, false},
		{"error with null id", `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"x"}}`, KindResponse, false},
		{"wrong version", `{"jsonrpc":"1.0","id":1,"method":"m"}`, 0, true},
		{"no version", `{"id":1,"method":"m"}`, 0, true},
		{"result and error", `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"x"}}`, 0, true},
		{"neither result nor error", `{"jsonrpc":"2.0","id":1}`, 0, true},
		{"method and result", `{"jsonrpc":"2.0","id":1,"method":"m","result":{}}`, 0, true},
		{"response without id", `{"jsonrpc":"2.0","result":{}}`, 0, true},
		{"object id", `{"jsonrpc":"2.0","id":{},"method":"m"}`, 0, true},
		{"null request id", `{"jsonrpc":"2.0","id":null,"method":"m"}`, 0, true},
		{"case-variant ID", `{"jsonrpc":"2.0","ID":1,"method":"m"}`, 0, true},
		{"duplicate method", `{"jsonrpc":"2.0","id":1,"method":"exec.hello","method":"exec.fs.write"}`, 0, true},
		{"unknown member", `{"jsonrpc":"2.0","id":1,"method":"m","extra":1}`, 0, true},
		{"not an object", `[1]`, 0, true},
		{"not JSON", `{`, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := DecodeMessage([]byte(c.in))
			if c.bad {
				if err == nil {
					t.Fatalf("DecodeMessage(%s) = %+v, want an error", c.in, m)
				}
				return
			}
			if err != nil || m.Kind() != c.want {
				t.Fatalf("DecodeMessage(%s) = kind %v, %v; want %v", c.in, m.Kind(), err, c.want)
			}
		})
	}
}

// TestRPCError_ExecRoundTrip: an executor *Error survives the trip through
// a JSON-RPC error object, so errors.Is(err, ErrInvalidState) holds on the
// daemon side for what the executor sent.
func TestRPCError_ExecRoundTrip(t *testing.T) {
	sent := NewError(ErrDenyList, "matched **/.env")
	b, err := json.Marshal(RPCErrorOf(sent))
	if err != nil {
		t.Fatal(err)
	}
	var back RPCError
	if err := Decode(b, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Err()
	if !errors.Is(got, ErrDenyList) {
		t.Fatalf("Err() = %v, want ErrDenyList", got)
	}
	var e *Error
	if !errors.As(got, &e) || e.Data != sent.Data {
		t.Errorf("Err() data = %+v, want %+v", e, sent.Data)
	}
}

// TestRPCError_ExecCodeIsAuthoritative: the error code decides the error,
// and its violation classification comes from the A06 §4.4 table, not from
// whatever data the peer sent with it. An executor that sent deny_list with
// violation false must not be able to suppress a sandbox.violation event.
func TestRPCError_ExecCodeIsAuthoritative(t *testing.T) {
	for _, data := range []string{``, `"x"`, `{"code":"busy","violation":false}`, `{"code":"deny_list","violation":false}`} {
		re := RPCError{Code: int(ErrDenyList), Message: "m"}
		if data != "" {
			re.Data = json.RawMessage(data)
		}
		var e *Error
		if !errors.As(re.Err(), &e) || e.Code != ErrDenyList || !e.Data.Violation || e.Data.Kind != ViolationDenyList {
			t.Errorf("data %s: Err() = %+v, want deny_list with violation", data, e)
		}
	}
}

// TestRPCError_StandardCodes: codes outside the executor range stay
// RPCErrors, and their text is bounded.
func TestRPCError_StandardCodes(t *testing.T) {
	re := RPCError{Code: RPCMethodNotFound, Message: strings.Repeat("m", 10000)}
	err := re.Err()
	var got *RPCError
	if !errors.As(err, &got) || got.Code != RPCMethodNotFound {
		t.Fatalf("Err() = %v, want *RPCError -32601", err)
	}
	if len(err.Error()) > MaxDetailBytes+64 {
		t.Errorf("error text is %d bytes, want it bounded", len(err.Error()))
	}
}
