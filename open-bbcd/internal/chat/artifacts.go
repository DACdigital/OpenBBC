package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// renderKey identifies one rendered artifact within a turn.
type renderKey struct{ storeID, uri, mime string }

// renderCache holds provider-native blocks rendered earlier in the same
// turn, so each blob is fetched at most once per turn however many tool
// rounds re-send it. It never holds refs, only rendered output, and is
// discarded when the turn ends. After every render pass it is pruned to
// the keys placed natively in that pass: history is append-only, so a ref
// that fell out of the native window never re-enters it.
type renderCache map[renderKey]llm.Block

func newRenderCache() renderCache { return renderCache{} }

// base64Len is the base64-encoded length of n raw bytes.
func base64Len(n int64) int64 { return ((n + 2) / 3) * 4 }

// renderArtifactsForLLM returns a fresh copy of msgs in which every
// ArtifactRefBlock is replaced by a provider-native block or its text
// surrogate. The input is never mutated: callers keep the ref form and
// render a new copy for every LLM call.
//
// Refs are visited in reverse (message, block) order — newest message
// first, last block first — against the provider's RenderBudget:
//
//   - once the budget is exhausted, every remaining (older) ref is a
//     surrogate and is never fetched;
//   - a ref whose store does not resolve, or that the provider would not
//     render natively (SupportsNative), is a surrogate, is not charged and
//     is not fetched;
//   - a ref whose declared size (base64-expanded) or block count would
//     exceed a cap exhausts the budget before any fetch;
//   - otherwise the ref is rendered (from the per-turn cache or by
//     fetching). ErrUnsupported downgrades it to an uncharged surrogate.
//     A rendered InlineMediaBlock is charged at the larger of its declared
//     and actual size; if that actual cost exceeds MaxBytes the budget is
//     exhausted and the ref becomes a surrogate.
//
// On return the cache holds only the keys placed natively in this pass.
func renderArtifactsForLLM(
	ctx context.Context,
	in []llm.Message,
	provider llm.LLM,
	resolver ArtifactFetcherResolver,
	cache renderCache,
	logger *slog.Logger,
) ([]llm.Message, error) {
	out := make([]llm.Message, len(in))
	for i, m := range in {
		out[i] = llm.Message{Role: m.Role, Content: append([]llm.Block(nil), m.Content...)}
	}

	renderer, providerSupports := provider.(llm.MultimodalRenderer)
	if !providerSupports || resolver == nil {
		for i := range out {
			for j, b := range out[i].Content {
				if ref, ok := b.(llm.ArtifactRefBlock); ok {
					out[i].Content[j] = llm.TextSurrogate(ref)
				}
			}
		}
		return out, nil
	}

	budget := renderer.NativeRenderBudget()
	overBytes := func(used, cost int64) bool { return budget.MaxBytes > 0 && used+cost > budget.MaxBytes }
	var usedBytes int64
	usedBlocks := 0
	exhausted := false
	placed := map[renderKey]bool{}

	for i := len(out) - 1; i >= 0; i-- {
		for j := len(out[i].Content) - 1; j >= 0; j-- {
			ref, ok := out[i].Content[j].(llm.ArtifactRefBlock)
			if !ok {
				continue
			}
			out[i].Content[j] = llm.TextSurrogate(ref)
			if exhausted {
				continue
			}
			fetcher := resolver(ref.StoreID)
			if fetcher == nil || !renderer.SupportsNative(ref) {
				continue
			}
			if (budget.MaxBlocks > 0 && usedBlocks+1 > budget.MaxBlocks) ||
				overBytes(usedBytes, base64Len(ref.SizeBytes)) {
				exhausted = true
				continue
			}
			key := renderKey{ref.StoreID, ref.URI, ref.MIME}
			rendered, err := renderOne(ctx, renderer, fetcher, cache, key, ref, logger)
			if err != nil {
				return nil, err
			}
			if rendered == nil {
				continue
			}
			size := ref.SizeBytes
			if inline, ok := rendered.(llm.InlineMediaBlock); ok {
				size = max(size, int64(len(inline.Data)))
			}
			cost := base64Len(size)
			if overBytes(usedBytes, cost) {
				exhausted = true
				continue
			}
			usedBlocks++
			usedBytes += cost
			placed[key] = true
			out[i].Content[j] = rendered
		}
	}

	for k := range cache {
		if !placed[k] {
			delete(cache, k)
		}
	}
	return out, nil
}

