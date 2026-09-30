package chat

import (
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// budgetLLM renders image/png natively through the fetcher with a
// configurable budget; everything else is ErrUnsupported.
type budgetLLM struct{ budget llm.RenderBudget }

func (budgetLLM) Name() string { return "budget" }
func (budgetLLM) Generate(context.Context, llm.Request) iter.Seq2[llm.Event, error] {
	return func(func(llm.Event, error) bool) {}
}
func (b budgetLLM) NativeRenderBudget() llm.RenderBudget       { return b.budget }
func (budgetLLM) SupportsNative(ref llm.ArtifactRefBlock) bool { return ref.MIME == "image/png" }
func (budgetLLM) RenderArtifactAsBlock(ctx context.Context, ref llm.ArtifactRefBlock, fetch llm.ArtifactFetcher) (llm.Block, error) {
	if ref.MIME != "image/png" {
		return nil, llm.ErrUnsupported
	}
	rc, err := fetch.Get(ctx, ref.URI)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	return llm.InlineMediaBlock{MIME: ref.MIME, Data: data}, nil
}

// countingFetcher counts Get calls; Stat reports `exists`. Get fails
// with getErr for every URI (or only for failURI when set) and otherwise
// returns data (a 4-byte PNG header when nil).
type countingFetcher struct {
	gets    int
	getErr  error
	failURI string
	data    []byte
	exists  bool
	statErr error
}

func (f *countingFetcher) Get(_ context.Context, uri string) (io.ReadCloser, error) {
	f.gets++
	if f.getErr != nil && (f.failURI == "" || f.failURI == uri) {
		return nil, f.getErr
	}
	data := f.data
	if data == nil {
		data = []byte{0x89, 'P', 'N', 'G'}
	}
	return io.NopCloser(bytesReader(data)), nil
}
func (f *countingFetcher) Sign(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (f *countingFetcher) PreferredDelivery() int                     { return 0 }
func (f *countingFetcher) Stat(context.Context, string) (bool, error) { return f.exists, f.statErr }

func png(uri string, size int64) llm.ArtifactRefBlock {
	return llm.ArtifactRefBlock{StoreID: "MAIN", URI: uri, MIME: "image/png", SizeBytes: size}
}

func render(t *testing.T, l llm.LLM, f llm.ArtifactFetcher, msgs []llm.Message) []llm.Message {
	t.Helper()
	out, err := renderArtifactsForLLM(context.Background(), msgs, l, func(string) llm.ArtifactFetcher { return f }, newRenderCache(), slog.Default())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return out
}

func isInline(b llm.Block) bool { _, ok := b.(llm.InlineMediaBlock); return ok }

func TestRenderBudget_MaxBlocks_NewestMessagesWin(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/1", 10)}},
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/2", 10)}},
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/3", 10)}},
	}
	out := render(t, budgetLLM{budget: llm.RenderBudget{MaxBlocks: 2}}, &countingFetcher{}, msgs)
	if isInline(out[0].Content[0]) || !isInline(out[1].Content[0]) || !isInline(out[2].Content[0]) {
		t.Fatalf("want oldest surrogate, two newest inline; got %#v", out)
	}
}

func TestRenderBudget_LastBlockInMessageChargedFirst(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{png("sha256/A", 10), png("sha256/B", 10)}}}
	out := render(t, budgetLLM{budget: llm.RenderBudget{MaxBlocks: 1}}, &countingFetcher{}, msgs)
	if isInline(out[0].Content[0]) || !isInline(out[0].Content[1]) {
		t.Fatalf("want A surrogate, B inline; got %#v", out[0].Content)
	}
}

func TestRenderBudget_MaxBytes_Base64Expanded(t *testing.T) {
	// 300 bytes → 400 base64 each. Budget 900: two fit (800), the third would be 1200.
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/1", 300)}},
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/2", 300)}},
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/3", 300)}},
	}
	out := render(t, budgetLLM{budget: llm.RenderBudget{MaxBytes: 900}}, &countingFetcher{}, msgs)
	if isInline(out[0].Content[0]) || !isInline(out[1].Content[0]) || !isInline(out[2].Content[0]) {
		t.Fatalf("got %#v", out)
	}
}

