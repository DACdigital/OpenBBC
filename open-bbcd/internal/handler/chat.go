package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/chat"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
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
	// IsChildSession is true when sessionID names a sub-agent child session.
	IsChildSession(ctx context.Context, sessionID string) (bool, error)
	// HasPendingArtifacts is true when the session has >=1 pending artifact (empty-turn rule).
	HasPendingArtifacts(ctx context.Context, sessionID string) (bool, error)
	// GetDescendant returns childID iff it strictly descends from the root
	// session rootID; ErrNotFound otherwise (child transcript route).
	GetDescendant(ctx context.Context, rootID, childID string) (*types.ChatSession, error)
	// ChildByParentToolCall returns the child spawned by the raw agent
	// tool_use id toolCallID in session parentID; ErrNotFound when none.
	ChildByParentToolCall(ctx context.Context, parentID, toolCallID string) (string, error)
}

// versionNumReader is optionally implemented by the ChatAgentReader
// (*repository.AgentVersionRepository does) to label a child transcript's
// pinned version with its ordinal.
type versionNumReader interface {
	GetVersionNum(ctx context.Context, versionID string) (int, error)
}

// PendingArtifactLister lists a session's pending artifacts for the BO chat
// view's chips. Set only when the artifact registry is enabled.
type PendingArtifactLister interface {
	ListPendingArtifacts(ctx context.Context, sessionID string) ([]*types.SessionArtifact, error)
}

// pendingChipView is one pending-artifact chip in the chat view.
type pendingChipView struct {
	ID    string
	Label string
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
	Turn(ctx context.Context, agentID, sessionID string, input []llm.Block, sink transport.Sink, opts chat.TurnOpts) (string, error)
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
	feedbackRepo *repository.FeedbackRepository
	datasetRepo  *repository.DatasetRepository
	logger       *slog.Logger
	sessionsTmpl *template.Template
	viewTmpl     *template.Template
	childTmpl    *template.Template
	headersTmpl  *template.Template
	pending      PendingArtifactLister
}

// WithPendingArtifacts enables the pending-artifact chips in the chat view.
func (h *ChatHandler) WithPendingArtifacts(l PendingArtifactLister) *ChatHandler {
	h.pending = l
	return h
}

func NewChatHandler(
	agents ChatAgentReader,
	chats ChatSessionStore,
	headerOvr HeaderOverridesStore,
	backends VersionBackendLister,
	orch TurnRunner,
	tf transport.Factory,
	feedbackRepo *repository.FeedbackRepository,
	datasetRepo *repository.DatasetRepository,
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
	viewTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/chat/view.html",
		"templates/chat/bubble.html",
		"templates/chat/feedback_footer.html",
		"templates/chat/assign_dataset_modal.html",
	)
	if err != nil {
		return nil, err
	}
	childTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/chat/child.html",
		"templates/chat/bubble.html",
		"templates/chat/feedback_footer.html",
	)
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
		orch: orch, transport: tf, feedbackRepo: feedbackRepo, datasetRepo: datasetRepo,
		logger: logger, sessionsTmpl: sessionsTmpl, viewTmpl: viewTmpl, childTmpl: childTmpl, headersTmpl: headersTmpl,
	}, nil
}

