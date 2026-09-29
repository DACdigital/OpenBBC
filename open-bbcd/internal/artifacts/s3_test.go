package artifacts

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func TestSplitEndpoint(t *testing.T) {
	cases := []struct {
		name       string
		endpoint   string
		wantHost   string
		wantSecure bool
		wantErr    bool
	}{
		{"https url", "https://s3.eu-west-1.amazonaws.com", "s3.eu-west-1.amazonaws.com", true, false},
		{"http url with port", "http://minio:9000", "minio:9000", false, false},
		{"scheme-less defaults to https", "s3.example.com", "s3.example.com", true, false},
		{"ftp scheme is rejected", "ftp://storage.example.com", "", false, true},
		{"ws scheme is rejected", "ws://storage.example.com", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, secure, err := splitEndpoint(tc.endpoint)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitEndpoint(%q) = nil error, want error", tc.endpoint)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitEndpoint(%q) = err %v, want nil", tc.endpoint, err)
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
			if secure != tc.wantSecure {
				t.Errorf("secure = %v, want %v", secure, tc.wantSecure)
			}
		})
	}
}

func TestNewS3Compatible_MissingEndpoint(t *testing.T) {
	_, err := NewS3Compatible(map[string]string{
		"BUCKET":     "b",
		"ACCESS_KEY": "a",
		"SECRET_KEY": "s",
	}, 300*time.Second)
	if err == nil {
		t.Fatal("expected error for missing ENDPOINT")
	}
	if !strings.Contains(err.Error(), "ENDPOINT") {
		t.Errorf("error should mention ENDPOINT, got: %v", err)
	}
}

func TestNewS3Compatible_MissingBucket(t *testing.T) {
	_, err := NewS3Compatible(map[string]string{
		"ENDPOINT":   "https://s3.example.com",
		"ACCESS_KEY": "a",
		"SECRET_KEY": "s",
	}, 300*time.Second)
	if err == nil {
		t.Fatal("expected error for missing BUCKET")
	}
	if !strings.Contains(err.Error(), "BUCKET") {
		t.Errorf("error should mention BUCKET, got: %v", err)
	}
}

func TestNewS3Compatible_MissingCredentials(t *testing.T) {
	// Missing both.
	_, err := NewS3Compatible(map[string]string{
		"ENDPOINT": "https://s3.example.com",
		"BUCKET":   "b",
	}, 300*time.Second)
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	// Missing just secret.
	_, err = NewS3Compatible(map[string]string{
		"ENDPOINT":   "https://s3.example.com",
		"BUCKET":     "b",
		"ACCESS_KEY": "a",
	}, 300*time.Second)
	if err == nil {
		t.Fatal("expected error for missing SECRET_KEY")
	}
}

func TestNewS3Compatible_ZeroTTL(t *testing.T) {
	_, err := NewS3Compatible(map[string]string{
		"ENDPOINT":   "https://s3.example.com",
		"BUCKET":     "b",
		"ACCESS_KEY": "a",
		"SECRET_KEY": "s",
	}, 0)
	if err == nil {
		t.Fatal("expected error for zero TTL")
	}
}

func TestNewS3Compatible_HappyPath(t *testing.T) {
	store, err := NewS3Compatible(map[string]string{
		"ENDPOINT":   "https://s3.eu-west-1.amazonaws.com",
		"BUCKET":     "artifacts",
		"ACCESS_KEY": "AKIA",
		"SECRET_KEY": "xxx",
		"REGION":     "eu-west-1",
	}, 300*time.Second)
	if err != nil {
		t.Fatalf("NewS3Compatible = err %v, want nil", err)
	}
	if store.Kind() != s3CompatibleKind {
		t.Errorf("Kind = %q, want %q", store.Kind(), s3CompatibleKind)
	}
	if store.PreferredDelivery() != DeliverySignedURL {
		t.Errorf("PreferredDelivery = %v, want DeliverySignedURL", store.PreferredDelivery())
	}
}

func TestIsS3NotFound(t *testing.T) {
	// Non-error should be false.
	if isS3NotFound(nil) {
		t.Error("isS3NotFound(nil) = true, want false")
	}
	// Non-minio errors should be false.
	if isS3NotFound(errors.New("network timeout")) {
		t.Error("isS3NotFound(generic error) = true, want false")
	}
	// Actual minio ErrorResponse with 404.
	notFound := minio.ErrorResponse{Code: "NoSuchKey", StatusCode: 404}
	if !isS3NotFound(notFound) {
		t.Error("isS3NotFound(NoSuchKey) = false, want true")
	}
	// Wrapped 404.
	if !isS3NotFound(minio.ErrorResponse{Code: "NoSuchBucket", StatusCode: 404}) {
		t.Error("isS3NotFound(NoSuchBucket) = false, want true")
	}
	// 500-style error should NOT be a not-found.
	server := minio.ErrorResponse{Code: "InternalError", StatusCode: 500}
	if isS3NotFound(server) {
		t.Error("isS3NotFound(500) = true, want false")
	}
}

func TestMapS3Err(t *testing.T) {
	// Nil passes through.
	if got := mapS3Err(nil); got != nil {
		t.Errorf("mapS3Err(nil) = %v, want nil", got)
	}
	// 404 becomes ErrBlobMissing.
	nf := minio.ErrorResponse{Code: "NoSuchKey", StatusCode: 404}
	got := mapS3Err(nf)
	if !errors.Is(got, ErrBlobMissing) {
		t.Errorf("mapS3Err(NoSuchKey) not ErrBlobMissing, got: %v", got)
	}
	// Non-404 returns the original error unwrapped.
	other := errors.New("some other error")
	if got := mapS3Err(other); got != other {
		t.Errorf("mapS3Err(other) = %v, want to pass through unchanged", got)
	}
}
