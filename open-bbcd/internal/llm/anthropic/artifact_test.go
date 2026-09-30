package anthropic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// stubFetcher is a hand-rolled llm.ArtifactFetcher for tests. Controls
// PreferredDelivery + returns canned bytes on Get / canned URL on Sign.
type stubFetcher struct {
	delivery   int
	bytes      []byte
	signedURL  string
	getErr     error
	signErr    error
	getCalls   int
	signCalls  int
}

func (f *stubFetcher) PreferredDelivery() int { return f.delivery }
func (f *stubFetcher) Stat(context.Context, string) (bool, error) { return true, nil }
func (f *stubFetcher) Get(ctx context.Context, uri string) (io.ReadCloser, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return io.NopCloser(bytes.NewReader(f.bytes)), nil
}
func (f *stubFetcher) Sign(ctx context.Context, uri string, ttl time.Duration) (string, error) {
	f.signCalls++
	if f.signErr != nil {
		return "", f.signErr
	}
	return f.signedURL, nil
}

func TestRenderArtifactAsBlock_UnsupportedMIME(t *testing.T) {
	l := New(config.AnthropicConfig{APIKey: "test"})
	f := &stubFetcher{delivery: 0, bytes: []byte("payload")}

	_, err := l.RenderArtifactAsBlock(context.Background(), llm.ArtifactRefBlock{
		StoreID: "MAIN",
		URI:     "sha256/abc",
		MIME:    "application/zip",
	}, f)
	if !errors.Is(err, llm.ErrUnsupported) {
		t.Fatalf("RenderArtifactAsBlock(zip) = %v, want ErrUnsupported", err)
	}
	// Must NOT have fetched bytes for an unsupported MIME.
	if f.getCalls != 0 || f.signCalls != 0 {
		t.Errorf("unsupported MIME triggered a fetch: get=%d sign=%d", f.getCalls, f.signCalls)
	}
}

func TestRenderArtifactAsBlock_ImagePNG_BytesMode(t *testing.T) {
	l := New(config.AnthropicConfig{APIKey: "test"})
	payload := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG header
	f := &stubFetcher{delivery: 0, bytes: payload}

	got, err := l.RenderArtifactAsBlock(context.Background(), llm.ArtifactRefBlock{
		StoreID: "MAIN",
		URI:     "sha256/abc",
		MIME:    "image/png",
	}, f)
	if err != nil {
		t.Fatalf("RenderArtifactAsBlock(png) = err %v", err)
	}
	inline, ok := got.(llm.InlineMediaBlock)
	if !ok {
		t.Fatalf("expected InlineMediaBlock, got %T", got)
	}
	if inline.MIME != "image/png" {
		t.Errorf("MIME = %q, want image/png", inline.MIME)
	}
	if !bytes.Equal(inline.Data, payload) {
		t.Errorf("Data = %v, want %v", inline.Data, payload)
	}
	if f.getCalls != 1 {
		t.Errorf("Get called %d times, want 1", f.getCalls)
	}
}

func TestRenderArtifactAsBlock_PDF_BytesMode(t *testing.T) {
	l := New(config.AnthropicConfig{APIKey: "test"})
	payload := []byte("%PDF-1.4\n...")
	f := &stubFetcher{delivery: 0, bytes: payload}

	got, err := l.RenderArtifactAsBlock(context.Background(), llm.ArtifactRefBlock{
		StoreID: "MAIN",
		URI:     "sha256/abc",
		MIME:    "application/pdf",
	}, f)
	if err != nil {
		t.Fatalf("RenderArtifactAsBlock(pdf) = err %v", err)
	}
	inline, ok := got.(llm.InlineMediaBlock)
	if !ok {
		t.Fatalf("expected InlineMediaBlock, got %T", got)
	}
	if inline.MIME != "application/pdf" {
		t.Errorf("MIME = %q, want application/pdf", inline.MIME)
	}
}

func TestRenderArtifactAsBlock_SignedURLMode(t *testing.T) {
	payload := []byte{0x89, 0x50, 0x4E, 0x47}
	// Local HTTP server that returns the payload — simulates the presigned
	// blob-store URL the fetcher would sign to.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	l := New(config.AnthropicConfig{APIKey: "test"})
	f := &stubFetcher{delivery: 1, signedURL: server.URL}

	got, err := l.RenderArtifactAsBlock(context.Background(), llm.ArtifactRefBlock{
		StoreID: "MAIN",
		URI:     "sha256/abc",
		MIME:    "image/png",
	}, f)
	if err != nil {
		t.Fatalf("RenderArtifactAsBlock(png via signed url) = err %v", err)
	}
	inline, ok := got.(llm.InlineMediaBlock)
	if !ok {
		t.Fatalf("expected InlineMediaBlock, got %T", got)
	}
	if !bytes.Equal(inline.Data, payload) {
		t.Errorf("Data = %v, want %v", inline.Data, payload)
	}
	// Must have taken the Sign path, not Get.
	if f.signCalls != 1 || f.getCalls != 0 {
		t.Errorf("expected exactly one Sign call and zero Get calls; get=%d sign=%d", f.getCalls, f.signCalls)
	}
}

func TestRenderArtifactAsBlock_SignedURL_ServerReturns500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	l := New(config.AnthropicConfig{APIKey: "test"})
	f := &stubFetcher{delivery: 1, signedURL: server.URL}

	_, err := l.RenderArtifactAsBlock(context.Background(), llm.ArtifactRefBlock{
		URI:  "sha256/abc",
		MIME: "image/png",
	}, f)
	if err == nil {
		t.Fatal("expected error for 500 from signed URL")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status, got: %v", err)
	}
}

func TestRenderArtifactAsBlock_BytesModeError(t *testing.T) {
	l := New(config.AnthropicConfig{APIKey: "test"})
	f := &stubFetcher{delivery: 0, getErr: errors.New("network timeout")}

	_, err := l.RenderArtifactAsBlock(context.Background(), llm.ArtifactRefBlock{
		URI:  "sha256/abc",
		MIME: "image/png",
	}, f)
	if err == nil {
		t.Fatal("expected error to propagate from Get")
	}
	if !strings.Contains(err.Error(), "network timeout") {
		t.Errorf("error should propagate underlying, got: %v", err)
	}
}
