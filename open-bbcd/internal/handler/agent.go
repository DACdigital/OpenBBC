package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type AgentRepository interface {
	Create(ctx context.Context, opts types.CreateAgentOpts) (*types.Agent, error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
	List(ctx context.Context) ([]*types.Agent, error)
	CreateVersion(ctx context.Context, parentID string, opts types.CreateVersionOpts) (*types.Agent, error)
	GetVersionChain(ctx context.Context, agentID string) (types.AgentChain, error)
}

type AgentHandler struct {
	repo   AgentRepository
	logger *slog.Logger
}

func NewAgentHandler(repo AgentRepository, logger *slog.Logger) *AgentHandler {
	return &AgentHandler{repo: repo, logger: logger}
}

func (h *AgentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var opts types.CreateAgentOpts
	if err := DecodeJSON(r, &opts); err != nil {
		h.logger.Warn("create agent: decode body", slog.Any("error", err))
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	agent, err := h.repo.Create(r.Context(), opts)
	if err != nil {
		h.logger.Error("create agent", slog.Any("error", err))
		Error(w, err)
		return
	}

	h.logger.Info("agent created", slog.String("id", agent.ID), slog.String("name", agent.Name))
	JSON(w, http.StatusCreated, agent)
}

func (h *AgentHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "id is required"})
		return
	}

	agent, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if !errors.Is(err, types.ErrNotFound) {
			h.logger.Error("get agent", slog.String("id", id), slog.Any("error", err))
		}
		Error(w, err)
		return
	}

	JSON(w, http.StatusOK, agent)
}

func (h *AgentHandler) List(w http.ResponseWriter, r *http.Request) {
	agents, err := h.repo.List(r.Context())
	if err != nil {
		h.logger.Error("list agents", slog.Any("error", err))
		Error(w, err)
		return
	}

	if agents == nil {
		agents = []*types.Agent{}
	}

	JSON(w, http.StatusOK, agents)
}

func (h *AgentHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "id is required"})
		return
	}
	chain, err := h.repo.GetVersionChain(r.Context(), id)
	if err != nil {
		if !errors.Is(err, types.ErrNotFound) {
			h.logger.Error("list versions", slog.String("id", id), slog.Any("error", err))
		}
		Error(w, err)
		return
	}
	JSON(w, http.StatusOK, chain)
}

func (h *AgentHandler) CreateVersion(w http.ResponseWriter, r *http.Request) {
	parentID := r.PathValue("id")
	var opts types.CreateVersionOpts
	if err := DecodeJSON(r, &opts); err != nil {
		h.logger.Warn("create version: decode body", slog.Any("error", err))
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	agent, err := h.repo.CreateVersion(r.Context(), parentID, opts)
	if err != nil {
		h.logger.Error("create version", slog.String("parent_id", parentID), slog.Any("error", err))
		Error(w, err)
		return
	}

	h.logger.Info("version created", slog.String("id", agent.ID), slog.String("parent_id", parentID))
	JSON(w, http.StatusCreated, agent)
}
