package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

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
	Messages  []messageView
	HasBundle bool
}

// messageView is a UI-ready projection of a persisted ChatMessage. The raw
// content (JSONB array of Anthropic-shape blocks) is unpacked into typed
// blockView entries so the template can render text inline, tool calls and
// tool results as collapsible <details>, matching the live-stream UI.
type messageView struct {
	Role   string
	Blocks []blockView
}

type blockView struct {
	Kind         string // "text" | "tool_call" | "tool_result"
	Text         string
	ToolName     string
	ToolArgs     string
	ToolResult   string
	ToolIsError  bool
	ToolIsMocked bool
}

func buildMessageViews(msgs []*types.ChatMessage) []messageView {
	out := make([]messageView, 0, len(msgs))
	for _, m := range msgs {
		var raw []json.RawMessage
		if err := json.Unmarshal(m.Content, &raw); err != nil {
			continue
		}
		mv := messageView{Role: string(m.Role)}
		for _, r := range raw {
			var head struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(r, &head); err != nil {
				continue
			}
			switch head.Type {
			case "text":
				var b struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(r, &b)
				mv.Blocks = append(mv.Blocks, blockView{Kind: "text", Text: b.Text})
			case "tool_use":
				var b struct {
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				}
				_ = json.Unmarshal(r, &b)
				mv.Blocks = append(mv.Blocks, blockView{
					Kind:     "tool_call",
					ToolName: b.Name,
					ToolArgs: prettyJSON(b.Input),
				})
			case "tool_result":
				var b struct {
					Content json.RawMessage `json:"content"`
					IsError bool            `json:"is_error"`
				}
				_ = json.Unmarshal(r, &b)
				mv.Blocks = append(mv.Blocks, blockView{
					Kind:         "tool_result",
					ToolResult:   prettyJSON(b.Content),
					ToolIsError:  b.IsError,
					ToolIsMocked: strings.Contains(string(b.Content), `"_mocked":true`),
				})
			}
		}
		out = append(out, mv)
	}
	return out
}

func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
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
		Messages:  buildMessageViews(msgs),
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

// Turn runs one chat turn end-to-end. Decodes the JSON request body,
// builds the input blocks, opens a Sink from the transport factory,
// hands off to the orchestrator. Errors after the first SSE byte have
// already been streamed by the orchestrator as ErrorEvent — they are
// only logged here.
func (h *ChatHandler) Turn(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	sessionID := r.PathValue("session_id")

	var req TurnRequest
	if err := DecodeJSON(r, &req); err != nil {
		h.logger.Error("chat turn: decode JSON request body failed",
			slog.String("agent_id", agentID),
			slog.String("session_id", sessionID),
			slog.Any("err", err),
		)
		Error(w, err)
		return
	}

	// Build typed input blocks. v1 supports only text inputs; other
	// block types are silently ignored (no error).
	input := make([]llm.Block, 0, len(req.Input))
	for _, b := range req.Input {
		if b.Type == "text" && b.Text != "" {
			input = append(input, llm.TextBlock{Text: b.Text})
		}
	}

	// Set SSE-friendly response headers BEFORE constructing the sink:
	// the sink may flush as soon as it's used, and headers can't change
	// after the first byte.
	w.Header().Set("Content-Type", h.transport.ContentType())
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx hint

	sink, err := h.transport.NewSink(w)
	if err != nil {
		h.logger.Error("chat turn: transport sink construction failed",
			slog.String("agent_id", agentID),
			slog.String("session_id", sessionID),
			slog.Any("err", err),
		)
		Error(w, err)
		return
	}

	// Status code emitted explicitly (200 OK) so the response body starts
	// streaming. Defer-closing the sink is owned here, NOT by the orchestrator.
	w.WriteHeader(http.StatusOK)

	if err := h.orch.Turn(r.Context(), agentID, sessionID, input, sink); err != nil {
		h.logger.Error("chat turn failed",
			slog.String("agent_id", agentID),
			slog.String("session_id", sessionID),
			slog.Any("err", err),
		)
		// Orchestrator already emitted ErrorEvent. Nothing more to do here.
	}
}
