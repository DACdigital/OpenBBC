package bifrost

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

type bytesFetcher struct{ data []byte }

func (f bytesFetcher) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(f.data))), nil
}
func (bytesFetcher) Sign(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("unused")
}
func (bytesFetcher) PreferredDelivery() int                     { return 0 }
func (bytesFetcher) Stat(context.Context, string) (bool, error) { return true, nil }

func ref(mime string, size int64) llm.ArtifactRefBlock {
	return llm.ArtifactRefBlock{StoreID: "MAIN", URI: "sha256/x", MIME: mime, SizeBytes: size}
}

func TestSupportsNative(t *testing.T) {
	l := &LLM{}
	cases := []struct {
		ref  llm.ArtifactRefBlock
		want bool
	}{
		{ref("image/png", 1<<20), true},
		{ref("image/jpeg", 10), true},
		{ref("image/gif", 10), true},
		{ref("image/webp", 10), true},
		{ref("image/png", 4<<20), false}, // base64 ≈ 5.6 MB > 5 MB
		{ref("application/pdf", 10), false},
		{ref("text/csv", 10), false},
		{ref("image/svg+xml", 10), false},
	}
	for _, c := range cases {
		if got := l.SupportsNative(c.ref); got != c.want {
			t.Errorf("SupportsNative(%s, %d) = %v, want %v", c.ref.MIME, c.ref.SizeBytes, got, c.want)
		}
	}
}

func TestNativeRenderBudget(t *testing.T) {
	if b := (&LLM{}).NativeRenderBudget(); b.MaxBytes != 24<<20 || b.MaxBlocks != 20 {
		t.Fatalf("budget = %+v", b)
	}
}

func TestRenderArtifactAsBlock_Image(t *testing.T) {
	b, err := (&LLM{}).RenderArtifactAsBlock(context.Background(), ref("image/png", 3), bytesFetcher{data: []byte("png")})
	if err != nil {
		t.Fatal(err)
	}
	im, ok := b.(llm.InlineMediaBlock)
	if !ok || im.MIME != "image/png" || string(im.Data) != "png" {
		t.Fatalf("block = %#v", b)
	}
}

func TestRenderArtifactAsBlock_Unsupported(t *testing.T) {
	_, err := (&LLM{}).RenderArtifactAsBlock(context.Background(), ref("application/pdf", 3), bytesFetcher{data: []byte("pdf")})
	if !errors.Is(err, llm.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderArtifactAsBlock_ActualBytesOverCap(t *testing.T) {
	big := make([]byte, 4<<20)
	_, err := (&LLM{}).RenderArtifactAsBlock(context.Background(), ref("image/png", 10), bytesFetcher{data: big})
	if !errors.Is(err, llm.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
