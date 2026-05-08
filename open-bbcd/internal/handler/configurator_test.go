package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
)

type stubConfigStore struct {
	cfg      types.FlowMapConfig
	getErr   error
	parseErr string
	updates  int
	updateFn func(cfg []byte) error
}

func (s *stubConfigStore) GetFlowMapConfig(ctx context.Context, agentID string) ([]byte, string, error) {
	if s.getErr != nil {
		return nil, "", s.getErr
	}
	b, _ := json.Marshal(s.cfg)
	return b, s.parseErr, nil
}

func (s *stubConfigStore) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return &types.Agent{ID: id, Name: s.cfg.Name, Status: "INITIALIZING"}, nil
}

func (s *stubConfigStore) UpdateFlowMapConfig(ctx context.Context, agentID string, cfg []byte) error {
	s.updates++
	if s.updateFn != nil {
		return s.updateFn(cfg)
	}
	var decoded types.FlowMapConfig
	if err := json.Unmarshal(cfg, &decoded); err != nil {
		return err
	}
	s.cfg = decoded
	return nil
}

func sampleConfig() types.FlowMapConfig {
	return types.FlowMapConfig{
		SchemaVersion: 1, Name: "test-agent",
		Source:       types.FlowMapSource{AppName: "sample"},
		Capabilities: []types.Capability{{Name: "orders", Summary: "orders"}},
		Skills: []types.Skill{{
			ID: "place-order", Origin: "discovered", Name: "Place order",
			Role: "write", CapabilityRef: "orders", ProposedTool: "orders.create",
		}},
		Flows: []types.Flow{{
			ID: "place-order", Origin: "discovered", Included: true,
			Name:     "Place order",
			Workflow: types.Workflow{Mermaid: "flowchart TD\n  start([start]) --> e([end])"},
		}},
	}
}

func newConfigHandler(t *testing.T, getter handler.ConfigStore) *handler.ConfiguratorHandler {
	t.Helper()
	h, err := handler.NewConfiguratorHandler(getter, web.Assets)
	if err != nil {
		t.Fatalf("NewConfiguratorHandler: %v", err)
	}
	return h
}

func TestConfigurator_FlowsTab_RendersFlowsList(t *testing.T) {
	h := newConfigHandler(t, &stubConfigStore{cfg: sampleConfig()})
	req := httptest.NewRequest(http.MethodGet, "/agents/abc/configure/flows", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Flows(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "place-order") {
		t.Errorf("body should contain flow id; first 200 chars: %s", w.Body.String()[:200])
	}
}

func TestConfigurator_SkillsTab_ShowsSkillRow(t *testing.T) {
	h := newConfigHandler(t, &stubConfigStore{cfg: sampleConfig()})
	req := httptest.NewRequest(http.MethodGet, "/agents/abc/configure/skills", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Skills(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "place-order") || !strings.Contains(w.Body.String(), "orders.create") {
		t.Errorf("Skills tab missing expected row content")
	}
}

func TestConfigurator_CapabilitiesTab_IsReadOnly(t *testing.T) {
	h := newConfigHandler(t, &stubConfigStore{cfg: sampleConfig()})
	req := httptest.NewRequest(http.MethodGet, "/agents/abc/configure/capabilities", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Capabilities(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Capabilities are derived") {
		t.Errorf("Capabilities tab missing read-only banner")
	}
}

func TestConfigurator_ParseError_ShowsErrorBanner(t *testing.T) {
	h := newConfigHandler(t, &stubConfigStore{cfg: sampleConfig(), parseErr: "missing tools-proposed.json"})
	req := httptest.NewRequest(http.MethodGet, "/agents/abc/configure", nil)
	req.SetPathValue("id", "abc")
	w := httptest.NewRecorder()
	h.Index(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "missing tools-proposed.json") {
		t.Errorf("Parse error not surfaced")
	}
}
