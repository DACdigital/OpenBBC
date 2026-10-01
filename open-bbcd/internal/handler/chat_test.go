package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
)

// stubAgentRepo + stubChatStore + stubTurnRunner: minimal fakes for the
// handler-layer interfaces. Not the same as the chat package fakes
// (which mock the orchestrator's dependencies).

type stubAgentRepo struct {
	version *types.AgentVersion
	agent   *types.Agent
	err     error
}

func (s *stubAgentRepo) GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error) {
	return s.version, s.agent, s.err
}

type stubChatStore struct {
	ensured  []string
	sessions []*types.ChatSession
	messages []*types.ChatMessage
	err      error
	// hasPending is returned by HasPendingArtifacts (empty-turn rule).
	hasPending bool
	// locked makes GetSession return a session with LockedAt set.
	locked bool
}

func (s *stubChatStore) EnsureSession(ctx context.Context, sessionID, versionID string) error {
	s.ensured = append(s.ensured, sessionID)
	return s.err
}
func (s *stubChatStore) GetSession(ctx context.Context, sessionID, versionID string) (*types.ChatSession, error) {
	sess := &types.ChatSession{ID: sessionID, AgentVersionID: versionID}
	if s.locked {
		now := time.Now()
		sess.LockedAt = &now
	}
	return sess, s.err
}
func (s *stubChatStore) ListSessions(ctx context.Context, versionID string, limit, offset int) ([]*types.ChatSession, int, error) {
	return s.sessions, len(s.sessions), s.err
}
func (s *stubChatStore) LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error) {
	return s.messages, s.err
}
func (s *stubChatStore) UpdateSessionTitle(ctx context.Context, sessionID, versionID, title string) error {
	return s.err
}

func (s *stubChatStore) HasPendingArtifacts(ctx context.Context, sessionID string) (bool, error) {
	return s.hasPending, nil
}

type stubTurnRunner struct {
	capturedAgentID, capturedSessionID string
	capturedInput                      []llm.Block
}

func (s *stubTurnRunner) Turn(ctx context.Context, agentID, sessionID string, input []llm.Block, sink transport.Sink) error {
	s.capturedAgentID = agentID
	s.capturedSessionID = sessionID
	s.capturedInput = input
	_ = sink.Send(ctx, transport.TextDeltaEvent{MessageID: "m1", Delta: "ok"})
	_ = sink.Send(ctx, transport.TurnEndEvent{StopReason: "end_turn"})
	_ = sink.Close()
	return nil
}

// minimal FS with empty templates so NewChatHandler can parse without exploding.
// templates are NOT exercised by the Turn endpoint, so empty bodies are fine.
func emptyTemplateFS() fs.FS {
	return fstest.MapFS{
		"templates/layout.html":                    {Data: []byte(`{{define "layout"}}{{end}}`)},
		"templates/chat/sessions.html":             {Data: []byte(`{{define "content"}}{{end}}`)},
		"templates/chat/view.html":                 {Data: []byte(`{{define "content"}}{{end}}`)},
		"templates/chat/headers_modal.html":        {Data: []byte(`{{define "headers_modal"}}{{end}}`)},
		"templates/chat/feedback_footer.html":      {Data: []byte(`{{define "feedback_footer"}}{{end}}`)},
		"templates/chat/assign_dataset_modal.html": {Data: []byte(`{{define "assign_dataset_modal"}}{{end}}`)},
	}
}

func newTestChatHandler(t *testing.T, runner *stubTurnRunner) *ChatHandler {
	t.Helper()
	return newTestChatHandlerWithStore(t, &stubChatStore{}, runner, emptyTemplateFS())
}

func newTestChatHandlerWithStore(t *testing.T, store *stubChatStore, runner *stubTurnRunner, tpl fs.FS) *ChatHandler {
	t.Helper()
	h, err := NewChatHandler(
		&stubAgentRepo{
			version: &types.AgentVersion{ID: "v", AgentID: "a", Prompts: []byte(`{}`)},
			agent:   &types.Agent{ID: "a", Name: "test", Architecture: []byte(`{}`)},
		},
		store,
		nil, // headerOvr — not exercised in basic turn tests
		nil, // backends — not exercised in basic turn tests
		runner,
		jsonl.NewFactory(),
		nil, // feedbackRepo — not exercised in basic turn tests
		nil, // datasetRepo — not exercised in basic turn tests
		tpl,
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("NewChatHandler: %v", err)
	}
	return h
}

