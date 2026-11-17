package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestClientContextBlock(t *testing.T) {
	for _, raw := range []string{"", "null", "  null\n"} {
		b, err := clientContextBlock(json.RawMessage(raw), 10)
		if b != nil || err != nil {
			t.Fatalf("%q: (%v, %v), want (nil, nil)", raw, b, err)
		}
	}
	// The cap is measured on the canonical string, not the raw bytes:
	// 15 raw bytes, canonical {"a":1} is 7.
	raw := json.RawMessage("{  \"a\" :  1  }")
	b, err := clientContextBlock(raw, 7)
	if err != nil || b != llm.Block(llm.ClientContextBlock{Canonical: `{"a":1}`}) {
		t.Fatalf("at cap: (%#v, %v)", b, err)
	}
	if _, err := clientContextBlock(raw, 6); !errors.Is(err, types.ErrClientContextTooLarge) {
		t.Fatalf("over cap: err = %v", err)
	}
	if _, err := clientContextBlock(json.RawMessage(`[]`), 10); !errors.Is(err, types.ErrClientContextInvalid) {
		t.Fatalf("array: err = %v", err)
	}
}

// turnRoute drives one turn request through the BO or the deployed handler.
type turnRoute struct {
	name string
	// body wraps route-specific fields around extra, e.g. `"input":[…]`.
	body func(extra string) string
	// do sends body; maxBytes 0 keeps the default cap.
	do func(t *testing.T, runner *stubTurnRunner, body string, maxBytes int, pending bool) *httptest.ResponseRecorder
}

func turnRoutes() []turnRoute {
	return []turnRoute{
		{
			name: "bo",
			body: func(extra string) string { return "{" + extra + "}" },
			do: func(t *testing.T, runner *stubTurnRunner, body string, maxBytes int, pending bool) *httptest.ResponseRecorder {
				h := newTestChatHandlerWithStore(t, &stubChatStore{hasPending: pending}, runner, emptyTemplateFS())
				if maxBytes > 0 {
					h.WithClientContextMaxBytes(maxBytes)
				}
				r := httptest.NewRequest("POST", "/agent_versions/v/chat/"+testSID+"/turn", strings.NewReader(body))
				r.SetPathValue("version_id", "v")
				r.SetPathValue("session_id", testSID)
				w := httptest.NewRecorder()
				h.Turn(w, r)
				return w
			},
		},
		{
			name: "deployed",
			body: func(extra string) string { return `{"user_id":"u1",` + extra + "}" },
			do: func(t *testing.T, runner *stubTurnRunner, body string, maxBytes int, pending bool) *httptest.ResponseRecorder {
				store := newStubDeployedStore()
				store.sessions[testSessionID] = &types.DeployedSession{ID: testSessionID, AgentID: testAgentID, UserID: "u1"}
				store.hasPending = pending
				h := NewDeployedHandler(&stubDeployedAgentReader{deployedID: "v1"}, store, nil, runner, jsonl.NewFactory(), testLogger())
				if maxBytes > 0 {
					h.WithClientContextMaxBytes(maxBytes)
				}
				r := httptest.NewRequest("POST", "/deployed/"+testAgentID+"/sessions/"+testSessionID+"/turn", strings.NewReader(body))
				r.SetPathValue("agent_id", testAgentID)
				r.SetPathValue("session_id", testSessionID)
				w := httptest.NewRecorder()
				h.Turn(w, r)
				return w
			},
		},
	}
}

const hiInput = `"input":[{"type":"text","text":"hi"}]`

// assertRejectedBeforeStream: 400 with the exact plain-text body, no SSE
// headers, and the orchestrator never ran.
func assertRejectedBeforeStream(t *testing.T, w *httptest.ResponseRecorder, runner *stubTurnRunner, want string) {
	t.Helper()
	if w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("status %d body %q, want 400 %q", w.Code, w.Body.String(), want)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q: stream headers were written", ct)
	}
	if w.Header().Get("Cache-Control") == "no-cache" || runner.calls != 0 {
		t.Fatal("stream opened or orchestrator ran")
	}
}

