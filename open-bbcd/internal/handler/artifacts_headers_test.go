package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// retrieveBytes runs HandleRetrieve against a DeliveryBytes fake store
// whose session-scope resolver reports the ref with the given mime.
func retrieveBytes(t *testing.T, mime string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliveryBytes, getData: payload}
	reg := buildRegistry(t, store)
	sessions := &fakeSessionStore{
		sessions: map[string]*types.ChatSession{"s1": {ID: "s1", AgentVersionID: "v1"}},
	}
	h := NewArtifactHandler(sessions, &fakeRefResolver{found: true, mime: mime}, reg, 10, nil)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/v1/chat/s1/artifacts/MAIN/sha256/abc", nil)
	req.SetPathValue("version_id", "v1")
	req.SetPathValue("session_id", "s1")
	req.SetPathValue("path", "MAIN/sha256/abc")
	rec := httptest.NewRecorder()
	h.HandleRetrieve(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	return rec
}

func TestRetrieve_BytesHeaders(t *testing.T) {
	cases := []struct {
		mime        string
		disposition string
	}{
		{"image/png", "inline"},
		{"application/pdf", "inline"},
		{"application/zip", "attachment"},
		{"text/html", "attachment"},
		{"", "attachment"},
	}
	for _, tc := range cases {
		t.Run(tc.mime, func(t *testing.T) {
			rec := retrieveBytes(t, tc.mime, []byte("<html><script>alert(1)</script></html>"))
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := rec.Header().Get("Content-Disposition"); got != tc.disposition {
				t.Errorf("Content-Disposition = %q, want %q", got, tc.disposition)
			}
			wantCT := tc.mime
			if wantCT == "" {
				wantCT = "application/octet-stream"
			}
			if got := rec.Header().Get("Content-Type"); got != wantCT {
				t.Errorf("Content-Type = %q, want %q", got, wantCT)
			}
		})
	}
}

func TestArtifactContentDisposition_Filename(t *testing.T) {
	cases := []struct{ mime, filename, want string }{
		{"image/png", "", "inline"},
		{"image/png", "chart.png", "inline; filename*=UTF-8''chart.png"},
		{"application/zip", `q3 "final";.zip`, "attachment; filename*=UTF-8''q3%20%22final%22%3B.zip"},
		{"text/plain", "zażółć.txt", "attachment; filename*=UTF-8''za%C5%BC%C3%B3%C5%82%C4%87.txt"},
	}
	for _, tc := range cases {
		if got := artifactContentDisposition(tc.mime, tc.filename); got != tc.want {
			t.Errorf("artifactContentDisposition(%q, %q) = %q, want %q", tc.mime, tc.filename, got, tc.want)
		}
	}
}
