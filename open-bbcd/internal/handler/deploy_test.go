package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type stubDeployAgentRepo struct {
	agent  *types.Agent
	getErr error
}

func (s *stubDeployAgentRepo) GetByID(ctx context.Context, agentID string) (*types.Agent, error) {
	return s.agent, s.getErr
}

type stubDeployVersionRepo struct {
	version       *types.AgentVersion
	getErr        error
	deployErr     error
	undeployErr   error
	prev          *string
	deployCalls   int
	undeployCalls int
	currentID     string
	currentErr    error
}

func (s *stubDeployVersionRepo) GetByID(ctx context.Context, versionID string) (*types.AgentVersion, error) {
	return s.version, s.getErr
}
func (s *stubDeployVersionRepo) Deploy(ctx context.Context, versionID string) (*string, error) {
	s.deployCalls++
	if s.deployErr != nil {
		return nil, s.deployErr
	}
	if s.version != nil {
		s.version.Status = string(types.AgentStatusDeployed)
	}
	return s.prev, nil
}
func (s *stubDeployVersionRepo) Undeploy(ctx context.Context, versionID string) error {
	s.undeployCalls++
	if s.undeployErr != nil {
		return s.undeployErr
	}
	if s.version != nil {
		s.version.Status = string(types.AgentStatusReady)
	}
	return nil
}
func (s *stubDeployVersionRepo) CurrentDeployedID(ctx context.Context, agentID string) (string, error) {
	return s.currentID, s.currentErr
}

type stubDeployWiringRepo struct {
	mapping map[string]string
}

func (s *stubDeployWiringRepo) ListEndpointBackends(ctx context.Context, agentID string) (map[string]string, error) {
	if s.mapping == nil {
		return map[string]string{}, nil
	}
	return s.mapping, nil
}

func newDeployMux(agents DeployAgentRepository, versions DeployVersionRepository, wiring DeployWiringRepo) *http.ServeMux {
	if wiring == nil {
		wiring = &stubDeployWiringRepo{}
	}
	h := NewDeployHandler(agents, versions, wiring)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agents/{agent_id}/deploy", h.Deploy)
	mux.HandleFunc("POST /agents/{agent_id}/undeploy", h.Undeploy)
	return mux
}

