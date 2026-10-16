package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// ErrorCode is a normalized model error code (WRD-05 §4). It implements
// error so that callers can write errors.Is(err, model.ErrRateLimited) on any
// error that wraps a *model.Error (CLAUDE.md §7).
type ErrorCode string

// The ten normalized codes of WRD-05 §4; no others exist.
const (
	ErrRateLimited           ErrorCode = "rate_limited"
	ErrAuthFailed            ErrorCode = "auth_failed"
	ErrContextTooLong        ErrorCode = "context_too_long"
	ErrProviderUnavailable   ErrorCode = "provider_unavailable"
	ErrContentFiltered       ErrorCode = "content_filtered"
	ErrInvalidRequest        ErrorCode = "invalid_request"
	ErrToolFormatUnsupported ErrorCode = "tool_format_unsupported"
	ErrModelNotFound         ErrorCode = "model_not_found"
	ErrTimeout               ErrorCode = "timeout"
	ErrCancelled             ErrorCode = "cancelled"
)

// ErrorCodes lists every normalized code in WRD-05 §4 order.
var ErrorCodes = []ErrorCode{
	ErrRateLimited, ErrAuthFailed, ErrContextTooLong, ErrProviderUnavailable, ErrContentFiltered,
	ErrInvalidRequest, ErrToolFormatUnsupported, ErrModelNotFound, ErrTimeout, ErrCancelled,
}

func (c ErrorCode) Error() string { return string(c) }

// Valid reports whether c is one of the ten codes.
func (c ErrorCode) Valid() bool {
	for _, k := range ErrorCodes {
		if c == k {
			return true
		}
	}
	return false
}

// DefaultRetryable is the retry class of a code when the adapter has no more
// specific knowledge (design A11 §8: rate limits, unavailability and
// timeouts are retryable; everything else is not).
func (c ErrorCode) DefaultRetryable() bool {
	switch c {
	case ErrRateLimited, ErrProviderUnavailable, ErrTimeout:
		return true
	}
	return false
}

// Fallback reports whether the router may fall back to another model on
// this code (WRD-06 §7, design A11 §8 "Fallback class").
func (c ErrorCode) Fallback() bool {
	switch c {
	case ErrRateLimited, ErrProviderUnavailable, ErrTimeout, ErrModelNotFound:
		return true
	}
	return false
}

// Error is a normalized model error. Message is a short fixed sentence that
// never contains a secret; Details carries diagnostics (status, provider
// error type, request id, redacted body ≤ 8 KiB) for events only, never for
// model context (WRD-05 §4).
type Error struct {
	Code       ErrorCode       `json:"code"`
	Message    string          `json:"message"`
	Retryable  bool            `json:"retryable"`
	RetryAfter time.Duration   `json:"-"`
	Details    json.RawMessage `json:"details,omitempty"`
}

// NewError returns an error with the code's default retry class.
func NewError(code ErrorCode, msg string) *Error {
	return &Error{Code: code, Message: msg, Retryable: code.DefaultRetryable()}
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "model: " + string(e.Code)
	}
	return fmt.Sprintf("model: %s: %s", e.Code, e.Message)
}

// Is makes errors.Is(err, model.ErrX) true for an *Error with code ErrX.
func (e *Error) Is(target error) bool {
	c, ok := target.(ErrorCode)
	return ok && c == e.Code
}

// MarshalJSON adds retry_after_ms (the wire name, WRD-05 §4).
func (e *Error) MarshalJSON() ([]byte, error) {
	type plain Error
	return json.Marshal(struct {
		*plain
		RetryAfterMS int64 `json:"retry_after_ms,omitempty"`
	}{(*plain)(e), e.RetryAfter.Milliseconds()})
}

// UnmarshalJSON reads retry_after_ms back.
func (e *Error) UnmarshalJSON(b []byte) error {
	type plain Error
	v := struct {
		*plain
		RetryAfterMS int64 `json:"retry_after_ms"`
	}{plain: (*plain)(e)}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	e.RetryAfter = time.Duration(v.RetryAfterMS) * time.Millisecond
	return nil
}
