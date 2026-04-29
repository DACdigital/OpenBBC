package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type mockResourceRepo struct {
	createFn func(ctx context.Context, opts types.CreateResourceOpts) (*types.Resource, error)
	getFn    func(ctx context.Context, id string) (*types.Resource, error)
	listFn   func(ctx context.Context, agentID string) ([]*types.Resource, error)
}

func (m *mockResourceRepo) Create(ctx context.Context, opts types.CreateResourceOpts) (*types.Resource, error) {
	return m.createFn(ctx, opts)
}

func (m *mockResourceRepo) GetByID(ctx context.Context, id string) (*types.Resource, error) {
	return m.getFn(ctx, id)
}

func (m *mockResourceRepo) ListByAgentID(ctx context.Context, agentID string) ([]*types.Resource, error) {
	return m.listFn(ctx, agentID)
}

func TestResourceHandler_Create_Success(t *testing.T) {
	h := NewResourceHandler(&mockResourceRepo{
		createFn: func(ctx context.Context, opts types.CreateResourceOpts) (*types.Resource, error) {
			return &types.Resource{
				ID:      "res-id",
				AgentID: opts.AgentID,
				Name:    opts.Name,
				Prompt:  opts.Prompt,
			}, nil
		},
	}, slog.Default())

	body := `{"agent_id":"agent-123","name":"get_users","prompt":"Fetches users"}`
	req := httptest.NewRequest(http.MethodPost, "/resources", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}

	var resp types.Resource
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != "res-id" {
		t.Errorf("ID = %q, want %q", resp.ID, "res-id")
	}
}

func TestResourceHandler_Create_ValidationError(t *testing.T) {
	h := NewResourceHandler(&mockResourceRepo{
		createFn: func(ctx context.Context, opts types.CreateResourceOpts) (*types.Resource, error) {
			return nil, types.ErrNameRequired
		},
	}, slog.Default())

	body := `{"agent_id":"agent-123","name":"","prompt":""}`
	req := httptest.NewRequest(http.MethodPost, "/resources", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestResourceHandler_Get_Success(t *testing.T) {
	h := NewResourceHandler(&mockResourceRepo{
		getFn: func(ctx context.Context, id string) (*types.Resource, error) {
			return &types.Resource{
				ID:      id,
				AgentID: "agent-123",
				Name:    "get_users",
				Prompt:  "Fetches users",
			}, nil
		},
	}, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/resources/res-123", nil)
	req.SetPathValue("id", "res-123")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp types.Resource
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != "res-123" {
		t.Errorf("ID = %q, want %q", resp.ID, "res-123")
	}
}

func TestResourceHandler_Get_NotFound(t *testing.T) {
	h := NewResourceHandler(&mockResourceRepo{
		getFn: func(ctx context.Context, id string) (*types.Resource, error) {
			return nil, types.ErrNotFound
		},
	}, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/resources/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestResourceHandler_Get_MissingID(t *testing.T) {
	h := NewResourceHandler(&mockResourceRepo{}, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/resources/", nil)
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestResourceHandler_ListByAgent_Success(t *testing.T) {
	h := NewResourceHandler(&mockResourceRepo{
		listFn: func(ctx context.Context, agentID string) ([]*types.Resource, error) {
			return []*types.Resource{
				{
					ID:      "res-1",
					AgentID: agentID,
					Name:    "get_users",
					Prompt:  "Fetches users",
				},
				{
					ID:      "res-2",
					AgentID: agentID,
					Name:    "get_posts",
					Prompt:  "Fetches posts",
				},
			}, nil
		},
	}, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/agents/agent-123/resources", nil)
	req.SetPathValue("agent_id", "agent-123")
	w := httptest.NewRecorder()

	h.ListByAgent(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp []*types.Resource
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) != 2 {
		t.Errorf("len = %d, want %d", len(resp), 2)
	}
}

func TestResourceHandler_ListByAgent_Empty(t *testing.T) {
	h := NewResourceHandler(&mockResourceRepo{
		listFn: func(ctx context.Context, agentID string) ([]*types.Resource, error) {
			return nil, nil
		},
	}, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/agents/agent-123/resources", nil)
	req.SetPathValue("agent_id", "agent-123")
	w := httptest.NewRecorder()

	h.ListByAgent(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp []*types.Resource
	json.NewDecoder(w.Body).Decode(&resp)
	if resp == nil {
		t.Errorf("expected empty slice, got nil")
	}
}

func TestResourceHandler_ListByAgent_MissingAgentID(t *testing.T) {
	h := NewResourceHandler(&mockResourceRepo{}, slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/agents//resources", nil)
	w := httptest.NewRecorder()

	h.ListByAgent(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
