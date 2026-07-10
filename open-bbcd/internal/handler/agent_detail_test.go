package handler_test

import (
	"context"
	"encoding/json"
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

// stubAgentDetailStore backs AgentDetailHandler in unit tests. Storage is a
// single in-memory FlowMapConfig — the "root version" of a single fake
// agent. Reads/writes on the store are keyed by agent_id but ignore the
// actual value (only one agent exists in the fake).
type stubAgentDetailStore struct {
	agent         *types.Agent
	cfg           types.FlowMapConfig
	parseErr      string
	versionID     string // defaults to "v1" if empty
	versionStatus string // defaults to "INITIALIZING" if empty
	statusFn      func(versionID, expectedFrom, to string) error
}

func (s *stubAgentDetailStore) rootVersionID() string {
	if s.versionID == "" {
		return "v1"
	}
	return s.versionID
}
func (s *stubAgentDetailStore) rootVersionStatus() string {
	if s.versionStatus == "" {
		return "INITIALIZING"
	}
	return s.versionStatus
}

func (s *stubAgentDetailStore) GetByID(ctx context.Context, agentID string) (*types.Agent, error) {
	if s.agent == nil {
		return &types.Agent{ID: agentID}, nil
	}
	a := *s.agent
	a.ID = agentID
	return &a, nil
}

func (s *stubAgentDetailStore) ListGrouped(ctx context.Context) ([]types.AgentGroup, error) {
	return nil, nil
}

func (s *stubAgentDetailStore) GetFlowMapConfigForAgent(ctx context.Context, agentID string) ([]byte, string, error) {
	b, err := json.Marshal(s.cfg)
	if err != nil {
		return nil, "", err
	}
	return b, s.parseErr, nil
}

func (s *stubAgentDetailStore) UpdateFlowMapConfigForAgent(ctx context.Context, agentID string, cfg []byte) error {
	var decoded types.FlowMapConfig
	if err := json.Unmarshal(cfg, &decoded); err != nil {
		return err
	}
	s.cfg = decoded
	return nil
}

func (s *stubAgentDetailStore) GetRootVersion(ctx context.Context, agentID string) (string, string, error) {
	return s.rootVersionID(), s.rootVersionStatus(), nil
}

func (s *stubAgentDetailStore) UpdateVersionStatus(ctx context.Context, versionID, expectedFrom, to string) error {
	if s.statusFn != nil {
		return s.statusFn(versionID, expectedFrom, to)
	}
	if s.rootVersionStatus() != expectedFrom {
		return types.ErrInvalidAgentStatus
	}
	s.versionStatus = to
	return nil
}

func (s *stubAgentDetailStore) Delete(ctx context.Context, agentID string) error { return nil }

func newAgentDetailHandler(t *testing.T, store handler.AgentDetailStore) *handler.AgentDetailHandler {
	t.Helper()
	schemaBytes, err := web.Assets.ReadFile("schemas/wizard-v1.yaml")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema types.WizardSchema
	if err := yaml.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	h, err := handler.NewAgentDetailHandler(store, nil, nil, nil, &schema, web.Assets)
	if err != nil {
		t.Fatalf("NewAgentDetailHandler: %v", err)
	}
	return h
}

// architectureRequest builds a GET request against the agent-scoped
// architecture route with the given subtab / selectedID path params.
func architectureRequest(agentID, subtab, selectedID string) *http.Request {
	url := "/agents/" + agentID + "/configure/architecture/" + subtab
	if selectedID != "" {
		url += "/" + selectedID
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("agent_id", agentID)
	req.SetPathValue("subtab", subtab)
	if selectedID != "" {
		req.SetPathValue("selectedID", selectedID)
	}
	return req
}

// -------- Architecture GET (edit mode) --------

func TestAgentDetail_ArchFlows_Edit_RendersFlowsList(t *testing.T) {
	h := newAgentDetailHandler(t, &stubAgentDetailStore{cfg: sampleConfig()})
	req := architectureRequest("abc", "flows", "")
	w := httptest.NewRecorder()
	h.Architecture(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "place-order") {
		t.Errorf("body should contain flow id; got: %s", w.Body.String()[:minInt(300, len(w.Body.String()))])
	}
}

func TestAgentDetail_ArchFlows_Edit_NodeCountReflectsMermaid(t *testing.T) {
	cfg := sampleConfig()
	cfg.Flows[0].Workflow = types.Workflow{
		Mermaid: "flowchart TD\n  start([start]) --> s_po[place-order]\n  s_po --> e([end])\n",
	}
	h := newAgentDetailHandler(t, &stubAgentDetailStore{cfg: cfg})
	req := architectureRequest("abc", "flows", "")
	w := httptest.NewRecorder()
	h.Architecture(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "3 nodes") {
		t.Errorf("flow row should show 3 nodes")
	}
}

func TestAgentDetail_ArchSkills_Edit_ShowsSkillRow(t *testing.T) {
	h := newAgentDetailHandler(t, &stubAgentDetailStore{cfg: sampleConfig()})
	req := architectureRequest("abc", "skills", "")
	w := httptest.NewRecorder()
	h.Architecture(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "place-order") || !strings.Contains(w.Body.String(), "1 endpoints") {
		t.Errorf("Skills tab missing expected row content")
	}
}

func TestAgentDetail_ArchEndpoints_Edit_ShowsEndpointRow(t *testing.T) {
	h := newAgentDetailHandler(t, &stubAgentDetailStore{cfg: sampleConfig()})
	req := architectureRequest("abc", "endpoints", "")
	w := httptest.NewRecorder()
	h.Architecture(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "orders.create") {
		t.Errorf("Endpoints tab missing endpoint ID")
	}
}

func TestAgentDetail_ArchFlows_Edit_ShowsParseErrorBanner(t *testing.T) {
	h := newAgentDetailHandler(t, &stubAgentDetailStore{cfg: sampleConfig(), parseErr: "missing tools-proposed.json"})
	req := architectureRequest("abc", "flows", "")
	w := httptest.NewRecorder()
	h.Architecture(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "missing tools-proposed.json") {
		t.Errorf("Parse error not surfaced")
	}
}

// -------- Architecture GET (read-only, post-finalize) --------

func TestAgentDetail_ArchFlows_ReadOnly_ShowsConfigContents(t *testing.T) {
	store := &stubAgentDetailStore{
		cfg:           sampleConfig(),
		versionStatus: "DRAFT",
	}
	h := newAgentDetailHandler(t, store)
	req := architectureRequest("abc", "flows", "")
	w := httptest.NewRecorder()
	h.Architecture(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "place-order") {
		t.Errorf("readonly view should still render config contents; body was %q", body[:minInt(400, len(body))])
	}
	// Read-only view should not surface the include-toggle-post URL.
	if strings.Contains(body, "/included") {
		t.Errorf("readonly view should not emit include-toggle hx-post URL")
	}
}

// -------- FlowIncluded --------

func TestAgentDetail_FlowIncluded_Toggle(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/flows/place-order/included",
		strings.NewReader("included=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("agent_id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.FlowIncluded(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if store.cfg.Flows[0].Included {
		t.Error("Flows[0].Included should be false after toggle")
	}
	if !strings.Contains(w.Body.String(), "place-order") {
		t.Errorf("response should re-render the flow row")
	}

	req2 := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/flows/place-order/included",
		strings.NewReader("included=true"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.SetPathValue("agent_id", "abc")
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

func TestAgentDetail_FlowIncluded_UnknownFlow_404(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/flows/ghost/included",
		strings.NewReader("included=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("agent_id", "abc")
	req.SetPathValue("flowId", "ghost")
	w := httptest.NewRecorder()
	h.FlowIncluded(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestAgentDetail_FlowIncluded_ReadOnly_409(t *testing.T) {
	store := &stubAgentDetailStore{
		cfg:           sampleConfig(),
		versionStatus: "DRAFT",
	}
	h := newAgentDetailHandler(t, store)
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/flows/place-order/included",
		strings.NewReader("included=false"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("agent_id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.FlowIncluded(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (agent finalized)", w.Code)
	}
}

// -------- SkillUpdate --------

func TestAgentDetail_SkillUpdate_HappyPath(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	form := url.Values{
		"name":         {"Place an order"},
		"description":  {"Updated description"},
		"domain":       {"Order management"},
		"user_phrases": {"check out\nplace order\nbuy"},
		"external":     {"false"},
	}
	form.Add("suggested_endpoints", "orders.create")
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/skills/place-order",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("agent_id", "abc")
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

func TestAgentDetail_SkillUpdate_External(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	form := url.Values{
		"name":          {"Send notification"},
		"external":      {"true"},
		"external_note": {"sends to webhook"},
		"user_phrases":  {""},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/skills/place-order",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("agent_id", "abc")
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
	if len(got.SuggestedEndpoints) != 0 {
		t.Errorf("SuggestedEndpoints should be empty when external=true, got %+v", got.SuggestedEndpoints)
	}
}

func TestAgentDetail_SkillUpdate_UnknownEndpoint(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	form := url.Values{
		"name":     {"Some skill"},
		"external": {"false"},
	}
	form.Add("suggested_endpoints", "ghost.endpoint")
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/skills/place-order",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("agent_id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillUpdate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// -------- SkillCreate --------

func TestAgentDetail_SkillCreate_HappyPath(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	form := url.Values{
		"name":          {"Send Email Alert"},
		"description":   {"Notify the user via email"},
		"external":      {"true"},
		"external_note": {"sends through SMTP relay"},
		"user_phrases":  {"send email\nemail me"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/skills",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("agent_id", "abc")
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

func TestAgentDetail_SkillCreate_NameCollision_GetsDiscriminator(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	form := url.Values{
		"name":     {"Place Order"},
		"external": {"true"},
	}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/skills",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("agent_id", "abc")
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

func TestAgentDetail_SkillCreate_NameRequired(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	form := url.Values{"external": {"true"}}
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/skills",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("agent_id", "abc")
	w := httptest.NewRecorder()
	h.SkillCreate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// -------- SkillDelete --------

func TestAgentDetail_SkillDelete_Custom_OK(t *testing.T) {
	cfg := sampleConfig()
	cfg.Skills = append(cfg.Skills, types.Skill{
		ID: "custom-thing", Origin: "custom", Name: "Custom thing", External: true,
	})
	store := &stubAgentDetailStore{cfg: cfg}
	h := newAgentDetailHandler(t, store)

	req := httptest.NewRequest(http.MethodDelete,
		"/agents/abc/configure/architecture/skills/custom-thing", nil)
	req.SetPathValue("agent_id", "abc")
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

func TestAgentDetail_SkillDelete_Discovered_409(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	req := httptest.NewRequest(http.MethodDelete,
		"/agents/abc/configure/architecture/skills/place-order", nil)
	req.SetPathValue("agent_id", "abc")
	req.SetPathValue("skillId", "place-order")
	w := httptest.NewRecorder()
	h.SkillDelete(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (cannot delete discovered skill)", w.Code)
	}
}

func TestAgentDetail_SkillDelete_Referenced_409(t *testing.T) {
	cfg := sampleConfig()
	cfg.Skills = append(cfg.Skills, types.Skill{
		ID: "needed-by-flow", Origin: "custom", Name: "Needed", External: true,
	})
	cfg.Flows[0].Workflow.Mermaid = "flowchart TD\n" +
		"  start([start]) --> a[needed-by-flow]\n" +
		"  a --> e([end])"
	store := &stubAgentDetailStore{cfg: cfg}
	h := newAgentDetailHandler(t, store)

	req := httptest.NewRequest(http.MethodDelete,
		"/agents/abc/configure/architecture/skills/needed-by-flow", nil)
	req.SetPathValue("agent_id", "abc")
	req.SetPathValue("skillId", "needed-by-flow")
	w := httptest.NewRecorder()
	h.SkillDelete(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (skill referenced by flow workflow)", w.Code)
	}
}

// -------- WorkflowUpdate --------

func TestAgentDetail_WorkflowUpdate_HappyPath(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	body := `{
		"mermaid": "flowchart TD\n  start([start]) --> s_x[place-order]\n  s_x --> e([end])",
		"layout": {"start": {"x": 40, "y": 40}, "s_x": {"x": 40, "y": 140}, "e": {"x": 40, "y": 240}}
	}`
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/flows/place-order/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("agent_id", "abc")
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

func TestAgentDetail_WorkflowUpdate_RejectsUnknownSkill(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	body := `{
		"mermaid": "flowchart TD\n  start([start]) --> s_x[ghost-skill]\n  s_x --> e([end])",
		"layout": {}
	}`
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/flows/place-order/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("agent_id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown skill)", w.Code)
	}
}

func TestAgentDetail_WorkflowUpdate_RejectsMalformedMermaid(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	body := `{"mermaid": "this is not mermaid", "layout": {}}`
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/flows/place-order/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("agent_id", "abc")
	req.SetPathValue("flowId", "place-order")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (malformed)", w.Code)
	}
}

// -------- Finalize --------

func TestAgentDetail_FinalizeConfirm_RendersPage(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	req := httptest.NewRequest(http.MethodGet,
		"/agents/abc/configure/finalize", nil)
	req.SetPathValue("agent_id", "abc")
	w := httptest.NewRecorder()
	h.FinalizeConfirm(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Finalize") {
		t.Errorf("response should contain a Finalize heading")
	}
	if !strings.Contains(body, "/agents/abc/finalize") {
		t.Errorf("response should include the POST target")
	}
}

func TestAgentDetail_FinalizeConfirm_WrongStatus_409(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig(), versionStatus: "DRAFT"}
	h := newAgentDetailHandler(t, store)
	req := httptest.NewRequest(http.MethodGet,
		"/agents/abc/configure/finalize", nil)
	req.SetPathValue("agent_id", "abc")
	w := httptest.NewRecorder()
	h.FinalizeConfirm(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestAgentDetail_Finalize_HappyPath_RedirectsToArchitecture(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/finalize", nil)
	req.SetPathValue("agent_id", "abc")
	w := httptest.NewRecorder()
	h.Finalize(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303, body = %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/agents/abc/configure/architecture/flows" {
		t.Errorf("Location = %q, want /agents/abc/configure/architecture/flows", loc)
	}
	if store.versionStatus != "DRAFT" {
		t.Errorf("status = %q, want DRAFT", store.versionStatus)
	}
}

func TestAgentDetail_Finalize_WrongStatus_409(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig(), versionStatus: "DRAFT"}
	h := newAgentDetailHandler(t, store)
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/finalize", nil)
	req.SetPathValue("agent_id", "abc")
	w := httptest.NewRecorder()
	h.Finalize(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestAgentDetail_WorkflowUpdate_UnknownFlow_404(t *testing.T) {
	store := &stubAgentDetailStore{cfg: sampleConfig()}
	h := newAgentDetailHandler(t, store)

	body := `{"mermaid": "flowchart TD\n  start([start]) --> e([end])", "layout": {}}`
	req := httptest.NewRequest(http.MethodPost,
		"/agents/abc/configure/architecture/flows/ghost/workflow",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("agent_id", "abc")
	req.SetPathValue("flowId", "ghost")
	w := httptest.NewRecorder()
	h.WorkflowUpdate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
