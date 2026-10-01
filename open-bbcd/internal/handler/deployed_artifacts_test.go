package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

const (
	otherAgentID        = "44444444-4444-4444-8444-444444444444"
	otherAgentSessionID = "55555555-5555-4555-8555-555555555555"
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
	sessions.sessions[testSessionID] = &types.DeployedSession{ID: testSessionID, AgentID: testAgentID, UserID: "u1"}
	sessions.sessions[otherAgentSessionID] = &types.DeployedSession{ID: otherAgentSessionID, AgentID: otherAgentID, UserID: "u1"}
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
	rec := d.do(t, "", "", deployedUploadReq(t, "/deployed/"+testAgentID+"/sessions/"+testSessionID+"/artifacts?user_id=u1", []byte("%PDF-1.4 x")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload %d: %s", rec.Code, rec.Body.String())
	}
	var up PendingArtifact
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	if up.Status != "pending" || up.StoreID != "MAIN" || up.MIME != "application/pdf" {
		t.Fatalf("upload = %+v", up)
	}
	if rec := d.do(t, http.MethodGet, "/deployed/"+testAgentID+"/sessions/"+testSessionID+"/pending-artifacts?user_id=u1", nil); rec.Code != http.StatusOK {
		t.Fatalf("list %d", rec.Code)
	}
	if rec := d.do(t, http.MethodGet, "/deployed/"+testAgentID+"/sessions/"+testSessionID+"/artifacts/"+up.StoreID+"/"+up.URI+"?user_id=u1", nil); rec.Code != http.StatusFound {
		t.Fatalf("retrieve pending %d", rec.Code)
	}
	if rec := d.do(t, http.MethodDelete, "/deployed/"+testAgentID+"/sessions/"+testSessionID+"/pending-artifacts/"+up.ID+"?user_id=u1", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete %d", rec.Code)
	}
	if rec := d.do(t, http.MethodGet, "/deployed/"+testAgentID+"/sessions/"+testSessionID+"/artifacts/"+up.StoreID+"/"+up.URI+"?user_id=u1", nil); rec.Code != http.StatusNotFound {
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
		{"missing user_id", "v1", testAgentID, testSessionID, "", http.StatusBadRequest},
		{"wrong user", "v1", testAgentID, testSessionID, "?user_id=u2", http.StatusNotFound},
		{"wrong agent", "v1", testAgentID, otherAgentSessionID, "?user_id=u1", http.StatusNotFound},
		{"not deployed", "", testAgentID, testSessionID, "?user_id=u1", http.StatusNotFound},
		{"malformed agent id", "v1", "not-a-uuid", testSessionID, "?user_id=u1", http.StatusNotFound},
		{"malformed session id", "v1", testAgentID, "not-a-uuid", "?user_id=u1", http.StatusNotFound},
		{"urn-form session id", "v1", testAgentID, "urn:uuid:" + testSessionID, "?user_id=u1", http.StatusNotFound},
		{"unknown session", "v1", testAgentID, "66666666-6666-4666-8666-666666666666", "?user_id=u1", http.StatusNotFound},
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

type readSpy struct{ read bool }

func (r *readSpy) Read(p []byte) (int, error) { r.read = true; return 0, io.EOF }

func TestDeployedArtifacts_FailingPreambleNeverReadsBody(t *testing.T) {
	d := newDeployedHarness(t, "v1")
	spy := &readSpy{}
	req := httptest.NewRequest(http.MethodPost, "/deployed/"+testAgentID+"/sessions/"+testSessionID+"/artifacts?user_id=u2", spy)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	rec := d.do(t, "", "", req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	if spy.read {
		t.Fatal("upload body was read despite failing preamble")
	}
}

func TestDeployedArtifacts_RegistryDisabled_RoutesAbsent(t *testing.T) {
	mux := newDeployedMux(&stubDeployedAgentReader{deployedID: "v1"}, newStubDeployedStore(), &stubTurnRunner{}, jsonl.NewFactory())
	base := "/deployed/" + testAgentID + "/sessions/" + testSessionID
	for _, p := range []string{
		base + "/pending-artifacts?user_id=u1",
		base + "/artifacts/MAIN/sha256/aa?user_id=u1",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (unregistered)", p, rec.Code)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != "404 page not found" {
			t.Errorf("GET %s body = %q, want mux default 404", p, body)
		}
	}
}
