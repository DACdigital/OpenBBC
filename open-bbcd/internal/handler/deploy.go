package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// DeployAgentRepository is the narrow per-agent dependency.
type DeployAgentRepository interface {
	ResolveID(ctx context.Context, ref string) (string, error)
	GetByID(ctx context.Context, agentID string) (*types.Agent, error)
}

// DeployVersionRepository is the narrow per-version dependency.
type DeployVersionRepository interface {
	ResolveID(ctx context.Context, ref string) (string, error)
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

// Deploy handles POST /agents/{agent_id}/deploy. Accepts either
// application/json ({"version_id":"..."}) or application/x-www-form-urlencoded
// (version_id=...). The BO deploy modal posts urlencoded via htmx's hx-vals;
// external callers can post JSON.
// Returns 200 with {agent, version, previous_deployed_version_id}.
// 400 if version_id is missing. 404 if the version doesn't belong to the agent
// or doesn't exist. 409 if the version is not READY or has unmapped endpoints.
func (h *DeployHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	agentID, ok := resolvePathID(w, r, h.agents, "agent_id")
	if !ok {
		return
	}
	// htmx's hx-vals serializes to urlencoded by default; external API
	// callers post JSON. Dispatch on Content-Type, defaulting to JSON so
	// existing curl-style callers that omit the header keep working.
	var body deployBody
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") || strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		body.VersionID = r.FormValue("version_id")
	} else {
		if err := DecodeJSON(r, &body); err != nil {
			Error(w, err)
			return
		}
	}
	if body.VersionID == "" {
		http.Error(w, "version_id is required", http.StatusBadRequest)
		return
	}

	// version_id may be a UUID or a version static ID; resolve it, then verify
	// the version belongs to this agent before mutating.
	versionID, err := h.versions.ResolveID(r.Context(), body.VersionID)
	if err != nil {
		Error(w, err)
		return
	}
	version, err := h.versions.GetByID(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	if version.AgentID != agentID {
		Error(w, types.ErrNotFound)
		return
	}

	// The repository Deploy enforces the READY precondition and the
	// all-endpoints-mapped check inside its transaction.
	prev, err := h.versions.Deploy(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}

	updatedAgent, err := h.agents.GetByID(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	updatedVersion, err := h.versions.GetByID(r.Context(), versionID)
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
	agentID, ok := resolvePathID(w, r, h.agents, "agent_id")
	if !ok {
		return
	}
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
