package handler

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
)

func TestArtifactUploader_ResolvesMIMEFromBytes(t *testing.T) {
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL}
	reg := buildRegistry(t, store)

	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := artifactUploader{registry: reg}.Upload(context.Background(), "application/octet-stream", png)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if ref.MIME != "image/png" {
		t.Fatalf("MIME = %q, want image/png", ref.MIME)
	}
	if ref.StoreID != "MAIN" {
		t.Fatalf("StoreID = %q, want MAIN", ref.StoreID)
	}
}