// renderOne renders a single ref, using and filling the per-turn cache.
// Returns (nil, nil) when the ref should become a surrogate.
func renderOne(
	ctx context.Context,
	renderer llm.MultimodalRenderer,
	fetcher llm.ArtifactFetcher,
	cache renderCache,
	key renderKey,
	ref llm.ArtifactRefBlock,
	logger *slog.Logger, // logger is used by the Stat fallback (Task 8).
) (llm.Block, error) {
	if b, ok := cache[key]; ok {
		return b, nil
	}
	b, err := renderer.RenderArtifactAsBlock(ctx, ref, fetcher)
	if errors.Is(err, llm.ErrUnsupported) {
		return nil, nil
	}
	if err != nil {
		return nil, err // refined in Task 8 (Stat fallback)
	}
	cache[key] = b
	return b, nil
}

// mcpContentItem is the shape MCP tool results use for a single content
// item inside the result's `content` array. See MCP spec §Tools —
// ContentBlock union of TextContent / ImageContent / EmbeddedResource.
// We intentionally use loose typing: unknown fields are preserved on
// the raw output so the LLM still sees them; only recognised inline-
// media shapes are extracted into ArtifactRefBlocks.
type mcpContentItem struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Data     string          `json:"data,omitempty"`      // base64 (ImageContent)
	MIMEType string          `json:"mimeType,omitempty"`  // ImageContent, EmbeddedResource
	Resource *mcpResourceObj `json:"resource,omitempty"`  // EmbeddedResource
}

