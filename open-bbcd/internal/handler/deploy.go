package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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

// DeployWiringRepo is the narrow wiring-repo interface DeployHandler needs.
// Implemented by *repository.VersionWiringRepository.
type DeployWiringRepo interface {
	ListEndpointBackends(ctx context.Context, versionID string) (map[string]string, error)
}

type DeployHandler struct {
	agents   DeployAgentRepository
	versions DeployVersionRepository
	wiring   DeployWiringRepo
}

func NewDeployHandler(agents DeployAgentRepository, versions DeployVersionRepository, wiring DeployWiringRepo) *DeployHandler {
	return &DeployHandler{agents: agents, versions: versions, wiring: wiring}
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
// or doesn't exist. 409 if the version is not READY or has unmapped endpoints.
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

	// Block deploy if any endpoint in the bundle is unmapped.
	if err := h.validateAllEndpointsMapped(r.Context(), version); err != nil {
		Error(w, err)
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

// validateAllEndpointsMapped returns ErrUnmappedEndpoints (wrapped with a
// detail message about which endpoints are missing) if the version's bundle
// has any tools[].id that isn't present in agent_version_endpoint_backend
// for this version. Returns nil when every endpoint is mapped.
func (h *DeployHandler) validateAllEndpointsMapped(ctx context.Context, version *types.AgentVersion) error {
	if len(version.Bundle) == 0 {
		// No bundle yet — can't have endpoints to map. The existing flow
		// already rejects non-READY versions with ErrAgentNotDeployable;
		// don't double-error here.
		return nil
	}

	// Minimal local shape: we only need the endpoint ids.
	var snap struct {
		Tools []struct {
			ID string `json:"id"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(version.Bundle, &snap); err != nil {
		// Malformed bundle is an internal error, not a deploy-validation
		// failure. Let it surface as 500.
		return err
	}

	if len(snap.Tools) == 0 {
		return nil // nothing to map
	}

	mapping, err := h.wiring.ListEndpointBackends(ctx, version.ID)
	if err != nil {
		return err
	}

	var missing []string
	for _, t := range snap.Tools {
		if t.ID == "" {
			continue
		}
		if _, ok := mapping[t.ID]; !ok {
			missing = append(missing, t.ID)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", types.ErrUnmappedEndpoints, strings.Join(missing, ", "))
	}
	return nil
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
