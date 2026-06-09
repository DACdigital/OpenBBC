// Package anthropic adapts the official anthropic-sdk-go to the open-bbcd
// internal/llm interface. Wraps streaming Messages.NewStreaming + normalizes
// stream events to the internal Event taxonomy.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"iter"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

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

// buildMessageNewParams maps llm.Request → SDK MessageNewParams.
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
	// sdk v1.49.0 uses param.Opt[float64] for optional scalar fields;
	// param.NewOpt sets the internal status to "included" (not just Value).
	if req.Temperature != 0 {
		params.Temperature = param.NewOpt(req.Temperature)
	}
	for _, m := range req.Messages {
		params.Messages = append(params.Messages, convertMessage(m))
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, convertTool(t))
	}
	return params
}

// convertMessage maps an llm.Message to the SDK's MessageParam.
// Anthropic convention: RoleTool messages become user-role messages containing
// tool_result content blocks.
func convertMessage(m llm.Message) sdk.MessageParam {
	var role sdk.MessageParamRole
	switch m.Role {
	case llm.RoleAssistant:
		role = sdk.MessageParamRoleAssistant
	default:
		// RoleUser and RoleTool both map to the Anthropic "user" role.
		role = sdk.MessageParamRoleUser
	}

	blocks := make([]sdk.ContentBlockParamUnion, 0, len(m.Content))
	for _, b := range m.Content {
		switch x := b.(type) {
		case llm.TextBlock:
			blocks = append(blocks, sdk.NewTextBlock(x.Text))
		case llm.ToolUseBlock:
			// Input is json.RawMessage; unmarshal to any so the SDK serialises
			// it without double-encoding.
			var input any
			if len(x.Input) > 0 {
				_ = json.Unmarshal(x.Input, &input)
			}
			blocks = append(blocks, sdk.NewToolUseBlock(x.ID, input, x.Name))
		case llm.ToolResultBlock:
			// Result is json.RawMessage; NewToolResultBlock expects a string
			// for the content parameter. Convert to string to pass through as-is.
			blocks = append(blocks, sdk.NewToolResultBlock(x.ToolUseID, string(x.Result), x.IsError))
		}
	}

	return sdk.MessageParam{
		Role:    role,
		Content: blocks,
	}
}

// convertTool maps an llm.ToolDef to the SDK's ToolUnionParam.
func convertTool(t llm.ToolDef) sdk.ToolUnionParam {
	schema := parseInputSchema(t.InputSchema)
	p := sdk.ToolUnionParamOfTool(schema, t.Name)
	// Description is optional in the SDK (param.Opt[string]); set it when present.
	if t.Description != "" && p.OfTool != nil {
		p.OfTool.Description = sdk.String(t.Description)
	}
	return p
}

// parseInputSchema deserialises a raw JSON Schema into ToolInputSchemaParam.
// param.Override is used so the raw JSON is serialised verbatim — no field
// mapping is needed and extra schema keywords are preserved.
func parseInputSchema(raw json.RawMessage) sdk.ToolInputSchemaParam {
	if len(raw) == 0 {
		return sdk.ToolInputSchemaParam{}
	}
	return param.Override[sdk.ToolInputSchemaParam](raw)
}
