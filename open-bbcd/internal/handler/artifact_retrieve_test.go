package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func onePixelPNGBytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func retrieveReq(versionID, sessionID, path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/chat/"+sessionID+"/artifacts/"+path, nil)
	req.SetPathValue("version_id", versionID)
	req.SetPathValue("session_id", sessionID)
	req.SetPathValue("path", path)
	return req
}

func seedRow(t *testing.T, rows *memRows, sessionID, uri, mime, filename string, size int64) {
	t.Helper()
	if _, err := rows.CommitUpload(context.Background(), types.SessionArtifact{
		SessionID: sessionID, StoreID: "MAIN", URI: uri, MIME: mime, SizeBytes: size, Sha256: "x", Filename: filename,
	}, 100); err != nil {
		t.Fatal(err)
	}
}

func TestRetrieve_PendingUploadIsRetrievable_SignedWithOverrides(t *testing.T) {
	b := newBOHarness(t, 10)
	_, up := b.upload(t, "shot.png", "image/png", onePixelPNGBytes(t))
	b.store.statHit = true
	rec := httptest.NewRecorder()
	b.h.HandleRetrieve(rec, retrieveReq("v1", "s1", up.StoreID+"/"+up.URI))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://signed.example/x" {
		t.Fatalf("status %d loc %q", rec.Code, rec.Header().Get("Location"))
	}
	want := artifacts.SignOptions{ContentType: "image/png", ContentDisposition: "inline; filename*=UTF-8''shot.png"}
	if b.store.lastSignOpts != want {
		t.Fatalf("sign opts = %+v, want %+v", b.store.lastSignOpts, want)
	}
}

func TestRetrieve_NonNativeSignedAsAttachmentOctetStream(t *testing.T) {
	b := newBOHarness(t, 10)
	seedRow(t, b.rows, "s1", "sha256/zz", "application/zip", "a.zip", 5)
	b.store.statHit = true
	b.h.HandleRetrieve(httptest.NewRecorder(), retrieveReq("v1", "s1", "MAIN/sha256/zz"))
	want := artifacts.SignOptions{ContentType: "application/octet-stream", ContentDisposition: "attachment; filename*=UTF-8''a.zip"}
	if b.store.lastSignOpts != want {
		t.Fatalf("sign opts = %+v", b.store.lastSignOpts)
	}
}

func TestRetrieve_Statuses(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, b *boHarness)
		path  string
		want  int
	}{
		{"no row", func(t *testing.T, b *boHarness) {}, "MAIN/sha256/aa", http.StatusNotFound},
		{"row in another session only", func(t *testing.T, b *boHarness) {
			b.sess.sessions["s2"] = &types.ChatSession{ID: "s2", AgentVersionID: "v1"}
			seedRow(t, b.rows, "s2", "sha256/aa", "image/png", "", 1)
			b.store.statHit = true
		}, "MAIN/sha256/aa", http.StatusNotFound},
		{"unregistered store", func(t *testing.T, b *boHarness) {
			if _, err := b.rows.CommitUpload(context.Background(), types.SessionArtifact{SessionID: "s1", StoreID: "OTHER", URI: "sha256/aa", MIME: "image/png"}, 10); err != nil {
				t.Fatal(err)
			}
		}, "OTHER/sha256/aa", http.StatusNotFound},
		{"blob removed", func(t *testing.T, b *boHarness) {
			seedRow(t, b.rows, "s1", "sha256/aa", "image/png", "", 1)
			b.store.statHit = false
		}, "MAIN/sha256/aa", http.StatusGone},
		{"stat error", func(t *testing.T, b *boHarness) {
			seedRow(t, b.rows, "s1", "sha256/aa", "image/png", "", 1)
			b.store.statErr = errors.New("net down")
		}, "MAIN/sha256/aa", http.StatusBadGateway},
		{"lookup error", func(t *testing.T, b *boHarness) {
			b.rows.lookupErr = errors.New("db down")
		}, "MAIN/sha256/aa", http.StatusInternalServerError},
		{"bad path", func(t *testing.T, b *boHarness) {}, "MAIN", http.StatusBadRequest},
		{"tool-result row", func(t *testing.T, b *boHarness) {
			b.rows.rows = append(b.rows.rows, &types.SessionArtifact{ID: "t", SessionID: "s1", Origin: types.ArtifactOriginToolResult,
				StoreID: "MAIN", URI: "sha256/tt", MIME: "image/png", MessageID: "m"})
			b.store.statHit = true
		}, "MAIN/sha256/tt", http.StatusFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newBOHarness(t, 10)
			c.setup(t, b)
			rec := httptest.NewRecorder()
			b.h.HandleRetrieve(rec, retrieveReq("v1", "s1", c.path))
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d (%s)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

func TestRetrieve_BytesMode_HeadersFromRow(t *testing.T) {
	b := newBOHarness(t, 10)
	b.store.delivery = artifacts.DeliveryBytes
	b.store.statHit = true
	b.store.getData = []byte("PK\x03\x04zip")
	seedRow(t, b.rows, "s1", "sha256/zz", "application/zip", "a.zip", 7)
	rec := httptest.NewRecorder()
	b.h.HandleRetrieve(rec, retrieveReq("v1", "s1", "MAIN/sha256/zz"))
	h := rec.Header()
	if rec.Code != http.StatusOK || h.Get("Content-Length") != "7" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Type") != "application/octet-stream" || h.Get("Content-Disposition") != "attachment; filename*=UTF-8''a.zip" {
		t.Fatalf("status %d headers %v", rec.Code, h)
	}
}
