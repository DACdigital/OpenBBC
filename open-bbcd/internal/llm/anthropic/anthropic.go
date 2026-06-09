// Package anthropic adapts the official anthropic-sdk-go to the open-bbcd
// internal/llm interface. Wraps streaming Messages.NewStreaming + normalizes
// stream events to the internal Event taxonomy.
package anthropic

import (
	"context"
	"errors"
	"iter"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// LLM is the Anthropic provider adapter. It holds a copy of the SDK client
// (which is a value type in sdk v1.49.0+).
type LLM struct {
	cfg    config.AnthropicConfig
	client *sdk.Client
}

// New constructs an LLM adapter. If APIKey is empty, the client field is
// left nil; Generate then yields an error on first call. This implements
// the lazy-fail policy from design §8.1.
func New(cfg config.AnthropicConfig) *LLM {
	var client *sdk.Client
	if cfg.APIKey != "" {
		// NewClient returns a value in sdk v1.49.0; take its address.
		c := sdk.NewClient(option.WithAPIKey(cfg.APIKey))
		client = &c
	}
	return &LLM{cfg: cfg, client: client}
}

// Name implements llm.LLM.
func (l *LLM) Name() string { return "anthropic" }

// Generate streams events for one turn. Stubs for convertMessage / convertTool /
// translateChunk are filled in by subsequent tasks (B11, B12).
func (l *LLM) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		if l.client == nil {
			yield(nil, errors.New("anthropic: ANTHROPIC_API_KEY not configured"))
			return
		}
		params := buildMessageNewParams(req)
		_ = params
		// Streaming loop intentionally not implemented yet — B12 fills it in.
		// For now, emit a single MessageStopEvent so the iterator terminates
		// cleanly if anyone tries to use this stub.
		yield(llm.MessageStopEvent{StopReason: "stub"}, nil)
	}
}

// buildMessageNewParams maps llm.Request → SDK MessageNewParams (basic fields
// only). Per-block message conversion and tool conversion arrive in B11.
func buildMessageNewParams(req llm.Request) sdk.MessageNewParams {
	params := sdk.MessageNewParams{
		// Model is a type alias for string in sdk v1.49.0.
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
	}
	// System as a single text block; cache-control marker comes in B13.
	if req.System != "" {
		params.System = []sdk.TextBlockParam{{Text: req.System}}
	}
	// Temperature only if non-zero (zero means "use SDK default").
	// sdk v1.49.0 uses param.Opt[float64] for optional scalar fields.
	// B11 handles temperature passthrough; skipped here per B10 scope.
	return params
}

// convertMessage is a stub filled in by B11.
func convertMessage(m llm.Message) sdk.MessageParam {
	return sdk.MessageParam{}
}

// convertTool is a stub filled in by B11.
func convertTool(t llm.ToolDef) sdk.ToolUnionParam {
	return sdk.ToolUnionParam{}
}
