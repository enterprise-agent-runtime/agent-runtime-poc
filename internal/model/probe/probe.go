// Package probe runs the protocol-independent capability probe steps of
// design A11 §9.2 (P1 stream, P2 native tools, P2b tool choice, P2e
// emulated tools, P3 structured output, P4t truncation) against any
// adapter's Generate. Adapters add the protocol-specific steps (P0 model
// listing, P4 context metadata) and assemble the model.ProbeResult.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"warden.dev/warden/internal/model"
)

// Generate is an adapter's Generate method.
type Generate func(ctx context.Context, req model.ModelRequest) (model.Stream, error)

// AddSchema is the probe_add tool's input schema.
var AddSchema = json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"]}`)

// AddTool is the tool the probe asks the model to call.
var AddTool = model.ToolDefinition{Name: "probe_add", Description: "Add two integers.", InputSchema: AddSchema}

// AnswerSchema is the P3 structured-output schema.
var AnswerSchema = json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"},"unit":{"type":"string","enum":["apples","pears"]}},"required":["answer","unit"],"additionalProperties":false}`)

// Options tune a model probe.
type Options struct {
	Tier        model.Tier
	StepTimeout time.Duration // per step; A11: 60 s (120 s for the first T0/T1 request)
	DeclaredCtx int           // catalog max_context, for P4t
	SkipP4t     bool
}

// Model runs P1–P4t for one catalog model and returns its probe record.
// Failures of one step only lower that capability (A11 §9.2).
func Model(ctx context.Context, gen Generate, modelID string, o Options) model.ModelProbe {
	if o.StepTimeout == 0 {
		o.StepTimeout = 60 * time.Second
	}
	mp := model.ModelProbe{ModelID: modelID, ToolCalling: model.ToolCallingNone, StructuredMode: "none", Quirks: map[string]any{}}
	first := o.StepTimeout
	if o.Tier == model.T0 || o.Tier == model.T1 {
		first = 2 * o.StepTimeout // model loading on local servers
	}

	// P1 stream.
	start := time.Now()
	r, err := run(ctx, gen, first, model.ModelRequest{ModelID: modelID, Messages: []model.Message{model.UserText("Reply with exactly: ok")}, Generation: &model.Generation{MaxOutputTokens: 16}})
	if err == nil && r.Text() != "" {
		mp.Streaming = true
		mp.TTFTMS = int(time.Since(start).Milliseconds())
		mp.Quirks["stream_usage"] = !r.Usage.Estimated
	} else {
		mp.Warnings = append(mp.Warnings, "P1 streaming failed: "+errText(err))
	}

	// P2 native tools.
	addReq := func(choice *model.ToolChoice, parallel *bool) model.ModelRequest {
		return model.ModelRequest{ModelID: modelID, Tools: []model.ToolDefinition{AddTool}, ToolChoice: choice, ParallelToolCalls: parallel,
			Messages:   []model.Message{model.UserText("Use the probe_add tool to add 2 and 3. Do not answer in text.")},
			Generation: &model.Generation{MaxOutputTokens: 256}}
	}
	r, err = run(ctx, gen, o.StepTimeout, addReq(&model.ToolChoice{Type: model.ToolChoiceAuto}, nil))
	if err == nil && calledAdd(r) {
		mp.ToolCalling = model.ToolCallingNative
		mp.Quirks["tools"] = true
		// P2b tool choice variants.
		r, err = run(ctx, gen, o.StepTimeout, addReq(&model.ToolChoice{Type: model.ToolChoiceTool, Name: "probe_add"}, nil))
		mp.Quirks["tool_choice_named"] = err == nil && calledAdd(r)
		r, err = run(ctx, gen, o.StepTimeout, addReq(&model.ToolChoice{Type: model.ToolChoiceRequired}, nil))
		mp.Quirks["tool_choice_required"] = err == nil && calledAdd(r)
		f := false
		r, err = run(ctx, gen, o.StepTimeout, addReq(&model.ToolChoice{Type: model.ToolChoiceAuto}, &f))
		mp.Quirks["parallel_tool_calls_param"] = err == nil && calledAdd(r)
	} else {
		mp.Quirks["tools"] = false
		mp.Warnings = append(mp.Warnings, "P2 native tool call failed: "+errText(err))
		// P2e emulated tools, up to two attempts.
		for attempt := 0; attempt < 2; attempt++ {
			req := model.ModelRequest{ModelID: modelID, Generation: &model.Generation{MaxOutputTokens: 256},
				Messages: []model.Message{model.SystemText(model.RenderEmulatedTools([]model.ToolDefinition{AddTool})),
					model.UserText("Use the probe_add tool to add 2 and 3. Do not answer in text.")}}
			r, err = run(ctx, gen, o.StepTimeout, req)
			if err != nil {
				continue
			}
			if u, _, perr := model.ParseEmulatedCall(r.Text(), r.StopReason); perr == nil && u.Name == "probe_add" && addArgs(u.Input) {
				mp.ToolCalling = model.ToolCallingEmulated
				break
			}
		}
	}

	// P3 structured output.
	r, err = run(ctx, gen, o.StepTimeout, model.ModelRequest{ModelID: modelID,
		Messages:       []model.Message{model.UserText("I have 2 apples and buy 3 more apples. Answer as JSON.")},
		ResponseFormat: &model.ResponseFormat{Type: "json_schema", Schema: AnswerSchema}, Generation: &model.Generation{MaxOutputTokens: 128}})
	if err == nil && validAnswer(r.Text()) {
		mp.StructuredOutput = true
		mp.StructuredMode = "json_schema"
	} else {
		mp.Warnings = append(mp.Warnings, "P3 structured output failed: "+errText(err))
	}

	// P4t truncation test (T0/T1 only, declared context ≥ 16384).
	if !o.SkipP4t && (o.Tier == model.T0 || o.Tier == model.T1) && o.DeclaredCtx >= 16384 {
		word := fmt.Sprintf("WARDEN-%06d", rand.IntN(1000000))
		var sb strings.Builder
		sb.WriteString("1. The code word is " + word + "\n")
		for i := 2; sb.Len() < 12000*4; i++ {
			fmt.Fprintf(&sb, "%d. This is filler line number %d; ignore it.\n", i, i)
		}
		sb.WriteString("What is the code word on line 1? Reply with the code word only.")
		est := (sb.Len() + 3) / 4
		r, err = run(ctx, gen, 2*o.StepTimeout, model.ModelRequest{ModelID: modelID, Messages: []model.Message{model.UserText(sb.String())}, Generation: &model.Generation{MaxOutputTokens: 32}})
		if err == nil && (r.Usage.Estimated || r.Usage.InputTokens >= est*8/10) && strings.Contains(r.Text(), word) {
			// passes: context as declared
		} else if err == nil {
			// Only a reported prompt_tokens can size the effective window; an
			// estimate of our own request would just echo what we sent. Without
			// usage the warning stands and context metadata (num_ctx) applies.
			if eff := (r.Usage.InputTokens / 1024) * 1024; eff > 0 && !r.Usage.Estimated {
				mp.MaxContextEffective = &eff
			}
			mp.Warnings = append(mp.Warnings, "server truncates prompts; configure the context length (Ollama OLLAMA_CONTEXT_LENGTH or num_ctx, vLLM --max-model-len)")
		}
	}
	return mp
}

