package handler

import (
	"context"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/google/uuid"
)

// ChatAgentReader is the narrow agent interface needed by ChatHandler.
type ChatAgentReader interface {
	GetByID(ctx context.Context, id string) (*types.Agent, error)
}

// ChatSessionStore is the narrow chat-repo interface needed by ChatHandler.
type ChatSessionStore interface {
	EnsureSession(ctx context.Context, sessionID, agentID string) error
	ListSessions(ctx context.Context, agentID string) ([]*types.ChatSession, error)
	LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error)
}

// TurnRunner is the orchestrator dependency. Implemented by *chat.Orchestrator.
// Defined here (not in chat package) so handler tests can substitute a stub.
type TurnRunner interface {
	Turn(ctx context.Context, agentID, sessionID string, input []llm.Block, sink transport.Sink) error
}

// Compile-time check that *chat.Orchestrator satisfies TurnRunner.
var _ TurnRunner = (*chat.Orchestrator)(nil)

type ChatHandler struct {
	agents       ChatAgentReader
	chats        ChatSessionStore
	orch         TurnRunner
	transport    transport.Factory
	logger       *slog.Logger
	sessionsTmpl *template.Template
	viewTmpl     *template.Template
}

func NewChatHandler(
	agents ChatAgentReader,
	chats ChatSessionStore,
	orch TurnRunner,
	tf transport.Factory,
	webFS fs.FS,
	logger *slog.Logger,
) (*ChatHandler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	funcs := template.FuncMap{
		"statusClass": statusClass,
		"urlEncode":   url.PathEscape,
	}
	parse := func(name string) (*template.Template, error) {
		return template.New("").Funcs(funcs).ParseFS(webFS,
			"templates/layout.html",
			"templates/chat/"+name+".html",
		)
	}
	sessionsTmpl, err := parse("sessions")
	if err != nil {
		return nil, err
	}
	viewTmpl, err := parse("view")
	if err != nil {
		return nil, err
	}
	return &ChatHandler{
		agents: agents, chats: chats, orch: orch, transport: tf, logger: logger,
		sessionsTmpl: sessionsTmpl, viewTmpl: viewTmpl,
	}, nil
}

// NewSession creates a new chat_sessions row and 303-redirects to the chat view.
// Returns 409 if the agent has no bundle yet.
func (h *ChatHandler) NewSession(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, err := h.agents.GetByID(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	if len(agent.Bundle) == 0 {
		Error(w, types.ErrAgentNotRunnable)
		return
	}
	sessionID := uuid.NewString()
	if err := h.chats.EnsureSession(r.Context(), sessionID, agentID); err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/agents/"+agentID+"/chat/"+sessionID, http.StatusSeeOther)
}

type sessionListPageData struct {
	Active    string
	AgentID   string
	AgentName string
	Sessions  []*types.ChatSession
}

// SessionList renders the session-list page for one agent version.
func (h *ChatHandler) SessionList(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, err := h.agents.GetByID(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}
	sessions, err := h.chats.ListSessions(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	data := sessionListPageData{
		Active:    "agents",
		AgentID:   agentID,
		AgentName: agent.Name,
		Sessions:  sessions,
	}
	renderTemplate(w, h.sessionsTmpl, "layout", data)
}

type chatViewPageData struct {
	Active    string
	AgentID   string
	AgentName string
	SessionID string
	Messages  []*types.ChatMessage
	HasBundle bool
}

// ChatView renders the chat UI for one session. If session_id doesn't
// exist yet, renders an empty view (lazy creation by first POST /turn).
func (h *ChatHandler) ChatView(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	sessionID := r.PathValue("session_id")

	agent, err := h.agents.GetByID(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}

	// LoadMessages returns empty slice for non-existent session — that's fine.
	msgs, err := h.chats.LoadMessages(r.Context(), sessionID)
	if err != nil {
		Error(w, err)
		return
	}

	data := chatViewPageData{
		Active:    "agents",
		AgentID:   agentID,
		AgentName: agent.Name,
		SessionID: sessionID,
		Messages:  msgs,
		HasBundle: len(agent.Bundle) > 0,
	}
	renderTemplate(w, h.viewTmpl, "layout", data)
}

// TurnRequest is the body of POST /turn — implemented in B24.
type TurnRequest struct {
	Input []TurnInputBlock `json:"input"`
}
type TurnInputBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
