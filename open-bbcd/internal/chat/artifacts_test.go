package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// --- test fakes --------------------------------------------------------

// mmLLM is a fake LLM that also implements MultimodalRenderer. Renders
// PNG as InlineMediaBlock; refuses everything else with ErrUnsupported.
type mmLLM struct{}

func (mmLLM) Name() string { return "mm-fake" }
func (mmLLM) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {}
}
func (mmLLM) RenderArtifactAsBlock(ctx context.Context, ref llm.ArtifactRefBlock, fetch llm.ArtifactFetcher) (llm.Block, error) {
	if ref.MIME != "image/png" {
		return nil, llm.ErrUnsupported
	}
	rc, err := fetch.Get(ctx, ref.URI)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return llm.InlineMediaBlock{MIME: ref.MIME, Data: data}, nil
}

// plainLLM is a fake LLM that does NOT implement MultimodalRenderer.
type plainLLM struct{}

func (plainLLM) Name() string { return "plain" }
func (plainLLM) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {}
}

// stubFetcher returns canned bytes on Get; Sign not exercised here.
type stubFetcher struct {
	bytes []byte
	err   error
}

func (f stubFetcher) Get(ctx context.Context, uri string) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(bytesReader(f.bytes)), nil
}
func (f stubFetcher) Sign(ctx context.Context, uri string, ttl time.Duration) (string, error) {
	return "", errors.New("not implemented")
}
func (f stubFetcher) PreferredDelivery() int { return 0 }

// bytesReader shim for the tests only.
func bytesReader(b []byte) io.Reader {
	return &sliceReader{data: b}
}

type sliceReader struct {
	data []byte
	pos  int
}

func (r *sliceReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// --- renderArtifactsForLLM tests --------------------------------------

func TestRenderArtifacts_NoResolverProducesSurrogates(t *testing.T) {
	msgs := []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.Block{
			llm.TextBlock{Text: "look at this:"},
			llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/abc", MIME: "image/png", Filename: "photo.png"},
		},
	}}
	got, err := renderArtifactsForLLM(context.Background(), msgs, mmLLM{}, nil)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(got) != 1 || len(got[0].Content) != 2 {
		t.Fatalf("shape mismatch: %#v", got)
	}
	if _, ok := got[0].Content[1].(llm.TextBlock); !ok {
		t.Errorf("artifact_ref not converted to TextBlock surrogate: %T", got[0].Content[1])
	}
}

func TestRenderArtifacts_ProviderNotMultimodal_ProducesSurrogates(t *testing.T) {
	msgs := []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.Block{
			llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/abc", MIME: "image/png", Filename: "photo.png"},
		},
	}}
	resolver := func(id string) llm.ArtifactFetcher { return stubFetcher{bytes: []byte{0x89}} }
	got, err := renderArtifactsForLLM(context.Background(), msgs, plainLLM{}, resolver)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if _, ok := got[0].Content[0].(llm.TextBlock); !ok {
		t.Errorf("expected surrogate when provider does not implement MultimodalRenderer, got %T", got[0].Content[0])
	}
}

func TestRenderArtifacts_SupportedMIMEProducesInlineMedia(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	msgs := []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.Block{
			llm.TextBlock{Text: "check:"},
			llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/abc", MIME: "image/png"},
		},
	}}
	resolver := func(id string) llm.ArtifactFetcher {
		if id == "MAIN" {
			return stubFetcher{bytes: pngBytes}
		}
		return nil
	}
	got, err := renderArtifactsForLLM(context.Background(), msgs, mmLLM{}, resolver)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	media, ok := got[0].Content[1].(llm.InlineMediaBlock)
	if !ok {
		t.Fatalf("expected InlineMediaBlock, got %T", got[0].Content[1])
	}
	if media.MIME != "image/png" || string(media.Data) != string(pngBytes) {
		t.Errorf("InlineMediaBlock content mismatch: %+v", media)
	}
}

func TestRenderArtifacts_UnknownStoreProducesSurrogate(t *testing.T) {
	msgs := []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.Block{
			llm.ArtifactRefBlock{StoreID: "UNKNOWN", URI: "sha256/abc", MIME: "image/png", Filename: "x.png"},
		},
	}}
	resolver := func(id string) llm.ArtifactFetcher { return nil } // always nil
	got, err := renderArtifactsForLLM(context.Background(), msgs, mmLLM{}, resolver)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if _, ok := got[0].Content[0].(llm.TextBlock); !ok {
		t.Errorf("expected surrogate when store not in registry")
	}
}

