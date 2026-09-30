package artifacts

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestS3Compatible_Integration runs the full ArtifactStore contract against
// a live S3-compatible endpoint. Skipped unless MINIO_URL is set (opt-in).
//
// Bootstrap:
//
//	docker compose --profile artifacts up -d minio
//	docker compose exec minio mc alias set local http://localhost:9000 minioadmin minioadmin
//	docker compose exec minio mc mb local/openbbc-artifacts-test
//	MINIO_URL=http://localhost:9000 \
//	  MINIO_BUCKET=openbbc-artifacts-test \
//	  MINIO_ACCESS_KEY=minioadmin \
//	  MINIO_SECRET_KEY=minioadmin \
//	  go test -v -run TestS3Compatible_Integration ./internal/artifacts/
//
// The test exercises Probe → Put → Stat → Get → Sign → Delete against
// unique object keys so it can be re-run without cleanup between runs.
func TestS3Compatible_Integration(t *testing.T) {
	endpoint := os.Getenv("MINIO_URL")
	bucket := os.Getenv("MINIO_BUCKET")
	accessKey := os.Getenv("MINIO_ACCESS_KEY")
	secretKey := os.Getenv("MINIO_SECRET_KEY")
	if endpoint == "" || bucket == "" || accessKey == "" || secretKey == "" {
		t.Skip("MINIO_URL / MINIO_BUCKET / MINIO_ACCESS_KEY / MINIO_SECRET_KEY not set; skipping live-store integration test")
	}

	store, err := NewS3Compatible(map[string]string{
		"ENDPOINT":   endpoint,
		"BUCKET":     bucket,
		"REGION":     "us-east-1",
		"ACCESS_KEY": accessKey,
		"SECRET_KEY": secretKey,
		"PATH_STYLE": "true",
	}, 60*time.Second)
	if err != nil {
		t.Fatalf("NewS3Compatible = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Probe first — verifies credentials, bucket existence, put+stat+delete
	// permissions in one shot. Deployers use the same probe at boot.
	if err := store.Probe(ctx); err != nil {
		t.Fatalf("Probe = %v", err)
	}

	// Content-addressable URI just like production code produces.
	payload := []byte("openbbc integration test payload " + time.Now().Format(time.RFC3339Nano))
	uri := "sha256/" + uniqueURITail(payload)

	// Put.
	if _, err := store.Put(ctx, uri, "text/plain", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Put = %v", err)
	}
	// Cleanup afterwards regardless of subsequent failures.
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Delete(cleanCtx, uri)
	})

	// Stat — object should exist.
	st, err := store.Stat(ctx, uri)
	if err != nil {
		t.Fatalf("Stat = %v", err)
	}
	if !st.Exists {
		t.Fatalf("Stat.Exists = false, want true")
	}
	if st.SizeBytes != int64(len(payload)) {
		t.Errorf("Stat.SizeBytes = %d, want %d", st.SizeBytes, len(payload))
	}

	// Sign — for the s3_compatible adapter PreferredDelivery is
	// SignedURL, so this is the primary read path.
	url, err := store.Sign(ctx, uri, 60*time.Second, SignOptions{})
	if err != nil {
		t.Fatalf("Sign = %v", err)
	}
	if !strings.Contains(url, "X-Amz-Signature") {
		t.Errorf("signed URL missing signature params: %s", url)
	}

	signed, err := store.Sign(ctx, uri, time.Minute, SignOptions{ContentType: "image/png", ContentDisposition: "inline; filename*=UTF-8''x.png"})
	if err != nil {
		t.Fatalf("Sign with options: %v", err)
	}
	resp, err := http.Get(signed)
	if err != nil {
		t.Fatalf("GET signed: %v", err)
	}
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("Content-Disposition") != "inline; filename*=UTF-8''x.png" {
		t.Fatalf("signed GET headers: ct=%q cd=%q", resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
	}

	// Get — for symmetry, verify the bytes-proxy path works even
	// though the adapter's PreferredDelivery is SignedURL.
	rc, err := store.Get(ctx, uri)
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("Get returned %v, want %v", got, payload)
	}

	// Stat after Delete should return Exists=false.
	if err := store.Delete(ctx, uri); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	st, err = store.Stat(ctx, uri)
	if err != nil {
		t.Fatalf("Stat after Delete = %v", err)
	}
	if st.Exists {
		t.Errorf("Stat.Exists after Delete = true, want false")
	}
}

// uniqueURITail builds a stable-shape but per-run-unique string used as
// the object-key tail. The integration test doesn't need a real sha256
// since it's not exercising dedup semantics — just a URI stable within
// one run for the sequence Put/Stat/Sign/Get/Delete.
func uniqueURITail(b []byte) string {
	// Not a real sha256 — just uses the byte length + prefix bytes to
	// produce a unique-per-payload string. That's enough for a
	// per-run integration test where cleanup happens in a Cleanup hook.
	h := make([]byte, 0, 16)
	h = append(h, "int-"...)
	for i := 0; i < 8 && i < len(b); i++ {
		h = append(h, byte('a'+(b[i]%26)))
	}
	h = append(h, '-')
	// Include time-based suffix for uniqueness across runs.
	t := time.Now().UnixNano()
	for t > 0 {
		h = append(h, byte('a'+(byte(t)%26)))
		t /= 26
	}
	return string(h)
}
