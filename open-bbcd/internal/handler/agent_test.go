package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type mockAgentRepo struct {
	createFn func(ctx context.Context, opts types.CreateAgentOpts) (*types.Agent, error)
	getFn    func(ctx context.Context, id string) (*types.Agent, error)
	listFn   func(ctx context.Context) ([]*types.Agent, error)
}

func (m *mockAgentRepo) Create(ctx context.Context, opts types.CreateAgentOpts) (*types.Agent, error) {
	return m.createFn(ctx, opts)
}

func (m *mockAgentRepo) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return m.getFn(ctx, id)
}

func (m *mockAgentRepo) List(ctx context.Context) ([]*types.Agent, error) {
	return m.listFn(ctx)
}

func TestAgentHandler_Create_Success(t *testing.T) {
	h := NewAgentHandler(&mockAgentRepo{
		createFn: func(ctx context.Context, opts types.CreateAgentOpts) (*types.Agent, error) {
			return &types.Agent{
				ID:     "test-id",
				Name:   opts.Name,
				Prompt: opts.Prompt,
			}, nil
		},
	})

	body := `{"name":"test","prompt":"test prompt"}`
	req := httptest.NewRequest(http.MethodPost, "/agents", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}

	var resp types.Agent
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != "test-id" {
		t.Errorf("ID = %q, want %q", resp.ID, "test-id")
	}
}

func TestAgentHandler_Create_ValidationError(t *testing.T) {
	h := NewAgentHandler(&mockAgentRepo{
		createFn: func(ctx context.Context, opts types.CreateAgentOpts) (*types.Agent, error) {
			return nil, types.ErrNameRequired
		},
	})

	body := `{"name":"","prompt":""}`
	req := httptest.NewRequest(http.MethodPost, "/agents", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
