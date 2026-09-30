package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"log/slog"
	"strings"
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
func (mmLLM) NativeRenderBudget() llm.RenderBudget { return llm.RenderBudget{} }
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
	got, err := renderArtifactsForLLM(context.Background(), msgs, mmLLM{}, nil, newRenderCache(), slog.Default())
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
	got, err := renderArtifactsForLLM(context.Background(), msgs, plainLLM{}, resolver, newRenderCache(), slog.Default())
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
	got, err := renderArtifactsForLLM(context.Background(), msgs, mmLLM{}, resolver, newRenderCache(), slog.Default())
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
	got, err := renderArtifactsForLLM(context.Background(), msgs, mmLLM{}, resolver, newRenderCache(), slog.Default())
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if _, ok := got[0].Content[0].(llm.TextBlock); !ok {
		t.Errorf("expected surrogate when store not in registry")
	}
}

// --- normaliseToolResult tests ----------------------------------------

// stubUploader records uploads and returns predictable refs. failCalls
// lists 1-based upload call numbers that fail.
type stubUploader struct {
	uploads   int
	failCalls map[int]bool
}

func (u *stubUploader) Upload(ctx context.Context, mime string, data []byte) (llm.ArtifactRefBlock, error) {
	u.uploads++
	if u.failCalls[u.uploads] {
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

func remainingContent(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var parsed struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("remaining not an MCP result: %v (%s)", err, raw)
	}
	return parsed.Content
}

func TestNormalise_ImageContentUploaded(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if len(res.Refs) != 1 || up.uploads != 1 {
		t.Fatalf("refs=%d uploads=%d, want 1/1", len(res.Refs), up.uploads)
	}
	if res.ForceError {
		t.Fatal("ForceError set on success")
	}
	if c := remainingContent(t, res.Output); len(c) != 0 {
		t.Errorf("remaining content = %v, want empty", c)
	}
}

func TestNormalise_EmbeddedResourceWithBlob(t *testing.T) {
	up := &stubUploader{}
	inline := base64.StdEncoding.EncodeToString([]byte("PDF data"))
	payload := []byte(`{"content":[{"type":"resource","resource":{"uri":"file:///x","mimeType":"application/pdf","blob":"` + inline + `"}}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if len(res.Refs) != 1 {
		t.Errorf("len(refs) = %d, want 1", len(res.Refs))
	}
}

func TestNormalise_EmbeddedResourceWithText(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"resource","resource":{"uri":"file:///x","mimeType":"text/csv","text":"a,b\n1,2"}}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if len(res.Refs) != 1 || res.Refs[0].MIME != "text/csv" {
		t.Fatalf("refs = %#v, want one text/csv ref", res.Refs)
	}
}

func TestNormalise_URIOnlyEmbeddedResource_PassesThrough(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"resource","resource":{"uri":"file:///x","mimeType":"application/pdf"}}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if len(res.Refs) != 0 || up.uploads != 0 {
		t.Fatalf("URI-only resource must not be uploaded: refs=%v uploads=%d", res.Refs, up.uploads)
	}
	if c := remainingContent(t, res.Output); len(c) != 1 {
		t.Errorf("URI-only item should be preserved, got %v", c)
	}
}

func TestNormalise_TextContentPreserved(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"text","text":"result summary"}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if len(res.Refs) != 0 || up.uploads != 0 {
		t.Fatalf("text must not be normalised")
	}
	if c := remainingContent(t, res.Output); len(c) != 1 || c[0]["text"] != "result summary" {
		t.Errorf("text item not preserved: %v", c)
	}
}

func TestNormalise_NonMCPShape_PassesThrough(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"status":"ok","result":42}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if len(res.Refs) != 0 || string(res.Output) != string(payload) {
		t.Fatalf("non-MCP payload must pass through verbatim; got %s", res.Output)
	}
}

func TestNormalise_UploadFailureBecomesTextNote(t *testing.T) {
	up := &stubUploader{failCalls: map[int]bool{1: true}}
	payload := []byte(`{"content":[{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if len(res.Refs) != 0 {
		t.Fatalf("no ref may be produced for a failed upload")
	}
	if strings.Contains(string(res.Output), "aGVsbG8=") {
		t.Fatalf("base64 leaked into remainder: %s", res.Output)
	}
	c := remainingContent(t, res.Output)
	if len(c) != 1 || c[0]["type"] != "text" || !strings.HasPrefix(c[0]["text"].(string), "[artifact unavailable: ") {
		t.Fatalf("failed item not replaced by a note: %v", c)
	}
	if c[0]["text"] != "[artifact unavailable: application/octet-stream, 5 B]" {
		// "hello" declared image/png does not sniff as an image, so the
		// resolved MIME is octet-stream; the size is the decoded 5 bytes.
		t.Errorf("note = %q", c[0]["text"])
	}
}

func TestNormalise_UndecodableBase64BecomesNoteWithUnknownSize(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"image","data":"%%%not-base64%%%","mimeType":"image/png"}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if up.uploads != 0 {
		t.Fatalf("undecodable item must not be uploaded")
	}
	c := remainingContent(t, res.Output)
	if len(c) != 1 || c[0]["text"] != "[artifact unavailable: image/png, unknown]" {
		t.Fatalf("remainder = %v", c)
	}
}

func TestNormalise_OnlyFailingItemIsReplaced(t *testing.T) {
	up := &stubUploader{failCalls: map[int]bool{2: true}}
	img := `{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}`
	payload := []byte(`{"content":[` + img + `,` + img + `]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if len(res.Refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(res.Refs))
	}
	c := remainingContent(t, res.Output)
	if len(c) != 1 || !strings.HasPrefix(c[0]["text"].(string), "[artifact unavailable: ") {
		t.Fatalf("remainder = %v, want exactly one note", c)
	}
}

func TestNormalise_MarshalFailureBecomesUnavailableError(t *testing.T) {
	orig := marshalJSON
	marshalJSON = func(any) ([]byte, error) { return nil, errors.New("boom") }
	defer func() { marshalJSON = orig }()

	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if !res.ForceError {
		t.Fatal("ForceError not set on re-marshal failure")
	}
	if string(res.Output) != `{"content":[{"type":"text","text":"[tool result unavailable]"}]}` {
		t.Fatalf("output = %s", res.Output)
	}
	if len(res.Refs) != 1 {
		t.Errorf("refs uploaded before the failure must be kept, got %d", len(res.Refs))
	}
	if strings.Contains(string(res.Output), "aGVsbG8=") {
		t.Fatal("raw payload returned")
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNormalise_MistypedMediaItemBecomesNote(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"image","data":"aGVsbG8=","mimeType":123}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if strings.Contains(string(res.Output), "aGVsbG8=") {
		t.Fatalf("base64 leaked: %s", res.Output)
	}
	c := remainingContent(t, res.Output)
	if len(c) != 1 || c[0]["text"] != "[artifact unavailable: unknown, unknown]" {
		t.Fatalf("remainder = %v", c)
	}
}

func TestNormalise_NonObjectItemStaysVerbatim(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[42]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if string(res.Output) != `{"content":[42]}` {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestNormalise_AudioContentUploaded(t *testing.T) {
	up := &stubUploader{}
	payload := []byte(`{"content":[{"type":"audio","data":"aGVsbG8=","mimeType":"audio/wav"}]}`)
	res := normaliseToolResult(context.Background(), payload, up, testLogger(), "Skill")
	if len(res.Refs) != 1 || up.uploads != 1 {
		t.Fatalf("refs=%d uploads=%d, want 1/1", len(res.Refs), up.uploads)
	}
	if c := remainingContent(t, res.Output); len(c) != 0 {
		t.Errorf("remaining = %v, want empty", c)
	}
}

func TestUnavailableNote_CapsMIME(t *testing.T) {
	n := string(unavailableNote(strings.Repeat("a", 500), "5 B"))
	if strings.Contains(n, strings.Repeat("a", 101)) {
		t.Fatalf("mime not capped: %s", n)
	}
}

func TestNormalise_UnknownTypeWithDataBecomesNote(t *testing.T) {
	for _, item := range []string{
		`{"type":"video","data":"aGVsbG8="}`,
		`{"data":"aGVsbG8="}`,
		`{"type":"resource","resource":{"uri":"file:///x","blob":"aGVsbG8="},"blob":""}`,
	} {
		up := &stubUploader{}
		res := normaliseToolResult(context.Background(), []byte(`{"content":[`+item+`]}`), up, testLogger(), "Skill")
		if strings.Contains(string(res.Output), "aGVsbG8=") {
			t.Fatalf("base64 leaked for %s: %s", item, res.Output)
		}
	}
	up := &stubUploader{}
	res := normaliseToolResult(context.Background(), []byte(`{"content":[{"type":"video","data":"aGVsbG8="}]}`), up, testLogger(), "Skill")
	c := remainingContent(t, res.Output)
	if len(c) != 1 || c[0]["text"] != "[artifact unavailable: unknown, unknown]" {
		t.Fatalf("remainder = %v", c)
	}
}
