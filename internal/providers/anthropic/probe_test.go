package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/providertest"
)

// sse renders canonical-looking Anthropic events for the probe fake.
func sse(events ...string) string { return strings.Join(events, "") + "" }

func ev(typ, data string) string { return "event: " + typ + "\ndata: " + data + "\n\n" }

const start = `{"type":"message_start","message":{"id":"m","model":"claude","usage":{"input_tokens":10,"output_tokens":1}}}`

func textReply(s string) string {
	b, _ := json.Marshal(s)
	return sse(ev("message_start", start),
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`),
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(b)+`}}`),
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`),
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`),
		ev("message_stop", `{"type":"message_stop"}`))
}

func toolReply(name, input string) string {
	b, _ := json.Marshal(input)
	return sse(ev("message_start", start),
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_p","name":"`+name+`"}}`),
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(b)+`}}`),
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`),
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`),
		ev("message_stop", `{"type":"message_stop"}`))
}

// probeServer behaves like the Messages API for the probe's requests.
func probeServer(t *testing.T, listStatus int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(listStatus)
			if listStatus == 200 {
				_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-x"},{"id":"claude-haiku-x"}]}`)
			} else if listStatus == 401 {
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`)
			} else {
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"no listing"}}`)
			}
			return
		}
		var req wireRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("content-type", "text/event-stream")
		switch {
		case len(req.Tools) == 1 && req.Tools[0].Name == jsonOutputTool:
			_, _ = io.WriteString(w, toolReply(jsonOutputTool, `{"answer":5,"unit":"apples"}`))
		case len(req.Tools) == 1 && req.Tools[0].Name == "probe_add":
			_, _ = io.WriteString(w, toolReply("probe_add", `{"a":2,"b":3}`))
		default:
			_, _ = io.WriteString(w, textReply("ok"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbe_ReportsCapabilitiesAndCaches(t *testing.T) {
	srv := probeServer(t, 200)
	cache := &providertest.FakeProbeCache{}
	p, err := New(catalog(srv.URL), creds(), cache)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Error != nil || len(res.ModelsListed) != 2 || len(res.Models) != 1 {
		t.Fatalf("result = %+v", res)
	}
	m := res.Models[0]
	if m.ToolCalling != model.ToolCallingNative || !m.StructuredOutput || m.StructuredMode != "tool_forcing" || !m.Streaming || m.MaxContext != 200000 || m.Quirks["tool_choice_named"] != true {
		t.Fatalf("model probe = %+v", m)
	}
	if m.MaxContextEffective != nil {
		t.Fatal("P4t ran against a T3 provider")
	}
	if cached, _ := cache.Load("anthropic"); cached == nil || !strings.HasPrefix(cached.EndpointHash, "sha256:") {
		t.Fatalf("cache = %+v", cached)
	}
}

func TestProbe_AuthFailureStopsAtListing(t *testing.T) {
	srv := probeServer(t, 401)
	p, _ := New(catalog(srv.URL), creds(), nil)
	res, err := p.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Error == nil || res.Error.Code != model.ErrAuthFailed || len(res.Models) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestProbe_ListingNotFoundContinues(t *testing.T) {
	srv := probeServer(t, 404)
	p, _ := New(catalog(srv.URL), creds(), nil)
	res, _ := p.Probe(context.Background())
	if !res.OK || len(res.Models) != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestEndpointHash_ChangesWithEndpoint(t *testing.T) {
	a, b := catalog("https://a"), catalog("https://b")
	if EndpointHash(a) == EndpointHash(b) || EndpointHash(a) != EndpointHash(catalog("https://a")) {
		t.Fatal("endpoint hash not a function of the endpoint")
	}
}

func TestHealth_RecordsRateLimit(t *testing.T) {
	f := newFake(t, 429, "application/json", `{"type":"error","error":{"type":"rate_limit_error","message":"x"}}`)
	f.headers = map[string]string{"anthropic-ratelimit-requests-remaining": "0", "retry-after": "3"}
	p := adapter(t, f.srv.URL)
	_, _ = p.Generate(context.Background(), simpleRequest())
	h := p.Health()
	if h.LastError != model.ErrRateLimited || h.RequestsRemaining != 0 || h.ResetAt.IsZero() {
		t.Fatalf("health = %+v", h)
	}
	if p.ID() != "anthropic" {
		t.Fatal("id")
	}
}
