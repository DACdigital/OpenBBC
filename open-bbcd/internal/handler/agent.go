package handler

import (
	"context"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type AgentRepository interface {
	Create(ctx context.Context, opts types.CreateAgentOpts) (*types.Agent, error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
	List(ctx context.Context) ([]*types.Agent, error)
}

type AgentHandler struct {
	repo AgentRepository
}

func NewAgentHandler(repo AgentRepository) *AgentHandler {
	return &AgentHandler{repo: repo}
}

func (h *AgentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var opts types.CreateAgentOpts
	if err := DecodeJSON(r, &opts); err != nil {
		Error(w, err)
		return
	}

	agent, err := h.repo.Create(r.Context(), opts)
	if err != nil {
		Error(w, err)
		return
	}

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
		Error(w, err)
		return
	}

	JSON(w, http.StatusOK, agent)
}

func (h *AgentHandler) List(w http.ResponseWriter, r *http.Request) {
	agents, err := h.repo.List(r.Context())
	if err != nil {
		Error(w, err)
		return
	}

	if agents == nil {
		agents = []*types.Agent{}
	}

	JSON(w, http.StatusOK, agents)
}
