package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
	"gopkg.in/yaml.v3"
)

type stubConfigStore struct {
	cfg           types.FlowMapConfig
	getErr        error
	parseErr      string
	updates       int
	updateFn      func(cfg []byte) error
	statusFn      func(versionID, expectedFrom, to string) error
	currentStatus string // optional override; defaults to "INITIALIZING"
}

func (s *stubConfigStore) GetFlowMapConfig(ctx context.Context, versionID string) ([]byte, string, error) {
	if s.getErr != nil {
		return nil, "", s.getErr
	}
	b, _ := json.Marshal(s.cfg)
	return b, s.parseErr, nil
}

func (s *stubConfigStore) GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error) {
	status := s.currentStatus
	if status == "" {
		status = "INITIALIZING"
	}
	// The stub uses the URL's version_id for both ids — there's only one
	// config in this fake, and per-version calls (GetFlowMapConfig /
	// UpdateFlowMapConfig) ignore the id anyway.
	version := &types.AgentVersion{ID: versionID, AgentID: versionID, Status: status}
	agent := &types.Agent{ID: versionID, Name: s.cfg.Name}
	return version, agent, nil
}

func (s *stubConfigStore) UpdateFlowMapConfig(ctx context.Context, versionID string, cfg []byte) error {
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

func (s *stubConfigStore) UpdateStatus(ctx context.Context, versionID, expectedFrom, to string) error {
	if s.statusFn != nil {
		return s.statusFn(versionID, expectedFrom, to)
	}
	cur := s.currentStatus
	if cur == "" {
		cur = "INITIALIZING"
	}
	if cur != expectedFrom {
		return fmt.Errorf("%w: have %q, want %q", types.ErrInvalidAgentStatus, cur, expectedFrom)
	}
	s.currentStatus = to
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
	schemaBytes, err := web.Assets.ReadFile("schemas/wizard-v1.yaml")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema types.WizardSchema
	if err := yaml.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	h, err := handler.NewConfiguratorHandler(getter, &schema, web.Assets)
	if err != nil {
		t.Fatalf("NewConfiguratorHandler: %v", err)
	}
	return h
}

func TestConfigurator_FlowsTab_RendersFlowsList(t *testing.T) {
	h := newConfigHandler(t, &stubConfigStore{cfg: sampleConfig()})
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/configure/flows", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.Flows(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "place-order") {
		t.Errorf("body should contain flow id; first 200 chars: %s", w.Body.String()[:200])
	}
}

func TestConfigurator_FlowsTab_NodeCountReflectsMermaid(t *testing.T) {
	cfg := sampleConfig()
	// Three nodes: start, one skill (place-order), end. Layout intentionally empty.
	cfg.Flows[0].Workflow = types.Workflow{
		Mermaid: "flowchart TD\n  start([start]) --> s_po[place-order]\n  s_po --> e([end])\n",
	}
	h := newConfigHandler(t, &stubConfigStore{cfg: cfg})
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/configure/flows", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.Flows(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "3 nodes") {
		t.Errorf("flow row should show 3 nodes; body did not contain it")
	}
}

func TestConfigurator_SkillsTab_ShowsSkillRow(t *testing.T) {
	h := newConfigHandler(t, &stubConfigStore{cfg: sampleConfig()})
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/configure/skills", nil)
	req.SetPathValue("version_id", "abc")
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
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/configure/capabilities", nil)
	req.SetPathValue("version_id", "abc")
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
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/configure", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.Index(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "missing tools-proposed.json") {
		t.Errorf("Parse error not surfaced")
	}
}

