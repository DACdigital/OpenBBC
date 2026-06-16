package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// DeployAgentRepository is the narrow agent interface DeployHandler needs.
type DeployAgentRepository interface {
	GetByID(ctx context.Context, id string) (*types.Agent, error)
	Deploy(ctx context.Context, versionID string) (*string, error)
	Undeploy(ctx context.Context, versionID string) error
}

type DeployHandler struct {
	repo DeployAgentRepository
}

func NewDeployHandler(repo DeployAgentRepository) *DeployHandler {
	return &DeployHandler{repo: repo}
}

type deployResponse struct {
	Agent                     *types.Agent `json:"agent"`
	PreviousDeployedVersionID *string      `json:"previous_deployed_version_id"`
}

// Deploy promotes a version to DEPLOYED, rotating any other deployed version
// in the same chain to READY. Returns 200 with {agent, previous_deployed_version_id}.
// 409 if the version is not READY. Idempotent on already-DEPLOYED versions
// (returns 200 with previous_deployed_version_id = null).
func (h *DeployHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	prev, err := h.repo.Deploy(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	updated, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(deployResponse{Agent: updated, PreviousDeployedVersionID: prev})
}

// Undeploy demotes a DEPLOYED version to READY. Returns 200 with the updated
// Agent JSON; 409 if not currently deployed; 404 if missing.
func (h *DeployHandler) Undeploy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.repo.Undeploy(r.Context(), id); err != nil {
		Error(w, err)
		return
	}
	updated, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}
