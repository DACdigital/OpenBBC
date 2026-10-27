// Package anthropic adapts the official anthropic-sdk-go to the open-bbcd
// internal/llm interface. Wraps streaming Messages.NewStreaming + normalizes
// stream events to the internal Event taxonomy.
package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"iter"
	"strings"

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
// per-stream state per content-block index: a mapping to tool-use ID so that
// InputJSONDelta events (which carry only an index) can be attributed to the
// correct tool-use call, and the initial Input payload captured from
// ContentBlockStartEvent — kept until either a delta supersedes it or
// ContentBlockStopEvent flushes it as a single fragment (no-delta case).
type chunkTranslator struct {
	toolUseIDAtIndex      map[int64]string
	pendingInitialInputAt map[int64]json.RawMessage
}

// translate maps one SDK MessageStreamEventUnion to zero or more internal Events.
// Returns nil for control events that carry no semantic content.
func (t *chunkTranslator) translate(chunk sdk.MessageStreamEventUnion) []llm.Event {
	if t.toolUseIDAtIndex == nil {
		t.toolUseIDAtIndex = map[int64]string{}
	}
	if t.pendingInitialInputAt == nil {
		t.pendingInitialInputAt = map[int64]json.RawMessage{}
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
		// If the block is a tool_use, record the mapping index→ID, stash the
		// initial Input payload (Anthropic always sends a placeholder here,
		// typically {}), and emit ToolUseStartEvent. The stashed input is
		// flushed on ContentBlockStop only if no InputJSONDelta arrived —
		// covering tools the model invokes with no arguments.
		switch b := v.ContentBlock.AsAny().(type) {
		case sdk.ToolUseBlock:
			t.toolUseIDAtIndex[v.Index] = b.ID
			if len(b.Input) > 0 {
				t.pendingInitialInputAt[v.Index] = b.Input
			}
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
			// Empty partial_json is stream noise — Anthropic sometimes emits
			// one alongside a tool_use that has no real arguments. Ignore it:
			// don't supersede the start-event placeholder and don't propagate
			// an empty delta (the AG-UI transport rejects empty deltas).
			if d.PartialJSON == "" {
				return nil
			}
			// A real fragment supersedes the start-event placeholder.
			delete(t.pendingInitialInputAt, v.Index)
			id := t.toolUseIDAtIndex[v.Index]
			return []llm.Event{llm.ToolUseInputEvent{
				ID:           id,
				JSONFragment: d.PartialJSON,
			}}
		}
		return nil

	case sdk.ContentBlockStopEvent:
		// If this block was a tool_use, emit ToolUseEndEvent and clean up.
		// If the initial Input payload was never superseded by a delta, flush
		// it as a single ToolUseInputEvent before the end so downstream
		// consumers always receive a complete input JSON.
		if id, ok := t.toolUseIDAtIndex[v.Index]; ok {
			initial, hadInitial := t.pendingInitialInputAt[v.Index]
			var events []llm.Event
			if hadInitial {
				events = append(events, llm.ToolUseInputEvent{
					ID:           id,
					JSONFragment: string(initial),
				})
				delete(t.pendingInitialInputAt, v.Index)
			}
			delete(t.toolUseIDAtIndex, v.Index)
			events = append(events, llm.ToolUseEndEvent{ID: id})
			return events
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
			// Result is json.RawMessage; NewToolResultBlock expects a string.
			// A JSON string result (e.g. the agent tool's child text) is sent
			// as its plain-text value so the LLM never sees JSON quoting;
			// anything else (including `null`, which json.Unmarshal would
			// accept into a string as "") passes through as-is.
			content := string(x.Result)
			if trimmed := bytes.TrimSpace(x.Result); len(trimmed) > 0 && trimmed[0] == '"' {
				var s string
				if err := json.Unmarshal(trimmed, &s); err == nil {
					content = s
				}
			}
			blocks = append(blocks, sdk.NewToolResultBlock(x.ToolUseID, content, x.IsError))
		case llm.InlineMediaBlock:
			// Materialised by RenderArtifactAsBlock upstream: raw bytes plus
			// MIME, ready for base64 inlining into the provider-native block
			// shape. Anthropic accepts image/* and application/pdf natively;
			// callers only produce InlineMediaBlock for these MIMEs (see
			// RenderArtifactAsBlock).
			b64 := base64.StdEncoding.EncodeToString(x.Data)
			switch {
			case strings.HasPrefix(x.MIME, "image/"):
				blocks = append(blocks, sdk.NewImageBlockBase64(x.MIME, b64))
			case x.MIME == "application/pdf":
				blocks = append(blocks, sdk.NewDocumentBlock(sdk.Base64PDFSourceParam{
					Data:      b64,
					MediaType: "application/pdf",
				}))
			}
			// Other MIMEs never reach this switch because RenderArtifactAsBlock
			// returns ErrUnsupported for them and the caller substitutes a
			// TextBlock via llm.TextSurrogate before convertMessage sees it.
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

// supportedImageMIMEs is the set Anthropic accepts as native image blocks.
// GIF and WEBP are accepted per the current Anthropic Vision docs; kept as
// a map for O(1) lookup during rendering.
var supportedImageMIMEs = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// Media limits, pinned 2026-09-30 from the Anthropic vision and PDF docs.
// Chosen to be safe on every platform the adapter may target:
//   - per image: 5 MB base64 (Bedrock/Vertex cap; the direct API allows 10 MB);
//   - per request: 32 MB total; native media budget 24 MiB base64 leaves
//     headroom for text and tool payloads;
//   - >20 image blocks trigger a stricter per-image pixel limit, so cap at 20.
//
// PDF page count (600) is not checked — accepted residual risk in the spec.
const (
	maxImageBase64Bytes   = 5_000_000
	maxRequestMediaBytes  = 24 << 20
	maxRequestMediaBlocks = 20
)

func base64Len(n int64) int64 { return ((n + 2) / 3) * 4 }

// nativeRenderable reports whether ref has a natively supported MIME and
// fits the per-block limits. Shared by SupportsNative and
// RenderArtifactAsBlock; never fetches.
func nativeRenderable(ref llm.ArtifactRefBlock) bool {
	limit := base64Limit(ref.MIME)
	return limit > 0 && base64Len(ref.SizeBytes) <= limit
}

// base64Limit returns the per-block base64 byte limit for a natively
// supported MIME, or 0 when the MIME is unsupported.
func base64Limit(mime string) int64 {
	switch {
	case supportedImageMIMEs[mime]:
		return maxImageBase64Bytes
	case mime == "application/pdf":
		return maxRequestMediaBytes
	}
	return 0
}

// SupportsNative implements llm.MultimodalRenderer. Must not fetch.
func (l *LLM) SupportsNative(ref llm.ArtifactRefBlock) bool { return nativeRenderable(ref) }

// NativeRenderBudget implements llm.MultimodalRenderer.
func (l *LLM) NativeRenderBudget() llm.RenderBudget {
	return llm.RenderBudget{MaxBytes: maxRequestMediaBytes, MaxBlocks: maxRequestMediaBlocks}
}

// RenderArtifactAsBlock implements llm.MultimodalRenderer for the Anthropic
// provider. Materialises the artifact_ref's bytes into an InlineMediaBlock
// when the MIME is natively supported (image/{png,jpeg,gif,webp} or
// application/pdf); returns llm.ErrUnsupported otherwise. Callers get the
// text-surrogate fallback via llm.TextSurrogate in that case.
//
// Byte fetch strategy: when the store's PreferredDelivery is DeliveryBytes
// (integer 0), use Get() directly. When SignedURL (integer 1), fetch the
// signed URL over HTTPS. Anthropic requires the bytes inlined as base64 —
// there is no image-by-URL block for user messages in the Messages API
// (only Anthropic's Files API supports that, out of phase 1 scope).
func (l *LLM) RenderArtifactAsBlock(ctx context.Context, ref llm.ArtifactRefBlock, fetch llm.ArtifactFetcher) (llm.Block, error) {
	if !nativeRenderable(ref) {
		return nil, llm.ErrUnsupported
	}

	bytes, err := llm.FetchBytes(ctx, ref.URI, fetch)
	if err != nil {
		return nil, err
	}
	// Re-check on actual bytes: the declared SizeBytes may be wrong.
	if base64Len(int64(len(bytes))) > base64Limit(ref.MIME) {
		return nil, llm.ErrUnsupported
	}
	return llm.InlineMediaBlock{MIME: ref.MIME, Data: bytes}, nil
}

// Compile-time check that *LLM satisfies llm.MultimodalRenderer.
var _ llm.MultimodalRenderer = (*LLM)(nil)
