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
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
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
	ListSessions(ctx context.Context, versionID string, limit, offset int) ([]*types.ChatSession, int, error)
	LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error)
	UpdateSessionTitle(ctx context.Context, sessionID, versionID, title string) error
}

// HeaderOverridesStore is the narrow interface for reading and writing
// per-session backend header overrides. Implemented by *repository.ChatRepository.
type HeaderOverridesStore interface {
	GetSessionHeaderOverrides(ctx context.Context, sessionID string) (map[string]map[string]string, error)
	SetSessionHeaderOverrides(ctx context.Context, sessionID string, ovr map[string]map[string]string) error
}

// VersionBackendLister lists all tool backends wired to a version (HTTP via
// the agent's endpoint_backend mapping resolved through agent_id, MCP via
// the version's own mcp_backend attachment). Used by the header overrides
// modal so the form shows one row per backend.
//
// ListEndpointBackends returns the agent-level endpoint→backend wiring map;
// the chat view uses it to surface a warning when the agent has endpoints
// without a backend assigned (the LLM otherwise has no way to call them).
type VersionBackendLister interface {
	ListBackendsForVersion(ctx context.Context, versionID string) ([]*types.ToolBackend, error)
	ListEndpointBackends(ctx context.Context, agentID string) (map[string]string, error)
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
	headerOvr    HeaderOverridesStore
	backends     VersionBackendLister
	orch         TurnRunner
	transport    transport.Factory
	logger       *slog.Logger
	sessionsTmpl *template.Template
	viewTmpl     *template.Template
	headersTmpl  *template.Template
}

func NewChatHandler(
	agents ChatAgentReader,
	chats ChatSessionStore,
	headerOvr HeaderOverridesStore,
	backends VersionBackendLister,
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
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
		"dict":        tplDict,
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
	headersTmpl, err := template.New("headers_modal").Funcs(funcs).ParseFS(webFS,
		"templates/chat/headers_modal.html",
	)
	if err != nil {
		return nil, err
	}
	return &ChatHandler{
		agents: agents, chats: chats, headerOvr: headerOvr, backends: backends,
		orch: orch, transport: tf, logger: logger,
		sessionsTmpl: sessionsTmpl, viewTmpl: viewTmpl, headersTmpl: headersTmpl,
	}, nil
}

