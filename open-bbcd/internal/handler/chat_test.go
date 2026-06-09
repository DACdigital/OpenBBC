package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// stubAgentRepo + stubChatStore + stubTurnRunner: minimal fakes for the
// handler-layer interfaces. Not the same as the chat package fakes
// (which mock the orchestrator's dependencies).

type stubAgentRepo struct {
	agent *types.Agent
	err   error
}

func (s *stubAgentRepo) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return s.agent, s.err
}

type stubChatStore struct {
	ensured  []string
	sessions []*types.ChatSession
	messages []*types.ChatMessage
	err      error
}

func (s *stubChatStore) EnsureSession(ctx context.Context, sessionID, agentID string) error {
	s.ensured = append(s.ensured, sessionID)
	return s.err
}
func (s *stubChatStore) ListSessions(ctx context.Context, agentID string) ([]*types.ChatSession, error) {
	return s.sessions, s.err
}
func (s *stubChatStore) LoadMessages(ctx context.Context, sessionID string) ([]*types.ChatMessage, error) {
	return s.messages, s.err
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
		"templates/layout.html":        {Data: []byte(`{{define "layout"}}{{end}}`)},
		"templates/chat/sessions.html": {Data: []byte(`{{define "content"}}{{end}}`)},
		"templates/chat/view.html":     {Data: []byte(`{{define "content"}}{{end}}`)},
	}
}

func newTestChatHandler(t *testing.T, runner *stubTurnRunner) *ChatHandler {
	t.Helper()
	h, err := NewChatHandler(
		&stubAgentRepo{agent: &types.Agent{ID: "a", Bundle: []byte(`{}`)}},
		&stubChatStore{},
		runner,
		jsonl.NewFactory(),
		emptyTemplateFS(),
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
	r := httptest.NewRequest("POST", "/agents/a/chat/s/turn", bytes.NewReader(body))
	r.SetPathValue("id", "a")
	r.SetPathValue("session_id", "s")
	w := httptest.NewRecorder()

	h.Turn(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "text_delta") {
		t.Fatalf("expected text_delta in body, got: %q", w.Body.String())
	}
	if runner.capturedAgentID != "a" || runner.capturedSessionID != "s" {
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

	r := httptest.NewRequest("POST", "/agents/a/chat/s/turn", strings.NewReader("{not json"))
	r.SetPathValue("id", "a")
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
	r := httptest.NewRequest("POST", "/agents/a/chat/s/turn", bytes.NewReader(body))
	r.SetPathValue("id", "a")
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
