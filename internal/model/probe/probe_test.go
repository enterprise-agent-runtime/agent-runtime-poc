package probe

import (
	"context"
	"strings"
	"testing"

	"warden.dev/warden/internal/model"
	"warden.dev/warden/internal/model/providertest"
)

// behaviour scripts a fake model per probe request.
type behaviour struct {
	nativeTools bool
	emulated    bool
	structured  bool
	namedChoice bool
	truncateTo  int // reported prompt tokens for the long prompt; 0 = honest
}

func (b behaviour) gen(_ context.Context, req model.ModelRequest) (model.Stream, error) {
	text := ""
	for _, m := range req.Messages {
		for _, c := range m.Content {
			text += c.Text
		}
	}
	switch {
	case len(req.Tools) > 0:
		if !b.nativeTools {
			return nil, model.NewError(model.ErrToolFormatUnsupported, "no tools")
		}
		if req.ToolChoice != nil && req.ToolChoice.Type == model.ToolChoiceTool && !b.namedChoice {
			return nil, model.NewError(model.ErrInvalidRequest, "tool_choice not supported")
		}
		return &model.SliceStream{Events: providertest.ToolResponse("c1", "probe_add", `{"a":2,"b":3.0}`)}, nil
	case req.ResponseFormat != nil:
		if b.structured {
			return &model.SliceStream{Events: providertest.TextResponse(`{"answer":5,"unit":"apples"}`)}, nil
		}
		return &model.SliceStream{Events: providertest.TextResponse("five apples")}, nil
	case strings.Contains(text, "<warden_tool_call>"):
		if b.emulated {
			return &model.SliceStream{Events: providertest.TextResponse("<warden_tool_call>\n{\"name\":\"probe_add\",\"arguments\":{\"a\":2,\"b\":3}}\n</warden_tool_call>")}, nil
		}
		return &model.SliceStream{Events: providertest.TextResponse("5")}, nil
	case strings.Contains(text, "code word"):
		word := text[strings.Index(text, "WARDEN-") : strings.Index(text, "WARDEN-")+13]
		evs := providertest.TextResponse(word)
		pt := len(text) / 4
		if b.truncateTo > 0 {
			pt = b.truncateTo
			evs = providertest.TextResponse("I don't know")
		}
		evs[2].Usage = &model.Usage{InputTokens: pt, OutputTokens: 3}
		return &model.SliceStream{Events: evs}, nil
	}
	return &model.SliceStream{Events: providertest.TextResponse("ok")}, nil
}

func TestModel_NativeEverything(t *testing.T) {
	mp := Model(context.Background(), behaviour{nativeTools: true, structured: true, namedChoice: true}.gen, "local/qwen", Options{Tier: model.T3})
	if mp.ToolCalling != model.ToolCallingNative || !mp.StructuredOutput || !mp.Streaming || mp.Quirks["tool_choice_named"] != true || mp.Quirks["tools"] != true {
		t.Fatalf("probe = %+v", mp)
	}
}

func TestModel_NamedChoiceRejectedIsRecorded(t *testing.T) {
	mp := Model(context.Background(), behaviour{nativeTools: true}.gen, "m", Options{})
	if mp.ToolCalling != model.ToolCallingNative || mp.Quirks["tool_choice_named"] != false || mp.Quirks["tool_choice_required"] != true {
		t.Fatalf("quirks = %+v", mp.Quirks)
	}
	if mp.StructuredOutput || mp.StructuredMode != "none" {
		t.Fatalf("structured output accepted from a prose answer: %+v", mp)
	}
}

func TestModel_EmulatedWhenNativeFails(t *testing.T) {
	mp := Model(context.Background(), behaviour{emulated: true}.gen, "local/qwen-7b", Options{})
	if mp.ToolCalling != model.ToolCallingEmulated || mp.Quirks["tools"] != false {
		t.Fatalf("probe = %+v", mp)
	}
}

func TestModel_NoneWhenNeitherWorks(t *testing.T) {
	mp := Model(context.Background(), behaviour{}.gen, "m", Options{})
	if mp.ToolCalling != model.ToolCallingNone {
		t.Fatalf("tool calling = %s", mp.ToolCalling)
	}
}

// TestModel_TruncationLowersContext reproduces the Ollama default-context
// trap (A11 §10): the server silently drops most of the prompt.
func TestModel_TruncationLowersContext(t *testing.T) {
	mp := Model(context.Background(), behaviour{nativeTools: true, truncateTo: 2100}.gen, "local/qwen", Options{Tier: model.T0, DeclaredCtx: 32768})
	if mp.MaxContextEffective == nil || *mp.MaxContextEffective != 2048 || len(mp.Warnings) == 0 {
		t.Fatalf("probe = %+v", mp)
	}
	honest := Model(context.Background(), behaviour{nativeTools: true}.gen, "local/qwen", Options{Tier: model.T0, DeclaredCtx: 32768})
	if honest.MaxContextEffective != nil {
		t.Fatalf("honest server flagged: %+v", honest)
	}
	skipped := Model(context.Background(), behaviour{nativeTools: true, truncateTo: 10}.gen, "m", Options{Tier: model.T3, DeclaredCtx: 32768})
	if skipped.MaxContextEffective != nil {
		t.Fatal("P4t ran for a T3 provider")
	}
}

// TestModel_TruncationWithoutUsageKeepsWarningOnly guards a bug found while
// probing a fake stock Ollama: with no reported usage, the "effective"
// window was computed from our own token estimate of the prompt we sent.
func TestModel_TruncationWithoutUsageKeepsWarningOnly(t *testing.T) {
	gen := func(ctx context.Context, req model.ModelRequest) (model.Stream, error) {
		s, err := behaviour{nativeTools: true, truncateTo: 2100}.gen(ctx, req)
		if err != nil {
			return nil, err
		}
		evs := s.(*model.SliceStream).Events
		for i := range evs {
			if evs[i].Usage != nil {
				evs[i].Usage.Estimated = true
			}
		}
		return s, nil
	}
	mp := Model(context.Background(), gen, "m", Options{Tier: model.T0, DeclaredCtx: 32768})
	if mp.MaxContextEffective != nil || len(mp.Warnings) == 0 {
		t.Fatalf("probe = %+v", mp)
	}
}

func TestFirstError(t *testing.T) {
	if FirstError(nil) != nil {
		t.Fatal("nil error produced a probe error")
	}
	if e := FirstError(model.NewError(model.ErrAuthFailed, "no")); e.Code != model.ErrAuthFailed {
		t.Fatalf("e = %+v", e)
	}
	if e := FirstError(context.DeadlineExceeded); e.Code != model.ErrProviderUnavailable {
		t.Fatalf("e = %+v", e)
	}
}