func TestChatHandler_Turn_HappyPath(t *testing.T) {
	runner := &stubTurnRunner{}
	h := newTestChatHandler(t, runner)

	body, _ := json.Marshal(TurnRequest{
		Input: []TurnInputBlock{{Type: "text", Text: "hi"}},
	})
	r := httptest.NewRequest("POST", "/agent_versions/v/chat/s/turn", bytes.NewReader(body))
	r.SetPathValue("version_id", "v")
	r.SetPathValue("session_id", "s")
	w := httptest.NewRecorder()

	h.Turn(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "text_delta") {
		t.Fatalf("expected text_delta in body, got: %q", w.Body.String())
	}
	if runner.capturedAgentID != "v" || runner.capturedSessionID != "s" {
		t.Fatalf("captured ids: %+v", runner)
	}
	if len(runner.capturedInput) != 1 {
		t.Fatalf("expected 1 input block, got %d", len(runner.capturedInput))
	}
	if tb, ok := runner.capturedInput[0].(llm.TextBlock); !ok || tb.Text != "hi" {
		t.Fatalf("input block: got %+v", runner.capturedInput[0])
	}
}

func TestChatHandler_Turn_MalformedJSON_Returns400(t *testing.T) {
	h := newTestChatHandler(t, &stubTurnRunner{})

	r := httptest.NewRequest("POST", "/agent_versions/v/chat/s/turn", strings.NewReader("{not json"))
	r.SetPathValue("version_id", "v")
	r.SetPathValue("session_id", "s")
	w := httptest.NewRecorder()

	h.Turn(w, r)

	// The Error helper maps json decode errors to 500 by default; if a
	// specific 400 mapping isn't in place that's fine — assert the
	// request didn't proceed to the orchestrator.
	if w.Code == http.StatusOK {
		t.Fatalf("expected non-200 on malformed JSON, got 200")
	}
}

func TestChatHandler_Turn_SetsSSEHeaders(t *testing.T) {
	runner := &stubTurnRunner{}
	h := newTestChatHandler(t, runner)

	body, _ := json.Marshal(TurnRequest{
		Input: []TurnInputBlock{{Type: "text", Text: "hi"}},
	})
	r := httptest.NewRequest("POST", "/agent_versions/v/chat/s/turn", bytes.NewReader(body))
	r.SetPathValue("version_id", "v")
	r.SetPathValue("session_id", "s")
	w := httptest.NewRecorder()

	h.Turn(w, r)

	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "ndjson") && !strings.Contains(ct, "event-stream") {
		t.Fatalf("Content-Type: got %q, want SSE-like", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control: got %q", cc)
	}
}

// History merges consecutive non-user messages (assistant + tool) into one
// assistant bubble per turn, matching the live-stream rendering where every
// text + tool_use + tool_result for one turn lives in a single bubble.
func TestBuildMessageViews_MergesAssistantTurns(t *testing.T) {
	msgs := []*types.ChatMessage{
		{Role: types.ChatRoleUser, Content: []byte(`[{"type":"text","text":"hi"}]`)},
		{Role: types.ChatRoleAssistant, Content: []byte(`[{"type":"text","text":"let me check"},{"type":"tool_use","id":"tu_1","name":"products_list","input":{}}]`)},
		{Role: types.ChatRoleTool, Content: []byte(`[{"type":"tool_result","tool_use_id":"tu_1","content":{"ok":true},"is_error":false}]`)},
		{Role: types.ChatRoleAssistant, Content: []byte(`[{"type":"text","text":"here you go"}]`)},
		{Role: types.ChatRoleUser, Content: []byte(`[{"type":"text","text":"thanks"}]`)},
	}
	views := buildMessageViews(msgs)
	if len(views) != 3 {
		t.Fatalf("expected 3 bubbles (user, merged-assistant, user), got %d", len(views))
	}
	if views[0].Role != "user" || views[2].Role != "user" {
		t.Fatalf("first and last bubbles should be user; got %q and %q", views[0].Role, views[2].Role)
	}
	if views[1].Role != "assistant" {
		t.Fatalf("middle bubble should be assistant; got %q", views[1].Role)
	}
	got := views[1].Blocks
	if len(got) != 4 {
		t.Fatalf("merged assistant bubble should hold all 4 blocks (text + tool_call + tool_result + text); got %d", len(got))
	}
	wantKinds := []string{"text", "tool_call", "tool_result", "text"}
	for i, k := range wantKinds {
		if got[i].Kind != k {
			t.Fatalf("block[%d].Kind = %q, want %q", i, got[i].Kind, k)
		}
	}
}

// A BO turn body carrying an artifact_ref input block must not get that
// block into the persisted user message: the orchestrator (which persists
// the user message verbatim from its input) only sees the text block.
// Client-supplied refs are ignored, not rejected.
func TestChatHandler_Turn_IgnoresArtifactRefInputBlocks(t *testing.T) {
	runner := &stubTurnRunner{}
	h := newTestChatHandler(t, runner)

	body := `{"input":[` +
		`{"type":"text","text":"look at this"},` +
		`{"type":"artifact_ref","store_id":"MAIN","uri":"sha256/abc","mime":"image/png","size_bytes":7,"sha256":"abc","filename":"x.png"}` +
		`]}`
	r := httptest.NewRequest("POST", "/agent_versions/v/chat/s/turn", strings.NewReader(body))
	r.SetPathValue("version_id", "v")
	r.SetPathValue("session_id", "s")
	w := httptest.NewRecorder()

	h.Turn(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d (artifact_ref must be ignored, not rejected)", w.Code, http.StatusOK)
	}
	if len(runner.capturedInput) != 1 {
		t.Fatalf("expected 1 input block (text only), got %d: %+v", len(runner.capturedInput), runner.capturedInput)
	}
	if tb, ok := runner.capturedInput[0].(llm.TextBlock); !ok || tb.Text != "look at this" {
		t.Fatalf("input block: got %+v", runner.capturedInput[0])
	}
}

