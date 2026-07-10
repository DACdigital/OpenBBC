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

type stubConfigStore struct {
	cfg                types.FlowMapConfig
	getErr             error
	parseErr           string
	updates            int
	updateFn           func(cfg []byte) error
	statusFn           func(versionID, expectedFrom, to string) error
	currentStatus      string // optional override; defaults to "INITIALIZING"
	architecture       []byte // optional agent-level architecture blob
	prompts            []byte // optional version-level prompts blob (rendered by the Prompts tab)
	createVersionFn    func(parentVersionID string, promptsJSON []byte) (string, error)
	lastPromptsParent  string
	lastPromptsJSON    []byte
	lastPromptsStatus  types.AgentStatus
}

func (s *stubConfigStore) GetVersionNum(ctx context.Context, versionID string) (int, error) {
	return 1, nil
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

func (s *stubConfigStore) CreateVersionFromPrompts(ctx context.Context, parentVersionID string, promptsJSON []byte, status types.AgentStatus) (string, error) {
	s.lastPromptsStatus = status
	if s.createVersionFn != nil {
		return s.createVersionFn(parentVersionID, promptsJSON)
	}
	s.lastPromptsParent = parentVersionID
	s.lastPromptsJSON = append([]byte(nil), promptsJSON...)
	return "new-version-id", nil
}

func (s *stubConfigStore) Delete(ctx context.Context, versionID string) error {
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
	VersionNum  int
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

func TestConfigurator_SavePrompts_CreatesNewVersionAndRedirects(t *testing.T) {
	parentID := "11111111-1111-1111-1111-111111111111"
	store := makePromptsConfigStore("READY", []byte(`{
		"main_prompt":"old",
		"skills":[{"name":"place_order","prompt":"old skill"}]
	}`))
	h := newConfigHandler(t, store)

	form := url.Values{}
	form.Set("main_prompt", "new main")
	form.Set("skill_prompt[place_order]", "new skill")
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/"+parentID+"/configure/prompts", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", parentID)
	w := httptest.NewRecorder()
	h.SavePrompts(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: want 303, got %d body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/agent_versions/new-version-id/configure/prompts") {
		t.Errorf("redirect target wrong: %q", loc)
	}
	if store.lastPromptsParent != parentID {
		t.Errorf("parent passed to repo: want %q, got %q", parentID, store.lastPromptsParent)
	}
	var got types.Prompts
	if err := json.Unmarshal(store.lastPromptsJSON, &got); err != nil {
		t.Fatalf("parse persisted prompts: %v", err)
	}
	if got.MainPrompt != "new main" || got.SkillPrompts["place_order"] != "new skill" {
		t.Errorf("persisted prompts wrong: %+v", got)
	}
	if store.lastPromptsStatus != types.AgentStatusDraft {
		t.Errorf("SavePrompts status: want DRAFT, got %q", store.lastPromptsStatus)
	}
}

func TestConfigurator_LandPrompts_CreatesReadyVersionAndRedirects(t *testing.T) {
	parentID := "22222222-2222-2222-2222-222222222222"
	store := makePromptsConfigStore("READY", []byte(`{
		"main_prompt":"old",
		"skills":[{"name":"place_order","prompt":"old skill"}]
	}`))
	h := newConfigHandler(t, store)

	form := url.Values{}
	form.Set("main_prompt", "trained main")
	form.Set("skill_prompt[place_order]", "trained skill")
	req := httptest.NewRequest(http.MethodPost, "/agent_versions/"+parentID+"/configure/prompts/land", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("version_id", parentID)
	w := httptest.NewRecorder()
	h.LandPrompts(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: want 303, got %d body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/agent_versions/new-version-id/configure/prompts") {
		t.Errorf("redirect target wrong: %q", loc)
	}
	if store.lastPromptsParent != parentID {
		t.Errorf("parent passed to repo: want %q, got %q", parentID, store.lastPromptsParent)
	}
	var got types.Prompts
	if err := json.Unmarshal(store.lastPromptsJSON, &got); err != nil {
		t.Fatalf("parse persisted prompts: %v", err)
	}
	if got.MainPrompt != "trained main" || got.SkillPrompts["place_order"] != "trained skill" {
		t.Errorf("persisted prompts wrong: %+v", got)
	}
	if store.lastPromptsStatus != types.AgentStatusReady {
		t.Errorf("LandPrompts status: want READY, got %q", store.lastPromptsStatus)
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
		tool_backends, agent_endpoint_backend, agent_version_mcp_backend
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
