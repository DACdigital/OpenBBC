package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type ResourceRepository interface {
	Create(ctx context.Context, opts types.CreateResourceOpts) (*types.Resource, error)
	GetByID(ctx context.Context, id string) (*types.Resource, error)
	ListByAgentID(ctx context.Context, agentID string) ([]*types.Resource, error)
}

type ResourceHandler struct {
	repo   ResourceRepository
	logger *slog.Logger
}

func NewResourceHandler(repo ResourceRepository, logger *slog.Logger) *ResourceHandler {
	return &ResourceHandler{repo: repo, logger: logger}
}

func (h *ResourceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var opts types.CreateResourceOpts
	if err := DecodeJSON(r, &opts); err != nil {
		h.logger.Warn("create resource: decode body", slog.Any("error", err))
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	resource, err := h.repo.Create(r.Context(), opts)
	if err != nil {
		h.logger.Error("create resource", slog.Any("error", err))
		Error(w, err)
		return
	}

	h.logger.Info("resource created", slog.String("id", resource.ID))
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
		if !errors.Is(err, types.ErrNotFound) {
			h.logger.Error("get resource", slog.String("id", id), slog.Any("error", err))
		}
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
		h.logger.Error("list resources", slog.String("agent_id", agentID), slog.Any("error", err))
		Error(w, err)
		return
	}

	if resources == nil {
		resources = []*types.Resource{}
	}

	JSON(w, http.StatusOK, resources)
}