func TestRenderBudget_UnsupportedRefsAreNotCharged(t *testing.T) {
	zip := llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/z", MIME: "application/zip", SizeBytes: 10}
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/1", 10)}},
		{Role: llm.RoleUser, Content: []llm.Block{zip}},
	}
	f := &countingFetcher{}
	out := render(t, budgetLLM{budget: llm.RenderBudget{MaxBlocks: 1}}, f, msgs)
	if !isInline(out[0].Content[0]) {
		t.Fatalf("older png must still be inline; the zip surrogate costs nothing: %#v", out)
	}
	if f.gets != 1 {
		t.Fatalf("gets = %d, want 1 (only the png is fetched)", f.gets)
	}
}

func TestRenderBudget_NilFetcherIsNotCharged(t *testing.T) {
	gone := png("sha256/gone", 10)
	gone.StoreID = "GONE"
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/1", 10)}},
		{Role: llm.RoleUser, Content: []llm.Block{gone}},
	}
	f := &countingFetcher{}
	resolver := func(id string) llm.ArtifactFetcher {
		if id == "GONE" {
			return nil
		}
		return f
	}
	out, err := renderArtifactsForLLM(context.Background(), msgs, budgetLLM{budget: llm.RenderBudget{MaxBlocks: 1}},
		resolver, newRenderCache(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if !isInline(out[0].Content[0]) || isInline(out[1].Content[0]) {
		t.Fatalf("want older MAIN inline, newer GONE surrogate; got %#v", out)
	}
}

func TestRenderCache_FetchedOnceAcrossPasses(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{png("sha256/1", 10)}}}
	f := &countingFetcher{}
	resolver := func(string) llm.ArtifactFetcher { return f }
	cache := newRenderCache()
	for i := 0; i < 2; i++ {
		out, err := renderArtifactsForLLM(context.Background(), msgs, budgetLLM{}, resolver, cache, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		if !isInline(out[0].Content[0]) {
			t.Fatalf("pass %d: want inline, got %T", i, out[0].Content[0])
		}
	}
	if f.gets != 1 {
		t.Fatalf("gets = %d, want 1 across two passes", f.gets)
	}
}

func TestRender_DoesNotMutateInput(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{png("sha256/1", 10)}}}
	_ = render(t, budgetLLM{}, &countingFetcher{}, msgs)
	if _, ok := msgs[0].Content[0].(llm.ArtifactRefBlock); !ok {
		t.Fatal("input message list was mutated")
	}
}

func TestRenderBudget_OverBudgetRefsAreNotFetched(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/1", 10)}},
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/2", 10)}},
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/3", 10)}},
	}
	f := &countingFetcher{}
	_ = render(t, budgetLLM{budget: llm.RenderBudget{MaxBlocks: 2}}, f, msgs)
	if f.gets != 2 {
		t.Fatalf("fetcher gets = %d, want 2 (the over-budget ref and older must not be fetched)", f.gets)
	}
}

func TestRenderBudget_OversizedRefIsNotFetched(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{png("sha256/big", 3000)}}}
	f := &countingFetcher{}
	out := render(t, budgetLLM{budget: llm.RenderBudget{MaxBytes: 1000}}, f, msgs)
	if isInline(out[0].Content[0]) || f.gets != 0 {
		t.Fatalf("oversized ref: inline=%v gets=%d, want surrogate and no fetch", isInline(out[0].Content[0]), f.gets)
	}
}

func TestRenderBudget_ExactCapFits(t *testing.T) {
	// 300 bytes → 400 base64 each; two exactly fill MaxBytes 800.
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/1", 300)}},
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/2", 300)}},
	}
	out := render(t, budgetLLM{budget: llm.RenderBudget{MaxBytes: 800}}, &countingFetcher{}, msgs)
	if !isInline(out[0].Content[0]) || !isInline(out[1].Content[0]) {
		t.Fatalf("both refs must be inline at the exact cap: %#v", out)
	}
}

