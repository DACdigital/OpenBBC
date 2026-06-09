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

// Generate streams events for one turn. Opens an SSE stream via
// Messages.NewStreaming and normalises each chunk to the internal Event
// taxonomy using chunkTranslator. Cancellation is propagated via ctx.
func (l *LLM) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		if l.client == nil {
			yield(nil, errors.New("anthropic: ANTHROPIC_API_KEY not configured"))
			return
		}
		params := buildMessageNewParams(req)
		stream := l.client.Messages.NewStreaming(ctx, params)
		t := &chunkTranslator{}
		for stream.Next() {
			chunk := stream.Current()
			for _, ev := range t.translate(chunk) {
				if !yield(ev, nil) {
					return
				}
			}
		}
		if err := stream.Err(); err != nil {
			yield(nil, err)
		}
	}
}

// chunkTranslator maps SDK stream events to internal llm.Events. It holds
// per-stream state: a mapping from content-block index to tool-use ID so that
// InputJSONDelta events (which carry only an index) can be attributed to the
// correct tool-use call.
type chunkTranslator struct {
	toolUseIDAtIndex map[int64]string
}

// translate maps one SDK MessageStreamEventUnion to zero or more internal Events.
// Returns nil for control events that carry no semantic content.
func (t *chunkTranslator) translate(chunk sdk.MessageStreamEventUnion) []llm.Event {
	if t.toolUseIDAtIndex == nil {
		t.toolUseIDAtIndex = map[int64]string{}
	}
	switch v := chunk.AsAny().(type) {
	case sdk.MessageStartEvent:
		// Input token count arrives in the opening preamble.
		if v.Message.Usage.InputTokens > 0 {
			return []llm.Event{
				llm.UsageEvent{InputTokens: int(v.Message.Usage.InputTokens)},
			}
		}
		return nil

	case sdk.ContentBlockStartEvent:
		// If the block is a tool_use, record the mapping index→ID and emit
		// ToolUseStartEvent. Text blocks don't need tracking.
		switch b := v.ContentBlock.AsAny().(type) {
		case sdk.ToolUseBlock:
			t.toolUseIDAtIndex[v.Index] = b.ID
			return []llm.Event{
				llm.ToolUseStartEvent{ID: b.ID, Name: b.Name},
			}
		}
		return nil

	case sdk.ContentBlockDeltaEvent:
		switch d := v.Delta.AsAny().(type) {
		case sdk.TextDelta:
			return []llm.Event{llm.TextDeltaEvent{Delta: d.Text}}
		case sdk.InputJSONDelta:
			// Attribute this fragment to the tool-use block that opened at
			// the same index in ContentBlockStartEvent.
			id := t.toolUseIDAtIndex[v.Index]
			return []llm.Event{llm.ToolUseInputEvent{
				ID:           id,
				JSONFragment: d.PartialJSON,
			}}
		}
		return nil

	case sdk.ContentBlockStopEvent:
		// If this block was a tool_use, emit ToolUseEndEvent and clean up.
		if id, ok := t.toolUseIDAtIndex[v.Index]; ok {
			delete(t.toolUseIDAtIndex, v.Index)
			return []llm.Event{llm.ToolUseEndEvent{ID: id}}
		}
		return nil

	case sdk.MessageDeltaEvent:
		// StopReason and output token count travel together in MessageDeltaEvent.
		var events []llm.Event
		if v.Delta.StopReason != "" {
			events = append(events, llm.MessageStopEvent{
				StopReason: string(v.Delta.StopReason),
			})
		}
		if v.Usage.OutputTokens > 0 {
			events = append(events, llm.UsageEvent{
				OutputTokens: int(v.Usage.OutputTokens),
			})
		}
		return events

	case sdk.MessageStopEvent:
		// No-op: stop reason has already been emitted via MessageDeltaEvent.
		return nil
	}
	return nil
}

// buildMessageNewParams maps llm.Request → SDK MessageNewParams.
func buildMessageNewParams(req llm.Request) sdk.MessageNewParams {
	params := sdk.MessageNewParams{
		// Model is a type alias for string in sdk v1.49.0.
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
	}
	// System as a single text block with ephemeral cache-control marker (B13).
	if req.System != "" {
		params.System = []sdk.TextBlockParam{
			{
				Text:         req.System,
				CacheControl: sdk.NewCacheControlEphemeralParam(),
			},
		}
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
	// Mark the last tool with ephemeral cache-control. Earlier tools are covered
	// by the breakpoint on the last one (per Anthropic caching docs).
	if n := len(params.Tools); n > 0 {
		if params.Tools[n-1].OfTool != nil {
			params.Tools[n-1].OfTool.CacheControl = sdk.NewCacheControlEphemeralParam()
		}
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
