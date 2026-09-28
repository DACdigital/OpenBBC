package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type stubVersionListRepo struct {
	got     [2]string // last {status, "N/A"} — captured for assertion
	rows    []*types.AgentVersion
	listErr error
}

func (s *stubVersionListRepo) List(ctx context.Context, status string, limit int) ([]*types.AgentVersion, error) {
	s.got[0] = status
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.rows, nil
}

func TestAgentVersionHandler_ListJSON_ReturnsRows(t *testing.T) {
	repo := &stubVersionListRepo{rows: []*types.AgentVersion{
		{ID: "v1", AgentID: "a1", Status: "PENDING"},
	}}
	h := handler.NewAgentVersionHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions.json?status=PENDING", nil)
	w := httptest.NewRecorder()
	h.ListJSON(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if repo.got[0] != "PENDING" {
		t.Errorf("repo saw status = %q, want PENDING", repo.got[0])
	}
	var out []types.AgentVersion
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body = %s", err, w.Body.String())
	}
	if len(out) != 1 || out[0].ID != "v1" {
		t.Errorf("rows = %+v, want [{ID:v1}]", out)
	}
}

func TestAgentVersionHandler_ListJSON_EmptyIsJSONArray(t *testing.T) {
	repo := &stubVersionListRepo{rows: nil}
	h := handler.NewAgentVersionHandler(repo)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions.json", nil)
	w := httptest.NewRecorder()
	h.ListJSON(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "[]\n" {
		t.Errorf("body = %q, want []", got)
	}
}

func TestAgentVersionHandler_ListJSON_InvalidStatus_400(t *testing.T) {
	h := handler.NewAgentVersionHandler(&stubVersionListRepo{})
	req := httptest.NewRequest(http.MethodGet, "/agent_versions.json?status=BOGUS", nil)
	w := httptest.NewRecorder()
	h.ListJSON(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAgentVersionHandler_ListJSON_InvalidLimit_400(t *testing.T) {
	h := handler.NewAgentVersionHandler(&stubVersionListRepo{})
	req := httptest.NewRequest(http.MethodGet, "/agent_versions.json?limit=-3", nil)
	w := httptest.NewRecorder()
	h.ListJSON(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAgentVersionHandler_ListJSON_RepoError_500(t *testing.T) {
	h := handler.NewAgentVersionHandler(&stubVersionListRepo{listErr: errors.New("boom")})
	req := httptest.NewRequest(http.MethodGet, "/agent_versions.json?status=PENDING", nil)
	w := httptest.NewRecorder()
	h.ListJSON(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