type mcpResourceObj struct {
	URI      string `json:"uri,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Blob     string `json:"blob,omitempty"` // base64
	Text     string `json:"text,omitempty"` // plain text
}

// marshalJSON is json.Marshal, replaceable in tests to exercise the
// re-serialisation failure branch.
var marshalJSON = json.Marshal

// unavailableToolResult replaces a tool result whose normalised remainder
// could not be re-serialised. Never fall back to the raw payload: it may
// carry base64 media.
var unavailableToolResult = json.RawMessage(`{"content":[{"type":"text","text":"[tool result unavailable]"}]}`)

// normaliseResult is the outcome of normalising one tool result.
type normaliseResult struct {
	// Refs are the artifacts uploaded from inline media, in item order.
	Refs []llm.ArtifactRefBlock
	// Output is what is streamed as the tool result and persisted in the
	// tool_result block: the payload with inline media removed (or
	// replaced by a text note when its upload failed).
	Output json.RawMessage
	// ForceError is set when Output is unavailableToolResult; the caller
	// marks the tool result is_error.
	ForceError bool
}

// normaliseToolResult walks an MCP-shaped tool result and moves inline
// media (ImageContent, EmbeddedResource with blob/text) into the artifact
// store, one ref per item. Per item:
//   - upload succeeds → the item is removed from the remainder and a ref returned;
//   - base64 does not decode, or upload fails → the item is replaced by
//     {"type":"text","text":"[artifact unavailable: <mime>, <size>]"} and a
//     warn is logged (tool, mime and err only — never a filename).
//
// Non-MCP payloads (no top-level content array) pass through unchanged.
// A re-serialisation failure yields unavailableToolResult with ForceError.
func normaliseToolResult(
	ctx context.Context,
	payload json.RawMessage,
	uploader ArtifactUploader,
	logger *slog.Logger,
	toolName string,
) normaliseResult {
	passThrough := normaliseResult{Output: payload}
	if len(payload) == 0 {
		return passThrough
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(payload, &top); err != nil {
		return passThrough
	}
	rawContent, ok := top["content"]
	if !ok {
		return passThrough
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawContent, &items); err != nil {
		return passThrough
	}

	var res normaliseResult
	kept := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		var item mcpContentItem
		if err := json.Unmarshal(raw, &item); err != nil {
			if looksLikeMedia(raw) {
				logger.Warn("tool result media item malformed; replaced with note",
					slog.String("tool", toolName))
				kept = append(kept, unavailableNote("unknown", "unknown"))
			} else {
				kept = append(kept, raw)
			}
			continue
		}
		declared, data, decoded, isMedia := inlineMedia(item)
		if !isMedia {
			if carriesInlineBytes(raw) {
				logger.Warn("tool result item of unhandled type carries inline bytes; replaced with note",
					slog.String("tool", toolName))
				kept = append(kept, unavailableNote("unknown", "unknown"))
			} else {
				kept = append(kept, raw)
			}
			continue
		}
		if !decoded {
			logger.Warn("tool result media not decodable; replaced with note",
				slog.String("tool", toolName), slog.String("mime", truncateMIME(declared)))
			kept = append(kept, unavailableNote(declared, "unknown"))
			continue
		}
		ref, err := uploader.Upload(ctx, declared, data)
		if err != nil {
			resolved := artifacts.ResolveMIME(declared, data)
			logger.Warn("tool result media upload failed; replaced with note",
				slog.String("tool", toolName), slog.String("mime", resolved), slog.Any("err", err))
			kept = append(kept, unavailableNote(resolved, llm.HumanBytes(int64(len(data)))))
			continue
		}
		res.Refs = append(res.Refs, ref)
	}

	keptJSON, err := marshalJSON(kept)
	if err == nil {
		top["content"] = keptJSON
		var out []byte
		if out, err = marshalJSON(top); err == nil {
			res.Output = out
			return res
		}
	}
	logger.Warn("tool result re-serialisation failed; replaced with unavailable result",
		slog.String("tool", toolName), slog.Any("err", err))
	res.Output = unavailableToolResult
	res.ForceError = true
	return res
}

// inlineMedia extracts inline bytes from an MCP content item. isMedia is
// false for text items, URI-only resources and unknown types (kept as-is).
// decoded is false when the item is media but its base64 does not decode.
func inlineMedia(item mcpContentItem) (declared string, data []byte, decoded, isMedia bool) {
	switch item.Type {
	case "image", "audio":
		declared = orDefault(item.MIMEType, "application/octet-stream")
		b, err := base64.StdEncoding.DecodeString(item.Data)
		return declared, b, err == nil, true
	case "resource":
		if item.Resource == nil {
			return "", nil, false, false
		}
		r := item.Resource
		switch {
		case r.Blob != "":
			declared = orDefault(r.MIMEType, "application/octet-stream")
			b, err := base64.StdEncoding.DecodeString(r.Blob)
			return declared, b, err == nil, true
		case r.Text != "":
			return orDefault(r.MIMEType, "text/plain"), []byte(r.Text), true, true
		}
	}
	return "", nil, false, false
}

// truncateMIME caps a possibly tool-controlled mime string at 100 bytes.
func truncateMIME(mime string) string {
	if len(mime) > 100 {
		return strings.ToValidUTF8(mime[:100], "")
	}
	return mime
}

// carriesInlineBytes reports whether a content item that inlineMedia did not
// handle still holds inline bytes: a non-empty string "data" or "blob", or a
// "resource" object with a non-empty "blob". Text and URI-only items do not.
func carriesInlineBytes(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	nonEmpty := func(v json.RawMessage) bool {
		var s string
		return json.Unmarshal(v, &s) == nil && s != ""
	}
	for _, k := range []string{"data", "blob"} {
		if v, ok := m[k]; ok && nonEmpty(v) {
			return true
		}
	}
	if v, ok := m["resource"]; ok {
		var r map[string]json.RawMessage
		if json.Unmarshal(v, &r) == nil {
			if b, ok := r["blob"]; ok && nonEmpty(b) {
				return true
			}
		}
	}
	return false
}

// looksLikeMedia reports whether a content item that failed typed decoding
// is a JSON object that may carry inline media.
func looksLikeMedia(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	var typ string
	_ = json.Unmarshal(m["type"], &typ)
	switch typ {
	case "image", "audio", "resource":
		return true
	}
	for _, k := range []string{"data", "blob", "resource"} {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func unavailableNote(mime, size string) json.RawMessage {
	mime = truncateMIME(mime)
	b, _ := json.Marshal(map[string]string{
		"type": "text",
		"text": "[artifact unavailable: " + mime + ", " + size + "]",
	})
	return b
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
