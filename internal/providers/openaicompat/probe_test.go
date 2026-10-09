package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/providertest"
)

// fakeLocal behaves like an OpenAI-compatible local server for the probe.
type fakeLocal struct {
	tools        bool   // supports native tools
	streamOpts   bool   // accepts stream_options
	listStatus   int    // /v1/models status
	vllmLen      int    // max_model_len in the listing
	numCtx       int    // Ollama /api/show parameters num_ctx
	lmLoaded     int    // LM Studio loaded_context_length
	truncateTo   int    // reported prompt tokens for long prompts
	jsonSchemaOK bool   // accepts response_format json_schema
	modelName    string // wire name
}

func chunkSSE(content string, tool *[2]string, usage string) string {
	var b strings.Builder
	if content != "" {
		c, _ := json.Marshal(content)
		fmt.Fprintf(&b, "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%s},\"finish_reason\":null}]}\n\n", c)
	}
	finish := "stop"
	if tool != nil {
		args, _ := json.Marshal(tool[1])
		fmt.Fprintf(&b, "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_p\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":null}]}\n\n", tool[0], args)
		finish = "tool_calls"
	}
	fmt.Fprintf(&b, "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\n", finish)
	if usage != "" {
		fmt.Fprintf(&b, "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[],\"usage\":%s}\n\n", usage)
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func (f fakeLocal) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(f.listStatus)
			if f.listStatus == 200 {
				fmt.Fprintf(w, `{"data":[{"id":%q,"max_model_len":%d}]}`, f.modelName, f.vllmLen)
			} else {
				_, _ = io.WriteString(w, `{"error":{"message":"nope"}}`)
			}
			return
		case "/api/show":
			if f.numCtx == 0 {
				w.WriteHeader(404)
				return
			}
			fmt.Fprintf(w, `{"parameters":"num_ctx                        %d\nstop \"<|im_end|>\"","model_info":{"qwen2.context_length":32768}}`, f.numCtx)
			return
		case "/api/v0/models/" + f.modelName:
			if f.lmLoaded == 0 {
				w.WriteHeader(404)
				return
			}
			fmt.Fprintf(w, `{"loaded_context_length":%d,"max_context_length":131072}`, f.lmLoaded)
			return
		}
		var req struct {
			Stream         bool              `json:"stream"`
			StreamOptions  json.RawMessage   `json:"stream_options"`
			Tools          []json.RawMessage `json:"tools"`
			ResponseFormat json.RawMessage   `json:"response_format"`
			Messages       []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if len(req.StreamOptions) > 0 && !f.streamOpts {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`)
			return
		}
		if len(req.Tools) > 0 && !f.tools {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"model does not support tools"}}`)
			return
		}
		if strings.Contains(string(req.ResponseFormat), "json_schema") && !f.jsonSchemaOK {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"response_format type json_schema is not supported"}}`)
			return
		}
		all := ""
		for _, m := range req.Messages {
			all += string(m.Content)
		}
		usage := ""
		if len(req.StreamOptions) > 0 {
			pt := len(all) / 4
			if f.truncateTo > 0 && len(all) > 20000 {
				pt = f.truncateTo
			}
			usage = fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":3}`, pt)
		}
		w.Header().Set("content-type", "text/event-stream")
		switch {
		case len(req.Tools) > 0:
			_, _ = io.WriteString(w, chunkSSE("", &[2]string{"probe_add", `{"a":2,"b":3}`}, usage))
		case strings.Contains(all, "warden_tool_call"):
			_, _ = io.WriteString(w, chunkSSE("<warden_tool_call>\n{\"name\":\"probe_add\",\"arguments\":{\"a\":2,\"b\":3}}\n</warden_tool_call>", nil, usage))
		case len(req.ResponseFormat) > 0 || strings.Contains(all, "JSON Schema"):
			_, _ = io.WriteString(w, chunkSSE(`{"answer":5,"unit":"apples"}`, nil, usage))
		case strings.Contains(all, "code word"):
			i := strings.Index(all, "WARDEN-")
			answer := all[i : i+13]
			if f.truncateTo > 0 {
				answer = "unknown"
			}
			_, _ = io.WriteString(w, chunkSSE(answer, nil, usage))
		default:
			_, _ = io.WriteString(w, chunkSSE("ok", nil, usage))
		}
	}
}

func probeWith(t *testing.T, f fakeLocal) (model.ProbeResult, *providertest.FakeProbeCache) {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	cfg := ollamaCfg(srv.URL + "/v1")
	cfg.Models = cfg.Models[:1]
	cfg.Models[0].Model = f.modelName
	cache := &providertest.FakeProbeCache{}
	res, err := client(t, cfg, nil, cache).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res, cache
}

