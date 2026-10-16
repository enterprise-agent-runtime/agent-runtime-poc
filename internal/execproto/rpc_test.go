package execproto

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
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

// unsafeRunes are the characters Neutralise must replace, written as
// numbers so the test source itself carries none of them: C0 (CR, LF, ESC,
// NUL), DEL, C1 (NEL), the bidi embedding and override controls, the bidi
// isolates, the line and paragraph separators, and (re-review of 8bcce6f)
// the bidi marks and the zero-width characters, which hide or reorder text
// just as well.
var unsafeRunes = []rune{
	0x0d, 0x0a, 0x1b, 0x00, 0x7f, 0x85, // C0, DEL, C1
	0x202a, 0x202b, 0x202c, 0x202d, 0x202e, // bidi embedding and override
	0x2066, 0x2067, 0x2068, 0x2069, // bidi isolates
	0x2028, 0x2029, // line and paragraph separators
	0x200e, 0x200f, 0x061c, // LRM, RLM, ALM
	0x200b, 0x200c, 0x200d, 0xfeff, // ZWSP, ZWNJ, ZWJ, BOM / ZWNBSP
}

// TestNeutralise_CutsBeforeReplacing guards the re-review finding on
// 8bcce6f: the peer's text was neutralised whole and cut afterwards, so an
// 8 MiB message of control bytes became a 24 MiB string (each byte a
// 3-byte U+FFFD) only to be cut to 1 KiB. Cutting to the byte limit first
// bounds the work by the limit. Measured as heap bytes (TotalAlloc), which
// is deterministic under -race.
func TestNeutralise_CutsBeforeReplacing(t *testing.T) {
	msg := strings.Repeat("\x01", MaxFrameBytes)
	e := &RPCError{Code: 7, Message: msg}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	s := e.Error()
	runtime.ReadMemStats(&after)
	if d := after.TotalAlloc - before.TotalAlloc; d > 64<<10 {
		t.Errorf("Error() of an 8 MiB control-byte message allocated %d bytes, want the work bounded by MaxDetailBytes", d)
	}
	if len(s) > MaxDetailBytes+64 || !utf8.ValidString(s) {
		t.Errorf("Error() = %d bytes (valid UTF-8 %v), want at most MaxDetailBytes of text", len(s), utf8.ValidString(s))
	}
}

// TestRPCError_PeerTextNeutralised guards the review finding on bcfa1fc
// (CWE-117, CWE-150): text the executor sends (error.message, data.detail,
// data.rel_path) reached daemon error strings with control characters
// intact. Those strings go to logs, the terminal and events; a CR LF forges
// a log line, an escape sequence rewrites the terminal, and a bidi override
// makes a path read differently from what it is. At the boundary each of
// unsafeRunes becomes U+FFFD; rel_path is cut to the schema's 4096; and a
// standard or reserved JSON-RPC code gets fixed text, not the peer's.
func TestRPCError_PeerTextNeutralised(t *testing.T) {
	var nb strings.Builder
	nb.WriteString("a\r\nb\x1b[2J")
	for _, r := range unsafeRunes {
		nb.WriteRune(r)
		nb.WriteString("z")
	}
	nasty := nb.String()
	clean := func(name, s string) {
		t.Helper()
		for _, r := range unsafeRunes {
			if strings.ContainsRune(s, r) {
				t.Errorf("%s keeps U+%04X: %q", name, r, s)
			}
		}
	}

	data, err := json.Marshal(ErrorData{Code: "deny_list", Violation: true, Kind: ViolationDenyList, Detail: nasty, RelPath: nasty + strings.Repeat("x", 4090)})
	if err != nil {
		t.Fatal(err)
	}
	got := (&RPCError{Code: int(ErrDenyList), Message: nasty, Data: data}).Err()
	var e *Error
	if !errors.As(got, &e) {
		t.Fatalf("Err() = %v, want *Error", got)
	}
	clean("err.Error()", got.Error())
	clean("data.detail", e.Data.Detail)
	clean("data.rel_path", e.Data.RelPath)
	repl := string(utf8.RuneError)
	if want := "a" + repl + repl + "b" + repl + "[2J"; !strings.HasPrefix(e.Data.Detail, want) {
		t.Errorf("data.detail = %q, want the controls replaced by U+FFFD and the rest kept", e.Data.Detail)
	}
	if len(e.Data.RelPath) > MaxRelPathBytes {
		t.Errorf("data.rel_path is %d bytes, want at most %d", len(e.Data.RelPath), MaxRelPathBytes)
	}

	for _, code := range []int{RPCParseError, RPCInvalidRequest, RPCMethodNotFound, RPCInvalidParams, RPCInternalError, -32000, -32050, -32699} {
		s := (&RPCError{Code: code, Message: nasty}).Err().Error()
		clean(fmt.Sprintf("code %d", code), s)
		if strings.Contains(s, "2J") {
			t.Errorf("code %d: the peer's message reached the error text: %q", code, s)
		}
	}
	clean("code 7", (&RPCError{Code: 7, Message: nasty}).Err().Error())
}