func TestConfigurator_FlowIncluded_Toggle(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	// Toggle off
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/flows/place-order/included",
		strings.NewReader("included=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.FlowIncluded(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if store.cfg.Flows[0].Included {
		t.Error("Flows[0].Included should be false after toggle")
	}
	// The response is an htmx-friendly fragment containing the updated row.
	if !strings.Contains(w.Body.String(), "place-order") {
		t.Errorf("response should re-render the flow row")
	}

	// Toggle back on.
	req2 := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/flows/place-order/included",
		strings.NewReader("included=true"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.SetPathValue("version_id", "abc")
	req2.SetPathValue("flowId", "place-order")
	w2 := httptest.NewRecorder()
	h.FlowIncluded(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d", w2.Code)
	}
	if !store.cfg.Flows[0].Included {
		t.Error("Flows[0].Included should be true after second toggle")
	}
}

func TestConfigurator_FlowIncluded_UnknownFlow_404(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/flows/ghost/included",
		strings.NewReader("included=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("flowId", "ghost")
	w := httptest.NewRecorder()
	h.FlowIncluded(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestConfigurator_SkillUpdate_HappyPath(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":          {"Place an order"},
		"description":   {"Updated description"},
		"role":          {"write"},
		"capability":    {"orders"},
		"proposed_tool": {"orders.create"},
		"user_phrases":  {"check out\nplace order\nbuy"},
		"external":      {"false"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/skills/place-order",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := store.cfg.Skills[0]
	if got.Name != "Place an order" || got.Description != "Updated description" {
		t.Errorf("metadata not saved: %+v", got)
	}
	if len(got.UserPhrases) != 3 || got.UserPhrases[0] != "check out" {
		t.Errorf("user_phrases not split correctly: %+v", got.UserPhrases)
	}
	if got.External {
		t.Error("External should be false")
	}
}

func TestConfigurator_SkillUpdate_External(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":          {"Send notification"},
		"role":          {"write"},
		"external":      {"true"},
		"external_note": {"sends to webhook"},
		"user_phrases":  {""},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/skills/place-order",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got := store.cfg.Skills[0]
	if !got.External || got.ExternalNote != "sends to webhook" {
		t.Errorf("External/Note not saved: %+v", got)
	}
	if got.CapabilityRef != "" {
		t.Errorf("CapabilityRef should be cleared when external=true, got %q", got.CapabilityRef)
	}
}

func TestConfigurator_SkillUpdate_InvalidRole(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name": {"Some skill"},
		"role": {"banana"}, // invalid
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/skills/place-order",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillUpdate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestConfigurator_SkillCreate_HappyPath(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":          {"Send Email Alert"},
		"description":   {"Notify the user via email"},
		"role":          {"write"},
		"external":      {"true"},
		"external_note": {"sends through SMTP relay"},
		"user_phrases":  {"send email\nemail me"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/skills",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.SkillCreate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	if len(store.cfg.Skills) != 2 {
		t.Fatalf("Skills len = %d, want 2", len(store.cfg.Skills))
	}
	created := store.cfg.Skills[1]
	if created.ID != "send-email-alert" {
		t.Errorf("created.ID = %q, want send-email-alert", created.ID)
	}
	if created.Origin != "custom" {
		t.Errorf("created.Origin = %q, want custom", created.Origin)
	}
	if !created.External {
		t.Error("External should be true")
	}
}

func TestConfigurator_SkillCreate_NameCollision_GetsDiscriminator(t *testing.T) {
	cfg := sampleConfig()
	// Existing skill is "place-order"; force a collision by using the same name.
	store := &stubConfigStore{cfg: cfg}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":     {"Place Order"},
		"role":     {"write"},
		"external": {"true"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/skills",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.SkillCreate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	created := store.cfg.Skills[len(store.cfg.Skills)-1]
	if !strings.HasPrefix(created.ID, "place-order-") || created.ID == "place-order" {
		t.Errorf("collision id = %q, want place-order-<hex>", created.ID)
	}
}

func TestConfigurator_SkillCreate_NameRequired(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{"role": {"write"}, "external": {"true"}}
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/skills",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.SkillCreate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestConfigurator_SkillDelete_Custom_OK(t *testing.T) {
	cfg := sampleConfig()
	cfg.Skills = append(cfg.Skills, types.Skill{
		ID: "custom-thing", Origin: "custom", Name: "Custom thing", Role: "write",
		External: true,
	})
	store := &stubConfigStore{cfg: cfg}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodDelete,
		"/agent_versions/abc/configure/skills/custom-thing", nil)
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("skillId", "custom-thing")
	w := httptest.NewRecorder()
	h.SkillDelete(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	for _, s := range store.cfg.Skills {
		if s.ID == "custom-thing" {
			t.Error("custom-thing should have been removed")
		}
	}
}

func TestConfigurator_SkillDelete_Discovered_409(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodDelete,
		"/agent_versions/abc/configure/skills/place-order", nil)
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillDelete(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (cannot delete discovered skill)", w.Code)
	}
}

func TestConfigurator_SkillDelete_Referenced_409(t *testing.T) {
	cfg := sampleConfig()
	// Add a custom skill that is referenced by the existing flow's workflow.
	cfg.Skills = append(cfg.Skills, types.Skill{
		ID: "needed-by-flow", Origin: "custom", Name: "Needed", Role: "write",
		External: true,
	})
	cfg.Flows[0].Workflow.Mermaid = "flowchart TD\n" +
		"  start([start]) --> a[needed-by-flow]\n" +
		"  a --> e([end])"
	store := &stubConfigStore{cfg: cfg}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodDelete,
		"/agent_versions/abc/configure/skills/needed-by-flow", nil)
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("skillId", "needed-by-flow")
	w := httptest.NewRecorder()
	h.SkillDelete(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (skill referenced by flow workflow)", w.Code)
	}
}

func TestConfigurator_WorkflowUpdate_HappyPath(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	body := `{
		"mermaid": "flowchart TD\n  start([start]) --> s_x[place-order]\n  s_x --> e([end])",
		"layout": {"start": {"x": 40, "y": 40}, "s_x": {"x": 40, "y": 140}, "e": {"x": 40, "y": 240}}
	}`
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/flows/place-order/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got := store.cfg.Flows[0].Workflow
	if !strings.Contains(got.Mermaid, "place-order") {
		t.Errorf("mermaid not saved: %q", got.Mermaid)
	}
	if got.Layout["s_x"].X != 40 || got.Layout["s_x"].Y != 140 {
		t.Errorf("layout not saved: %+v", got.Layout)
	}
}

func TestConfigurator_WorkflowUpdate_RejectsUnknownSkill(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	body := `{
		"mermaid": "flowchart TD\n  start([start]) --> s_x[ghost-skill]\n  s_x --> e([end])",
		"layout": {}
	}`
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/flows/place-order/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown skill)", w.Code)
	}
}

func TestConfigurator_WorkflowUpdate_RejectsMalformedMermaid(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	body := `{"mermaid": "this is not mermaid", "layout": {}}`
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/flows/place-order/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (malformed)", w.Code)
	}
}

func TestConfigurator_WorkflowUpdate_UnknownFlow_404(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	body := `{"mermaid": "flowchart TD\n  start([start]) --> e([end])", "layout": {}}`
	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/configure/flows/ghost/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("version_id", "abc")
	req.SetPathValue("flowId", "ghost")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestConfigurator_FinalizeConfirm_RendersPage(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodGet,
		"/agent_versions/abc/configure/finalize", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.FinalizeConfirm(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Finalize") {
		t.Errorf("response should contain a Finalize heading or button: first 200 chars = %s", body[:minInt(200, len(body))])
	}
	if !strings.Contains(body, "/agent_versions/abc/finalize") {
		t.Errorf("response should include the POST target: first 200 chars = %s", body[:minInt(200, len(body))])
	}
}

func TestConfigurator_Finalize_HappyPath_RedirectsToAgentsUI(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()} // currentStatus defaults to "INITIALIZING"
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/finalize", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.Finalize(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/agents/ui" {
		t.Errorf("Location = %q, want /agents/ui", loc)
	}
	if store.currentStatus != "DRAFT" {
		t.Errorf("status = %q, want DRAFT", store.currentStatus)
	}
}

func TestConfigurator_Finalize_WrongStatus_409(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig(), currentStatus: "DRAFT"}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodPost,
		"/agent_versions/abc/finalize", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.Finalize(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestConfigurator_DownloadYAML_HappyPath(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig(), currentStatus: "DRAFT"}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/config.yaml", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.DownloadYAML(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want to contain 'attachment'", cd)
	}

	body := w.Body.String()
	if !strings.Contains(body, "schema_version") {
		t.Errorf("yaml should contain schema_version: %s", body[:minInt(200, len(body))])
	}
	if !strings.Contains(body, "test-agent") {
		t.Errorf("yaml should contain the agent name: %s", body[:minInt(200, len(body))])
	}
}