func TestDeployHandler_HappyPath(t *testing.T) {
	agent := &types.Agent{ID: "a1", Name: "test"}
	// No bundle — validation trivially passes (empty bundle early-return).
	version := &types.AgentVersion{ID: "v1", AgentID: "a1", Status: "READY"}
	mux := newDeployMux(
		&stubDeployAgentRepo{agent: agent},
		&stubDeployVersionRepo{version: version},
		nil, // empty wiring stub — no endpoints to validate
	)
	body, _ := json.Marshal(deployBody{VersionID: "v1"})
	req := httptest.NewRequest("POST", "/agents/a1/deploy", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp deployResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Version.Status != "DEPLOYED" {
		t.Fatalf("version status %q", resp.Version.Status)
	}
}

// The BO deploy modal posts via htmx's hx-vals which serialises to
// application/x-www-form-urlencoded, not JSON. Verify the handler accepts
// that content type too (regression: prior implementation only decoded JSON
// and returned 400 "invalid character 'v'..." on urlencoded bodies).
func TestDeployHandler_FormURLEncoded_HappyPath(t *testing.T) {
	agent := &types.Agent{ID: "a1", Name: "test"}
	version := &types.AgentVersion{ID: "v1", AgentID: "a1", Status: "READY"}
	mux := newDeployMux(
		&stubDeployAgentRepo{agent: agent},
		&stubDeployVersionRepo{version: version},
		nil,
	)
	req := httptest.NewRequest("POST", "/agents/a1/deploy", strings.NewReader("version_id=v1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, body: %s", rr.Code, rr.Body.String())
	}
}

func TestDeployHandler_MissingVersionID_400(t *testing.T) {
	mux := newDeployMux(&stubDeployAgentRepo{agent: &types.Agent{ID: "a1"}}, &stubDeployVersionRepo{}, nil)
	req := httptest.NewRequest("POST", "/agents/a1/deploy", bytes.NewReader([]byte(`{}`)))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestDeployHandler_WrongAgent_404(t *testing.T) {
	version := &types.AgentVersion{ID: "v1", AgentID: "a2", Status: "READY"} // different agent
	mux := newDeployMux(&stubDeployAgentRepo{agent: &types.Agent{ID: "a1"}}, &stubDeployVersionRepo{version: version}, nil)
	body, _ := json.Marshal(deployBody{VersionID: "v1"})
	req := httptest.NewRequest("POST", "/agents/a1/deploy", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestDeployHandler_NotDeployable_409(t *testing.T) {
	version := &types.AgentVersion{ID: "v1", AgentID: "a1", Status: "DRAFT"}
	mux := newDeployMux(
		&stubDeployAgentRepo{agent: &types.Agent{ID: "a1"}},
		&stubDeployVersionRepo{version: version, deployErr: types.ErrAgentNotDeployable},
		nil,
	)
	body, _ := json.Marshal(deployBody{VersionID: "v1"})
	req := httptest.NewRequest("POST", "/agents/a1/deploy", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestDeployHandler_ReportsPreviousDeployed(t *testing.T) {
	prev := "v-old"
	version := &types.AgentVersion{ID: "v2", AgentID: "a1", Status: "READY"}
	mux := newDeployMux(
		&stubDeployAgentRepo{agent: &types.Agent{ID: "a1"}},
		&stubDeployVersionRepo{version: version, prev: &prev},
		nil,
	)
	body, _ := json.Marshal(deployBody{VersionID: "v2"})
	req := httptest.NewRequest("POST", "/agents/a1/deploy", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var resp deployResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.PreviousDeployedVersionID == nil || *resp.PreviousDeployedVersionID != prev {
		t.Fatalf("prev=%v want %q", resp.PreviousDeployedVersionID, prev)
	}
}

func TestUndeployHandler_HappyPath(t *testing.T) {
	agent := &types.Agent{ID: "a1"}
	version := &types.AgentVersion{ID: "v1", AgentID: "a1", Status: "DEPLOYED"}
	mux := newDeployMux(
		&stubDeployAgentRepo{agent: agent},
		&stubDeployVersionRepo{version: version, currentID: "v1"},
		nil,
	)
	req := httptest.NewRequest("POST", "/agents/a1/undeploy", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestUndeployHandler_NoneDeployed_409(t *testing.T) {
	mux := newDeployMux(
		&stubDeployAgentRepo{agent: &types.Agent{ID: "a1"}},
		&stubDeployVersionRepo{currentID: ""}, // none deployed
		nil,
	)
	req := httptest.NewRequest("POST", "/agents/a1/undeploy", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestDeployHandler_BlocksWhenEndpointsUnmapped(t *testing.T) {
	architectureJSON := `{"tools":[{"id":"orders.create"},{"id":"orders.list"}]}`
	agentRepo := &stubDeployAgentRepo{
		agent: &types.Agent{ID: "a1", Name: "test", Architecture: json.RawMessage(architectureJSON)},
	}
	versionRepo := &stubDeployVersionRepo{
		version: &types.AgentVersion{
			ID:      "v1",
			AgentID: "a1",
			Status:  string(types.AgentStatusReady),
		},
	}
	wiring := &stubDeployWiringRepo{
		mapping: map[string]string{
			"orders.create": "backend-1",
			// orders.list is missing → deploy must fail
		},
	}
	mux := newDeployMux(agentRepo, versionRepo, wiring)

	body, _ := json.Marshal(map[string]string{"version_id": "v1"})
	req := httptest.NewRequest("POST", "/agents/a1/deploy", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "orders.list") {
		t.Fatalf("error should name the missing endpoint, got %s", w.Body.String())
	}
}

func TestDeployHandler_AllowsWhenAllEndpointsMapped(t *testing.T) {
	architectureJSON := `{"tools":[{"id":"orders.create"}]}`
	agentRepo := &stubDeployAgentRepo{
		agent: &types.Agent{ID: "a1", Name: "test", Architecture: json.RawMessage(architectureJSON)},
	}
	versionRepo := &stubDeployVersionRepo{
		version: &types.AgentVersion{
			ID:      "v1",
			AgentID: "a1",
			Status:  string(types.AgentStatusReady),
		},
	}
	wiring := &stubDeployWiringRepo{
		mapping: map[string]string{"orders.create": "backend-1"},
	}
	mux := newDeployMux(agentRepo, versionRepo, wiring)

	body, _ := json.Marshal(map[string]string{"version_id": "v1"})
	req := httptest.NewRequest("POST", "/agents/a1/deploy", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
}