func run(ctx context.Context, gen Generate, timeout time.Duration, req model.ModelRequest) (model.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s, err := gen(ctx, req)
	if err != nil {
		return model.Response{}, err
	}
	evs, err := model.Collect(s)
	if err != nil {
		return model.Response{}, err
	}
	return model.Assemble(evs)
}

func calledAdd(r model.Response) bool {
	for _, u := range r.ToolUses() {
		if u.Name == "probe_add" && addArgs(u.Input) {
			return true
		}
	}
	return false
}

// addArgs accepts {"a":2,"b":3} with numeric comparison (2.0 counts).
func addArgs(in json.RawMessage) bool {
	var v struct{ A, B float64 }
	return json.Unmarshal(in, &v) == nil && v.A == 2 && v.B == 3
}

func validAnswer(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimSuffix(s, "```"), "```json")
	var v struct {
		Answer *float64 `json:"answer"`
		Unit   string   `json:"unit"`
	}
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(s)))
	dec.DisallowUnknownFields()
	return dec.Decode(&v) == nil && v.Answer != nil && *v.Answer == 5 && v.Unit == "apples"
}

func errText(err error) string {
	if err == nil {
		return "unexpected answer"
	}
	return err.Error()
}

// FirstError picks the provider-level error to report from a P0 failure.
func FirstError(err error) *model.ProbeError {
	if err == nil {
		return nil
	}
	var me *model.Error
	if e, ok := err.(*model.Error); ok {
		me = e
	} else {
		me = model.NewError(model.ErrProviderUnavailable, err.Error())
	}
	msg := me.Message
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return &model.ProbeError{Code: me.Code, Message: msg}
}
