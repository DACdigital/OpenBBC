package handler

import (
	"context"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type ResourceRepository interface {
	Create(ctx context.Context, input types.CreateResourceInput) (*types.Resource, error)
	GetByID(ctx context.Context, id string) (*types.Resource, error)
	ListByAgentID(ctx context.Context, agentID string) ([]*types.Resource, error)
}

type ResourceHandler struct {
	repo ResourceRepository
}

func NewResourceHandler(repo ResourceRepository) *ResourceHandler {
	return &ResourceHandler{repo: repo}
}

func (h *ResourceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input types.CreateResourceInput
	if err := DecodeJSON(r, &input); err != nil {
		Error(w, err)
		return
	}

	resource, err := h.repo.Create(r.Context(), input)
	if err != nil {
		Error(w, err)
		return
	}

	JSON(w, http.StatusCreated, resource)
}

func (h *ResourceHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "id is required"})
		return
	}

	resource, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}

	JSON(w, http.StatusOK, resource)
}

func (h *ResourceHandler) ListByAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if agentID == "" {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "agent_id is required"})
		return
	}

	resources, err := h.repo.ListByAgentID(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	if resources == nil {
		resources = []*types.Resource{}
	}

	JSON(w, http.StatusOK, resources)
}
