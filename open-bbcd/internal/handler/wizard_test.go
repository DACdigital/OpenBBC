package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"gopkg.in/yaml.v3"
)

type mockWizardRepo struct {
	createFromWizardFn func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error)
}

func (m *mockWizardRepo) CreateFromWizard(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error) {
	return m.createFromWizardFn(ctx, opts)
}

func buildWizardForm(t *testing.T, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for k, v := range fields {
		w.WriteField(k, v)
	}
	w.Close()
	return body, w.FormDataContentType()
}

func TestWizardHandler_Submit_RedirectsOnSuccess(t *testing.T) {
	var schema types.WizardSchema
	if err := yaml.Unmarshal([]byte(uiTestSchema), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error) {
			return &types.Agent{ID: "new-id", Name: opts.Name, Status: "INITIALIZING"}, nil
		},
	}

	h := NewWizardHandler(repo, &schema)
	body, ct := buildWizardForm(t, map[string]string{
		"name":  "My Agent",
		"scope": "Handle support queries",
	})
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/agents/ui" {
		t.Errorf("Location = %q, want /agents/ui", loc)
	}
}

func TestWizardHandler_Submit_MissingName(t *testing.T) {
	var schema types.WizardSchema
	if err := yaml.Unmarshal([]byte(uiTestSchema), &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	repo := &mockWizardRepo{
		createFromWizardFn: func(ctx context.Context, opts types.CreateAgentFromWizardOpts) (*types.Agent, error) {
			panic("CreateFromWizard should not be called when name is empty")
		},
	}

	h := NewWizardHandler(repo, &schema)
	body, ct := buildWizardForm(t, map[string]string{"name": ""})
	req := httptest.NewRequest(http.MethodPost, "/agents/wizard", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.Submit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
