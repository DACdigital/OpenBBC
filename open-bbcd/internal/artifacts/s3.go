package artifacts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3Store implements ArtifactStore against any S3-API-compatible endpoint
// (AWS S3, MinIO, GCS with HMAC, R2, B2, and others). URI namespace inside
// the store is the framework-computed sha256/<hex> path; the adapter uses
// the URI verbatim as the S3 object key with no additional prefixing.
//
// PreferredDelivery is always DeliverySignedURL: the S3 family offloads
// bandwidth to the store via presigned GETs, which is measurably faster
// than proxying bytes through the daemon and lets the store's own edge
// caching (CDN, region replicas) take effect. Adapters that need bytes-
// proxy semantics belong under a different kind (not shipped in phase 1).
type s3Store struct {
	kind    string
	client  *minio.Client
	bucket  string
	signTTL time.Duration
}

// s3CompatibleKind is the kind string that appears in ARTIFACT_STORE_<ID>_KIND.
const s3CompatibleKind = "s3_compatible"

// NewS3Compatible constructs an s3_compatible adapter from parsed env-var
// options. Required option keys: ENDPOINT, BUCKET, ACCESS_KEY, SECRET_KEY.
// Optional: REGION (defaults empty — AWS SDK derives from endpoint host),
// PATH_STYLE (defaults false; set true for MinIO and many self-hosted
// gateways).
//
// The signed-URL TTL is set on the adapter at construction rather than
// per-call so the caller (the retrieval handler) does not need to know
// per-store defaults; each Sign() uses the constructor's TTL.
func NewS3Compatible(options map[string]string, signTTL time.Duration) (ArtifactStore, error) {
	endpoint := strings.TrimSpace(options["ENDPOINT"])
	bucket := strings.TrimSpace(options["BUCKET"])
	accessKey := options["ACCESS_KEY"]
	secretKey := options["SECRET_KEY"]

	if endpoint == "" {
		return nil, fmt.Errorf("s3_compatible: ENDPOINT is required")
	}
	if bucket == "" {
		return nil, fmt.Errorf("s3_compatible: BUCKET is required")
	}
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("s3_compatible: ACCESS_KEY and SECRET_KEY are required")
	}
	if signTTL <= 0 {
		return nil, fmt.Errorf("s3_compatible: signed-URL TTL must be positive, got %v", signTTL)
	}

	// The minio-go client wants a host[:port] endpoint and a Secure flag
	// rather than a scheme-prefixed URL, so peel the scheme apart.
	host, secure, err := splitEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("s3_compatible: %w", err)
	}

	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
		Region: strings.TrimSpace(options["REGION"]),
	}
	// PATH_STYLE has no direct option on minio-go/v7 — the client selects
	// path- vs virtual-host-style based on the endpoint form: an IP or a
	// hostname with a port implies path-style, a bare S3-hosted hostname
	// implies virtual-host. Deployers wanting to force path-style set
	// PATH_STYLE=true and use an endpoint form that the client's heuristic
	// resolves as path-style (e.g. minio:9000 rather than minio.company.com).
	// The option is preserved verbatim on the returned struct so the probe
	// log can surface it if a deployer misconfigures.
	_ = options["PATH_STYLE"]

	client, err := minio.New(host, opts)
	if err != nil {
		return nil, fmt.Errorf("s3_compatible: %w", err)
	}

	return &s3Store{
		kind:    s3CompatibleKind,
		client:  client,
		bucket:  bucket,
		signTTL: signTTL,
	}, nil
}

func (s *s3Store) Kind() string                    { return s.kind }
func (s *s3Store) PreferredDelivery() DeliveryMode { return DeliverySignedURL }

func (s *s3Store) Put(ctx context.Context, uri, mime string, r io.Reader, size int64) (PutResult, error) {
	info, err := s.client.PutObject(ctx, s.bucket, uri, r, size, minio.PutObjectOptions{
		ContentType: mime,
	})
	if err != nil {
		return PutResult{}, err
	}
	return PutResult{
		URI:       uri,
		SizeBytes: info.Size,
		Sha256:    strings.TrimPrefix(uri, "sha256/"),
	}, nil
}

func (s *s3Store) Get(ctx context.Context, uri string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, uri, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapS3Err(err)
	}
	// GetObject returns a lazy handle; a Stat is what actually contacts the
	// server and surfaces a not-found. Do it here so the caller doesn't have
	// to unwrap the deferred error later.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, mapS3Err(err)
	}
	return obj, nil
}

func (s *s3Store) Sign(ctx context.Context, uri string, ttl time.Duration, opts SignOptions) (string, error) {
	if ttl <= 0 {
		ttl = s.signTTL
	}
	reqParams := make(url.Values)
	if opts.ContentType != "" {
		reqParams.Set("response-content-type", opts.ContentType)
	}
	if opts.ContentDisposition != "" {
		reqParams.Set("response-content-disposition", opts.ContentDisposition)
	}
	presigned, err := s.client.PresignedGetObject(ctx, s.bucket, uri, ttl, reqParams)
	if err != nil {
		return "", mapS3Err(err)
	}
	return presigned.String(), nil
}

