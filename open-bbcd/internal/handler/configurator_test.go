package handler_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
	"gopkg.in/yaml.v3"
)

func TestConfiguratorRouter_OldFlowsRedirectsToArchitecture(t *testing.T) {
	mux := http.NewServeMux()
	handler.RegisterConfiguratorRedirects(mux)
	versionID := "11111111-1111-1111-1111-111111111111"
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/configure/flows", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", w.Code)
	}
	want := "/agent_versions/" + versionID + "/configure/architecture/flows"
	if got := w.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestConfiguratorRouter_OldSkillsRedirectsToArchitecture(t *testing.T) {
	mux := http.NewServeMux()
	handler.RegisterConfiguratorRedirects(mux)
	versionID := "22222222-2222-2222-2222-222222222222"
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/configure/skills", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", w.Code)
	}
	want := "/agent_versions/" + versionID + "/configure/architecture/skills"
	if got := w.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestConfiguratorRouter_OldCapabilitiesRedirectsToEndpoints(t *testing.T) {
	// In v2, /configure/capabilities was renamed to /configure/endpoints.
	// Both the pre-architecture URL and the pre-rename name should redirect
	// to the current canonical path /configure/architecture/endpoints.
	mux := http.NewServeMux()
	handler.RegisterConfiguratorRedirects(mux)
	versionID := "33333333-3333-3333-3333-333333333333"
	// /configure/endpoints → /configure/architecture/endpoints
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/configure/endpoints", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", w.Code)
	}
	want := "/agent_versions/" + versionID + "/configure/architecture/endpoints"
	if got := w.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestConfiguratorRouter_ArchitectureIndexRedirectsToFlows(t *testing.T) {
	mux := http.NewServeMux()
	handler.RegisterConfiguratorRedirects(mux)
	versionID := "44444444-4444-4444-4444-444444444444"
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/configure/architecture", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusFound && w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301 or 302", w.Code)
	}
	want := "/agent_versions/" + versionID + "/configure/architecture/flows"
	if got := w.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

type stubConfigStore struct {
	cfg           types.FlowMapConfig
	getErr        error
	parseErr      string
	updates       int
	updateFn      func(cfg []byte) error
	statusFn      func(versionID, expectedFrom, to string) error
	currentStatus string // optional override; defaults to "INITIALIZING"
	architecture  []byte // optional agent-level architecture blob
	prompts       []byte // optional version-level prompts blob (rendered by the Prompts tab)
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
	version := &types.AgentVersion{ID: versionID, AgentID: versionID, Status: status, Prompts: s.prompts}
	agent := &types.Agent{ID: versionID, Name: s.cfg.Name, Architecture: s.architecture}
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
		SchemaVersion: 2, Name: "test-agent",
		Source: types.FlowMapSource{AppName: "sample"},
		Endpoints: []types.Endpoint{
			{ID: "orders.create", Method: "POST", Path: "/api/orders", Auth: "bearer", UsedBySkills: []string{"place-order"}},
		},
		Skills: []types.Skill{{
			ID: "place-order", Origin: "discovered", Name: "Place order",
			SuggestedEndpoints: []types.SkillEndpointRef{{Endpoint: "orders.create", Role: "write"}},
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
	h, err := handler.NewConfiguratorHandler(getter, nil, nil, nil, &schema, web.Assets)
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
	if !strings.Contains(w.Body.String(), "place-order") || !strings.Contains(w.Body.String(), "1 endpoints") {
		t.Errorf("Skills tab missing expected row content")
	}
}

func TestConfigurator_EndpointsTab_IsReadOnly(t *testing.T) {
	h := newConfigHandler(t, &stubConfigStore{cfg: sampleConfig()})
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/configure/architecture/endpoints", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.Endpoints(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "orders.create") {
		t.Errorf("Endpoints tab missing endpoint ID in list")
	}
}

func TestConfigurator_ParseError_ShowsErrorBanner(t *testing.T) {
	h := newConfigHandler(t, &stubConfigStore{cfg: sampleConfig(), parseErr: "missing tools-proposed.json"})
	// Index now 302s to /configure/architecture/flows; assert the banner on the
	// landing handler (Flows) which is where the redirect ends up rendering.
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/abc/configure/architecture/flows", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.Flows(w, req)
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
		"name":        {"Place an order"},
		"description": {"Updated description"},
		"domain":      {"Order management"},
		"user_phrases": {"check out\nplace order\nbuy"},
		"external":    {"false"},
	}
	form.Add("suggested_endpoints", "orders.create")
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
	if len(got.SuggestedEndpoints) != 1 || got.SuggestedEndpoints[0].Endpoint != "orders.create" {
		t.Errorf("SuggestedEndpoints not saved correctly: %+v", got.SuggestedEndpoints)
	}
}

