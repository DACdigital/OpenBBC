package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// FetchBytes reads the blob addressed by uri through the fetcher's preferred
// delivery mode: bytes mode gets a direct Get, signed-URL mode gets a Sign and
// a follow. The signed-URL TTL is deliberately short (60s): the caller uses
// the URL immediately and inlines the bytes into the LLM request, so a long
// TTL adds no value and risks the URL leaking. Shared by every adapter that
// implements MultimodalRenderer.
func FetchBytes(ctx context.Context, uri string, fetch ArtifactFetcher) ([]byte, error) {
	// PreferredDelivery uses the integer contract on ArtifactFetcher:
	// 0 = DeliveryBytes, 1 = DeliverySignedURL.
	if fetch.PreferredDelivery() == 0 {
		rc, err := fetch.Get(ctx, uri)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	url, err := fetch.Sign(ctx, uri, 60*time.Second)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("llm: signed URL fetch returned status " + resp.Status)
	}
	return io.ReadAll(resp.Body)
}
