package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// DeployedAgentReader is the narrow interface the deployed handler needs.
type DeployedAgentReader interface {
	CurrentDeployedVersionID(ctx context.Context, chainRootID string) (string, error)
}

// DeployedStore is the narrow repo interface for deployed sessions/messages.
type DeployedStore interface {
	CreateSession(ctx context.Context, chainRootID, userID, title string) (*types.DeployedSession, error)
	GetSession(ctx context.Context, sessionID, userID string) (*types.DeployedSession, error)
	ListSessions(ctx context.Context, chainRootID, userID string) ([]*types.DeployedSession, error)
	UpdateSessionTitle(ctx context.Context, sessionID, userID, title string) error
	DeleteSession(ctx context.Context, sessionID, userID string) error
	LoadMessages(ctx context.Context, sessionID string) ([]*types.DeployedMessage, error)
}

type DeployedHandler struct {
	agents    DeployedAgentReader
	store     DeployedStore
	orch      TurnRunner
	transport transport.Factory
	chatStore chat.ChatStore // unused at the handler layer; orchestrator was constructed with it
	logger    *slog.Logger
}

func NewDeployedHandler(
	agents DeployedAgentReader,
	store DeployedStore,
	chatStore chat.ChatStore,
	orch TurnRunner,
	tf transport.Factory,
	logger *slog.Logger,
) *DeployedHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeployedHandler{
		agents: agents, store: store, orch: orch,
		transport: tf, chatStore: chatStore, logger: logger,
	}
}

// requireDeployed resolves the chain root's currently-deployed version.
// Returns (versionID, true) if a version is deployed; ("", false) and a
// 404 written to w if no version is deployed (no existence leak).
func (h *DeployedHandler) requireDeployed(w http.ResponseWriter, r *http.Request, chainRootID string) (string, bool) {
	v, err := h.agents.CurrentDeployedVersionID(r.Context(), chainRootID)
	if err != nil {
		Error(w, err)
		return "", false
	}
	if v == "" {
		http.NotFound(w, r)
		return "", false
	}
	return v, true
}

// CreateSession handles POST /deployed/{agent_id}/sessions
func (h *DeployedHandler) CreateSession(w http.ResponseWriter, r *http.Request) {
	chainRootID := r.PathValue("agent_id")
	if _, ok := h.requireDeployed(w, r, chainRootID); !ok {
		return
	}
	var body struct {
		UserID string `json:"user_id"`
		Title  string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.UserID) == "" {
		Error(w, types.ErrUserIDRequired)
		return
	}
	sess, err := h.store.CreateSession(r.Context(), chainRootID, body.UserID, body.Title)
	if err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(sess)
}

// ListSessions handles GET /deployed/{agent_id}/sessions?user_id=X
func (h *DeployedHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	chainRootID := r.PathValue("agent_id")
	if _, ok := h.requireDeployed(w, r, chainRootID); !ok {
		return
	}
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		Error(w, types.ErrUserIDRequired)
		return
	}
	sessions, err := h.store.ListSessions(r.Context(), chainRootID, userID)
	if err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessions)
}

// GetSession handles GET /deployed/{agent_id}/sessions/{session_id}?user_id=X
func (h *DeployedHandler) GetSession(w http.ResponseWriter, r *http.Request) {
	chainRootID := r.PathValue("agent_id")
	sessionID := r.PathValue("session_id")
	if _, ok := h.requireDeployed(w, r, chainRootID); !ok {
		return
	}
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		Error(w, types.ErrUserIDRequired)
		return
	}
	sess, err := h.store.GetSession(r.Context(), sessionID, userID)
	if err != nil {
		Error(w, err)
		return
	}
	if sess.ChainRootID != chainRootID {
		http.NotFound(w, r)
		return
	}
	msgs, err := h.store.LoadMessages(r.Context(), sessionID)
	if err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Session  *types.DeployedSession   `json:"session"`
		Messages []*types.DeployedMessage `json:"messages"`
	}{sess, msgs})
}

// UpdateTitle handles PATCH /deployed/{agent_id}/sessions/{session_id}/title
func (h *DeployedHandler) UpdateTitle(w http.ResponseWriter, r *http.Request) {
	chainRootID := r.PathValue("agent_id")
	sessionID := r.PathValue("session_id")
	if _, ok := h.requireDeployed(w, r, chainRootID); !ok {
		return
	}
	var body struct {
		UserID string `json:"user_id"`
		Title  string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.UserID == "" {
		Error(w, types.ErrUserIDRequired)
		return
	}
	if err := h.store.UpdateSessionTitle(r.Context(), sessionID, body.UserID, body.Title); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteSession handles DELETE /deployed/{agent_id}/sessions/{session_id}?user_id=X
func (h *DeployedHandler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	chainRootID := r.PathValue("agent_id")
	sessionID := r.PathValue("session_id")
	if _, ok := h.requireDeployed(w, r, chainRootID); !ok {
		return
	}
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		Error(w, types.ErrUserIDRequired)
		return
	}
	if err := h.store.DeleteSession(r.Context(), sessionID, userID); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Turn — implemented in Task 11. Placeholder so the route is wireable now.
func (h *DeployedHandler) Turn(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented yet (Task 11)", http.StatusNotImplemented)
}
