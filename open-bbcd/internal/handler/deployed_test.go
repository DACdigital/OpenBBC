package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// stubDeployedAgentReader returns a fixed currently-deployed version (or "" for none).
type stubDeployedAgentReader struct {
	deployedID string
	err        error
}

func (s *stubDeployedAgentReader) CurrentDeployedVersionID(ctx context.Context, chainRootID string) (string, error) {
	return s.deployedID, s.err
}

// stubDeployedStore is an in-memory DeployedStore.
type stubDeployedStore struct {
	sessions  map[string]*types.DeployedSession // by id
	messages  map[string][]*types.DeployedMessage
	createErr error
}

func newStubDeployedStore() *stubDeployedStore {
	return &stubDeployedStore{
		sessions: map[string]*types.DeployedSession{},
		messages: map[string][]*types.DeployedMessage{},
	}
}
func (s *stubDeployedStore) CreateSession(ctx context.Context, chainRootID, userID, title string) (*types.DeployedSession, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	id := "sess-" + userID + "-" + chainRootID + "-" + title
	sess := &types.DeployedSession{ID: id, ChainRootID: chainRootID, UserID: userID, Title: title, CreatedAt: time.Now()}
	s.sessions[id] = sess
	return sess, nil
}
func (s *stubDeployedStore) GetSession(ctx context.Context, sessionID, userID string) (*types.DeployedSession, error) {
	sess, ok := s.sessions[sessionID]
	if !ok || sess.UserID != userID {
		return nil, types.ErrNotFound
	}
	return sess, nil
}
func (s *stubDeployedStore) ListSessions(ctx context.Context, chainRootID, userID string) ([]*types.DeployedSession, error) {
	var out []*types.DeployedSession
	for _, sess := range s.sessions {
		if sess.ChainRootID == chainRootID && sess.UserID == userID {
			out = append(out, sess)
		}
	}
	return out, nil
}
func (s *stubDeployedStore) UpdateSessionTitle(ctx context.Context, sessionID, userID, title string) error {
	sess, ok := s.sessions[sessionID]
	if !ok || sess.UserID != userID {
		return types.ErrNotFound
	}
	sess.Title = title
	return nil
}
func (s *stubDeployedStore) DeleteSession(ctx context.Context, sessionID, userID string) error {
	sess, ok := s.sessions[sessionID]
	if !ok || sess.UserID != userID {
		return types.ErrNotFound
	}
	_ = sess
	delete(s.sessions, sessionID)
	delete(s.messages, sessionID)
	return nil
}
func (s *stubDeployedStore) LoadMessages(ctx context.Context, sessionID string) ([]*types.DeployedMessage, error) {
	return s.messages[sessionID], nil
}

func newDeployedMux(ar DeployedAgentReader, ds DeployedStore, orch TurnRunner, tf transport.Factory) *http.ServeMux {
	// chat.ChatStore is not used at the handler layer (only constructed-into the orchestrator).
	// We can pass nil here for tests.
	h := NewDeployedHandler(ar, ds, nil, orch, tf, testLogger())
	mux := http.NewServeMux()
	mux.HandleFunc("POST /deployed/{agent_id}/sessions", h.CreateSession)
	mux.HandleFunc("GET /deployed/{agent_id}/sessions", h.ListSessions)
	mux.HandleFunc("GET /deployed/{agent_id}/sessions/{session_id}", h.GetSession)
	mux.HandleFunc("PATCH /deployed/{agent_id}/sessions/{session_id}/title", h.UpdateTitle)
	mux.HandleFunc("DELETE /deployed/{agent_id}/sessions/{session_id}", h.DeleteSession)
	mux.HandleFunc("POST /deployed/{agent_id}/sessions/{session_id}/turn", h.Turn)
	return mux
}

func TestDeployedHandler_CreateSession_RequiresUserID(t *testing.T) {
	mux := newDeployedMux(
		&stubDeployedAgentReader{deployedID: "v1"},
		newStubDeployedStore(),
		&stubTurnRunner{},
		jsonl.NewFactory(),
	)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/deployed/chain-a/sessions", bytes.NewReader([]byte(`{}`))))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestDeployedHandler_NotDeployed_404(t *testing.T) {
	mux := newDeployedMux(
		&stubDeployedAgentReader{deployedID: ""}, // no deployed version
		newStubDeployedStore(),
		&stubTurnRunner{},
		jsonl.NewFactory(),
	)
	body, _ := json.Marshal(map[string]string{"user_id": "u"})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/deployed/chain-x/sessions", bytes.NewReader(body)))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d", rr.Code)
	}
}

