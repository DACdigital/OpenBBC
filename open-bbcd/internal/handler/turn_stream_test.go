package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport/jsonl"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// countingFactory wraps the JSONL transport and counts Close on every sink
// it hands out.
type countingFactory struct {
	inner  transport.Factory
	mu     sync.Mutex
	closes int
}

func newCountingFactory() *countingFactory { return &countingFactory{inner: jsonl.NewFactory()} }

func (f *countingFactory) ContentType() string { return f.inner.ContentType() }
func (f *countingFactory) NewSink(w http.ResponseWriter) (transport.Sink, error) {
	s, err := f.inner.NewSink(w)
	if err != nil {
		return nil, err
	}
	return &countingSink{Sink: s, f: f}, nil
}
func (f *countingFactory) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

type countingSink struct {
	transport.Sink
	f *countingFactory
}

func (s *countingSink) Close() error {
	s.f.mu.Lock()
	s.f.closes++
	s.f.mu.Unlock()
	return s.Sink.Close()
}

const deployedTestAgent = "11111111-1111-4111-8111-111111111111"

func boTurnRequest() *http.Request {
	body, _ := json.Marshal(TurnRequest{Input: []TurnInputBlock{{Type: "text", Text: "hi"}}})
	r := httptest.NewRequest("POST", "/agent_versions/v/chat/"+testSID+"/turn", bytes.NewReader(body))
	r.SetPathValue("version_id", "v")
	r.SetPathValue("session_id", testSID)
	return r
}

// createDeployedSession creates a session for user-A through mux and
// returns its id.
func createDeployedSession(t *testing.T, mux http.Handler) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"user_id": "user-A"})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/deployed/"+deployedTestAgent+"/sessions", bytes.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create session: %d %s", rr.Code, rr.Body.String())
	}
	var sess types.DeployedSession
	if err := json.NewDecoder(rr.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess.ID
}

func deployedTurnBody() []byte {
	b, _ := json.Marshal(map[string]any{
		"user_id": "user-A",
		"input":   []map[string]string{{"type": "text", "text": "hi"}},
	})
	return b
}

func TestTurnHandlers_CloseSinkExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"turn fails before streaming", types.ErrAgentNotRunnable},
	} {
		t.Run("bo/"+tc.name, func(t *testing.T) {
			f := newCountingFactory()
			runner := &stubTurnRunner{err: tc.err}
			h := newTestChatHandler(t, runner)
			h.transport = f
			w := httptest.NewRecorder()
			h.Turn(w, boTurnRequest())
			if runner.calls != 1 {
				t.Fatalf("runner calls = %d, want 1", runner.calls)
			}
			if got := f.closeCount(); got != 1 {
				t.Fatalf("sink closes = %d, want 1", got)
			}
		})
		t.Run("deployed/"+tc.name, func(t *testing.T) {
			f := newCountingFactory()
			runner := &stubTurnRunner{err: tc.err}
			mux := newDeployedMux(&stubDeployedAgentReader{deployedID: "v-current"}, newStubDeployedStore(), runner, f)
			sid := createDeployedSession(t, mux)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest("POST", "/deployed/"+deployedTestAgent+"/sessions/"+sid+"/turn", bytes.NewReader(deployedTurnBody())))
			if rr.Code != http.StatusOK {
				t.Fatalf("turn status %d: %s", rr.Code, rr.Body.String())
			}
			if runner.calls != 1 {
				t.Fatalf("runner calls = %d, want 1", runner.calls)
			}
			if got := f.closeCount(); got != 1 {
				t.Fatalf("sink closes = %d, want 1", got)
			}
		})
	}
}

func TestTurnHandlers_PassZeroTurnOpts(t *testing.T) {
	runner := &stubTurnRunner{}
	h := newTestChatHandler(t, runner)
	h.Turn(httptest.NewRecorder(), boTurnRequest())
	if runner.capturedOpts.Depth != 0 || runner.capturedOpts.ParentSessionID != "" || runner.capturedOpts.RootSessionID != "" {
		t.Fatalf("opts = %+v, want zero", runner.capturedOpts)
	}
}

// TestTurnHandlers_LongTurnOutlivesWriteTimeout: both streaming turn routes
// clear the per-connection write deadline, so a turn longer than the server
// WriteTimeout streams to completion, while a non-streaming route on the
// same server still fails at the deadline.
func TestTurnHandlers_LongTurnOutlivesWriteTimeout(t *testing.T) {
	const writeTimeout = 200 * time.Millisecond
	const turnDelay = 2 * writeTimeout

	longTurn := func(ctx context.Context, sink transport.Sink) {
		_ = sink.Send(ctx, transport.TextDeltaEvent{MessageID: "m1", Delta: "first"})
		time.Sleep(turnDelay)
		_ = sink.Send(ctx, transport.TextDeltaEvent{MessageID: "m1", Delta: "second"})
		_ = sink.Send(ctx, transport.TurnEndEvent{StopReason: "end_turn"})
	}

	boRunner := &stubTurnRunner{onTurn: longTurn}
	boHandler := newTestChatHandler(t, boRunner)
	deployedRunner := &stubTurnRunner{onTurn: longTurn}
	deployedMux := newDeployedMux(&stubDeployedAgentReader{deployedID: "v-current"}, newStubDeployedStore(), deployedRunner, jsonl.NewFactory())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /agent_versions/{version_id}/chat/{session_id}/turn", boHandler.Turn)
	mux.Handle("/deployed/", deployedMux)
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(turnDelay)
		_, _ = w.Write([]byte("late"))
	})

	srv := httptest.NewUnstartedServer(RequestLogger(slog.New(slog.NewTextHandler(io.Discard, nil)), mux))
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	t.Cleanup(srv.Close)

	readStream := func(t *testing.T, req *http.Request) string {
		t.Helper()
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("stream ended with error after %q: %v", b, err)
		}
		return string(b)
	}
	assertBothEvents := func(t *testing.T, body string) {
		t.Helper()
		if !strings.Contains(body, "first") || !strings.Contains(body, "second") || !strings.Contains(body, "run_finished") {
			t.Fatalf("stream incomplete: %q", body)
		}
	}

	t.Run("bo turn", func(t *testing.T) {
		body, _ := json.Marshal(TurnRequest{Input: []TurnInputBlock{{Type: "text", Text: "hi"}}})
		req, _ := http.NewRequest("POST", srv.URL+"/agent_versions/v/chat/"+testSID+"/turn", bytes.NewReader(body))
		assertBothEvents(t, readStream(t, req))
	})

	t.Run("deployed turn", func(t *testing.T) {
		sid := createDeployedSession(t, deployedMux)
		req, _ := http.NewRequest("POST", srv.URL+"/deployed/"+deployedTestAgent+"/sessions/"+sid+"/turn", bytes.NewReader(deployedTurnBody()))
		assertBothEvents(t, readStream(t, req))
	})

	t.Run("non-streaming route still times out", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL + "/slow")
		if err != nil {
			return // connection closed at the write deadline (EOF/reset)
		}
		b, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr == nil && string(b) == "late" {
			t.Fatalf("slow route completed despite WriteTimeout: status %d body %q", resp.StatusCode, b)
		}
	})
}
