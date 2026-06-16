package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// stubDeployRepo implements DeployAgentRepository in memory.
type stubDeployRepo struct {
	agent         *types.Agent
	getErr        error
	deployErr     error
	undeployErr   error
	prev          *string
	deployCalls   int
	undeployCalls int
}

func (s *stubDeployRepo) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return s.agent, s.getErr
}
func (s *stubDeployRepo) Deploy(ctx context.Context, versionID string) (*string, error) {
	s.deployCalls++
	if s.deployErr != nil {
		return nil, s.deployErr
	}
	if s.agent != nil {
		s.agent.Status = string(types.AgentStatusDeployed)
	}
	return s.prev, nil
}
func (s *stubDeployRepo) Undeploy(ctx context.Context, versionID string) error {
	s.undeployCalls++
	if s.undeployErr != nil {
		return s.undeployErr
	}
	if s.agent != nil {
		s.agent.Status = string(types.AgentStatusReady)
	}
	return nil
}

func newDeployMux(repo DeployAgentRepository) *http.ServeMux {
	h := NewDeployHandler(repo)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agents/{id}/deploy", h.Deploy)
	mux.HandleFunc("POST /agents/{id}/undeploy", h.Undeploy)
	return mux
}

func TestDeployHandler_HappyPath(t *testing.T) {
	repo := &stubDeployRepo{
		agent: &types.Agent{ID: "v1", AgentID: "v1", Status: "READY"},
	}
	mux := newDeployMux(repo)

	req := httptest.NewRequest("POST", "/agents/v1/deploy", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, body: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Agent                     types.Agent `json:"agent"`
		PreviousDeployedVersionID *string     `json:"previous_deployed_version_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Agent.Status != "DEPLOYED" {
		t.Fatalf("status: %q", body.Agent.Status)
	}
	if body.PreviousDeployedVersionID != nil {
		t.Fatalf("expected nil prev, got %v", body.PreviousDeployedVersionID)
	}
	if repo.deployCalls != 1 {
		t.Fatalf("deploy calls = %d", repo.deployCalls)
	}
}

func TestDeployHandler_NotDeployable_409(t *testing.T) {
	repo := &stubDeployRepo{
		agent:     &types.Agent{ID: "v1", AgentID: "v1", Status: "DRAFT"},
		deployErr: types.ErrAgentNotDeployable,
	}
	mux := newDeployMux(repo)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/agents/v1/deploy", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("got %d, body: %s", rr.Code, rr.Body.String())
	}
}

func TestDeployHandler_ReportsPreviousDeployed(t *testing.T) {
	prev := "v-old"
	repo := &stubDeployRepo{
		agent: &types.Agent{ID: "v2", AgentID: "v1", Status: "READY"},
		prev:  &prev,
	}
	mux := newDeployMux(repo)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/agents/v2/deploy", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d", rr.Code)
	}
	var body struct {
		Agent                     types.Agent `json:"agent"`
		PreviousDeployedVersionID *string     `json:"previous_deployed_version_id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.PreviousDeployedVersionID == nil || *body.PreviousDeployedVersionID != prev {
		t.Fatalf("prev=%v want %q", body.PreviousDeployedVersionID, prev)
	}
}

func TestUndeployHandler_HappyPath(t *testing.T) {
	repo := &stubDeployRepo{
		agent: &types.Agent{ID: "v1", AgentID: "v1", Status: "DEPLOYED"},
	}
	mux := newDeployMux(repo)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/agents/v1/undeploy", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rr.Code, rr.Body.String())
	}
	var ag types.Agent
	_ = json.Unmarshal(rr.Body.Bytes(), &ag)
	if ag.Status != "READY" {
		t.Fatalf("status: %q", ag.Status)
	}
}

func TestUndeployHandler_NotDeployed_409(t *testing.T) {
	repo := &stubDeployRepo{
		agent:       &types.Agent{ID: "v1", AgentID: "v1", Status: "DRAFT"},
		undeployErr: types.ErrAgentNotDeployed,
	}
	mux := newDeployMux(repo)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/agents/v1/undeploy", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("got %d", rr.Code)
	}
}
