package chat

import (
	"context"
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
func (b budgetLLM) NativeRenderBudget() llm.RenderBudget { return b.budget }
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

// countingFetcher counts Get calls; Stat reports `exists`.
type countingFetcher struct {
	gets    int
	getErr  error
	exists  bool
	statErr error
}

func (f *countingFetcher) Get(context.Context, string) (io.ReadCloser, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return io.NopCloser(bytesReader([]byte{0x89, 'P', 'N', 'G'})), nil
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
	out := render(t, budgetLLM{budget: llm.RenderBudget{MaxBlocks: 1}}, &countingFetcher{}, msgs)
	if !isInline(out[0].Content[0]) {
		t.Fatalf("older png must still be inline; the zip surrogate costs nothing: %#v", out)
	}
}

func TestRender_DoesNotMutateInput(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{png("sha256/1", 10)}}}
	_ = render(t, budgetLLM{}, &countingFetcher{}, msgs)
	if _, ok := msgs[0].Content[0].(llm.ArtifactRefBlock); !ok {
		t.Fatal("input message list was mutated")
	}
}