func TestDeployedHandler_ListSessions_ScopedByUser(t *testing.T) {
	store := newStubDeployedStore()
	mux := newDeployedMux(&stubDeployedAgentReader{deployedID: "v1"}, store, &stubTurnRunner{}, jsonl.NewFactory())

	for i, u := range []string{"user-A", "user-A", "user-B"} {
		body, _ := json.Marshal(map[string]string{"user_id": u, "title": u + "-t" + string(rune('1'+i))})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("POST", "/deployed/chain-a/sessions", bytes.NewReader(body)))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create: %d body=%s", rr.Code, rr.Body.String())
		}
	}

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/deployed/chain-a/sessions?user_id=user-A", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}
	var got []*types.DeployedSession
	_ = json.NewDecoder(rr.Body).Decode(&got)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestDeployedHandler_GetSession_WrongUser_404(t *testing.T) {
	store := newStubDeployedStore()
	mux := newDeployedMux(&stubDeployedAgentReader{deployedID: "v1"}, store, &stubTurnRunner{}, jsonl.NewFactory())

	body, _ := json.Marshal(map[string]string{"user_id": "user-A"})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/deployed/chain-a/sessions", bytes.NewReader(body)))
	var sess types.DeployedSession
	_ = json.NewDecoder(rr.Body).Decode(&sess)

	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, httptest.NewRequest("GET",
		"/deployed/chain-a/sessions/"+sess.ID+"?user_id=user-B", nil))
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rr2.Code)
	}
}

func TestDeployedHandler_Turn_HappyPath(t *testing.T) {
	store := newStubDeployedStore()
	runner := &stubTurnRunner{}
	mux := newDeployedMux(&stubDeployedAgentReader{deployedID: "v-current"}, store, runner, jsonl.NewFactory())

	body, _ := json.Marshal(map[string]string{"user_id": "user-A"})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/deployed/chain-a/sessions", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d", rr.Code)
	}
	var sess types.DeployedSession
	_ = json.NewDecoder(rr.Body).Decode(&sess)

	body2, _ := json.Marshal(map[string]any{
		"user_id": "user-A",
		"input":   []map[string]string{{"type": "text", "text": "hi"}},
	})
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, httptest.NewRequest("POST",
		"/deployed/chain-a/sessions/"+sess.ID+"/turn", bytes.NewReader(body2)))

	if rr2.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rr2.Code, rr2.Body.String())
	}
	if runner.capturedAgentID != "v-current" {
		t.Fatalf("agentID=%q want v-current", runner.capturedAgentID)
	}
	if runner.capturedSessionID != sess.ID {
		t.Fatalf("sessionID=%q want %q", runner.capturedSessionID, sess.ID)
	}
}

func TestDeployedHandler_Turn_WrongUser_404(t *testing.T) {
	store := newStubDeployedStore()
	mux := newDeployedMux(&stubDeployedAgentReader{deployedID: "v-current"}, store, &stubTurnRunner{}, jsonl.NewFactory())

	body, _ := json.Marshal(map[string]string{"user_id": "user-A"})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/deployed/chain-a/sessions", bytes.NewReader(body)))
	var sess types.DeployedSession
	_ = json.NewDecoder(rr.Body).Decode(&sess)

	body2, _ := json.Marshal(map[string]any{
		"user_id": "user-B",
		"input":   []map[string]string{{"type": "text", "text": "x"}},
	})
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, httptest.NewRequest("POST",
		"/deployed/chain-a/sessions/"+sess.ID+"/turn", bytes.NewReader(body2)))
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rr2.Code)
	}
}

func TestDeployedHandler_Turn_NoDeployedVersion_404(t *testing.T) {
	store := newStubDeployedStore()
	// Pre-seed a session as if a deploy used to exist (so the session is real).
	store.sessions["s1"] = &types.DeployedSession{ID: "s1", ChainRootID: "chain-a", UserID: "u"}
	mux := newDeployedMux(&stubDeployedAgentReader{deployedID: ""}, store, &stubTurnRunner{}, jsonl.NewFactory())

	body, _ := json.Marshal(map[string]any{"user_id": "u", "input": []map[string]string{{"type": "text", "text": "x"}}})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/deployed/chain-a/sessions/s1/turn", bytes.NewReader(body)))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d", rr.Code)
	}
}