func TestTurn_ClientContextObjectAccepted(t *testing.T) {
	for _, rt := range turnRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			runner := &stubTurnRunner{}
			w := rt.do(t, runner, rt.body(hiInput+`,"client_context":{"b":1,"a":"<x>"}`), 0, false)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d body %q", w.Code, w.Body.String())
			}
			if len(runner.capturedInput) != 2 {
				t.Fatalf("input = %#v", runner.capturedInput)
			}
			if cc, ok := runner.capturedInput[0].(llm.ClientContextBlock); !ok || cc.Canonical != `{"a":"<x>","b":1}` {
				t.Fatalf("input[0] = %#v", runner.capturedInput[0])
			}
			if tb, ok := runner.capturedInput[1].(llm.TextBlock); !ok || tb.Text != "hi" {
				t.Fatalf("input[1] = %#v", runner.capturedInput[1])
			}
		})
	}
}

func TestTurn_ClientContextNullOrAbsentUnchanged(t *testing.T) {
	for _, rt := range turnRoutes() {
		for _, extra := range []string{hiInput, hiInput + `,"client_context":null`} {
			t.Run(rt.name, func(t *testing.T) {
				runner := &stubTurnRunner{}
				w := rt.do(t, runner, rt.body(extra), 0, false)
				if w.Code != http.StatusOK || len(runner.capturedInput) != 1 {
					t.Fatalf("status %d input %#v", w.Code, runner.capturedInput)
				}
				if _, ok := runner.capturedInput[0].(llm.TextBlock); !ok {
					t.Fatalf("input[0] = %#v", runner.capturedInput[0])
				}
			})
		}
	}
}

func TestTurn_ClientContextNotObject400(t *testing.T) {
	for _, rt := range turnRoutes() {
		for _, v := range []string{`[1]`, `"s"`, `42`, `true`} {
			t.Run(rt.name+"/"+v, func(t *testing.T) {
				runner := &stubTurnRunner{}
				w := rt.do(t, runner, rt.body(hiInput+`,"client_context":`+v), 0, false)
				assertRejectedBeforeStream(t, w, runner, "invalid client_context: must be a JSON object")
			})
		}
	}
}

func TestTurn_ClientContextNUL400(t *testing.T) {
	for _, rt := range turnRoutes() {
		for _, v := range []string{`{"a\u0000":1}`, `{"a":["x\u0000"]}`} {
			t.Run(rt.name, func(t *testing.T) {
				runner := &stubTurnRunner{}
				w := rt.do(t, runner, rt.body(hiInput+`,"client_context":`+v), 0, false)
				assertRejectedBeforeStream(t, w, runner, `invalid client_context: contains \u0000`)
			})
		}
	}
}

func TestTurn_ClientContextOverCap400(t *testing.T) {
	for _, rt := range turnRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			runner := &stubTurnRunner{}
			// canonical {"a":"0123456789"} is 18 bytes > 16.
			w := rt.do(t, runner, rt.body(hiInput+`,"client_context":{"a":"0123456789"}`), 16, false)
			assertRejectedBeforeStream(t, w, runner, "client_context exceeds OPENBBC_CLIENT_CONTEXT_MAX_BYTES")
		})
	}
}

func TestTurn_ClientContextOnlyIsEmptyTurn(t *testing.T) {
	for _, rt := range turnRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			runner := &stubTurnRunner{}
			w := rt.do(t, runner, rt.body(`"input":[],"client_context":{"a":1}`), 0, false)
			assertRejectedBeforeStream(t, w, runner, types.ErrEmptyTurn.Error())
		})
	}
}

func TestTurn_ClientContextWithPendingArtifactAccepted(t *testing.T) {
	for _, rt := range turnRoutes() {
		t.Run(rt.name, func(t *testing.T) {
			runner := &stubTurnRunner{}
			w := rt.do(t, runner, rt.body(`"input":[],"client_context":{"a":1}`), 0, true)
			if w.Code != http.StatusOK || len(runner.capturedInput) != 1 {
				t.Fatalf("status %d input %#v", w.Code, runner.capturedInput)
			}
			if _, ok := runner.capturedInput[0].(llm.ClientContextBlock); !ok {
				t.Fatalf("input[0] = %#v", runner.capturedInput[0])
			}
		})
	}
}
