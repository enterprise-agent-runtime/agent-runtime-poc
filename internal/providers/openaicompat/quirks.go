package openaicompat

import (
	"strings"
)

// Quirks are per-model server differences, learned by the probe or by the
// shape retry and cached (design A11 §4.8). The zero value is not valid;
// use defaultQuirks.
type Quirks struct {
	Tools                  bool
	StreamTools            bool
	ToolChoiceRequired     bool
	ToolChoiceNamed        bool
	ToolChoiceNone         bool
	ParallelToolCallsParam bool
	StreamUsage            bool
	OmitTemperature        bool
	ResponseFormat         string // json_schema | json_object | none
	MaxTokensField         string // max_tokens | max_completion_tokens
	SystemRole             string // system | developer
}

func defaultQuirks() Quirks {
	return Quirks{Tools: true, StreamTools: true, ToolChoiceRequired: true, ToolChoiceNamed: true, ToolChoiceNone: true,
		ParallelToolCallsParam: true, StreamUsage: true, ResponseFormat: "json_schema", MaxTokensField: "max_tokens", SystemRole: "system"}
}

// apply overlays cached flags (probe-cache JSON names) on q.
func (q *Quirks) apply(m map[string]any) {
	b := func(k string, dst *bool) {
		if v, ok := m[k].(bool); ok {
			*dst = v
		}
	}
	s := func(k string, dst *string) {
		if v, ok := m[k].(string); ok && v != "" {
			*dst = v
		}
	}
	b("tools", &q.Tools)
	b("stream_tools", &q.StreamTools)
	b("tool_choice_required", &q.ToolChoiceRequired)
	b("tool_choice_named", &q.ToolChoiceNamed)
	b("tool_choice_none", &q.ToolChoiceNone)
	b("parallel_tool_calls_param", &q.ParallelToolCallsParam)
	b("stream_usage", &q.StreamUsage)
	b("omit_temperature", &q.OmitTemperature)
	s("response_format", &q.ResponseFormat)
	s("max_tokens_field", &q.MaxTokensField)
	s("system_role", &q.SystemRole)
}

// sentFields records which optional fields a request carried, so a shape
// retry only reacts to a field it actually sent.
type sentFields struct {
	streamOptions, parallel, maxTokens, temperature, responseFormat bool
	toolChoice                                                      string // "" | none | required | named
}

var unsupportedWords = []string{"unsupported", "not supported", "unknown", "unrecognized", "not allowed", "not permitted", "extra", "does not support", "invalid"}

// learn inspects a 400/422 body and returns the flag to flip and its new
// value when the body names an optional field that was sent (A11 §4.8
// "shape retry"). ok is false when no retry applies.
func learn(body string, sent sentFields, q Quirks) (flag string, value any, ok bool) {
	lb := strings.ToLower(body)
	mentions := func(field string) bool {
		if !strings.Contains(lb, field) {
			return false
		}
		for _, w := range unsupportedWords {
			if strings.Contains(lb, w) {
				return true
			}
		}
		return false
	}
	switch {
	case sent.streamOptions && mentions("stream_options"):
		return "stream_usage", false, true
	case sent.parallel && mentions("parallel_tool_calls"):
		return "parallel_tool_calls_param", false, true
	case sent.maxTokens && q.MaxTokensField == "max_tokens" && mentions("max_tokens") && !strings.Contains(lb, "max_completion_tokens is not"):
		return "max_tokens_field", "max_completion_tokens", true
	case sent.temperature && (mentions("temperature") || mentions("top_p")):
		return "omit_temperature", true, true
	case sent.toolChoice != "" && mentions("tool_choice"):
		switch sent.toolChoice {
		case "named":
			return "tool_choice_named", false, true
		case "required":
			return "tool_choice_required", false, true
		case "none":
			return "tool_choice_none", false, true
		}
	case sent.responseFormat && mentions("response_format"):
		switch q.ResponseFormat {
		case "json_schema":
			return "response_format", "json_object", true
		case "json_object":
			return "response_format", "none", true
		}
	}
	return "", nil, false
}

// set flips one flag on q.
func (q *Quirks) set(flag string, value any) {
	q.apply(map[string]any{flag: value})
}