// NewSession creates a new chat_sessions row and 303-redirects to the chat view.
// Returns 409 if the agent isn't finalized yet (no architecture / no prompts).
func (h *ChatHandler) NewSession(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	version, agent, err := h.agents.GetWithAgent(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	if len(agent.Architecture) == 0 || len(version.Prompts) == 0 {
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
	Page      PageView
	BasePath  string // URL path without query string, used by the page template to build prev/next links
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
	pr := ParsePageRequest(r)
	sessions, total, err := h.chats.ListSessions(r.Context(), versionID, pr.Limit(), pr.Offset())
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
		Page:      NewPageView(pr, total),
		BasePath:  r.URL.Path,
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
	// TotalEndpoints / UnmappedEndpoints surface a warning at the top of
	// the chat page when bundle.tools[] entries lack an endpoint→backend
	// row. With no wiring, the LLM has no way to call those endpoints —
	// it will tend to hallucinate a result in prose rather than emit a
	// tool_use block, which is highly confusing during testing.
	TotalEndpoints    int
	UnmappedEndpoints int
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
		HasBundle:    len(agent.Architecture) > 0 && len(version.Prompts) > 0,
	}
	// Count unmapped endpoints so the view can show a warning banner.
	// Best-effort: errors here don't block the chat page from rendering.
	// Endpoints live on the agent (post-017); wiring is also agent-keyed.
	if data.HasBundle && h.backends != nil {
		var snap struct {
			Tools []struct {
				ID string `json:"id"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(agent.Architecture, &snap); err == nil {
			data.TotalEndpoints = len(snap.Tools)
			if mapping, err := h.backends.ListEndpointBackends(r.Context(), agent.ID); err == nil {
				for _, t := range snap.Tools {
					if t.ID == "" {
						continue
					}
					if _, ok := mapping[t.ID]; !ok {
						data.UnmappedEndpoints++
					}
				}
			}
		}
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

	// Refuse turns on locked sessions (session belongs to a closed dataset version).
	// Check before writing SSE headers so we can return a clean JSON error.
	if session, err := h.chats.GetSession(r.Context(), sessionID, versionID); err == nil {
		if session.LockedAt != nil {
			Error(w, types.ErrSessionLocked)
			return
		}
	} else if !errors.Is(err, types.ErrNotFound) && !errors.Is(err, types.ErrSessionAgentMismatch) {
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

	// Build the base context with forwarded FE headers.
	ctx := tools.WithForwardedHeaders(r.Context(), r.Header)

	// Parse the per-backend routing envelope and stash it on ctx. Malformed
	// envelopes are logged and silently dropped — fail safe: no FE headers
	// reach any backend.
	if raw := r.Header.Get(tools.RoutingEnvelopeHeader); raw != "" {
		routing, err := tools.ParseBackendHeaderRouting(raw)
		if err != nil {
			h.logger.Warn("malformed backend header routing envelope; ignoring",
				slog.String("err", err.Error()))
		} else {
			ctx = tools.WithBackendHeaderRouting(ctx, routing)
		}
	}

	// Layer session-scoped backend header overrides on top (BO testing only).
	// Missing session row is silently ignored — overrides are optional.
	if h.headerOvr != nil {
		if ovr, err := h.headerOvr.GetSessionHeaderOverrides(ctx, sessionID); err == nil {
			ctx = tools.WithSessionHeaderOverrides(ctx, tools.SessionHeaderOverrides(ovr))
		}
	}

	// orch.Turn's first scope-id param is still named `agentID` (orchestrator
	// legacy — see Task 6 notes). It expects a version row's ID.
	if err := h.orch.Turn(ctx, versionID, sessionID, input, sink); err != nil {
		h.logger.Error("chat turn failed",
			slog.String("version_id", versionID),
			slog.String("session_id", sessionID),
			slog.Any("err", err),
		)
		// Orchestrator already emitted ErrorEvent. Nothing more to do here.
	}
}

// headerOverrideRow is one backend row in the headers override modal.
type headerOverrideRow struct {
	BackendID   string
	BackendName string
	Kind        string // "http_endpoint" | "mcp_client"
	Entries     []headerEntry
}

type headerEntry struct {
	Key   string
	Value string
}

type headersModalData struct {
	VersionID string
	SessionID string
	Backends  []headerOverrideRow
}

// ShowHeaderOverridesModal renders the per-backend header overrides form as an
// htmx partial. Returns 200 with the modal HTML fragment.
func (h *ChatHandler) ShowHeaderOverridesModal(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")

	data, err := h.buildHeadersModalData(r.Context(), versionID, sessionID)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.headersTmpl, "headers_modal", data)
}

// UpdateHeaderOverrides parses the form submission from the headers modal and
// persists the updated per-backend header overrides for the session.
func (h *ChatHandler) UpdateHeaderOverrides(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	// Form encoding: backend_id[<id>][<key_n>] = key, backend_id[<id>][<val_n>] = value.
	// Simpler approach: pairs of hidden inputs named header_backend[], header_key[], header_val[].
	backendIDs := r.Form["header_backend[]"]
	keys := r.Form["header_key[]"]
	vals := r.Form["header_val[]"]

	ovr := map[string]map[string]string{}
	for i := range backendIDs {
		bid := strings.TrimSpace(backendIDs[i])
		k := strings.TrimSpace(keys[i])
		v := vals[i] // value may legitimately be blank (to clear a header)
		if bid == "" || k == "" {
			continue
		}
		if _, ok := ovr[bid]; !ok {
			ovr[bid] = map[string]string{}
		}
		ovr[bid][k] = v
	}

	if err := h.headerOvr.SetSessionHeaderOverrides(r.Context(), sessionID, ovr); err != nil {
		Error(w, err)
		return
	}

	// Re-render the modal with the saved state so the user sees confirmation.
	data, err := h.buildHeadersModalData(r.Context(), versionID, sessionID)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.headersTmpl, "headers_modal", data)
}

func (h *ChatHandler) buildHeadersModalData(ctx context.Context, versionID, sessionID string) (headersModalData, error) {
	existing, err := h.headerOvr.GetSessionHeaderOverrides(ctx, sessionID)
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		return headersModalData{}, err
	}
	if existing == nil {
		existing = map[string]map[string]string{}
	}

	var rows []headerOverrideRow
	if h.backends != nil {
		bes, err := h.backends.ListBackendsForVersion(ctx, versionID)
		if err != nil {
			return headersModalData{}, err
		}
		for _, be := range bes {
			row := headerOverrideRow{
				BackendID:   be.ID,
				BackendName: be.Name,
				Kind:        string(be.Kind),
			}
			// Populate existing entries for this backend.
			for k, v := range existing[be.ID] {
				row.Entries = append(row.Entries, headerEntry{Key: k, Value: v})
			}
			// Always ensure at least one blank entry for adding new headers.
			row.Entries = append(row.Entries, headerEntry{})
			rows = append(rows, row)
		}
	}

	return headersModalData{
		VersionID: versionID,
		SessionID: sessionID,
		Backends:  rows,
	}, nil
}