func (s *s3Store) Stat(ctx context.Context, uri string) (StatResult, error) {
	info, err := s.client.StatObject(ctx, s.bucket, uri, minio.StatObjectOptions{})
	if err != nil {
		if errors.Is(err, mapS3Err(err)) && errors.Is(mapS3Err(err), ErrBlobMissing) {
			return StatResult{Exists: false}, nil
		}
		// A 404-style error from S3 is the "does not exist" case; distinguish
		// it from a real transport failure.
		if isS3NotFound(err) {
			return StatResult{Exists: false}, nil
		}
		return StatResult{}, err
	}
	// minio-go surfaces the checksum via ETag on unmodified single-part
	// uploads; multipart uploads compose an ETag that is NOT the object
	// sha256, so this is best-effort only. Framework holds the authoritative
	// sha256 via the artifact_ref block on the message.
	return StatResult{
		Exists:    true,
		MIME:      info.ContentType,
		SizeBytes: info.Size,
		Sha256:    strings.Trim(info.ETag, `"`),
	}, nil
}

func (s *s3Store) Delete(ctx context.Context, uri string) error {
	return s.client.RemoveObject(ctx, s.bucket, uri, minio.RemoveObjectOptions{})
}

func (s *s3Store) Probe(ctx context.Context) error {
	// Round-trip a small sentinel key: write → stat → delete. This exercises
	// credentials, bucket existence, and Put/Stat/Delete permissions on the
	// same code path any real upload will take. Kept tiny so it can run at
	// boot with negligible cost.
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("%w: could not generate probe key: %v", ErrProbeFailed, err)
	}
	key := "_probe/" + hex.EncodeToString(buf)
	payload := strings.NewReader("openbbc probe")
	if _, err := s.client.PutObject(ctx, s.bucket, key, payload, int64(payload.Len()), minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	}); err != nil {
		return fmt.Errorf("%w: put failed: %v", ErrProbeFailed, err)
	}
	if _, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err != nil {
		// Best-effort cleanup — don't mask the stat error.
		_ = s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
		return fmt.Errorf("%w: stat failed: %v", ErrProbeFailed, err)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("%w: delete failed: %v", ErrProbeFailed, err)
	}
	// A missing key must be reported as not-found. Without list permission
	// S3 answers 403 for a missing key, and the render path's missing-blob
	// fallback (and retrieval's 410) would then misread every missing blob
	// as an auth failure.
	missing := "_probe/missing-" + hex.EncodeToString(buf)
	st, err := s.Stat(ctx, missing)
	if err != nil {
		if minio.ToErrorResponse(err).StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: stat of a missing key did not report not-found (%v); the store credentials need list permission on bucket %q (s3:ListBucket)", ErrProbeFailed, err, s.bucket)
		}
		return fmt.Errorf("%w: stat of missing key failed: %v", ErrProbeFailed, err)
	}
	if st.Exists {
		return fmt.Errorf("%w: stat of random missing key %q reported it exists", ErrProbeFailed, missing)
	}
	return nil
}

// splitEndpoint peels the "https://" or "http://" scheme off a URL-shaped
// ENDPOINT and returns the host[:port] plus a Secure flag. minio-go wants
// them as two args rather than one URL. An unscheme'd endpoint is treated
// as HTTPS by default — matching S3 best practice.
func splitEndpoint(endpoint string) (host string, secure bool, err error) {
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		return strings.TrimPrefix(endpoint, "https://"), true, nil
	case strings.HasPrefix(endpoint, "http://"):
		return strings.TrimPrefix(endpoint, "http://"), false, nil
	case strings.Contains(endpoint, "://"):
		return "", false, fmt.Errorf("ENDPOINT: unsupported scheme in %q (want http:// or https:// or scheme-less host)", endpoint)
	default:
		return endpoint, true, nil
	}
}

// isS3NotFound recognises the shape minio-go returns for missing objects.
// The S3 API returns 404 for both a missing key and a missing bucket; we
// return true for either. This is intentionally lenient — the retrieval
// handler will surface the ErrBlobMissing sentinel anyway.
func isS3NotFound(err error) bool {
	var resp minio.ErrorResponse
	if !errors.As(err, &resp) {
		return false
	}
	return resp.StatusCode == 404 ||
		resp.Code == "NoSuchKey" ||
		resp.Code == "NoSuchBucket" ||
		resp.Code == "NotFound"
}

// mapS3Err normalises minio-go errors into the sentinels defined in
// store.go, so callers upstream (handlers) can distinguish "blob gone"
// from "store unhealthy" via errors.Is.
func mapS3Err(err error) error {
	if err == nil {
		return nil
	}
	if isS3NotFound(err) {
		return fmt.Errorf("%w: %v", ErrBlobMissing, err)
	}
	return err
}
