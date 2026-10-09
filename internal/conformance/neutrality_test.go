// Package conformance holds cross-adapter tests. It is test-only: the
// provider packages may not import each other (CLAUDE.md §6), so tests that
// drive several adapters with one canonical request live here.
package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/providertest"
	"warden.dev/warden/internal/providers/anthropic"
	"warden.dev/warden/internal/providers/openaicompat"
)

const anthropicSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-x","usage":{"input_tokens":200,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"fs__write"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \"src/routes/users.ts\", "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"content\": \"export {}\\n\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}

event: message_stop
data: {"type":"message_stop"}

`

// ollamaSSE: whole arguments in one chunk, finish_reason "stop".
const ollamaSSE = `data: {"id":"c1","model":"qwen2.5-coder:32b","choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"id":"call_o","index":0,"type":"function","function":{"name":"fs__write","arguments":"{\"content\":\"export {}\\n\",\"path\":\"src/routes/users.ts\"}"}}]},"finish_reason":null}]}

data: {"id":"c1","model":"qwen2.5-coder:32b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"c1","model":"qwen2.5-coder:32b","choices":[],"usage":{"prompt_tokens":210,"completion_tokens":25}}

data: [DONE]

`

// vllmSSE: arguments split across deltas, finish_reason "tool_calls".
const vllmSSE = `data: {"id":"c2","model":"Qwen/Qwen2.5-Coder-32B-Instruct","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"chatcmpl-tool-v","type":"function","function":{"name":"fs__write","arguments":""}}]},"finish_reason":null}]}

data: {"id":"c2","model":"Qwen/Qwen2.5-Coder-32B-Instruct","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\": \"src/routes/users.ts\","}}]},"finish_reason":null}]}

