package handler

import (
	"context"
	"html/template"
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

type mockGroupedAgentRepo struct {
	listGroupedFn func(ctx context.Context) ([]types.AgentChain, error)
}

func (m *mockGroupedAgentRepo) ListGrouped(ctx context.Context) ([]types.AgentChain, error) {
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