func TestConfigurator_SkillUpdate_External(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":          {"Send notification"},
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
	// External skills must not carry suggested endpoints.
	if len(got.SuggestedEndpoints) != 0 {
		t.Errorf("SuggestedEndpoints should be empty when external=true, got %+v", got.SuggestedEndpoints)
	}
}

// TestConfigurator_SkillUpdate_UnknownEndpoint verifies that submitting a
// suggested_endpoints value that is not in the agent's endpoint inventory
// returns 400. This is the v2 equivalent of the old invalid-role check.
func TestConfigurator_SkillUpdate_UnknownEndpoint(t *testing.T) {
	store := &stubConfigStore{cfg: sampleConfig()}
	h := newConfigHandler(t, store)

	form := url.Values{
		"name":    {"Some skill"},
		"external": {"false"},
	}
	form.Add("suggested_endpoints", "ghost.endpoint") // not in inventory
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

	form := url.Values{"external": {"true"}}
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
		ID: "custom-thing", Origin: "custom", Name: "Custom thing",
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
		ID: "needed-by-flow", Origin: "custom", Name: "Needed",
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
	if len(decoded.Endpoints) != len(cfg.Endpoints) {
		t.Errorf("Endpoints len mismatch: %d vs %d", len(decoded.Endpoints), len(cfg.Endpoints))
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
	cfg.Endpoints[0].ProseMD = "# Orders\n\n<!-- AGENT id=\"overview\" -->\n" +
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
	if decoded.Endpoints[0].ProseMD != cfg.Endpoints[0].ProseMD {
		t.Errorf("ProseMD content not preserved through normalization+roundtrip")
	}
}

// TestConfigurator_DownloadYAML_CleanDropsExcludedFlows verifies the
// ?clean=true query parameter strips flows with included=false. In v2 the
// endpoint inventory is always preserved (endpoints are the runtime tool
// catalog), so ?clean=true only filters flows — skills and endpoints are
// unchanged.
func TestConfigurator_DownloadYAML_CleanDropsExcludedFlows(t *testing.T) {
	cfg := types.FlowMapConfig{
		SchemaVersion: 2, Name: "test-agent",
		Source: types.FlowMapSource{AppName: "sample"},
		Endpoints: []types.Endpoint{
			{ID: "orders.create", Method: "POST", Path: "/api/orders", Auth: "bearer", UsedBySkills: []string{"place-order"}},
			{ID: "settings.get", Method: "GET", Path: "/api/settings", Auth: "bearer", UsedBySkills: []string{}},
		},
		Skills: []types.Skill{{
			ID: "place-order", Origin: "discovered", Name: "Place order",
			SuggestedEndpoints: []types.SkillEndpointRef{{Endpoint: "orders.create", Role: "write"}},
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
	if len(full.Endpoints) != 2 {
		t.Errorf("full export should keep both endpoints, got %d", len(full.Endpoints))
	}

	// ?clean=true: excluded flows stripped; endpoints and skills preserved.
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
			func() []string {
				ids := make([]string, len(clean.Flows))
				for i, f := range clean.Flows {
					ids[i] = f.ID
				}
				return ids
			}())
	}
	// In v2, endpoints are always preserved — not filtered.
	if len(clean.Endpoints) != 2 {
		t.Errorf("clean export should keep all endpoints in v2, got %d", len(clean.Endpoints))
	}
	if len(clean.Skills) != len(cfg.Skills) {
		t.Errorf("clean export should not touch skills, got %d (want %d)",
			len(clean.Skills), len(cfg.Skills))
	}
}

// configFixture is the minimum field-set the configurator's layout.html
// reads. It mirrors the shape of (unexported) configPageData; the template
// engine is duck-typed so a local struct works fine for header-rendering
// tests that don't exercise tab content.
type configFixture struct {
	Active      string
	Tab         string
	SubTab      string
	ReadOnly    bool
	HasBundle   bool
	AgentID     string
	AgentName   string
	AgentStatus string
	VersionID   string
	ParseError  string
}

// renderConfigLayoutFixture parses the real layout.html + a stub tab_content
// block and returns the rendered HTML. It registers only the FuncMap entries
// layout.html actually references (statusClass on the status pill); the
// configurator's other helpers belong to per-tab templates that the stub
// replaces.
func renderConfigLayoutFixture(t *testing.T, f configFixture) string {
	t.Helper()
	statusClass := func(status string) string {
		switch status {
		case "DEPLOYED":
			return "deployed"
		case "READY":
			return "ready"
		case "TRAINING":
			return "training"
		case "INITIALIZING":
			return "initializing"
		default:
			return "draft"
		}
	}
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"statusClass": statusClass,
	}).ParseFiles(
		"../../web/templates/layout.html",
		"../../web/templates/configurator/layout.html",
	))
	// Stub tab_content so layout.html's {{template "tab_content" .}} works
	// without parsing the per-tab templates (which pull in other funcs).
	template.Must(tmpl.Parse(`{{define "tab_content"}}<div></div>{{end}}`))
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", f); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return buf.String()
}