data: {"id":"c2","model":"Qwen/Qwen2.5-Coder-32B-Instruct","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" \"content\": \"export {}\\n\"}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`

func serve(t *testing.T, body string, check func(*http.Request, map[string]any)) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		check(r, req)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestNeutrality_SameRequestYieldsToolProposals is the M1 acceptance test
// for H1 at the adapter level (WRD-16 §16 week 1): one canonical request
// with tools goes to Anthropic (T3, API key), a local Ollama (T0, no auth)
// and a company-hosted vLLM behind a gateway token (T1); each wire format
// differs, and each yields the same canonical tool proposal.
func TestNeutrality_SameRequestYieldsToolProposals(t *testing.T) {
	writeTool := model.ToolDefinition{Name: "fs__write", Description: "Write a file in the worktree.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)}
	req := func(modelID string) model.ModelRequest {
		return model.ModelRequest{ModelID: modelID, Tools: []model.ToolDefinition{writeTool}, ToolChoice: &model.ToolChoice{Type: model.ToolChoiceAuto},
			Messages: []model.Message{model.SystemText("You are the coder."), model.UserText("Add a GET /users/:id endpoint returning the user or 404, with tests.")},
			Trace:    model.Trace{SessionID: "ses_test", Step: 1}}
	}
	creds := &providertest.FakeCredentials{Values: map[string]string{
		"secret://providers/anthropic/api_key":  providertest.FakeKey,
		"secret://providers/company-vllm/token": "gateway-token-for-tests",
	}}
	anthropicCreds := &providertest.FakeCredentials{Header: "x-api-key", Values: creds.Values}

	wantTools := func(t *testing.T, r map[string]any, field string) {
		t.Helper()
		if _, ok := r[field]; !ok {
			t.Errorf("request has no %q", field)
		}
	}
	cases := []struct {
		name    string
		modelID string
		build   func(url string) (model.Provider, error)
		sse     string
		check   func(*http.Request, map[string]any)
	}{
		{"anthropic T3", "anthropic/claude-sonnet", func(url string) (model.Provider, error) {
			return anthropic.New(model.ProviderConfig{ID: "anthropic", Protocol: model.ProtocolAnthropic, BaseURL: url, Tier: model.T3,
				Auth:   model.AuthConfig{Mode: model.AuthAPIKey, Secret: "secret://providers/anthropic/api_key"},
				Models: []model.ModelConfig{{ID: "anthropic/claude-sonnet", Model: "claude-sonnet-x", Capabilities: model.DeclaredCapabilities{ToolCalling: "native", MaxOutput: 64000}}}}, anthropicCreds, nil)
		}, anthropicSSE, func(r *http.Request, b map[string]any) {
			wantTools(t, b, "tools")
			if r.Header.Get("x-api-key") == "" || b["system"] == nil {
				t.Errorf("anthropic wire request: %v", b)
			}
		}},
		{"ollama T0", "local/qwen-coder-32b", func(url string) (model.Provider, error) {
			return openaicompat.New(model.ProviderConfig{ID: "ollama", Protocol: model.ProtocolOpenAICompat, BaseURL: url + "/v1", Tier: model.T0,
				Auth:   model.AuthConfig{Mode: model.AuthNone},
				Models: []model.ModelConfig{{ID: "local/qwen-coder-32b", Model: "qwen2.5-coder:32b", Capabilities: model.DeclaredCapabilities{ToolCalling: "native"}}}}, creds, nil)
		}, ollamaSSE, func(r *http.Request, b map[string]any) {
			wantTools(t, b, "tools")
			if r.Header.Get("Authorization") != "" {
				t.Error("auth header sent to a no-auth local server")
			}
		}},
		{"company vLLM T1 gateway", "company/qwen-coder-32b", func(url string) (model.Provider, error) {
			return openaicompat.New(model.ProviderConfig{ID: "company-vllm", Protocol: model.ProtocolOpenAICompat, BaseURL: url + "/v1", Tier: model.T1,
				Auth:   model.AuthConfig{Mode: model.AuthGateway, Kind: model.KindBearer, Secret: "secret://providers/company-vllm/token"},
				Models: []model.ModelConfig{{ID: "company/qwen-coder-32b", Model: "Qwen/Qwen2.5-Coder-32B-Instruct", Capabilities: model.DeclaredCapabilities{ToolCalling: "native"}}}}, creds, nil)
		}, vllmSSE, func(r *http.Request, b map[string]any) {
			wantTools(t, b, "tools")
			if r.Header.Get("Authorization") != "Bearer gateway-token-for-tests" {
				t.Errorf("gateway token not injected: %q", r.Header.Get("Authorization"))
			}
		}},
	}
	var proposals []string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := c.build(serve(t, c.sse, c.check))
			if err != nil {
				t.Fatal(err)
			}
			s, err := p.Generate(context.Background(), req(c.modelID))
			if err != nil {
				t.Fatal(err)
			}
			evs, err := model.Collect(s)
			if err != nil {
				t.Fatal(err)
			}
			if err := model.ValidateSequence(evs); err != nil {
				t.Fatal(err)
			}
			r, err := model.Assemble(evs)
			if err != nil {
				t.Fatal(err)
			}
			uses := r.ToolUses()
			if r.StopReason != model.StopToolUse || len(uses) != 1 {
				t.Fatalf("response = %+v", r)
			}
			var in map[string]string
			if err := json.Unmarshal(uses[0].Input, &in); err != nil {
				t.Fatal(err)
			}
			if uses[0].Name != "fs__write" || in["path"] != "src/routes/users.ts" || in["content"] != "export {}\n" || uses[0].ID == "" {
				t.Fatalf("proposal = %+v", uses[0])
			}
			proposals = append(proposals, uses[0].Name+" "+string(uses[0].Input))
		})
	}
	if len(proposals) != 3 || proposals[0] != proposals[1] || proposals[1] != proposals[2] {
		t.Fatalf("canonical proposals differ across adapters:\n%v", proposals)
	}
}
