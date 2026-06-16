package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
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

type turnRequest struct {
	UserID string `json:"user_id"`
	Input  []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"input"`
}

// Turn runs one chat turn against the currently-deployed version of the chain.
// AG-UI SSE on success. Validates (session_id, user_id) before opening the
// stream so we can return clean 4xx codes for auth/scope failures.
func (h *DeployedHandler) Turn(w http.ResponseWriter, r *http.Request) {
	chainRootID := r.PathValue("agent_id")
	sessionID := r.PathValue("session_id")

	versionID, ok := h.requireDeployed(w, r, chainRootID)
	if !ok {
		return
	}

	var req turnRequest
	if err := DecodeJSON(r, &req); err != nil {
		Error(w, err)
		return
	}
	if req.UserID == "" {
		Error(w, types.ErrUserIDRequired)
		return
	}

	// Verify (session_id, user_id) matches a stored row BEFORE we open the SSE.
	// Once SSE headers are set, errors must flow as ErrorEvent only.
	sess, err := h.store.GetSession(r.Context(), sessionID, req.UserID)
	if err != nil {
		Error(w, err)
		return
	}
	if sess.ChainRootID != chainRootID {
		http.NotFound(w, r)
		return
	}

	input := make([]llm.Block, 0, len(req.Input))
	for _, b := range req.Input {
		if b.Type == "text" && b.Text != "" {
			input = append(input, llm.TextBlock{Text: b.Text})
		}
	}

	w.Header().Set("Content-Type", h.transport.ContentType())
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	sink, err := h.transport.NewSink(w)
	if err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	if err := h.orch.Turn(r.Context(), versionID, sessionID, input, sink); err != nil {
		h.logger.Error("deployed turn failed",
			slog.String("chain_root_id", chainRootID),
			slog.String("version_id", versionID),
			slog.String("session_id", sessionID),
			slog.Any("err", err),
		)
		// Orchestrator already emitted ErrorEvent in-band. Nothing more to do.
	}
}
