package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/artifacts"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// DeployedSessionReader is the (session_id, user_id) lookup the deployed
// artifact preamble needs.
type DeployedSessionReader interface {
	GetSession(ctx context.Context, sessionID, userID string) (*types.DeployedSession, error)
}

// DeployedArtifactHandler serves the deployed artifact routes (spec § REST —
// deployed). Every route runs the turn preamble, then delegates to the
// shared artifactService with the deployed_session_artifacts table.
// Registered only when the artifact registry is enabled.
type DeployedArtifactHandler struct {
	agents   DeployedAgentReader
	sessions DeployedSessionReader
	svc      *artifactService
}

func NewDeployedArtifactHandler(
	agents DeployedAgentReader,
	sessions DeployedSessionReader,
	rows SessionArtifactStore,
	registry *artifacts.Registry,
	maxUploadMB, maxPending int,
	logger *slog.Logger,
) *DeployedArtifactHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeployedArtifactHandler{
		agents: agents, sessions: sessions,
		svc: &artifactService{rows: rows, registry: registry, maxUploadMB: maxUploadMB,
			maxPending: maxPending, logger: logger},
	}
}

// Register mounts the four deployed artifact routes on mux.
func (h *DeployedArtifactHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /deployed/{agent_id}/sessions/{session_id}/artifacts", h.guard(h.svc.upload))
	mux.HandleFunc("GET /deployed/{agent_id}/sessions/{session_id}/artifacts/{path...}", h.guard(h.svc.retrieve))
	mux.HandleFunc("GET /deployed/{agent_id}/sessions/{session_id}/pending-artifacts", h.guard(h.svc.listPending))
	mux.HandleFunc("DELETE /deployed/{agent_id}/sessions/{session_id}/pending-artifacts/{id}", h.guard(h.svc.deletePending))
}

// guard runs the deployed turn preamble — agent deployed, user_id present,
// (session_id, user_id) exists, session belongs to agent_id — then calls
// next with the session id. Every scope failure is 404 (no existence leak);
// a missing user_id is 400.
func (h *DeployedArtifactHandler) guard(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := r.PathValue("agent_id")
		sessionID := r.PathValue("session_id")
		v, err := h.agents.CurrentDeployedID(r.Context(), agentID)
		if err != nil {
			Error(w, err)
			return
		}
		if v == "" {
			Error(w, types.ErrNotFound)
			return
		}
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			Error(w, types.ErrUserIDRequired)
			return
		}
		sess, err := h.sessions.GetSession(r.Context(), sessionID, userID)
		if err != nil {
			Error(w, err)
			return
		}
		if sess.AgentID != agentID {
			Error(w, types.ErrNotFound)
			return
		}
		next(w, r, sessionID)
	}
}
