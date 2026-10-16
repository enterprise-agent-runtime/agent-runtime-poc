package wire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"warden.dev/warden/internal/model"
)

func readAll(t *testing.T, body string) ([]Event, error) {
	t.Helper()
	r := NewSSEReader(strings.NewReader(body), nil)
	var out []Event
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}

func TestSSEReader_LineEndingsCommentsAndFields(t *testing.T) {
	body := ": ping\n" +
		"event: message_start\ndata: {\"a\":1}\n\n" +
		"data:{\"b\":2}\r\n\r\n" + // no space after colon, CRLF
		"data: line1\rdata: line2\r\r" + // bare CR, multi-line data
		"id: 7\nretry: 100\n\n" + // no data: not dispatched
		"data: [DONE]" // unterminated final event
	evs, err := readAll(t, body)
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{{"message_start", `{"a":1}`}, {"", `{"b":2}`}, {"", "line1\nline2"}, {"", "[DONE]"}}
	if len(evs) != len(want) {
		t.Fatalf("events = %#v", evs)
	}
	for i := range want {
		if evs[i] != want[i] {
			t.Errorf("event %d = %#v, want %#v", i, evs[i], want[i])
		}
	}
}

func TestSSEReader_OnLineCountsComments(t *testing.T) {
	n := 0
	r := NewSSEReader(strings.NewReader(": keep\n: alive\ndata: x\n\n"), func() { n++ })
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("OnLine called %d times, want 4 (comments reset the idle timer)", n)
	}
}

func TestSSEReader_LineTooLong(t *testing.T) {
	r := NewSSEReader(strings.NewReader("data: "+strings.Repeat("x", 100)+"\n\n"), nil)
	r.max = 50
	if _, err := r.Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
}

func TestRetryAfter_Order(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		h    map[string]string
		want time.Duration
	}{
		{"ms wins", map[string]string{"retry-after-ms": "1500", "retry-after": "9"}, 1500 * time.Millisecond},
		{"seconds", map[string]string{"retry-after": "3"}, 3 * time.Second},
		{"http date", map[string]string{"retry-after": now.Add(10 * time.Second).Format(http.TimeFormat)}, 10 * time.Second},
		{"anthropic later reset", map[string]string{
			"anthropic-ratelimit-requests-reset": now.Add(5 * time.Second).Format(time.RFC3339),
			"anthropic-ratelimit-tokens-reset":   now.Add(20 * time.Second).Format(time.RFC3339)}, 20 * time.Second},
		{"openai durations", map[string]string{"x-ratelimit-reset-requests": "20ms", "x-ratelimit-reset-tokens": "6m0s"}, 6 * time.Minute},
		{"none", map[string]string{}, 0},
	}
	for _, c := range cases {
		h := http.Header{}
		for k, v := range c.h {
			h.Set(k, v)
		}
		if got := RetryAfter(h, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHealth_ObserveAndFail(t *testing.T) {
	h := NewHealth()
	now := time.Now()
	hdr := http.Header{}
	hdr.Set("x-ratelimit-remaining-requests", "7")
	hdr.Set("anthropic-ratelimit-tokens-remaining", "900")
	hdr.Set("retry-after", "2")
	h.Observe(hdr, now)
	h.Fail(model.ErrRateLimited, now)
	s := h.Snapshot()
	if s.RequestsRemaining != 7 || s.TokensRemaining != 900 || !s.ResetAt.Equal(now.Add(2*time.Second)) || s.LastError != model.ErrRateLimited {
		t.Fatalf("snapshot = %+v", s)
	}
}

// fakeCreds is a CredentialSource returning a fixed credential.
type fakeCreds struct {
	header, scheme, value string
	fail                  bool
	calls                 atomic.Int32
	lastConsumer          string
}

func (f *fakeCreds) Credential(_ context.Context, ref, consumer string) (*model.Credential, error) {
	f.calls.Add(1)
	f.lastConsumer = consumer
	if f.fail {
		return nil, errors.New("backend-internal-detail-7f3a")
	}
	return &model.Credential{Header: f.header, Scheme: f.scheme, Value: []byte(f.value)}, nil
}

func TestAuthTransport_InjectsOnCloneOnly(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer srv.Close()
	creds := &fakeCreds{header: "Authorization", scheme: "Bearer", value: "tok-123"}
	tr := &AuthTransport{Base: http.DefaultTransport, ProviderID: "company-vllm", Ref: "secret://providers/company-vllm/token", Creds: creds}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.Get("Authorization") != "Bearer tok-123" {
		t.Fatalf("server saw %q", got.Get("Authorization"))
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("credential leaked into the caller's request object")
	}
	if creds.lastConsumer != "adapter:company-vllm" {
		t.Fatalf("consumer = %q", creds.lastConsumer)
	}
}

func TestAuthTransport_CredentialFailureIsAuthFailed(t *testing.T) {
	tr := &AuthTransport{Base: http.DefaultTransport, ProviderID: "p", Ref: "secret://providers/p/api_key", Creds: &fakeCreds{fail: true}}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:1", nil)
	_, err := tr.RoundTrip(req)
	if !errors.Is(err, model.ErrAuthFailed) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "backend-internal-detail-7f3a") {
		t.Fatal("underlying keychain error text leaked into the model error")
	}
}

func TestNewHTTPClient_URLRules(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"http://127.0.0.1:11434/v1", true},
		{"http://localhost:1234/v1", true},
		{"http://[::1]:8000/v1", true},
		{"https://llm.example/v1", true},
		{"http://llm.example/v1", false}, // OQ-22: no plain http beyond loopback
		{"http://10.0.0.5:8000/v1", false},
		{"https://user:pass@llm.example/v1", false},
		{"ftp://llm.example", false},
		{"::bad", false},
	}
	for _, c := range cases {
		_, err := NewHTTPClient(model.ProviderConfig{ID: "p", BaseURL: c.url, Auth: model.AuthConfig{Mode: model.AuthNone}}, nil)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.url, err, c.ok)
		}
	}
}

