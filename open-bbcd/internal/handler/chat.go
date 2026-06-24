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

// ChatAgentReader is the narrow agent-version interface needed by ChatHandler.
// One call returns the version row and its owning Agent via JOIN so the
// handler can populate both VersionID (URL param) and AgentID (back-link)
// page-data fields without a second round-trip.
type ChatAgentReader interface {
	GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error)
}

// ChatSessionStore is the narrow chat-repo interface needed by ChatHandler.
// All non-message methods are scoped to an agent version (chat_sessions.agent_version_id).
type ChatSessionStore interface {
	EnsureSession(ctx context.Context, sessionID, versionID string) error
	GetSession(ctx context.Context, sessionID, versionID string) (*types.ChatSession, error)
	ListSessions(ctx context.Context, versionID string) ([]*types.ChatSession, error)
	LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error)
	UpdateSessionTitle(ctx context.Context, sessionID, versionID, title string) error
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
// Returns 409 if the version has no bundle yet.
func (h *ChatHandler) NewSession(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	version, _, err := h.agents.GetWithAgent(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	if len(version.Bundle) == 0 {
		Error(w, types.ErrAgentNotRunnable)
		return
	}
	sessionID := uuid.NewString()
	if err := h.chats.EnsureSession(r.Context(), sessionID, versionID); err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/agent_versions/"+versionID+"/chat/"+sessionID, http.StatusSeeOther)
}

type sessionListPageData struct {
	Active    string
	VersionID string // URL path param value (a version row's ID)
	AgentID   string // stable agent ID, used for default back-link to agent listing
	AgentName string
	Sessions  []*types.ChatSession
	// BackHref + BackLabel control the "back" link at the top of the
	// session list. Derived from ?from= so users return to wherever they
	// came in (configurator view, a specific chat session, or the default
	// agent listing).
	BackHref  string
	BackLabel string
}

// SessionList renders the session-list page for one agent version.
func (h *ChatHandler) SessionList(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	_, agent, err := h.agents.GetWithAgent(r.Context(), versionID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}
	sessions, err := h.chats.ListSessions(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	backHref, backLabel := resolveSessionListBack(r.URL.Query().Get("from"), versionID, agent.ID, agent.Name)
	data := sessionListPageData{
		Active:    "agents",
		VersionID: versionID,
		AgentID:   agent.ID,
		AgentName: agent.Name,
		Sessions:  sessions,
		BackHref:  backHref,
		BackLabel: backLabel,
	}
	renderTemplate(w, h.sessionsTmpl, "layout", data)
}

// resolveSessionListBack interprets the ?from= query parameter on the session
// list page and returns (href, label) for the page's back link.
//
// Supported `from` values:
//   - "version"          → back to the version's configurator (Architecture tab).
//   - "chat:<sessionID>" → back to that chat session.
//   - anything else (or empty) → back to the agent's version listing (default).
func resolveSessionListBack(from, versionID, agentID, agentName string) (string, string) {
	switch {
	case from == "version":
		return "/agent_versions/" + versionID + "/configure/architecture/flows", "← Configurator"
	case strings.HasPrefix(from, "chat:"):
		sessionID := strings.TrimPrefix(from, "chat:")
		if sessionID != "" {
			return "/agent_versions/" + versionID + "/chat/" + sessionID, "← Back to chat"
		}
	}
	return "/agents/ui?agent=" + agentID, "← " + agentName
}

type chatViewPageData struct {
	Active       string
	VersionID    string // URL path param value (a version row's ID)
	AgentID      string // stable agent ID, used for back-link to agent listing
	AgentName    string
	SessionID    string
	SessionTitle string
	Messages     []messageView
	HasBundle    bool
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

// buildMessageViews turns persisted ChatMessage rows into UI bubbles. Each
// user message is its own bubble; every non-user message (assistant text +
// tool_use, plus the tool-role wrappers carrying tool_result) is merged into
// a single assistant bubble per turn, so the history matches the in-stream
// rendering (one bubble per assistant turn, regardless of how many DB rows
// the orchestrator split it across).
func buildMessageViews(msgs []*types.ChatMessage) []messageView {
	out := make([]messageView, 0, len(msgs))
	var pending *messageView // open assistant bubble waiting for more blocks
	flush := func() {
		if pending != nil {
			out = append(out, *pending)
			pending = nil
		}
	}
	for _, m := range msgs {
		var raw []json.RawMessage
		if err := json.Unmarshal(m.Content, &raw); err != nil {
			continue
		}
		blocks := decodeBlocks(raw)
		if m.Role == types.ChatRoleUser {
			flush()
			out = append(out, messageView{Role: string(types.ChatRoleUser), Blocks: blocks})
			continue
		}
		// Assistant or tool — both go into the current assistant bubble.
		if pending == nil {
			pending = &messageView{Role: string(types.ChatRoleAssistant)}
		}
		pending.Blocks = append(pending.Blocks, blocks...)
	}
	flush()
	return out
}

func decodeBlocks(raw []json.RawMessage) []blockView {
	blocks := make([]blockView, 0, len(raw))
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
			blocks = append(blocks, blockView{Kind: "text", Text: b.Text})
		case "tool_use":
			var b struct {
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(r, &b)
			blocks = append(blocks, blockView{
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
			blocks = append(blocks, blockView{
				Kind:         "tool_result",
				ToolResult:   prettyJSON(b.Content),
				ToolIsError:  b.IsError,
				ToolIsMocked: strings.Contains(string(b.Content), `"_mocked":true`),
			})
		}
	}
	return blocks
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
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")

	version, agent, err := h.agents.GetWithAgent(r.Context(), versionID)
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

	// Session row may not exist yet (lazy-created on first turn). A missing
	// row is fine — leave title empty. Other errors propagate.
	var sessionTitle string
	if sess, err := h.chats.GetSession(r.Context(), sessionID, versionID); err == nil {
		sessionTitle = sess.Title
	} else if !errors.Is(err, types.ErrNotFound) {
		Error(w, err)
		return
	}

	data := chatViewPageData{
		Active:       "agents",
		VersionID:    versionID,
		AgentID:      agent.ID,
		AgentName:    agent.Name,
		SessionID:    sessionID,
		SessionTitle: sessionTitle,
		Messages:     buildMessageViews(msgs),
		HasBundle:    len(version.Bundle) > 0,
	}
	renderTemplate(w, h.viewTmpl, "layout", data)
}

// UpdateSessionTitle accepts a JSON body {"title": "..."} and updates the
// session title. Empty string clears the title (reverts to "Untitled session").
func (h *ChatHandler) UpdateSessionTitle(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(body.Title)
	const maxTitleLen = 200
	if len(title) > maxTitleLen {
		title = title[:maxTitleLen]
	}
	if err := h.chats.UpdateSessionTitle(r.Context(), sessionID, versionID, title); err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"title": title})
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
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")

	var req TurnRequest
	if err := DecodeJSON(r, &req); err != nil {
		h.logger.Error("chat turn: decode JSON request body failed",
			slog.String("version_id", versionID),
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
			slog.String("version_id", versionID),
			slog.String("session_id", sessionID),
			slog.Any("err", err),
		)
		Error(w, err)
		return
	}

	// Status code emitted explicitly (200 OK) so the response body starts
	// streaming. Defer-closing the sink is owned here, NOT by the orchestrator.
	w.WriteHeader(http.StatusOK)

	// orch.Turn's first scope-id param is still named `agentID` (orchestrator
	// legacy — see Task 6 notes). It expects a version row's ID.
	if err := h.orch.Turn(r.Context(), versionID, sessionID, input, sink); err != nil {
		h.logger.Error("chat turn failed",
			slog.String("version_id", versionID),
			slog.String("session_id", sessionID),
			slog.Any("err", err),
		)
		// Orchestrator already emitted ErrorEvent. Nothing more to do here.
	}
}