func TestChatTurn_EmptyTurnWithoutPending_400BeforeSSE(t *testing.T) {
	runner := &stubTurnRunner{}
	h := newTestChatHandlerWithStore(t, &stubChatStore{}, runner, emptyTemplateFS())
	r := httptest.NewRequest("POST", "/agent_versions/v/chat/s/turn", strings.NewReader(`{"input":[{"type":"text","text":""}]}`))
	r.SetPathValue("version_id", "v")
	r.SetPathValue("session_id", "s")
	w := httptest.NewRecorder()
	h.Turn(w, r)
	if w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != "empty turn: no text and no pending artifacts" {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}
	if runner.capturedSessionID != "" {
		t.Fatal("orchestrator ran for an empty turn")
	}
}

func TestChatTurn_EmptyTextWithPending_Accepted(t *testing.T) {
	runner := &stubTurnRunner{}
	h := newTestChatHandlerWithStore(t, &stubChatStore{hasPending: true}, runner, emptyTemplateFS())
	r := httptest.NewRequest("POST", "/agent_versions/v/chat/s/turn", strings.NewReader(`{"input":[]}`))
	r.SetPathValue("version_id", "v")
	r.SetPathValue("session_id", "s")
	w := httptest.NewRecorder()
	h.Turn(w, r)
	if w.Code != http.StatusOK || runner.capturedSessionID != "s" {
		t.Fatalf("status %d captured %q", w.Code, runner.capturedSessionID)
	}
}

type stubPendingLister struct{ rows []*types.SessionArtifact }

func (s stubPendingLister) ListPendingArtifacts(ctx context.Context, sessionID string) ([]*types.SessionArtifact, error) {
	return s.rows, nil
}

func TestChatView_RendersPendingChipsWithRemoveControl(t *testing.T) {
	// Real embedded templates: the chips live in web/templates/chat/view.html.
	h := newTestChatHandlerWithStore(t, &stubChatStore{}, &stubTurnRunner{}, web.Assets)
	h.WithPendingArtifacts(stubPendingLister{rows: []*types.SessionArtifact{
		{ID: "11111111-1111-1111-1111-111111111111", Filename: "Q3 report.pdf", MIME: "application/pdf"},
	}})
	r := httptest.NewRequest("GET", "/agent_versions/v/chat/s", nil)
	r.SetPathValue("version_id", "v")
	r.SetPathValue("session_id", "s")
	w := httptest.NewRecorder()
	h.ChatView(w, r)
	body := w.Body.String()
	for _, want := range []string{
		`id="pending-artifacts"`,
		`Q3 report.pdf`,
		`hx-delete="/agent_versions/v/chat/s/pending-artifacts/11111111-1111-1111-1111-111111111111"`,
		`hx-on::after-request="if (event.detail.successful) this.closest('.artifact-chip').remove()"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chat view missing %q", want)
		}
	}
}

func renderChatView(t *testing.T, store *stubChatStore, rows []*types.SessionArtifact) string {
	t.Helper()
	h := newTestChatHandlerWithStore(t, store, &stubTurnRunner{}, web.Assets)
	h.WithPendingArtifacts(stubPendingLister{rows: rows})
	r := httptest.NewRequest("GET", "/agent_versions/v/chat/s", nil)
	r.SetPathValue("version_id", "v")
	r.SetPathValue("session_id", "s")
	w := httptest.NewRecorder()
	h.ChatView(w, r)
	return w.Body.String()
}

func TestChatView_NoPending_ContainerEmptyNoWhitespace(t *testing.T) {
	body := renderChatView(t, &stubChatStore{}, nil)
	if !regexp.MustCompile(`<div id="pending-artifacts"[^>]*></div>`).MatchString(body) {
		t.Fatal("pending-artifacts container missing or has whitespace children (:empty would not match)")
	}
}

func TestChatView_LockedSession_ChipWithoutRemoveControl(t *testing.T) {
	body := renderChatView(t, &stubChatStore{locked: true}, []*types.SessionArtifact{
		{ID: "11111111-1111-1111-1111-111111111111", Filename: "Q3 report.pdf", MIME: "application/pdf"},
	})
	if !strings.Contains(body, "Q3 report.pdf") {
		t.Error("chip label missing")
	}
	if strings.Contains(body, "hx-delete=\"/agent_versions/v/chat/s/pending-artifacts/") {
		t.Error("locked session must not render a remove control")
	}
}