func TestNewHTTPClient_DoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/steal", http.StatusFound)
	}))
	defer srv.Close()
	creds := &fakeCreds{header: "x-api-key", value: "k"}
	c, err := NewHTTPClient(model.ProviderConfig{ID: "p", BaseURL: srv.URL, Auth: model.AuthConfig{Mode: model.AuthAPIKey, Secret: "secret://providers/p/api_key"}}, creds)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d; redirect was followed", resp.StatusCode)
	}
}

func TestNewHTTPClient_ModeNoneIgnoresSecret(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { auth = r.Header.Get("Authorization") }))
	defer srv.Close()
	creds := &fakeCreds{header: "Authorization", scheme: "Bearer", value: "x"}
	c, _ := NewHTTPClient(model.ProviderConfig{ID: "p", BaseURL: srv.URL, Auth: model.AuthConfig{Mode: model.AuthNone, Secret: "secret://providers/p/api_key"}}, creds)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if auth != "" || creds.calls.Load() != 0 {
		t.Fatalf("mode none sent %q after %d credential reads", auth, creds.calls.Load())
	}
}

func TestTransportError_Classification(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if e := TransportError(errors.New("x"), cancelled, nil); e.Code != model.ErrCancelled || e.Retryable {
		t.Errorf("cancelled: %+v", e)
	}
	_, wd := StartWatchdog(context.Background(), time.Nanosecond, time.Second)
	time.Sleep(20 * time.Millisecond)
	if e := TransportError(context.Canceled, context.Background(), wd); e.Code != model.ErrTimeout || !e.Retryable {
		t.Errorf("watchdog: %+v", e)
	}
	if e := TransportError(errors.New("dial tcp: connection refused"), context.Background(), nil); e.Code != model.ErrProviderUnavailable || !e.Retryable {
		t.Errorf("refused: %+v", e)
	}
	if e := TransportError(errors.New("remote error: tls: bad certificate"), context.Background(), nil); e.Code != model.ErrAuthFailed {
		t.Errorf("client cert rejected: %+v", e)
	}
	// An untrusted server certificate (httptest's self-signed CA) is
	// provider_unavailable and not retryable (A11 §8 row 3).
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	c, err := NewHTTPClient(model.ProviderConfig{ID: "p", BaseURL: srv.URL, Auth: model.AuthConfig{Mode: model.AuthNone}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(srv.URL)
	if e := TransportError(err, context.Background(), nil); e.Code != model.ErrProviderUnavailable || e.Retryable || !strings.Contains(string(e.Details), "server_certificate_untrusted") {
		t.Errorf("untrusted cert: %+v %s", e, e.Details)
	}
}

func TestWatchdog_TouchKeepsAlive(t *testing.T) {
	ctx, wd := StartWatchdog(context.Background(), 50*time.Millisecond, 50*time.Millisecond)
	defer wd.Stop()
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		wd.Touch()
	}
	if ctx.Err() != nil || wd.Fired() {
		t.Fatal("watchdog fired although touched")
	}
	time.Sleep(120 * time.Millisecond)
	if ctx.Err() == nil || !wd.Fired() {
		t.Fatal("watchdog did not fire after idle")
	}
}

func TestTimeouts_Defaults(t *testing.T) {
	c, f, i := Timeouts("T0", 0, 0, 0)
	if c != 10*time.Second || f != 120*time.Second || i != 60*time.Second {
		t.Errorf("T0 = %v %v %v", c, f, i)
	}
	_, f, _ = Timeouts("T3", 0, 0, 0)
	if f != 60*time.Second {
		t.Errorf("T3 first byte = %v", f)
	}
	c, f, i = Timeouts("T3", 1, 2, 3)
	if c != time.Millisecond || f != 2*time.Millisecond || i != 3*time.Millisecond {
		t.Errorf("override = %v %v %v", c, f, i)
	}
}

func TestDetailsJSON_TruncatesBody(t *testing.T) {
	d := DetailsJSON(Details{Status: 500, Body: strings.Repeat("a", MaxDetailBody+100)})
	if len(d) > MaxDetailBody+100 {
		t.Fatalf("details len %d", len(d))
	}
}
