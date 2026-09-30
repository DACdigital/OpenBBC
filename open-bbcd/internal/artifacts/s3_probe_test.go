package artifacts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeS3 answers the probe's round-trip. missingStatus is the HEAD status
// for any key under _probe/missing-: 404 (list permission) or 403 (none).
func fakeS3(t *testing.T, missingStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/_probe/missing-"):
			w.WriteHeader(missingStatus)
		case r.Method == http.MethodHead:
			w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
			w.Header().Set("Content-Length", "13")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
}

func probeStore(t *testing.T, srv *httptest.Server) ArtifactStore {
	t.Helper()
	st, err := NewS3Compatible(map[string]string{
		"ENDPOINT": srv.URL, "BUCKET": "bkt", "ACCESS_KEY": "a", "SECRET_KEY": "s", "REGION": "us-east-1",
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestProbe_MissingKeyReportedNotFound_OK(t *testing.T) {
	srv := fakeS3(t, http.StatusNotFound)
	defer srv.Close()
	if err := probeStore(t, srv).Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

func TestProbe_MissingKeyForbidden_FailsNamingListPermission(t *testing.T) {
	srv := fakeS3(t, http.StatusForbidden)
	defer srv.Close()
	err := probeStore(t, srv).Probe(context.Background())
	if !errors.Is(err, ErrProbeFailed) || !strings.Contains(err.Error(), "s3:ListBucket") {
		t.Fatalf("err = %v, want ErrProbeFailed naming s3:ListBucket", err)
	}
}
