// Package artifacts declares the pluggable storage substrate for chat
// artifact bytes. open-bbcd holds only refs in Postgres: each artifact in a
// session's read scope is a row in that context's session-artifact table
// (chat_session_artifacts for BO, deployed_session_artifacts for deployed),
// and messages carry `artifact_ref` content blocks that retrieval authorises
// by row lookup. The bytes themselves live in a deployer-configured Object
// store reached through an ArtifactStore adapter.
//
// The store registry is loaded once at daemon boot from env vars
// (ARTIFACT_STORE_<ID>_* groups + ARTIFACT_STORE_DEFAULT); there is no
// REST/BO surface for reconfiguration. See docs/superpowers/specs/
// 2026-09-28-chat-artifacts-design.md for the full contract.
package artifacts

import (
	"context"
	"errors"
	"io"
	"time"
)

// DeliveryMode tells the retrieval route how the adapter wants reads to be
// served: as proxied bytes through the daemon, or as a 302 redirect to a
// short-lived signed URL that clients follow directly to the store.
//
// Each adapter declares its mode at construction; the framework caches it.
// Store kind is the deployer's real choice — delivery mode falls out of
// which adapter kind they configure (e.g. s3_compatible → DeliverySignedURL
// because S3 offloads bandwidth via presigned GETs).
type DeliveryMode int

const (
	// DeliveryBytes: retrieval route calls ArtifactStore.Get and streams
	// bytes back with the ref's mime as Content-Type.
	DeliveryBytes DeliveryMode = iota

	// DeliverySignedURL: retrieval route calls ArtifactStore.Sign and
	// responds with 302 Location: <signed-url>.
	DeliverySignedURL
)

// PutResult carries the outcome of a successful upload. URI, SizeBytes,
// and Sha256 are echoed to the caller so the framework can construct the
// artifact_ref block without a second round-trip.
type PutResult struct {
	URI       string
	SizeBytes int64
	Sha256    string
}

// StatResult carries object metadata for a blob addressable by URI.
// Exists=false means "the store has no object at this URI"; other fields
// are meaningless in that case.
type StatResult struct {
	Exists    bool
	MIME      string
	SizeBytes int64
	Sha256    string
}

// SignOptions are per-request response overrides a signed URL carries, so
// the store serves the artifact with the same Content-Type and
// Content-Disposition the bytes-delivery path sets. Per request because
// content-addressed blobs are shared by rows with different filenames.
// Kinds that cannot set response overrides ignore them.
type SignOptions struct {
	ContentType        string
	ContentDisposition string
}

// ArtifactStore is the pluggable interface every store-kind implementation
// satisfies. Framework-side callers (the upload/retrieval handlers, the
// tool-result normaliser, the LLM multimodal renderer) always call through
// this interface — they never construct kind-specific adapters directly.
//
// Kind() returns the string that appeared in ARTIFACT_STORE_<ID>_KIND at
// registration time. Callers do not usually need this; it is exposed so
// diagnostics and integration tests can assert against a specific adapter.
//
// PreferredDelivery() is queried once at construction and cached. Adapters
// must never change their declared mode at runtime.
type ArtifactStore interface {
	Kind() string
	PreferredDelivery() DeliveryMode

	// Put uploads bytes to the store at the given URI. The URI is
	// framework-computed (content-addressable, "sha256/<hex>") so the
	// adapter does not choose it. Size must match the number of bytes
	// readable from r; adapters use it for range/length metadata but the
	// caller is authoritative.
	Put(ctx context.Context, uri, mime string, r io.Reader, size int64) (PutResult, error)

	// Get streams bytes back. Only called when PreferredDelivery()
	// returns DeliveryBytes. Adapters whose PreferredDelivery is
	// DeliverySignedURL may implement Get as a not-supported stub.
	Get(ctx context.Context, uri string) (io.ReadCloser, error)

	// Sign returns a time-bounded HTTPS URL that any HTTP client can
	// GET without further auth. Only called when PreferredDelivery()
	// returns DeliverySignedURL. TTL is honoured on a best-effort basis;
	// some kinds may enforce a floor or ceiling. opts are applied as
	// response overrides (S3 response-content-type /
	// response-content-disposition).
	Sign(ctx context.Context, uri string, ttl time.Duration, opts SignOptions) (string, error)

	// Stat returns metadata about the blob at URI. The framework calls
	// this on the upload path before Put to short-circuit dedup: if
	// Stat says the blob already exists (by sha256-based URI), Put is
	// skipped and the existing ref is returned.
	Stat(ctx context.Context, uri string) (StatResult, error)

	// Delete removes the blob. No REST route in chat-artifacts phase 1
	// calls Delete on the raw adapter; the framework's Registry.DeleteWithGuard
	// wraps it with a "no locked session references this ref" check. Kept
	// on the interface so a future chat-artifacts-gc feature can reach it
	// without an interface change.
	Delete(ctx context.Context, uri string) error

	// Probe performs a boot-time self-check: a small write+read+delete
	// round-trip against a sentinel key that verifies credentials, bucket
	// reachability, and permission set, and that Stat of a random missing
	// key reports not-found (for s3_compatible this needs s3:ListBucket;
	// without it S3 answers 403 and a missing blob is indistinguishable
	// from an auth failure). Failure of the default store's
	// Probe (after bounded retry) fails daemon boot.
	Probe(ctx context.Context) error
}

// Sentinel errors adapters return in place of stringly-typed errors, so the
// retrieval handler can distinguish "blob was externally removed" (→ 410)
// from "underlying store is unhealthy" (→ 502).

// ErrBlobMissing means Stat/Get/Sign found no object at the given URI even
// though a caller expected one to exist. Retrieval route maps this to
// HTTP 410 Gone.
var ErrBlobMissing = errors.New("artifacts: blob not found at uri")

// ErrDeleteRefused means Registry.DeleteWithGuard refused the delete
// because at least one locked (dataset-closed) session's message still
// references the blob. Not returned by raw adapters; only by the guard.
var ErrDeleteRefused = errors.New("artifacts: delete refused — locked session references this ref")

// ErrProbeFailed wraps the underlying transport error so the boot code
// can surface "the store is unreachable / misconfigured" without a caller
// needing to type-assert.
var ErrProbeFailed = errors.New("artifacts: probe failed")
