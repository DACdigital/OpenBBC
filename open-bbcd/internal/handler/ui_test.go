package handler

import (
	"bytes"
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

type mockGroupedAgentRepo struct {
	listGrouped     func(ctx context.Context) ([]types.AgentGroup, error)
	getByID         func(ctx context.Context, id string) (*types.Agent, error)
	getDiscoveryZip func(ctx context.Context, id string) ([]byte, error)
}

func (m *mockGroupedAgentRepo) ListGrouped(ctx context.Context) ([]types.AgentGroup, error) {
	return m.listGrouped(ctx)
}
func (m *mockGroupedAgentRepo) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return m.getByID(ctx, id)
}
func (m *mockGroupedAgentRepo) GetDiscoveryZip(ctx context.Context, id string) ([]byte, error) {
	if m.getDiscoveryZip == nil {
		return nil, types.ErrNotFound
	}
	return m.getDiscoveryZip(ctx, id)
}

func mustParseAgentVersionsTmpl(t *testing.T) *template.Template {
	t.Helper()
	const layout = `{{define "layout"}}{{template "content" .}}{{end}}`
	const content = `{{define "content"}}` +
		`<h1>{{.Name}}</h1>` +
		`<p class="desc">{{.Description}}</p>` +
		`<p class="path">{{if .HasDiscoveryZip}}has{{end}}</p>` +
		`<p class="deployed">{{if .CurrentDeployedVersionNum}}v{{.CurrentDeployedVersionNum}}{{else}}—{{end}}</p>` +
		`{{range .Versions}}<div class="v">v{{.VersionNum}}:{{.Version.Status}}</div>{{end}}` +
		`{{end}}`
	return template.Must(template.New("").Funcs(template.FuncMap{
		"statusClass": statusClass,
	}).Parse(layout + content))
}

// TestUIHandler_AgentDetail_RedirectsToTabbedPage covers the legacy
// /agents/ui?agent=X entry point: post-tab-refactor it 302-redirects to
// the new tabbed agent detail at /agents/{id}/configure/versions.
// The previous rendering tests moved to TestAgentDetailHandler_Versions.
func TestUIHandler_AgentDetail_RedirectsToTabbedPage(t *testing.T) {
	agentID := "11111111-1111-1111-1111-111111111111"
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo{
			listGrouped: func(ctx context.Context) ([]types.AgentGroup, error) { return nil, nil },
			getByID:     func(ctx context.Context, id string) (*types.Agent, error) { return &types.Agent{ID: agentID}, nil },
		},
		logger: slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/ui?agent="+agentID, nil)
	w := httptest.NewRecorder()
	h.AgentsPage(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", w.Code, w.Body.String())
	}
	want := "/agents/" + agentID + "/configure/versions"
	if got := w.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestUIHandler_DiscoveryDownload_StreamsZip(t *testing.T) {
	agentID := "33333333-3333-3333-3333-333333333333"
	body := []byte("PK\x03\x04 fake zip")
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo{
			getDiscoveryZip: func(ctx context.Context, id string) ([]byte, error) {
				if id != agentID {
					t.Fatalf("GetDiscoveryZip called with %q, want %q", id, agentID)
				}
				return body, nil
			},
		},
		logger: slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/"+agentID+"/discovery", nil)
	req.SetPathValue("agent_id", agentID)
	w := httptest.NewRecorder()
	h.DiscoveryDownload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.Bytes(); !bytes.Equal(got, body) {
		t.Errorf("body mismatch: got %q", got)
	}
	if w.Header().Get("Content-Type") != "application/zip" {
		t.Errorf("content-type = %q", w.Header().Get("Content-Type"))
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, `attachment; filename="`+agentID+`.zip"`) {
		t.Errorf("content-disposition = %q", got)
	}
}

func TestUIHandler_DiscoveryDownload_AgentNotFound(t *testing.T) {
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo{
			getDiscoveryZip: func(ctx context.Context, id string) ([]byte, error) {
				return nil, types.ErrNotFound
			},
		},
		logger: slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/missing/discovery", nil)
	req.SetPathValue("agent_id", "missing")
	w := httptest.NewRecorder()
	h.DiscoveryDownload(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// GetDiscoveryZip returns ErrNotFound for agents that exist but have no zip
// stored — the handler folds both cases into the same 404.
func TestUIHandler_DiscoveryDownload_NoDiscoveryZip(t *testing.T) {
	agentID := "44444444-4444-4444-4444-444444444444"
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo{
			getDiscoveryZip: func(ctx context.Context, id string) ([]byte, error) {
				return nil, types.ErrNotFound
			},
		},
		logger: slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/"+agentID+"/discovery", nil)
	req.SetPathValue("agent_id", agentID)
	w := httptest.NewRecorder()
	h.DiscoveryDownload(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
