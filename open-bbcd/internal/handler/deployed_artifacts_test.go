package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type deployedHarness struct {
	mux   *http.ServeMux
	rows  *memRows
	store *fakeArtifactStore
}

func newDeployedHarness(t *testing.T, deployedID string) *deployedHarness {
	t.Helper()
	store := &fakeArtifactStore{kind: "test-fake", delivery: artifacts.DeliverySignedURL, signedURL: "https://signed.example/x", statHit: true}
	sessions := newStubDeployedStore()
	sessions.sessions["s1"] = &types.DeployedSession{ID: "s1", AgentID: "a1", UserID: "u1"}
	sessions.sessions["s-other-agent"] = &types.DeployedSession{ID: "s-other-agent", AgentID: "a2", UserID: "u1"}
	rows := &memRows{}
	h := NewDeployedArtifactHandler(&stubDeployedAgentReader{deployedID: deployedID}, sessions, rows, buildRegistry(t, store), 1, 10, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	return &deployedHarness{mux: mux, rows: rows, store: store}
}

func (d *deployedHarness) do(t *testing.T, method, target string, body *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	req := body
	if req == nil {
		req = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	d.mux.ServeHTTP(rec, req)
	return rec
}

func deployedUploadReq(t *testing.T, target string, data []byte) *http.Request {
	t.Helper()
	body, ct := newMultipartBody(t, "doc.pdf", "application/pdf", data)
	req := httptest.NewRequest(http.MethodPost, target, body)
	req.Header.Set("Content-Type", ct)
	return req
}

func TestDeployedArtifacts_UploadListRetrieveDelete(t *testing.T) {
	d := newDeployedHarness(t, "v1")
	rec := d.do(t, "", "", deployedUploadReq(t, "/deployed/a1/sessions/s1/artifacts?user_id=u1", []byte("%PDF-1.4 x")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload %d: %s", rec.Code, rec.Body.String())
	}
	var up PendingArtifact
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	if up.Status != "pending" || up.StoreID != "MAIN" || up.MIME != "application/pdf" {
		t.Fatalf("upload = %+v", up)
	}
	if rec := d.do(t, http.MethodGet, "/deployed/a1/sessions/s1/pending-artifacts?user_id=u1", nil); rec.Code != http.StatusOK {
		t.Fatalf("list %d", rec.Code)
	}
	if rec := d.do(t, http.MethodGet, "/deployed/a1/sessions/s1/artifacts/"+up.StoreID+"/"+up.URI+"?user_id=u1", nil); rec.Code != http.StatusFound {
		t.Fatalf("retrieve pending %d", rec.Code)
	}
	if rec := d.do(t, http.MethodDelete, "/deployed/a1/sessions/s1/pending-artifacts/"+up.ID+"?user_id=u1", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete %d", rec.Code)
	}
	if rec := d.do(t, http.MethodGet, "/deployed/a1/sessions/s1/artifacts/"+up.StoreID+"/"+up.URI+"?user_id=u1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("retrieve after delete %d", rec.Code)
	}
}

func TestDeployedArtifacts_PreambleFailures(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodPost, "/deployed/%s/sessions/%s/artifacts"},
		{http.MethodGet, "/deployed/%s/sessions/%s/artifacts/MAIN/sha256/aa"},
		{http.MethodGet, "/deployed/%s/sessions/%s/pending-artifacts"},
		{http.MethodDelete, "/deployed/%s/sessions/%s/pending-artifacts/00000000-0000-0000-0000-000000000001"},
	}
	cases := []struct {
		name, deployedID, agent, session, query string
		want                                    int
	}{
		{"missing user_id", "v1", "a1", "s1", "", http.StatusBadRequest},
		{"wrong user", "v1", "a1", "s1", "?user_id=u2", http.StatusNotFound},
		{"wrong agent", "v1", "a1", "s-other-agent", "?user_id=u1", http.StatusNotFound},
		{"not deployed", "", "a1", "s1", "?user_id=u1", http.StatusNotFound},
		{"unknown session", "v1", "a1", "nope", "?user_id=u1", http.StatusNotFound},
	}
	for _, c := range cases {
		for _, rt := range routes {
			t.Run(c.name+" "+rt.method+" "+rt.path, func(t *testing.T) {
				d := newDeployedHarness(t, c.deployedID)
				target := fmt.Sprintf(rt.path, c.agent, c.session) + c.query
				var req *http.Request
				if rt.method == http.MethodPost {
					req = deployedUploadReq(t, target, []byte("x"))
				}
				rec := d.do(t, rt.method, target, req)
				if rec.Code != c.want {
					t.Fatalf("status %d, want %d", rec.Code, c.want)
				}
				if d.store.putCalls != 0 || len(d.rows.rows) != 0 {
					t.Fatal("preamble failure reached the store or the table")
				}
			})
		}
	}
}
