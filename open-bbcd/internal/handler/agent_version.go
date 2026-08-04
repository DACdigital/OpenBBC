package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// AgentVersionListRepository is the narrow interface AgentVersionHandler
// needs. Kept separate from the fatter interfaces on agentDetail /
// configurator so the JSON list surface stays scoped to what it uses.
type AgentVersionListRepository interface {
	List(ctx context.Context, status string, limit int) ([]*types.AgentVersion, error)
}

type AgentVersionHandler struct {
	repo AgentVersionListRepository
}

func NewAgentVersionHandler(repo AgentVersionListRepository) *AgentVersionHandler {
	return &AgentVersionHandler{repo: repo}
}

// isValidVersionStatus mirrors the agent_versions.status CHECK constraint
// (see migration 025). Kept local — the shared isValidJobStatus() is
// PENDING/IN_PROGRESS/DONE/FAILED and doesn't apply to versions.
func isValidVersionStatus(s string) bool {
	switch s {
	case "INITIALIZING", "PENDING", "DRAFT", "TRAINING", "READY", "DEPLOYED":
		return true
	}
	return false
}

// ListJSON handles GET /agent_versions.json.
// Optional query params: status (any value from the CHECK constraint),
// limit (positive int, default 100; repo clamps values > 500 to 500).
// The alpha-generation drainer polls this with ?status=PENDING to find
// finalized versions awaiting bundle generation.
func (h *AgentVersionHandler) ListJSON(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && !isValidVersionStatus(status) {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid status"})
		return
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			JSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid limit"})
			return
		}
		limit = n
	}

	versions, err := h.repo.List(r.Context(), status, limit)
	if err != nil {
		Error(w, err)
		return
	}
	if versions == nil {
		versions = []*types.AgentVersion{}
	}
	JSON(w, http.StatusOK, versions)
}