func TestProbe_NativeLocalModelWithVLLMMetadata(t *testing.T) {
	res, cache := probeWith(t, fakeLocal{tools: true, streamOpts: true, listStatus: 200, vllmLen: 16384, jsonSchemaOK: true, modelName: "qwen2.5-coder:32b"})
	if !res.OK || len(res.Models) != 1 || res.ModelsListed[0] != "qwen2.5-coder:32b" {
		t.Fatalf("result = %+v", res)
	}
	m := res.Models[0]
	if m.ToolCalling != model.ToolCallingNative || !m.StructuredOutput || m.StructuredMode != "json_schema" || m.MaxContextMeta == nil || *m.MaxContextMeta != 16384 {
		t.Fatalf("model = %+v", m)
	}
	// The effective catalog takes the smaller metadata context.
	if caps := model.Effective(ollamaCfg("").Models[0].Capabilities, &m); caps.MaxContext != 16384 {
		t.Fatalf("effective = %+v", caps)
	}
	if r, _ := cache.Load("ollama"); r == nil {
		t.Fatal("result not cached")
	}
}

// TestProbe_OllamaDefaultsLearnedAndTruncationDetected reproduces a stock
// Ollama: no stream_options support (learned by shape retry), no
// json_schema (stepped down), a small num_ctx and silent truncation.
func TestProbe_OllamaDefaultsLearnedAndTruncationDetected(t *testing.T) {
	res, cache := probeWith(t, fakeLocal{tools: true, streamOpts: false, listStatus: 200, numCtx: 4096, truncateTo: 4100, modelName: "qwen2.5-coder:32b"})
	m := res.Models[0]
	if m.MaxContextMeta == nil || *m.MaxContextMeta != 4096 {
		t.Fatalf("num_ctx not read: %+v", m)
	}
	// Usage is not reported once stream_options is off, so the effective size
	// comes from num_ctx metadata; the truncation warning must still be there.
	if len(m.Warnings) == 0 || !strings.Contains(strings.Join(m.Warnings, ";"), "truncates") {
		t.Fatalf("truncation not detected: %+v", m)
	}
	if m.MaxContextEffective != nil {
		t.Fatalf("effective context derived from an estimate: %d", *m.MaxContextEffective)
	}
	if caps := model.Effective(ollamaCfg("").Models[0].Capabilities, &m); caps.MaxContext != 4096 {
		t.Fatalf("effective = %+v", caps)
	}
	if m.Quirks["response_format"] != "json_object" || !m.StructuredOutput {
		t.Fatalf("json_schema step-down not applied: %+v", m.Quirks)
	}
	learned := strings.Join(cache.Learned, ",")
	if !strings.Contains(learned, "stream_usage=false") || !strings.Contains(learned, "response_format=json_object") {
		t.Fatalf("learned = %s", learned)
	}
}

func TestProbe_EmulatedWhenServerRejectsTools(t *testing.T) {
	res, _ := probeWith(t, fakeLocal{tools: false, streamOpts: true, listStatus: 200, lmLoaded: 8192, jsonSchemaOK: true, modelName: "qwen2.5-coder:7b"})
	m := res.Models[0]
	if m.ToolCalling != model.ToolCallingEmulated || m.Quirks["tools"] != false {
		t.Fatalf("model = %+v", m)
	}
	if m.MaxContextMeta == nil || *m.MaxContextMeta != 8192 {
		t.Fatalf("LM Studio loaded context not read: %+v", m)
	}
}

func TestProbe_ListingFailures(t *testing.T) {
	res, _ := probeWith(t, fakeLocal{listStatus: 401, modelName: "m"})
	if res.OK || res.Error == nil || res.Error.Code != model.ErrAuthFailed {
		t.Fatalf("401 = %+v", res)
	}
	res, _ = probeWith(t, fakeLocal{tools: true, streamOpts: true, listStatus: 404, jsonSchemaOK: true, modelName: "m"})
	if !res.OK || len(res.Models) != 1 {
		t.Fatalf("404 must continue: %+v", res)
	}
}

func TestProbe_Unreachable(t *testing.T) {
	cfg := ollamaCfg("http://127.0.0.1:1/v1")
	res, err := client(t, cfg, nil, nil).Probe(context.Background())
	if err != nil || res.OK || res.Error == nil || res.Error.Code != model.ErrProviderUnavailable {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}
