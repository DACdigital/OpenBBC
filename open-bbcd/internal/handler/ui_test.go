package handler

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"gopkg.in/yaml.v3"
)

const uiTestSchema = `
version: v1
wizard:
  name:
    label: "Agent name"
    type: text
    required: true
    order: 1
  scope:
    label: "Scope"
    type: textarea
    required: true
    order: 2
`

type mockGroupedAgentRepo struct {
	listGroupedFn func(ctx context.Context) ([]types.AgentGroup, error)
}

func (m *mockGroupedAgentRepo) ListGrouped(ctx context.Context) ([]types.AgentGroup, error) {
	return m.listGroupedFn(ctx)
}

func mustParseStepTmpl(t *testing.T) *template.Template {
	t.Helper()
	const src = `{{define "step"}}{{range $k,$v := .Values}}<input name="{{$k}}" value="{{$v}}">{{end}}{{.Field.Field.Label}}{{end}}`
	return template.Must(template.New("").Funcs(template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
	}).Parse(src))
}

func TestUIHandler_WizardStep_InvalidStep(t *testing.T) {
	var schema types.WizardSchema
	if err := yaml.Unmarshal([]byte(uiTestSchema), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	h := &UIHandler{schema: &schema}

	for _, n := range []string{"0", "99", "abc"} {
		req := httptest.NewRequest(http.MethodGet, "/agents/new/step/"+n, nil)
		req.SetPathValue("n", n)
		w := httptest.NewRecorder()
		h.WizardStep(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("step %q: status = %d, want 404", n, w.Code)
		}
	}
}

func TestUIHandler_WizardStep_AccumulatesValues(t *testing.T) {
	var schema types.WizardSchema
	if err := yaml.Unmarshal([]byte(uiTestSchema), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	h := &UIHandler{schema: &schema, stepTmpl: mustParseStepTmpl(t)}

	req := httptest.NewRequest(http.MethodGet, "/agents/new/step/2?name=TestAgent", nil)
	req.SetPathValue("n", "2")
	w := httptest.NewRecorder()
	h.WizardStep(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `name="name"`) || !strings.Contains(body, `value="TestAgent"`) {
		t.Errorf("expected hidden input for name=TestAgent in body:\n%s", body)
	}
}

type mockGroupedAgentRepo2 struct {
	listGrouped func(ctx context.Context) ([]types.AgentGroup, error)
	getByID     func(ctx context.Context, id string) (*types.Agent, error)
}

func (m *mockGroupedAgentRepo2) ListGrouped(ctx context.Context) ([]types.AgentGroup, error) {
	return m.listGrouped(ctx)
}
func (m *mockGroupedAgentRepo2) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return m.getByID(ctx, id)
}

func mustParseAgentVersionsTmpl(t *testing.T) *template.Template {
	t.Helper()
	const layout = `{{define "layout"}}{{template "content" .}}{{end}}`
	const content = `{{define "content"}}` +
		`<h1>{{.Name}}</h1>` +
		`<p class="desc">{{.Description}}</p>` +
		`<p class="path">{{.DiscoveryFilePath}}</p>` +
		`<p class="deployed">{{if .CurrentDeployedVersionNum}}v{{.CurrentDeployedVersionNum}}{{else}}—{{end}}</p>` +
		`{{range .Versions}}<div class="v">v{{.VersionNum}}:{{.Version.Status}}</div>{{end}}` +
		`{{end}}`
	return template.Must(template.New("").Funcs(template.FuncMap{
		"statusClass": statusClass,
	}).Parse(layout + content))
}

func TestUIHandler_AgentDetail_PopulatesHeader(t *testing.T) {
	createdAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	agentID := "11111111-1111-1111-1111-111111111111"
	agent := &types.Agent{
		ID:                agentID,
		Name:              "Coffee Bot",
		Description:       "Shop assistant",
		DiscoveryFilePath: "coffee.zip",
		CreatedAt:         createdAt,
	}
	groups := []types.AgentGroup{{
		AgentID: agentID,
		Name:    "Coffee Bot",
		Versions: []types.AgentVersionListItem{
			{VersionNum: 2, Version: &types.AgentVersion{ID: "v2", Status: "DEPLOYED"}},
			{VersionNum: 1, Version: &types.AgentVersion{ID: "v1", Status: "READY"}},
		},
	}}
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo2{
			listGrouped: func(ctx context.Context) ([]types.AgentGroup, error) { return groups, nil },
			getByID:     func(ctx context.Context, id string) (*types.Agent, error) { return agent, nil },
		},
		agentVersionsTmpl: mustParseAgentVersionsTmpl(t),
		logger:            slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/ui?agent="+agentID, nil)
	w := httptest.NewRecorder()
	h.AgentsPage(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"Coffee Bot",
		"Shop assistant",
		"coffee.zip",
		`<p class="deployed">v2</p>`,
		`<div class="v">v2:DEPLOYED</div>`,
		`<div class="v">v1:READY</div>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestUIHandler_AgentDetail_NoneDeployed(t *testing.T) {
	agentID := "22222222-2222-2222-2222-222222222222"
	agent := &types.Agent{ID: agentID, Name: "X", DiscoveryFilePath: "x.zip"}
	groups := []types.AgentGroup{{
		AgentID: agentID, Name: "X",
		Versions: []types.AgentVersionListItem{
			{VersionNum: 1, Version: &types.AgentVersion{ID: "v1", Status: "READY"}},
		},
	}}
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo2{
			listGrouped: func(ctx context.Context) ([]types.AgentGroup, error) { return groups, nil },
			getByID:     func(ctx context.Context, id string) (*types.Agent, error) { return agent, nil },
		},
		agentVersionsTmpl: mustParseAgentVersionsTmpl(t),
		logger:            slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/ui?agent="+agentID, nil)
	w := httptest.NewRecorder()
	h.AgentsPage(w, req)
	if !strings.Contains(w.Body.String(), `<p class="deployed">—</p>`) {
		t.Errorf("expected em-dash for no-deploy:\n%s", w.Body.String())
	}
}
