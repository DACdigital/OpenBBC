package handler

import (
	"bytes"
	"context"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/storage"
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
	listGrouped func(ctx context.Context) ([]types.AgentGroup, error)
	getByID     func(ctx context.Context, id string) (*types.Agent, error)
}

func (m *mockGroupedAgentRepo) ListGrouped(ctx context.Context) ([]types.AgentGroup, error) {
	return m.listGrouped(ctx)
}
func (m *mockGroupedAgentRepo) GetByID(ctx context.Context, id string) (*types.Agent, error) {
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
		agentRepo: &mockGroupedAgentRepo{
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

func TestUIHandler_AgentDetail_RealTemplateRenders(t *testing.T) {
	agentID := "00000000-0000-0000-0000-000000000000"
	agent := &types.Agent{ID: agentID, Name: "Bot", Description: "Desc", DiscoveryFilePath: "bot.zip", CreatedAt: time.Now()}
	groups := []types.AgentGroup{{
		AgentID: agentID, Name: "Bot",
		Versions: []types.AgentVersionListItem{
			{VersionNum: 1, Version: &types.AgentVersion{ID: "v1", Status: "READY", CreatedAt: time.Now()}},
		},
	}}
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"statusClass": statusClass,
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
		"urlEncode":   func(s string) string { return s },
	}).ParseFiles("../../web/templates/layout.html", "../../web/templates/agent-versions.html")
	if err != nil {
		t.Fatalf("ParseFiles: %v", err)
	}
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo{
			listGrouped: func(ctx context.Context) ([]types.AgentGroup, error) { return groups, nil },
			getByID:     func(ctx context.Context, id string) (*types.Agent, error) { return agent, nil },
		},
		agentVersionsTmpl: tmpl,
		logger:            slog.Default(),
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/ui?agent="+agentID, nil)
	w := httptest.NewRecorder()
	h.AgentsPage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"agent-header-card", "Bot", "Desc", "bot.zip", `v1`} {
		if !strings.Contains(body, want) {
			t.Errorf("real template missing %q", want)
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
		agentRepo: &mockGroupedAgentRepo{
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

type stubStorage struct {
	openFn func(ctx context.Context, key string) (io.ReadCloser, error)
}

func (s *stubStorage) Put(ctx context.Context, key string, r io.Reader) error { return nil }
func (s *stubStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.openFn(ctx, key)
}

func TestUIHandler_DiscoveryDownload_StreamsZip(t *testing.T) {
	agentID := "33333333-3333-3333-3333-333333333333"
	agent := &types.Agent{ID: agentID, Name: "X", DiscoveryFilePath: "abc.zip"}
	body := []byte("PK\x03\x04 fake zip")
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo{
			getByID: func(ctx context.Context, id string) (*types.Agent, error) { return agent, nil },
		},
		storage: &stubStorage{openFn: func(ctx context.Context, key string) (io.ReadCloser, error) {
			if key != "abc.zip" {
				t.Fatalf("Open called with %q, want abc.zip", key)
			}
			return io.NopCloser(bytes.NewReader(body)), nil
		}},
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
			getByID: func(ctx context.Context, id string) (*types.Agent, error) {
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

func TestUIHandler_DiscoveryDownload_NoDiscoveryFile(t *testing.T) {
	agentID := "44444444-4444-4444-4444-444444444444"
	agent := &types.Agent{ID: agentID, Name: "X", DiscoveryFilePath: ""}
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo{
			getByID: func(ctx context.Context, id string) (*types.Agent, error) { return agent, nil },
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

func TestUIHandler_DiscoveryDownload_FileMissingOnDisk(t *testing.T) {
	agentID := "55555555-5555-5555-5555-555555555555"
	agent := &types.Agent{ID: agentID, Name: "X", DiscoveryFilePath: "abc.zip"}
	h := &UIHandler{
		agentRepo: &mockGroupedAgentRepo{
			getByID: func(ctx context.Context, id string) (*types.Agent, error) { return agent, nil },
		},
		storage: &stubStorage{openFn: func(ctx context.Context, key string) (io.ReadCloser, error) {
			return nil, storage.ErrNotFound
		}},
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