func TestRenderBudget_ChargesActualBytesWhenSizeUnderReported(t *testing.T) {
	// SizeBytes claims 1 byte, but the blob is 600 bytes (800 base64).
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{png("sha256/liar", 1)}}}
	f := &countingFetcher{data: make([]byte, 600)}
	out := render(t, budgetLLM{budget: llm.RenderBudget{MaxBytes: 500}}, f, msgs)
	if isInline(out[0].Content[0]) {
		t.Fatalf("under-reported ref exceeding MaxBytes must be a surrogate, got %T", out[0].Content[0])
	}
}

func TestRenderBudget_FailingFetchForOverBudgetRefDoesNotFail(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/broken", 10)}},
		{Role: llm.RoleUser, Content: []llm.Block{png("sha256/ok", 10)}},
	}
	f := &countingFetcher{getErr: errors.New("boom"), failURI: "sha256/broken"}
	out, err := renderArtifactsForLLM(context.Background(), msgs, budgetLLM{budget: llm.RenderBudget{MaxBlocks: 1}},
		func(string) llm.ArtifactFetcher { return f }, newRenderCache(), slog.Default())
	if err != nil {
		t.Fatalf("over-budget ref with a failing fetcher must not fail the render: %v", err)
	}
	if isInline(out[0].Content[0]) || !isInline(out[1].Content[0]) {
		t.Fatalf("want broken surrogate, ok inline; got %#v", out)
	}
}

func TestRenderCache_PrunesKeysNotPlacedNatively(t *testing.T) {
	l := budgetLLM{budget: llm.RenderBudget{MaxBlocks: 1}}
	f := &countingFetcher{}
	resolver := func(string) llm.ArtifactFetcher { return f }
	cache := newRenderCache()
	a, b := png("sha256/A", 10), png("sha256/B", 10)
	keyA := renderKey{a.StoreID, a.URI, a.MIME}
	keyB := renderKey{b.StoreID, b.URI, b.MIME}

	pass1 := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{a}}}
	if _, err := renderArtifactsForLLM(context.Background(), pass1, l, resolver, cache, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache[keyA]; !ok {
		t.Fatal("A should be cached after pass 1")
	}

	pass2 := append(pass1, llm.Message{Role: llm.RoleUser, Content: []llm.Block{b}})
	if _, err := renderArtifactsForLLM(context.Background(), pass2, l, resolver, cache, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache[keyA]; ok {
		t.Fatal("A fell out of the native window and must be pruned from the cache")
	}
	if _, ok := cache[keyB]; !ok {
		t.Fatal("B should be cached after pass 2")
	}
}

func TestRender_MissingBlobBecomesSurrogate(t *testing.T) {
	f := &countingFetcher{getErr: errors.New("403 AccessDenied"), exists: false}
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{png("sha256/gone", 10)}}}
	out := render(t, budgetLLM{}, f, msgs)
	if _, ok := out[0].Content[0].(llm.TextBlock); !ok {
		t.Fatalf("missing blob must render as surrogate, got %#v", out[0].Content[0])
	}
}

func TestRender_TransientFetchErrorFailsWhenBlobExists(t *testing.T) {
	f := &countingFetcher{getErr: errors.New("connection reset"), exists: true}
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{png("sha256/x", 10)}}}
	_, err := renderArtifactsForLLM(context.Background(), msgs, budgetLLM{}, func(string) llm.ArtifactFetcher { return f }, newRenderCache(), slog.Default())
	if err == nil {
		t.Fatal("transient error on an existing blob must fail the render")
	}
}

func TestRender_StatErrorFails(t *testing.T) {
	f := &countingFetcher{getErr: errors.New("timeout"), statErr: errors.New("timeout")}
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{png("sha256/x", 10)}}}
	_, err := renderArtifactsForLLM(context.Background(), msgs, budgetLLM{}, func(string) llm.ArtifactFetcher { return f }, newRenderCache(), slog.Default())
	if err == nil {
		t.Fatal("Stat error must fail the render")
	}
}