func TestConfigurator_DownloadYAML_RoundTrip(t *testing.T) {
	cfg := sampleConfig()
	store := &stubConfigStore{cfg: cfg, currentStatus: "DRAFT"}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/config.yaml", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.DownloadYAML(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var decoded types.FlowMapConfig
	if err := yaml.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}

	if decoded.Name != cfg.Name {
		t.Errorf("Name mismatch: %q vs %q", decoded.Name, cfg.Name)
	}
	if len(decoded.Flows) != len(cfg.Flows) {
		t.Fatalf("Flows len mismatch: %d vs %d", len(decoded.Flows), len(cfg.Flows))
	}
	if decoded.Flows[0].Workflow.Mermaid != cfg.Flows[0].Workflow.Mermaid {
		t.Errorf("Workflow.Mermaid not preserved")
	}
	if len(decoded.Skills) != len(cfg.Skills) {
		t.Errorf("Skills len mismatch: %d vs %d", len(decoded.Skills), len(cfg.Skills))
	}
	if len(decoded.Capabilities) != len(cfg.Capabilities) {
		t.Errorf("Capabilities len mismatch: %d vs %d", len(decoded.Capabilities), len(cfg.Capabilities))
	}
}

// TestConfigurator_DownloadYAML_NoBlockScalarIndentIndicators verifies the
// emitted YAML never carries explicit indent indicators (`|4`, `>+2`) on
// block scalar headers. These are valid YAML 1.2 but interoperability-hostile
// — other YAML readers can interpret them differently from yaml.v3's
// emission. The handler strips the digits so any downstream reader sees
// a bare `|`.
func TestConfigurator_DownloadYAML_NoBlockScalarIndentIndicators(t *testing.T) {
	cfg := sampleConfig()
	// Multi-line prose with internally-indented content is exactly what
	// triggers yaml.v3 to emit `|N` as a safety measure.
	cfg.Capabilities[0].ProseMD = "# Orders\n\n<!-- AGENT id=\"overview\" -->\n" +
		"    indented body line\n" +
		"<!-- /AGENT -->\n\n## Concepts\n\n    nested code block\n"
	store := &stubConfigStore{cfg: cfg, currentStatus: "DRAFT"}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/config.yaml", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.DownloadYAML(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	body := w.Body.String()
	// No header line should end with a digit after | or >. Scan every line.
	for i, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimRight(line, " ")
		// We only care about block-scalar headers: a line where the value
		// portion is exactly | or > (optionally with +/- and a digit).
		if !strings.Contains(trimmed, ": |") && !strings.Contains(trimmed, ": >") {
			continue
		}
		// Find the indicator and check for a trailing digit.
		for _, marker := range []string{": |", ": >"} {
			idx := strings.Index(trimmed, marker)
			if idx < 0 {
				continue
			}
			tail := trimmed[idx+len(marker):]
			// Skip optional + or - chomping indicator.
			tail = strings.TrimLeft(tail, "+-")
			if len(tail) > 0 && tail[0] >= '0' && tail[0] <= '9' {
				t.Errorf("line %d carries explicit indent indicator: %q", i+1, line)
			}
		}
	}

	// And the round-trip still works.
	var decoded types.FlowMapConfig
	if err := yaml.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	if decoded.Capabilities[0].ProseMD != cfg.Capabilities[0].ProseMD {
		t.Errorf("ProseMD content not preserved through normalization+roundtrip")
	}
}

