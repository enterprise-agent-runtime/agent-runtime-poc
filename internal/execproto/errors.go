package execproto

import (
	"strconv"
	"unicode/utf8"
)

// Code is a numeric executor error code, -32101 … -32120 (A06 §4.4). A Code
// is itself an error, so the constants are the sentinel values:
// errors.Is(err, execproto.ErrDenyList) holds for any *Error carrying that code.
// Being constants, they are not global mutable state (CLAUDE.md §7).
type Code int

// The executor error codes of A06 §4.4, in table order.
const (
	ErrPathEscape      Code = -32101
	ErrOutsideRoots    Code = -32102
	ErrDenyList        Code = -32103
	ErrNotFound        Code = -32104
	ErrNotRegular      Code = -32105
	ErrNotAllowed      Code = -32106
	ErrShellString     Code = -32107
	ErrProfileMismatch Code = -32108
	ErrTooLarge        Code = -32109
	ErrTimeout         Code = -32110
	ErrInvalidArgument Code = -32111
	ErrConflict        Code = -32112
	ErrPatchFailed     Code = -32113
	ErrRedactionMarker Code = -32114
	ErrModeForbidden   Code = -32115
	ErrBranchMismatch  Code = -32116
	ErrBusy            Code = -32117
	ErrIOError         Code = -32118
	ErrInvalidState    Code = -32119
	ErrShuttingDown    Code = -32120
)

// First and last code of the executor range.
const (
	CodeFirst = ErrPathEscape
	CodeLast  = ErrShuttingDown
)

// Codes returns every executor code in table order (a fresh slice).
func Codes() []Code {
	out := make([]Code, 0, CodeFirst-CodeLast+1)
	for c := CodeFirst; c >= CodeLast; c-- {
		out = append(out, c)
	}
	return out
}

// Name is the error.data.code string of A06 §4.4, "" for a code outside the
// executor range. These names are wire values: never rename one.
func (c Code) Name() string {
	switch c {
	case ErrPathEscape:
		return "path_escape"
	case ErrOutsideRoots:
		return "outside_roots"
	case ErrDenyList:
		return "deny_list"
	case ErrNotFound:
		return "not_found"
	case ErrNotRegular:
		return "not_regular"
	case ErrNotAllowed:
		return "not_allowed"
	case ErrShellString:
		return "shell_string"
	case ErrProfileMismatch:
		return "profile_mismatch"
	case ErrTooLarge:
		return "too_large"
	case ErrTimeout:
		return "timeout"
	case ErrInvalidArgument:
		return "invalid_argument"
	case ErrConflict:
		return "conflict"
	case ErrPatchFailed:
		return "patch_failed"
	case ErrRedactionMarker:
		return "redaction_marker"
	case ErrModeForbidden:
		return "mode_forbidden"
	case ErrBranchMismatch:
		return "branch_mismatch"
	case ErrBusy:
		return "busy"
	case ErrIOError:
		return "io_error"
	case ErrInvalidState:
		return "invalid_state"
	case ErrShuttingDown:
		return "shutting_down"
	}
	return ""
}

// Violation is the sandbox.violation kind the daemon emits for this code,
// "" when the code is not a violation (A06 §4.4 last column).
func (c Code) Violation() ViolationKind {
	switch c {
	case ErrPathEscape:
		return ViolationPathEscape
	case ErrDenyList:
		return ViolationDenyList
	case ErrOutsideRoots, ErrNotAllowed, ErrShellString, ErrProfileMismatch, ErrModeForbidden, ErrBranchMismatch:
		return ViolationRootCheck
	}
	return ""
}

// Error makes a Code usable as a sentinel error.
func (c Code) Error() string {
	if n := c.Name(); n != "" {
		return "exec: " + n
	}
	return "exec: code " + strconv.Itoa(int(c))
}

// CodeByName maps an error.data.code string back to its Code.
func CodeByName(name string) (Code, bool) {
	for _, c := range Codes() {
		if c.Name() == name {
			return c, true
		}
	}
	return 0, false
}

// Error is an executor error as it travels in a JSON-RPC error object:
// error.code is Code, error.data is Data.
type Error struct {
	Code Code
	Data ErrorData
}

// MaxDetailBytes is the maxLength of error.data.detail (common.json).
const MaxDetailBytes = 1024

// NewError builds an Error whose data carries the code's name and violation
// classification. A code outside the executor range becomes ErrIOError, so
// data.code is always a name A06 defines. detail is cut to MaxDetailBytes on
// a UTF-8 boundary.
//
// detail goes to the daemon, into events and into the model's tool result:
// it must be a fixed, sanitized description, and must never contain argv
// elements, file content, environment values or OS error text that quotes
// them (A06 §4.4 io_error: "message sanitized"; INV-C).
func NewError(code Code, detail string) *Error {
	if code.Name() == "" {
		code = ErrIOError
	}
	if len(detail) > MaxDetailBytes {
		cut := MaxDetailBytes
		for cut > 0 && !utf8.RuneStart(detail[cut]) {
			cut--
		}
		detail = detail[:cut]
	}
	k := code.Violation()
	return &Error{Code: code, Data: ErrorData{Code: code.Name(), Violation: k != "", Kind: k, Detail: detail}}
}

// Error implements error.
func (e *Error) Error() string {
	if e.Data.Detail == "" {
		return e.Code.Error()
	}
	return e.Code.Error() + ": " + e.Data.Detail
}

// Is reports whether target is this error's Code.
func (e *Error) Is(target error) bool {
	c, ok := target.(Code)
	return ok && c == e.Code
}
