package artifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
)

// TestAzureBlob_Integration runs the full ArtifactStore contract against a
// live Azure Blob endpoint. Skipped unless AZURE_BLOB_ACCOUNT_NAME /
// AZURE_BLOB_ACCOUNT_KEY / AZURE_BLOB_CONTAINER are set (opt-in).
//
// Azurite:
//
//	docker compose --profile artifacts up -d azurite
//	AZURE_BLOB_ACCOUNT_NAME=devstoreaccount1 \
//	  AZURE_BLOB_ACCOUNT_KEY=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw== \
//	  AZURE_BLOB_CONTAINER=openbbc-artifacts-test \
//	  AZURE_BLOB_ENDPOINT=http://127.0.0.1:10000/devstoreaccount1/ \
//	  go test -v -run TestAzureBlob_Integration ./internal/artifacts/
//
// The test creates the container if absent (test-only; the adapter never
// creates containers).
func TestAzureBlob_Integration(t *testing.T) {
	opts := map[string]string{
		"ACCOUNT_NAME": os.Getenv("AZURE_BLOB_ACCOUNT_NAME"),
		"ACCOUNT_KEY":  os.Getenv("AZURE_BLOB_ACCOUNT_KEY"),
		"CONTAINER":    os.Getenv("AZURE_BLOB_CONTAINER"),
		"ENDPOINT":     os.Getenv("AZURE_BLOB_ENDPOINT"),
	}
	if opts["ACCOUNT_NAME"] == "" || opts["ACCOUNT_KEY"] == "" || opts["CONTAINER"] == "" {
		t.Skip("AZURE_BLOB_ACCOUNT_NAME / AZURE_BLOB_ACCOUNT_KEY / AZURE_BLOB_CONTAINER not set; skipping live-store integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := newAzureBlob(opts, time.Minute, nil)
	if err != nil {
		t.Fatalf("newAzureBlob = %v", err)
	}
	// Test-only container creation; production code never imports the
	// container package (it pulls Apache Arrow into the binary).
	cc, err := container.NewClientWithSharedKeyCredential(s.containerURL, s.cred, nil)
	if err != nil {
		t.Fatalf("container client: %v", err)
	}
	if _, err := cc.Create(ctx, nil); err != nil && !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
		t.Fatalf("create container: %v", err)
	}

	if err := s.Probe(ctx); err != nil {
		t.Fatalf("Probe = %v", err)
	}

	payload := []byte("openbbc azure integration payload " + time.Now().Format(time.RFC3339Nano))
	uri := "sha256/" + randHex(t)

	st, err := s.Stat(ctx, uri)
	if err != nil || st.Exists {
		t.Fatalf("Stat before Put = %+v, %v; want not-exists", st, err)
	}

	if _, err := s.Put(ctx, uri, "text/plain", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Put = %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Delete(c, uri)
	})

	st, err = s.Stat(ctx, uri)
	if err != nil || !st.Exists || st.MIME != "text/plain" || st.SizeBytes != int64(len(payload)) {
		t.Fatalf("Stat after Put = %+v, %v", st, err)
	}

	rc, err := s.Get(ctx, uri)
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("Get bytes = %q, %v; want %q", got, err, payload)
	}

	signed, err := s.Sign(ctx, uri, time.Minute, SignOptions{
		ContentType:        "text/plain",
		ContentDisposition: `attachment; filename="a.txt"`,
	})
	if err != nil {
		t.Fatalf("Sign = %v", err)
	}
	resp, err := http.Get(signed)
	if err != nil {
		t.Fatalf("GET signed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(body, payload) {
		t.Fatalf("signed GET status=%d body=%q", resp.StatusCode, body)
	}
	if ct, cd := resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"); ct != "text/plain" || cd != `attachment; filename="a.txt"` {
		t.Fatalf("signed GET headers ct=%q cd=%q", ct, cd)
	}

	if err := s.Delete(ctx, uri); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	if st, err := s.Stat(ctx, uri); err != nil || st.Exists {
		t.Fatalf("Stat after Delete = %+v, %v", st, err)
	}
	if _, err := s.Get(ctx, uri); !errors.Is(err, ErrBlobMissing) {
		t.Fatalf("Get after Delete err = %v, want ErrBlobMissing", err)
	}
	if err := s.Delete(ctx, uri); err != nil {
		t.Fatalf("second Delete = %v, want nil", err)
	}

	t.Run("wrong key fails probe", func(t *testing.T) {
		bad := map[string]string{}
		for k, v := range opts {
			bad[k] = v
		}
		bad["ACCOUNT_KEY"] = base64.StdEncoding.EncodeToString(make([]byte, 64))
		bs, err := newAzureBlob(bad, time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := bs.Probe(ctx); !errors.Is(err, ErrProbeFailed) {
			t.Fatalf("Probe with wrong key = %v, want ErrProbeFailed", err)
		}
	})

	t.Run("missing container fails probe", func(t *testing.T) {
		mc := map[string]string{}
		for k, v := range opts {
			mc[k] = v
		}
		mc["CONTAINER"] = "openbbc-missing-" + randHex(t)[:12]
		ms, err := newAzureBlob(mc, time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = ms.Probe(ctx)
		if !errors.Is(err, ErrProbeFailed) || !strings.Contains(err.Error(), "ContainerNotFound") {
			t.Fatalf("Probe on missing container = %v, want ErrProbeFailed naming ContainerNotFound", err)
		}
	})
}

func randHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