// TestConfigurator_DownloadYAML_CleanDropsExcludedAndOrphans verifies the
// ?clean=true query parameter strips flows with included=false and
// capabilities not referenced by any remaining skill. The full export
// (default) keeps everything for audit and round-trip.
func TestConfigurator_DownloadYAML_CleanDropsExcludedAndOrphans(t *testing.T) {
	cfg := types.FlowMapConfig{
		SchemaVersion: 1, Name: "test-agent",
		Source: types.FlowMapSource{AppName: "sample"},
		Capabilities: []types.Capability{
			{Name: "orders", Summary: "used"},
			{Name: "settings", Summary: "orphan — no skill references it"},
		},
		Skills: []types.Skill{{
			ID: "place-order", Origin: "discovered", Name: "Place order",
			Role: "write", CapabilityRef: "orders", ProposedTool: "orders.create",
		}},
		Flows: []types.Flow{
			{
				ID: "place-order-flow", Origin: "discovered", Included: true,
				Name:     "Place order flow",
				Workflow: types.Workflow{Mermaid: "flowchart TD\n  a-->b"},
			},
			{
				ID: "abandoned-flow", Origin: "discovered", Included: false,
				Name:     "Abandoned",
				Workflow: types.Workflow{Mermaid: "flowchart TD\n  x-->y"},
			},
		},
	}
	store := &stubConfigStore{cfg: cfg, currentStatus: "DRAFT"}
	h := newConfigHandler(t, store)

	// Default (no clean=true): full content preserved.
	reqFull := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/config.yaml", nil)
	reqFull.SetPathValue("version_id", "abc")
	wFull := httptest.NewRecorder()
	h.DownloadYAML(wFull, reqFull)
	var full types.FlowMapConfig
	if err := yaml.Unmarshal(wFull.Body.Bytes(), &full); err != nil {
		t.Fatalf("full unmarshal: %v", err)
	}
	if len(full.Flows) != 2 {
		t.Errorf("full export should keep both flows, got %d", len(full.Flows))
	}
	if len(full.Capabilities) != 2 {
		t.Errorf("full export should keep both capabilities, got %d", len(full.Capabilities))
	}

	// ?clean=true: filtered.
	reqClean := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/config.yaml?clean=true", nil)
	reqClean.SetPathValue("version_id", "abc")
	wClean := httptest.NewRecorder()
	h.DownloadYAML(wClean, reqClean)
	var clean types.FlowMapConfig
	if err := yaml.Unmarshal(wClean.Body.Bytes(), &clean); err != nil {
		t.Fatalf("clean unmarshal: %v", err)
	}
	if len(clean.Flows) != 1 || clean.Flows[0].ID != "place-order-flow" {
		t.Errorf("clean export should keep only the included flow, got %+v",
			[]string{clean.Flows[0].ID})
	}
	if len(clean.Capabilities) != 1 || clean.Capabilities[0].Name != "orders" {
		t.Errorf("clean export should drop the orphan capability, got %d caps",
			len(clean.Capabilities))
	}
	if len(clean.Skills) != len(cfg.Skills) {
		t.Errorf("clean export should not touch skills, got %d (want %d)",
			len(clean.Skills), len(cfg.Skills))
	}
}
