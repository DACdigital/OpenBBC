package bifrost

import (
	"context"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// supportedImageMIMEs are rendered natively as image_url data URIs; every
// other MIME (PDFs included) falls back to the framework's text surrogate.
var supportedImageMIMEs = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// Media limits: the anthropic adapter's values (internal/llm/anthropic), which
// are conservative for the OpenAI and Gemini vision limits too.
const (
	maxImageBase64Bytes   = 5_000_000
	maxRequestMediaBytes  = 24 << 20
	maxRequestMediaBlocks = 20
)

func base64Len(n int64) int64 { return ((n + 2) / 3) * 4 }

func nativeRenderable(ref llm.ArtifactRefBlock) bool {
	return supportedImageMIMEs[ref.MIME] && base64Len(ref.SizeBytes) <= maxImageBase64Bytes
}

// SupportsNative implements llm.MultimodalRenderer. Must not fetch.
func (l *LLM) SupportsNative(ref llm.ArtifactRefBlock) bool { return nativeRenderable(ref) }

// NativeRenderBudget implements llm.MultimodalRenderer.
func (l *LLM) NativeRenderBudget() llm.RenderBudget {
	return llm.RenderBudget{MaxBytes: maxRequestMediaBytes, MaxBlocks: maxRequestMediaBlocks}
}

// RenderArtifactAsBlock implements llm.MultimodalRenderer: supported images
// become an InlineMediaBlock (sent as a data URI); anything else returns
// llm.ErrUnsupported so the caller substitutes a text surrogate.
func (l *LLM) RenderArtifactAsBlock(ctx context.Context, ref llm.ArtifactRefBlock, fetch llm.ArtifactFetcher) (llm.Block, error) {
	if !nativeRenderable(ref) {
		return nil, llm.ErrUnsupported
	}
	data, err := llm.FetchBytes(ctx, ref.URI, fetch)
	if err != nil {
		return nil, err
	}
	// Re-check on actual bytes: the declared SizeBytes may be wrong.
	if base64Len(int64(len(data))) > maxImageBase64Bytes {
		return nil, llm.ErrUnsupported
	}
	return llm.InlineMediaBlock{MIME: ref.MIME, Data: data}, nil
}

var _ llm.MultimodalRenderer = (*LLM)(nil)
