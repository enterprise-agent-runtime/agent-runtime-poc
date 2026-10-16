package wire

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"warden.dev/warden/internal/model"
)

// RetryAfter parses retry metadata in the order of design A11 §7.3:
// retry-after-ms, retry-after (seconds or HTTP date), the later of the
// anthropic-ratelimit-*-reset times (RFC 3339), the later of the
// x-ratelimit-reset-* durations ("1s", "6m0s", "20ms"). Zero means unknown.
func RetryAfter(h http.Header, now time.Time) time.Duration {
	if v := h.Get("retry-after-ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	if v := h.Get("retry-after"); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
			return time.Duration(s * float64(time.Second))
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := t.Sub(now); d > 0 {
				return d
			}
			return 0
		}
	}
	var best time.Duration
	for _, k := range []string{"anthropic-ratelimit-requests-reset", "anthropic-ratelimit-tokens-reset"} {
		if t, err := time.Parse(time.RFC3339, h.Get(k)); err == nil {
			if d := t.Sub(now); d > best {
				best = d
			}
		}
	}
	if best > 0 {
		return best
	}
	for _, k := range []string{"x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"} {
		if d, err := time.ParseDuration(strings.TrimSpace(h.Get(k))); err == nil && d > best {
			best = d
		}
	}
	return best
}

// Health tracks the last rate-limit headers and error of a provider.
type Health struct {
	mu sync.Mutex
	h  model.ProviderHealth
}

// NewHealth returns an empty tracker.
func NewHealth() *Health { return &Health{} }

// Snapshot returns the current state.
func (h *Health) Snapshot() model.ProviderHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.h
}

// Observe records rate-limit headers (Anthropic and OpenAI-style names).
func (h *Health) Observe(hdr http.Header, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range []string{"anthropic-ratelimit-requests-remaining", "x-ratelimit-remaining-requests"} {
		if n, err := strconv.Atoi(hdr.Get(k)); err == nil {
			h.h.RequestsRemaining = n
		}
	}
	for _, k := range []string{"anthropic-ratelimit-tokens-remaining", "x-ratelimit-remaining-tokens"} {
		if n, err := strconv.Atoi(hdr.Get(k)); err == nil {
			h.h.TokensRemaining = n
		}
	}
	if d := RetryAfter(hdr, now); d > 0 {
		h.h.ResetAt = now.Add(d)
	}
}

// Fail records an error code.
func (h *Health) Fail(code model.ErrorCode, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.h.LastError, h.h.LastErrAt = code, now
}
