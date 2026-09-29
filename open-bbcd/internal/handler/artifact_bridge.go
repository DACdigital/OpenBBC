package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// artifactFetcherShim adapts artifacts.ArtifactStore to llm.ArtifactFetcher.
// The shim converts DeliveryMode (typed) to int (llm's cross-package
// contract) so llm has no dependency on the artifacts package.
type artifactFetcherShim struct {
	store artifacts.ArtifactStore
}

func (s artifactFetcherShim) Get(ctx context.Context, uri string) (io.ReadCloser, error) {
	return s.store.Get(ctx, uri)
}
func (s artifactFetcherShim) Sign(ctx context.Context, uri string, ttl time.Duration) (string, error) {
	return s.store.Sign(ctx, uri, ttl)
}
func (s artifactFetcherShim) PreferredDelivery() int {
	return int(s.store.PreferredDelivery())
}

// artifactResolverFrom builds a chat.ArtifactFetcherResolver over the
// registry. Returns nil resolver when the registry is nil (feature off).
func artifactResolverFrom(reg *artifacts.Registry) chat.ArtifactFetcherResolver {
	if reg == nil {
		return nil
	}
	return func(storeID string) llm.ArtifactFetcher {
		store := reg.Get(storeID)
		if store == nil {
			return nil
		}
		return artifactFetcherShim{store: store}
	}
}

// artifactUploader implements chat.ArtifactUploader by uploading to the
// registry's default store with content-addressable URIs.
type artifactUploader struct {
	registry *artifacts.Registry
}

func (u artifactUploader) Upload(ctx context.Context, mime string, data []byte) (llm.ArtifactRefBlock, error) {
	if u.registry == nil {
		return llm.ArtifactRefBlock{}, errors.New("artifact uploader: registry is nil")
	}
	store := u.registry.Default()
	if store == nil {
		return llm.ArtifactRefBlock{}, errors.New("artifact uploader: registry has no default store")
	}
	sum := sha256.Sum256(data)
	sumHex := hex.EncodeToString(sum[:])
	uri := "sha256/" + sumHex

	// Dedup pre-check identical to the upload handler's path.
	if stat, err := store.Stat(ctx, uri); err == nil && stat.Exists {
		return llm.ArtifactRefBlock{
			StoreID:   u.registry.DefaultID(),
			URI:       uri,
			MIME:      mime,
			SizeBytes: firstNonZero(stat.SizeBytes, int64(len(data))),
			Sha256:    sumHex,
		}, nil
	}

	if _, err := store.Put(ctx, uri, mime, bytesReader(data), int64(len(data))); err != nil {
		return llm.ArtifactRefBlock{}, fmt.Errorf("artifact uploader: put failed: %w", err)
	}
	return llm.ArtifactRefBlock{
		StoreID:   u.registry.DefaultID(),
		URI:       uri,
		MIME:      mime,
		SizeBytes: int64(len(data)),
		Sha256:    sumHex,
	}, nil
}

// bytesReader wraps a []byte in an io.Reader without pulling in bytes package
// where a single call would be simpler. Reused by both the upload handler and
// the uploader shim so callers don't fight over reader ownership.
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

// Compile-time interface checks so drift is caught at build time rather
// than in tests.
var (
	_ llm.ArtifactFetcher   = artifactFetcherShim{}
	_ chat.ArtifactUploader = artifactUploader{}
)
