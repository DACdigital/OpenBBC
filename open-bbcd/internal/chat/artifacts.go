package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// renderArtifactsForLLM walks the message content lists, converting each
// llm.ArtifactRefBlock into a provider-native inline media block via
// MultimodalRenderer (when both the provider implements it AND the ref's
// store resolves through the resolver), or into a text-surrogate block
// via llm.TextSurrogate otherwise.
//
// The function returns a freshly-allocated []llm.Message so the caller
// can substitute rendered content into the LLM request without mutating
// history in-place. Refs whose store_id doesn't resolve — either because
// the resolver is nil (feature disabled) or the store isn't in the
// registry — fall through to text surrogate; the model still gets a
// human-readable pointer even when the bytes can't be fetched.
//
// Non-ErrUnsupported errors from RenderArtifactAsBlock abort the whole
// operation; the caller fails the turn. This keeps a transient fetch
// error from silently downgrading a native-image call into a surrogate.
func renderArtifactsForLLM(
	ctx context.Context,
	in []llm.Message,
	provider llm.LLM,
	resolver ArtifactFetcherResolver,
) ([]llm.Message, error) {
	// Fast path: if the LLM provider isn't a MultimodalRenderer OR the
	// resolver is nil, every artifact_ref becomes a text surrogate.
	renderer, providerSupports := provider.(llm.MultimodalRenderer)
	if !providerSupports || resolver == nil {
		return substituteAllWithSurrogate(in), nil
	}

	out := make([]llm.Message, len(in))
	for i, m := range in {
		newContent := make([]llm.Block, 0, len(m.Content))
		for _, b := range m.Content {
			ref, isRef := b.(llm.ArtifactRefBlock)
			if !isRef {
				newContent = append(newContent, b)
				continue
			}
			fetcher := resolver(ref.StoreID)
			if fetcher == nil {
				// Store not in registry — the ref will still tell the
				// model there was an attachment; the actual bytes are
				// out of reach.
				newContent = append(newContent, llm.TextSurrogate(ref))
				continue
			}
			rendered, err := renderer.RenderArtifactAsBlock(ctx, ref, fetcher)
			if errors.Is(err, llm.ErrUnsupported) {
				newContent = append(newContent, llm.TextSurrogate(ref))
				continue
			}
			if err != nil {
				return nil, err
			}
			newContent = append(newContent, rendered)
		}
		out[i] = llm.Message{Role: m.Role, Content: newContent}
	}
	return out, nil
}

// substituteAllWithSurrogate walks messages once and replaces every
// ArtifactRefBlock with its TextSurrogate. Used on the fast path
// (feature disabled or provider doesn't support multimodal at all).
func substituteAllWithSurrogate(in []llm.Message) []llm.Message {
	out := make([]llm.Message, len(in))
	for i, m := range in {
		newContent := make([]llm.Block, 0, len(m.Content))
		for _, b := range m.Content {
			if ref, ok := b.(llm.ArtifactRefBlock); ok {
				newContent = append(newContent, llm.TextSurrogate(ref))
				continue
			}
			newContent = append(newContent, b)
		}
		out[i] = llm.Message{Role: m.Role, Content: newContent}
	}
	return out
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

// normaliseToolResult walks a JSON tool-result payload looking for MCP-
// shaped inline media (ImageContent, EmbeddedResource with `blob` or
// `text`) and uploads the extracted bytes to the artifact store,
// producing one ArtifactRefBlock per recognised item. Non-media items
// (TextContent, URI-only EmbeddedResource) stay in the returned
// `remaining` bytes so the model still sees them in the tool_result.
//
// If the payload is not a JSON object with a `content` array — the
// canonical MCP wire shape — the function passes through: nil refs,
// original bytes unchanged, no error. This keeps http_endpoint tool
// results (which are arbitrary REST responses) untouched.
//
// Returns:
//   - refs: newly-created ArtifactRefBlocks for each normalised item.
//   - remaining: the tool result JSON with normalised items removed
//     from the content array. Empty if every content item was
//     normalised (caller may drop the ToolResultBlock in that case,
//     though current code always keeps it for tool_use_id linkage).
//   - err: only fatal errors (upload failures) propagate; malformed
//     payloads pass through.
func normaliseToolResult(
	ctx context.Context,
	payload json.RawMessage,
	uploader ArtifactUploader,
) (refs []llm.Block, remaining json.RawMessage, err error) {
	// Attempt to parse as an MCP-shaped tool result. Failure is not an
	// error — many tool backends return arbitrary JSON.
	var parsed struct {
		Content []json.RawMessage `json:"content"`
		Other   map[string]json.RawMessage
	}
	if len(payload) == 0 {
		return nil, payload, nil
	}
	if e := json.Unmarshal(payload, &parsed); e != nil {
		return nil, payload, nil
	}
	if parsed.Content == nil {
		return nil, payload, nil
	}

	// Rewind: parse into a map to preserve top-level fields other than
	// "content" (some backends attach metadata alongside).
	var top map[string]json.RawMessage
	if e := json.Unmarshal(payload, &top); e != nil {
		return nil, payload, nil
	}

	preserved := make([]json.RawMessage, 0, len(parsed.Content))
	for _, raw := range parsed.Content {
		var item mcpContentItem
		if e := json.Unmarshal(raw, &item); e != nil {
			preserved = append(preserved, raw)
			continue
		}
		switch item.Type {
		case "image":
			bytes, decErr := base64.StdEncoding.DecodeString(item.Data)
			if decErr != nil {
				preserved = append(preserved, raw)
				continue
			}
			mime := item.MIMEType
			if mime == "" {
				mime = "application/octet-stream"
			}
			ref, upErr := uploader.Upload(ctx, mime, bytes)
			if upErr != nil {
				return nil, payload, upErr
			}
			refs = append(refs, ref)
		case "resource":
			if item.Resource == nil {
				preserved = append(preserved, raw)
				continue
			}
			r := item.Resource
			mime := r.MIMEType
			var b []byte
			switch {
			case r.Blob != "":
				decoded, decErr := base64.StdEncoding.DecodeString(r.Blob)
				if decErr != nil {
					preserved = append(preserved, raw)
					continue
				}
				b = decoded
			case r.Text != "":
				if mime == "" {
					mime = "text/plain"
				}
				b = []byte(r.Text)
			default:
				// URI-only EmbeddedResource — preserved as opaque content;
				// framework does not fetch external URIs (see spec § Scope).
				preserved = append(preserved, raw)
				continue
			}
			if mime == "" {
				mime = "application/octet-stream"
			}
			ref, upErr := uploader.Upload(ctx, mime, b)
			if upErr != nil {
				return nil, payload, upErr
			}
			refs = append(refs, ref)
		default:
			// TextContent and unknown types: preserve verbatim.
			preserved = append(preserved, raw)
		}
	}

	// Reserialize the top-level object with the preserved content array
	// (which may be empty if everything got normalised).
	preservedContent, marshalErr := json.Marshal(preserved)
	if marshalErr != nil {
		return refs, payload, nil // fall back to raw output
	}
	top["content"] = preservedContent
	newPayload, marshalErr := json.Marshal(top)
	if marshalErr != nil {
		return refs, payload, nil
	}
	return refs, newPayload, nil
}

// unused: keep errors imported until normaliseToolResult grows a real
// error case beyond the upload failure path.
var _ = errors.New