// rejectChild writes 404 and returns true when sessionID is malformed or
// names a sub-agent child session (spec § root-only rule: a child id is
// indistinguishable from an unknown id). Per-session BO handlers call it
// first, before any read or write, because several of them treat
// GetSession's ErrNotFound as "not created yet" and would otherwise adopt
// or write to the child.
func (h *ChatHandler) rejectChild(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	if !validUUID(sessionID) {
		Error(w, types.ErrNotFound)
		return true
	}
	child, err := h.chats.IsChildSession(r.Context(), sessionID)
	if err != nil {
		Error(w, err)
		return true
	}
	if child {
		Error(w, types.ErrNotFound)
		return true
	}
	return false
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
//   - "version"          → back to the agent-detail Architecture tab.
//   - "chat:<sessionID>" → back to that chat session.
//   - anything else (or empty) → back to the agent's version listing (default).
func resolveSessionListBack(from, versionID, agentID, agentName string) (string, string) {
	switch {
	case from == "version":
		return "/agents/" + agentID + "/configure/architecture/flows", "← Configurator"
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
	// Feedback maps message UUID → ChatMessageFeedback for rendering footers.
	Feedback map[string]*types.ChatMessageFeedback
	// Locked is true when the session belongs to a closed dataset version.
	Locked bool
	// HasFeedback is true when at least one feedback row exists for the session.
	HasFeedback bool
	// Assignment is the session's current dataset membership, or nil if unassigned.
	Assignment *repository.AssignmentView

	// ArtifactsEnabled / PendingArtifacts drive the pending-artifact chips.
	ArtifactsEnabled bool
	PendingArtifacts []pendingChipView
}

// messageView is a UI-ready projection of a persisted ChatMessage. The raw
// content (JSONB array of Anthropic-shape blocks) is unpacked into typed
// blockView entries so the template can render text inline, tool calls and
// tool results as collapsible <details>, matching the live-stream UI.
type messageView struct {
	ID     string // chat_messages.id (UUID) — used for feedback footer anchoring
	Role   string
	Blocks []blockView
}

type blockView struct {
	Kind         string // "text" | "tool_call" | "tool_result" | "artifact_ref" | "subagent"
	Text         string
	ToolName     string
	ToolArgs     string
	ToolResult   string
	ToolIsError  bool
	ToolIsMocked bool

	// artifact_ref: retrieval path parts and display label.
	ArtifactStoreID string
	ArtifactURI     string
	ArtifactLabel   string
	ArtifactHref    string // empty: not safely linkable, render as plain label

	// subagent: an `agent` tool_use paired with its tool_result.
	SubagentName        string
	SubagentDescription string
	SubagentPrompt      string
	SubagentState       string // "done" | "error" | "interrupted"
	SubagentResult      string
	SubagentHref        string // child transcript; empty when no child row
}

// childLinker returns the child-transcript href for the child spawned by
// the raw agent tool_use id in the session being rendered, or "" when there
// is none. nil renders every card without a link.
type childLinker func(toolCallID string) string

// buildMessageViews turns persisted ChatMessage rows into UI bubbles. Each
// user message is its own bubble; every non-user message (assistant text +
// tool_use, plus the tool-role wrappers carrying tool_result) is merged into
// a single assistant bubble per turn, so the history matches the in-stream
// rendering (one bubble per assistant turn, regardless of how many DB rows
// the orchestrator split it across).
//
// An `agent` tool_use in an assistant row renders as a sub-agent card paired
// with the tool_result carrying the same tool_use_id in the immediately
// following message (spec § AG-UI stream → BO history view): done/error from
// is_error, or "interrupted" when there is none. The paired tool_result is
// not rendered separately. link resolves the card's child transcript.
func buildMessageViews(msgs []*types.ChatMessage, artifactBase string, link childLinker) []messageView {
	raws := make([][]json.RawMessage, len(msgs))
	ok := make([]bool, len(msgs))
	for i, m := range msgs {
		ok[i] = json.Unmarshal(m.Content, &raws[i]) == nil
	}

	out := make([]messageView, 0, len(msgs))
	var pending *messageView // open assistant bubble waiting for more blocks
	flush := func() {
		if pending != nil {
			out = append(out, *pending)
			pending = nil
		}
	}
	consumed := map[string]bool{} // tool_use ids whose result is paired into a card, for row i
	for i, m := range msgs {
		if !ok[i] {
			consumed = map[string]bool{}
			continue
		}
		var next []json.RawMessage
		if i+1 < len(msgs) && ok[i+1] {
			next = raws[i+1]
		}
		blocks := make([]blockView, 0, len(raws[i]))
		nextConsumed := map[string]bool{}
		for _, r := range raws[i] {
			if m.Role == types.ChatRoleAssistant {
				if card, id, isAgent := subagentCard(r, next, link); isAgent {
					blocks = append(blocks, card)
					if card.SubagentState != "interrupted" {
						nextConsumed[id] = true
					}
					continue
				}
			}
			if id, isResult := toolResultID(r); isResult && consumed[id] {
				continue
			}
			if b, ok := decodeBlock(r, artifactBase); ok {
				blocks = append(blocks, b)
			}
		}
		consumed = nextConsumed
		if m.Role == types.ChatRoleUser {
			flush()
			out = append(out, messageView{ID: m.ID, Role: string(types.ChatRoleUser), Blocks: blocks})
			continue
		}
		// Assistant or tool — both go into the current assistant bubble.
		if pending == nil {
			// Use the first assistant/tool message ID as the bubble's canonical ID
			// so feedback rows keyed by this ID can be looked up in the view.
			pending = &messageView{ID: m.ID, Role: string(types.ChatRoleAssistant)}
		}
		pending.Blocks = append(pending.Blocks, blocks...)
	}
	flush()
	return out
}

// subagentCard builds the card for r when it is an `agent` tool_use,
// pairing it with the matching tool_result in next. Returns the raw
// tool_use id and isAgent=false for any other block.
func subagentCard(r json.RawMessage, next []json.RawMessage, link childLinker) (blockView, string, bool) {
	var tu struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(r, &tu); err != nil || tu.Type != "tool_use" || tu.Name != tools.AgentToolName {
		return blockView{}, "", false
	}
	var in struct {
		Subagent    string `json:"subagent"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	_ = json.Unmarshal(tu.Input, &in)
	card := blockView{
		Kind:                "subagent",
		SubagentName:        in.Subagent,
		SubagentDescription: in.Description,
		SubagentPrompt:      in.Prompt,
		SubagentState:       "interrupted",
	}
	if tu.ID != "" {
		for _, nr := range next {
			var res struct {
				Type      string          `json:"type"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
			}
			if json.Unmarshal(nr, &res) != nil || res.Type != "tool_result" || res.ToolUseID != tu.ID {
				continue
			}
			card.SubagentState = "done"
			if res.IsError {
				card.SubagentState = "error"
			}
			var text string
			if json.Unmarshal(res.Content, &text) == nil {
				card.SubagentResult = text
			} else {
				card.SubagentResult = prettyJSON(res.Content)
			}
			break
		}
		if link != nil {
			card.SubagentHref = link(tu.ID)
		}
	}
	return card, tu.ID, true
}