func TestConfiguratorLayout_DeployButton_VisibleWhenReady(t *testing.T) {
	body := renderConfigLayoutFixture(t, configFixture{
		Tab: "architecture", SubTab: "flows",
		ReadOnly: true, AgentStatus: "READY",
		AgentID: "a1", VersionID: "v1", AgentName: "Bot",
	})
	if !strings.Contains(body, `hx-get="/agents/a1/deploy/confirm?version_id=v1"`) {
		t.Errorf("expected Deploy button hx-get; body:\n%s", body)
	}
}

func TestConfiguratorLayout_UndeployButton_VisibleWhenDeployed(t *testing.T) {
	body := renderConfigLayoutFixture(t, configFixture{
		Tab: "architecture", SubTab: "flows",
		ReadOnly: true, AgentStatus: "DEPLOYED",
		AgentID: "a1", VersionID: "v1", AgentName: "Bot",
	})
	if !strings.Contains(body, `hx-get="/agents/a1/undeploy/confirm"`) {
		t.Errorf("expected Undeploy button hx-get; body:\n%s", body)
	}
	if strings.Contains(body, `/agents/a1/deploy/confirm`) {
		t.Errorf("Deploy button must not render for DEPLOYED; body:\n%s", body)
	}
}

func TestConfiguratorLayout_DeployAndUndeploy_HiddenWhenDraft(t *testing.T) {
	body := renderConfigLayoutFixture(t, configFixture{
		Tab: "architecture", SubTab: "flows",
		ReadOnly: true, AgentStatus: "DRAFT",
		AgentID: "a1", VersionID: "v1", AgentName: "Bot",
	})
	if strings.Contains(body, `/agents/a1/deploy/confirm`) {
		t.Errorf("Deploy button must not render for DRAFT; body:\n%s", body)
	}
	if strings.Contains(body, `/agents/a1/undeploy/confirm`) {
		t.Errorf("Undeploy button must not render for DRAFT; body:\n%s", body)
	}
}

// TestConfigurator_InputsTab_OmitsAgentLevelFields verifies that the Inputs
// tab renders only the 4 per-version wizard fields (scope, should_do,
// should_not_do, business_domain). The agent-level fields `name` and
// `discovery_file` live on the agent row and are rendered on the agent
// detail header — showing them per-version would be misleading because a
// version cannot diverge from its agent on those fields.
func TestConfigurator_InputsTab_OmitsAgentLevelFields(t *testing.T) {
	cfg := sampleConfig()
	cfg.Scope = "in-scope text"
	cfg.ShouldDo = "should-do text"
	cfg.ShouldNotDo = "should-not-do text"
	cfg.BusinessDomain = "business-domain text"
	store := &stubConfigStore{cfg: cfg, currentStatus: "DRAFT"}
	h := newConfigHandler(t, store)

	req := httptest.NewRequest(http.MethodGet,
		"/agent_versions/abc/configure/inputs", nil)
	req.SetPathValue("version_id", "abc")
	w := httptest.NewRecorder()
	h.Inputs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Per-version labels (from web/schemas/wizard-v1.yaml) must be present.
	perVersionLabels := []string{
		"Describe the scope of your agent",
		"What should your agent do?",
		"What should your agent never do?",
		"Describe your platform business domain",
	}
	for _, label := range perVersionLabels {
		if !strings.Contains(body, label) {
			t.Errorf("Inputs tab missing per-version label %q", label)
		}
	}

	// Agent-level labels must NOT appear in the Inputs tab.
	agentLevelLabels := []string{
		"Agent name",
		"Upload discovery zip",
	}
	for _, label := range agentLevelLabels {
		if strings.Contains(body, label) {
			t.Errorf("Inputs tab should NOT contain agent-level label %q "+
				"(it belongs on the agent detail header)", label)
		}
	}

	// And the count of <dt> rows (one per WizardFields entry) must be 4.
	if got := strings.Count(body, `class="inputs-label"`); got != 4 {
		t.Errorf("expected 4 inputs-label rows, got %d", got)
	}
}

