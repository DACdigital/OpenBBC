package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// DeployAgentRepository is the narrow per-agent dependency.
type DeployAgentRepository interface {
	GetByID(ctx context.Context, agentID string) (*types.Agent, error)
}

// DeployVersionRepository is the narrow per-version dependency.
type DeployVersionRepository interface {
	GetByID(ctx context.Context, versionID string) (*types.AgentVersion, error)
	Deploy(ctx context.Context, versionID string) (*string, error)
	Undeploy(ctx context.Context, versionID string) error
	CurrentDeployedID(ctx context.Context, agentID string) (string, error)
}

type DeployHandler struct {
	agents   DeployAgentRepository
	versions DeployVersionRepository
}

func NewDeployHandler(agents DeployAgentRepository, versions DeployVersionRepository) *DeployHandler {
	return &DeployHandler{agents: agents, versions: versions}
}

type deployBody struct {
	VersionID string `json:"version_id"`
}

type deployResponse struct {
	Agent                     *types.Agent        `json:"agent"`
	Version                   *types.AgentVersion `json:"version"`
	PreviousDeployedVersionID *string             `json:"previous_deployed_version_id"`
}

// Deploy handles POST /agents/{agent_id}/deploy with body {"version_id":"..."}.
// Returns 200 with {agent, version, previous_deployed_version_id}.
// 400 if version_id is missing. 404 if the version doesn't belong to the agent
// or doesn't exist. 409 if the version is not READY.
func (h *DeployHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	var body deployBody
	if err := DecodeJSON(r, &body); err != nil {
		Error(w, err)
		return
	}
	if body.VersionID == "" {
		http.Error(w, "version_id is required", http.StatusBadRequest)
		return
	}

	// Verify the version belongs to this agent before mutating.
	version, err := h.versions.GetByID(r.Context(), body.VersionID)
	if err != nil {
		Error(w, err)
		return
	}
	if version.AgentID != agentID {
		Error(w, types.ErrNotFound)
		return
	}

	prev, err := h.versions.Deploy(r.Context(), body.VersionID)
	if err != nil {
		Error(w, err)
		return
	}

	updatedAgent, err := h.agents.GetByID(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	updatedVersion, err := h.versions.GetByID(r.Context(), body.VersionID)
	if err != nil {
		Error(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(deployResponse{
		Agent:                     updatedAgent,
		Version:                   updatedVersion,
		PreviousDeployedVersionID: prev,
	})
}

// Undeploy handles POST /agents/{agent_id}/undeploy. Takes the currently-
// deployed version of the agent offline. Returns 409 if nothing is deployed.
func (h *DeployHandler) Undeploy(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	curID, err := h.versions.CurrentDeployedID(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	if curID == "" {
		Error(w, types.ErrAgentNotDeployed)
		return
	}
	if err := h.versions.Undeploy(r.Context(), curID); err != nil {
		Error(w, err)
		return
	}
	updatedAgent, err := h.agents.GetByID(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updatedAgent)
}