// --- normaliseToolResult tests ----------------------------------------

// stubUploader records uploads and returns predictable refs.
type stubUploader struct {
	uploads int
	fail    bool
}

func (u *stubUploader) Upload(ctx context.Context, mime string, data []byte) (llm.ArtifactRefBlock, error) {
	u.uploads++
	if u.fail {
		return llm.ArtifactRefBlock{}, errors.New("upload failed")
	}
	return llm.ArtifactRefBlock{
		StoreID:   "MAIN",
		URI:       "sha256/stub",
		MIME:      mime,
		SizeBytes: int64(len(data)),
		Sha256:    "stub",
	}, nil
}

func TestNormalise_ImageContentUploaded(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}]}`)
	refs, remaining, err := normaliseToolResult(context.Background(), payload, up)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("len(refs) = %d, want 1", len(refs))
	}
	if up.uploads != 1 {
		t.Errorf("uploads = %d, want 1", up.uploads)
	}
	// content array in remaining should be empty (item was normalised).
	var parsed struct{ Content []json.RawMessage }
	_ = json.Unmarshal(remaining, &parsed)
	if len(parsed.Content) != 0 {
		t.Errorf("remaining content = %v, want empty", parsed.Content)
	}
}

func TestNormalise_EmbeddedResourceWithBlob(t *testing.T) {
	up := &stubUploader{}
	inline := base64.StdEncoding.EncodeToString([]byte("PDF data"))
	payload := []byte(`{"content":[{"type":"resource","resource":{"uri":"file:///x","mimeType":"application/pdf","blob":"` + inline + `"}}]}`)
	refs, _, err := normaliseToolResult(context.Background(), payload, up)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("len(refs) = %d, want 1", len(refs))
	}
}

func TestNormalise_EmbeddedResourceWithText(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"resource","resource":{"uri":"file:///x","mimeType":"text/csv","text":"a,b\n1,2"}}]}`)
	refs, _, err := normaliseToolResult(context.Background(), payload, up)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("len(refs) = %d, want 1", len(refs))
	}
	ref := refs[0].(llm.ArtifactRefBlock)
	if ref.MIME != "text/csv" {
		t.Errorf("MIME = %q, want text/csv", ref.MIME)
	}
}

func TestNormalise_URIOnlyEmbeddedResource_PassesThrough(t *testing.T) {
	up := &stubUploader{}
	// EmbeddedResource with NO blob or text — should be passed through.
	payload := []byte(`{"content":[{"type":"resource","resource":{"uri":"file:///x","mimeType":"application/pdf"}}]}`)
	refs, remaining, err := normaliseToolResult(context.Background(), payload, up)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("URI-only resource should NOT be uploaded; refs=%v", refs)
	}
	if up.uploads != 0 {
		t.Errorf("uploads = %d, want 0", up.uploads)
	}
	// Item should remain in content.
	var parsed struct{ Content []json.RawMessage }
	_ = json.Unmarshal(remaining, &parsed)
	if len(parsed.Content) != 1 {
		t.Errorf("URI-only item should be preserved in remaining content, got %v", parsed.Content)
	}
}

func TestNormalise_TextContentPreserved(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"text","text":"result summary"}]}`)
	refs, remaining, err := normaliseToolResult(context.Background(), payload, up)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("text should NOT be normalised; refs=%v", refs)
	}
	if up.uploads != 0 {
		t.Errorf("uploads = %d, want 0 for text content", up.uploads)
	}
	if string(remaining) == "" {
		t.Errorf("remaining should preserve original payload")
	}
}

func TestNormalise_NonMCPShape_PassesThrough(t *testing.T) {
	up := &stubUploader{}
	// Arbitrary REST response (no content array).
	payload := []byte(`{"status":"ok","result":42}`)
	refs, remaining, err := normaliseToolResult(context.Background(), payload, up)
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("non-MCP payload should be passed through untouched")
	}
	if string(remaining) != string(payload) {
		t.Errorf("payload should be preserved verbatim; got %s", string(remaining))
	}
}

func TestNormalise_UploadErrorPropagates(t *testing.T) {
	up := &stubUploader{fail: true}
	payload := []byte(`{"content":[{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}]}`)
	_, _, err := normaliseToolResult(context.Background(), payload, up)
	if err == nil {
		t.Fatal("expected upload error to propagate")
	}
}