// makePromptsConfigStore returns a stubConfigStore wired for the Prompts
// tab: non-INITIALIZING status (ReadOnly=true in the layout), plus the
// post-017 split shape — architecture (agent-level skill metadata) and
// prompts (version-level main + per-skill prompts). The legacy `bundle`
// argument is split internally so the test surface stays small.
func makePromptsConfigStore(status string, bundle []byte) *stubConfigStore {
	store := &stubConfigStore{
		cfg:           sampleConfig(),
		currentStatus: status,
	}
	if len(bundle) > 0 {
		arch, prompts, err := types.SplitBundle(bundle)
		if err != nil {
			// Test inputs deliberately include "not json" cases. Leave both
			// blobs untouched so the empty-state path triggers.
			return store
		}
		store.architecture = arch
		store.prompts = prompts
	}
	return store
}

func TestConfigurator_Prompts_RendersMainAndSkillPrompts(t *testing.T) {
	versionID := "11111111-1111-1111-1111-111111111111"
	bundle := []byte(`{
		"main_prompt": "<role>Coffee bot</role>",
		"skills": [
			{"name":"place_order","description":"Place an order","prompt":"<role>order</role>"},
			{"name":"check_rewards","description":"Check rewards","prompt":"<role>rewards</role>"}
		]
	}`)
	h := newConfigHandler(t, makePromptsConfigStore("READY", bundle))

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/configure/prompts", nil)
	req.SetPathValue("version_id", versionID)
	w := httptest.NewRecorder()
	h.Prompts(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"&lt;role&gt;Coffee bot&lt;/role&gt;",
		"place_order",
		"&lt;role&gt;order&lt;/role&gt;",
		"check_rewards",
		"&lt;role&gt;rewards&lt;/role&gt;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestConfigurator_Prompts_EmptyStateWhenBundleNull(t *testing.T) {
	versionID := "22222222-2222-2222-2222-222222222222"
	h := newConfigHandler(t, makePromptsConfigStore("DRAFT", nil))
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/configure/prompts", nil)
	req.SetPathValue("version_id", versionID)
	w := httptest.NewRecorder()
	h.Prompts(w, req)
	if !strings.Contains(w.Body.String(), "No bundle has been generated") {
		t.Errorf("expected empty-state copy; body:\n%s", w.Body.String())
	}
}

func TestConfigurator_Prompts_EmptyStateOnMalformedBundle(t *testing.T) {
	versionID := "33333333-3333-3333-3333-333333333333"
	h := newConfigHandler(t, makePromptsConfigStore("READY", []byte("not json")))
	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/configure/prompts", nil)
	req.SetPathValue("version_id", versionID)
	w := httptest.NewRecorder()
	h.Prompts(w, req)
	if !strings.Contains(w.Body.String(), "No bundle has been generated") {
		t.Errorf("expected empty-state copy on malformed bundle; body:\n%s", w.Body.String())
	}
}

// --- DB-gated test helpers (configurator package) ---

// openConfiguratorTestDB opens a Postgres connection and truncates all
// relevant tables. Skips if DATABASE_URL is unset.
func openConfiguratorTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`TRUNCATE
		deployed_messages, deployed_sessions, chat_messages, chat_sessions,
		resources, agent_versions, agents,
		tool_backends, agent_version_endpoint_backend, agent_version_mcp_backend
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return db
}

// seedConfiguratorAgentVersion creates a minimal agents + agent_versions pair
// and returns the version id. flow_map_config is seeded as an empty object so
// loadConfig returns an empty FlowMapConfig without error.
func seedConfiguratorAgentVersion(t *testing.T, db *sql.DB) string {
	t.Helper()
	var versionID string
	err := db.QueryRow(`
		WITH a AS (
			INSERT INTO agents (name) VALUES ('test-' || gen_random_uuid())
			RETURNING id
		)
		INSERT INTO agent_versions (agent_id, status, flow_map_config)
		SELECT id, 'DRAFT', '{}'::jsonb FROM a
		RETURNING id
	`).Scan(&versionID)
	if err != nil {
		t.Fatalf("seedConfiguratorAgentVersion: %v", err)
	}
	return versionID
}

// seedConfiguratorMCPBackend creates a tool_backends row of kind mcp_client
// with a known URL and returns its id.
func seedConfiguratorMCPBackend(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var id string
	cfgJSON, _ := json.Marshal(map[string]string{
		"url":       "https://mcp.example.com/" + name,
		"transport": "streamable_http",
	})
	err := db.QueryRow(`
		INSERT INTO tool_backends (name, kind, config)
		VALUES ($1, 'mcp_client', $2::jsonb)
		RETURNING id
	`, name, string(cfgJSON)).Scan(&id)
	if err != nil {
		t.Fatalf("seedConfiguratorMCPBackend: %v", err)
	}
	return id
}

// newConfigHandlerWithDB constructs a ConfiguratorHandler backed by real
// DB repositories.
func newConfigHandlerWithDB(t *testing.T, db *sql.DB) *handler.ConfiguratorHandler {
	t.Helper()
	schemaBytes, err := web.Assets.ReadFile("schemas/wizard-v1.yaml")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema types.WizardSchema
	if err := yaml.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	versionRepo := repository.NewAgentVersionRepository(db)
	backendRepo := repository.NewToolBackendRepository(db)
	wiringRepo := repository.NewVersionWiringRepository(db)
	agentWiringRepo := repository.NewAgentWiringRepository(db)
	h, err := handler.NewConfiguratorHandler(versionRepo, backendRepo, wiringRepo, agentWiringRepo, &schema, web.Assets)
	if err != nil {
		t.Fatalf("NewConfiguratorHandler: %v", err)
	}
	return h
}

// TestDownloadYAML_IncludesAttachedMCPs verifies that DownloadYAML joins the
// wiring tables at serve time and emits attached_mcps + schema_version 3.
func TestDownloadYAML_IncludesAttachedMCPs(t *testing.T) {
	db := openConfiguratorTestDB(t)

	versionID := seedConfiguratorAgentVersion(t, db)
	backend1 := seedConfiguratorMCPBackend(t, db, "slack-test")
	backend2 := seedConfiguratorMCPBackend(t, db, "github-test")

	wiringRepo := repository.NewVersionWiringRepository(db)
	if err := wiringRepo.AttachMCP(context.Background(), versionID, backend1, "use for escalations"); err != nil {
		t.Fatal(err)
	}
	if err := wiringRepo.AttachMCP(context.Background(), versionID, backend2, ""); err != nil {
		t.Fatal(err)
	}

	h := newConfigHandlerWithDB(t, db)

	req := httptest.NewRequest(http.MethodGet, "/agent_versions/"+versionID+"/config.yaml", nil)
	req.SetPathValue("version_id", versionID)
	w := httptest.NewRecorder()
	h.DownloadYAML(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}

	var parsed struct {
		SchemaVersion int `yaml:"schema_version"`
		AttachedMCPs  []struct {
			Name string `yaml:"name"`
			URL  string `yaml:"url"`
			Note string `yaml:"note"`
		} `yaml:"attached_mcps"`
	}
	if err := yaml.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if parsed.SchemaVersion != 3 {
		t.Fatalf("want schema_version 3, got %d", parsed.SchemaVersion)
	}
	if len(parsed.AttachedMCPs) != 2 {
		t.Fatalf("want 2 mcps, got %d: body=%s", len(parsed.AttachedMCPs), w.Body.String())
	}
	notes := map[string]string{} // name → note
	for _, m := range parsed.AttachedMCPs {
		notes[m.Name] = m.Note
	}
	if notes["slack-test"] != "use for escalations" {
		t.Fatalf("slack-test note mismatch: %q", notes["slack-test"])
	}
	if notes["github-test"] != "" {
		t.Fatalf("github-test note should be empty, got %q", notes["github-test"])
	}
}