// toolResultID returns the tool_use_id of a tool_result block.
func toolResultID(r json.RawMessage) (string, bool) {
	var b struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
	}
	if json.Unmarshal(r, &b) != nil || b.Type != "tool_result" {
		return "", false
	}
	return b.ToolUseID, true
}

// artifactHref builds the BO retrieval URL for a persisted ref, escaping each
// path segment. It returns "" (render without a link) when the store id or
// any uri segment could retarget the link: '/' in the store id, or an empty,
// "." or ".." uri segment — and always for an empty base.
func artifactHref(base, storeID, uri string) string {
	if base == "" { // no retrieval route (child transcripts): label only
		return ""
	}
	if strings.Contains(storeID, "/") || storeID == "." || storeID == ".." {
		return ""
	}
	segs := strings.Split(uri, "/")
	for i, sg := range segs {
		if sg == "" || sg == "." || sg == ".." {
			return ""
		}
		segs[i] = url.PathEscape(sg)
	}
	return base + url.PathEscape(storeID) + "/" + strings.Join(segs, "/")
}

// decodeBlock projects one persisted content block; ok=false skips it.
func decodeBlock(r json.RawMessage, artifactBase string) (blockView, bool) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(r, &head); err != nil {
		return blockView{}, false
	}
	switch head.Type {
	case "text":
		var b struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(r, &b)
		return blockView{Kind: "text", Text: b.Text}, true
	case "tool_use":
		var b struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(r, &b)
		return blockView{
			Kind:     "tool_call",
			ToolName: b.Name,
			ToolArgs: prettyJSON(b.Input),
		}, true
	case "tool_result":
		var b struct {
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"is_error"`
		}
		_ = json.Unmarshal(r, &b)
		return blockView{
			Kind:         "tool_result",
			ToolResult:   prettyJSON(b.Content),
			ToolIsError:  b.IsError,
			ToolIsMocked: strings.Contains(string(b.Content), `"_mocked":true`),
		}, true
	case "artifact_ref":
		var b types.ArtifactRefContent
		_ = json.Unmarshal(r, &b)
		if b.StoreID == "" || b.URI == "" {
			return blockView{}, false
		}
		name := b.Filename
		if name == "" {
			name = "file"
		}
		mime := b.MIME
		if mime == "" {
			mime = "unknown"
		}
		return blockView{
			Kind:            "artifact_ref",
			ArtifactHref:    artifactHref(artifactBase, b.StoreID, b.URI),
			ArtifactStoreID: b.StoreID,
			ArtifactURI:     b.URI,
			ArtifactLabel:   name + " (" + mime + ", " + llm.HumanBytes(b.SizeBytes) + ")",
		}, true
	}
	return blockView{}, false
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

	if h.rejectChild(w, r, sessionID) {
		return
	}

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
	// row is fine — leave title empty and locked=false. Other errors propagate.
	var sessionTitle string
	var locked bool
	if sess, err := h.chats.GetSession(r.Context(), sessionID, versionID); err == nil {
		sessionTitle = sess.Title
		locked = sess.LockedAt != nil
	} else if !errors.Is(err, types.ErrNotFound) {
		Error(w, err)
		return
	}

	// Load per-message feedback for the session so the view can render footers.
	var feedback map[string]*types.ChatMessageFeedback
	if h.feedbackRepo != nil {
		if fb, err := h.feedbackRepo.GetForSession(r.Context(), sessionID); err == nil {
			feedback = fb
		}
		// Best-effort: errors here don't block the chat page from rendering.
	}

	hasFeedback := len(feedback) > 0
	var assignment *repository.AssignmentView
	if h.datasetRepo != nil {
		if a, err := h.datasetRepo.GetSessionAssignment(r.Context(), sessionID); err != nil {
			Error(w, err)
			return
		} else {
			assignment = a
		}
	}

	var chips []pendingChipView
	if h.pending != nil {
		// Best-effort, like feedback: the chat page renders without chips on error.
		if rows, err := h.pending.ListPendingArtifacts(r.Context(), sessionID); err == nil {
			for _, a := range rows {
				label := a.Filename
				if label == "" {
					label = "file (" + a.MIME + ")"
				}
				chips = append(chips, pendingChipView{ID: a.ID, Label: label})
			}
		} else {
			h.logger.Warn("chat view: list pending artifacts failed", slog.String("session_id", sessionID), slog.Any("err", err))
		}
	}

	data := chatViewPageData{
		Active:       "agents",
		VersionID:    versionID,
		AgentID:      agent.ID,
		AgentName:    agent.Name,
		SessionID:    sessionID,
		SessionTitle: sessionTitle,
		Messages:     buildMessageViews(msgs, "/agent_versions/"+url.PathEscape(versionID)+"/chat/"+url.PathEscape(sessionID)+"/artifacts/", h.childLinker(r.Context(), versionID, sessionID, sessionID)),
		HasBundle:    len(agent.Architecture) > 0 && len(version.Prompts) > 0,
		Feedback:     feedback,
		Locked:       locked,
		HasFeedback:  hasFeedback,
		Assignment:   assignment,
	}
	data.ArtifactsEnabled = h.pending != nil
	data.PendingArtifacts = chips
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

// childLinker resolves sub-agent cards rendered for session parentID to
// child-transcript hrefs. The path always goes through the root (rootID of
// root version versionID), whatever the card's depth. Lookup failures other
// than "no child" are logged and render the card without a link.
func (h *ChatHandler) childLinker(ctx context.Context, versionID, rootID, parentID string) childLinker {
	base := "/agent_versions/" + url.PathEscape(versionID) + "/chat/" + url.PathEscape(rootID) + "/children/"
	return func(toolCallID string) string {
		childID, err := h.chats.ChildByParentToolCall(ctx, parentID, toolCallID)
		if err != nil {
			if !errors.Is(err, types.ErrNotFound) {
				h.logger.Warn("chat history: resolve sub-agent child failed",
					slog.String("session_id", parentID), slog.String("tool_call_id", toolCallID), slog.Any("err", err))
			}
			return ""
		}
		return base + url.PathEscape(childID)
	}
}

// childTranscriptPageData feeds chat/child.html.
type childTranscriptPageData struct {
	Active       string
	VersionID    string // the ROOT's version (URL path)
	SessionID    string // the root session
	ChildID      string
	RootTitle    string
	AgentLabel   string // the child's pinned agent/version
	ChildVersion string // the child's pinned version id
	Depth        int
	Messages     []messageView
}

// ChildTranscript handles GET
// /agent_versions/{version_id}/chat/{session_id}/children/{child_id}: a
// read-only transcript of a sub-agent child session, scoped through its
// root (spec § REST — child transcripts). 404 unless session_id is a root of
// version_id and child_id descends from it. Child artifact_ref blocks render
// as labels (no retrieval route serves a child id); nested cards link via
// the root.
func (h *ChatHandler) ChildTranscript(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	rootID := r.PathValue("session_id")
	childID := r.PathValue("child_id")
	if !validUUID(versionID) || !validUUID(rootID) || !validUUID(childID) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	notFoundOr := func(err error) {
		if errors.Is(err, types.ErrNotFound) || errors.Is(err, types.ErrSessionAgentMismatch) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
	}
	root, err := h.chats.GetSession(ctx, rootID, versionID)
	if err != nil {
		notFoundOr(err)
		return
	}
	child, err := h.chats.GetDescendant(ctx, rootID, childID)
	if err != nil {
		notFoundOr(err)
		return
	}
	_, childAgent, err := h.agents.GetWithAgent(ctx, child.AgentVersionID)
	if err != nil {
		notFoundOr(err)
		return
	}
	label := childAgent.Name
	if vn, ok := h.agents.(versionNumReader); ok {
		if n, err := vn.GetVersionNum(ctx, child.AgentVersionID); err == nil {
			label = fmt.Sprintf("%s · v%d", childAgent.Name, n)
		}
	}
	msgs, err := h.chats.LoadMessages(ctx, childID)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.childTmpl, "layout", childTranscriptPageData{
		Active:       "agents",
		VersionID:    versionID,
		SessionID:    rootID,
		ChildID:      childID,
		RootTitle:    root.Title,
		AgentLabel:   label,
		ChildVersion: child.AgentVersionID,
		Depth:        child.Depth,
		Messages:     buildMessageViews(msgs, "", h.childLinker(ctx, versionID, rootID, childID)),
	})
}

// UpdateSessionTitle accepts a JSON body {"title": "..."} and updates the
// session title. Empty string clears the title (reverts to "Untitled session").
func (h *ChatHandler) UpdateSessionTitle(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	sessionID := r.PathValue("session_id")
	if h.rejectChild(w, r, sessionID) {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	// Refuse writes on locked sessions before touching the DB.
	if session, err := h.chats.GetSession(r.Context(), sessionID, versionID); err == nil {
		if session.LockedAt != nil {
			Error(w, types.ErrSessionLocked)
			return
		}
	} else if !errors.Is(err, types.ErrNotFound) {
		Error(w, err)
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

// TurnInputBlock is the wire shape for one content block in the turn
// input body. Discriminated on Type. Only "text" is read (Text field).
//
// Every other Type — including "artifact_ref" — is silently dropped at
// the switch below. Client-supplied artifact_ref blocks are ignored, not
// rejected: persisting them would let a client mint refs that the
// retrieval route then authorises (it trusts refs found in the session's
// history). Older clients that still send them keep working.
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

	// Before decoding the body: a child id opens no stream and writes nothing.
	if h.rejectChild(w, r, sessionID) {
		return
	}

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

	// Build typed input blocks. Only "text" is recognised; every other
	// type (including client-supplied "artifact_ref") is silently ignored
	// so older clients and clients running ahead of the server degrade
	// gracefully.
	input := make([]llm.Block, 0, len(req.Input))
	for _, b := range req.Input {
		if b.Type == "text" && b.Text != "" {
			input = append(input, llm.TextBlock{Text: b.Text})
		}
	}

	// Empty-turn rule (spec § REST — turn): no non-empty text and nothing
	// pending -> 400 before the stream opens. A DELETE racing this check is
	// caught by AppendUserTurn (in-band RUN_ERROR empty_turn).
	if len(input) == 0 {
		has, err := h.chats.HasPendingArtifacts(r.Context(), sessionID)
		if err != nil {
			Error(w, err)
			return
		}
		if !has {
			http.Error(w, types.ErrEmptyTurn.Error(), http.StatusBadRequest)
			return
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
	// The handler owns the sink; the orchestrator never closes it.
	defer sink.Close()

	// Status code emitted explicitly (200 OK) so the response body starts
	// streaming.
	w.WriteHeader(http.StatusOK)

	// Multi-agent turns routinely outlive the server-wide WriteTimeout;
	// clear the per-connection deadline for this stream only (spec § Scope,
	// SSE write-deadline fix). Non-streaming routes keep the 30s timeout.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		h.logger.Warn("chat turn: clear write deadline failed", slog.Any("err", err))
	}

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
	if _, err := h.orch.Turn(ctx, versionID, sessionID, input, sink, chat.TurnOpts{}); err != nil {
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
	if h.rejectChild(w, r, sessionID) {
		return
	}

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
	if h.rejectChild(w, r, sessionID) {
		return
	}

	// Refuse writes on locked sessions before parsing the form.
	if session, err := h.chats.GetSession(r.Context(), sessionID, versionID); err == nil {
		if session.LockedAt != nil {
			Error(w, types.ErrSessionLocked)
			return
		}
	} else if !errors.Is(err, types.ErrNotFound) {
		Error(w, err)
		return
	}

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

// AssignDatasetModal renders GET /agent_versions/{version_id}/chat/{session_id}/assign-dataset — modal.
// Renders one of three states:
//   - session has no feedback yet → "add feedback first" explainer
//   - datasets exist and session has feedback → dataset picker
//   - session has feedback but no datasets exist → link to /datasets
func (h *ChatHandler) AssignDatasetModal(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	versionID := r.PathValue("version_id")
	if h.rejectChild(w, r, sessionID) {
		return
	}

	hasFeedback := false
	if h.feedbackRepo != nil {
		fbMap, err := h.feedbackRepo.GetForSession(r.Context(), sessionID)
		if err != nil {
			Error(w, err)
			return
		}
		hasFeedback = len(fbMap) > 0
	}

	var datasets []*types.Dataset
	if hasFeedback {
		var err error
		datasets, err = h.datasetRepo.List(r.Context())
		if err != nil {
			Error(w, err)
			return
		}
	}

	renderTemplate(w, h.viewTmpl, "assign_dataset_modal", map[string]any{
		"SessionID":   sessionID,
		"VersionID":   versionID,
		"Datasets":    datasets,
		"HasFeedback": hasFeedback,
	})
}

// AssignDataset handles POST /agent_versions/{version_id}/chat/{session_id}/assign-dataset
func (h *ChatHandler) AssignDataset(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	versionID := r.PathValue("version_id")
	if h.rejectChild(w, r, sessionID) {
		return
	}
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	datasetID := r.FormValue("dataset_id")
	if datasetID == "" {
		http.Error(w, "dataset_id required", http.StatusBadRequest)
		return
	}
	if _, err := h.datasetRepo.AssignSessionToDraft(r.Context(), datasetID, sessionID); err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("HX-Redirect", "/agent_versions/"+versionID+"/chat/"+sessionID)
	w.WriteHeader(http.StatusNoContent)
}

// UnassignDataset handles DELETE /agent_versions/{version_id}/chat/{session_id}/assign-dataset
func (h *ChatHandler) UnassignDataset(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	if h.rejectChild(w, r, sessionID) {
		return
	}
	if err := h.datasetRepo.UnassignSession(r.Context(), sessionID); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
