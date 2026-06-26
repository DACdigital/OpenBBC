package chat

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// TestOrchestrator_E2E_HTTPBackend drives one turn end-to-end:
//   - LLM emits a tool_use for an endpoint mapped to a real HTTPEndpointBackend
//   - that backend dispatches to a fake httptest.Server
//   - the test verifies the server saw the expected URL + body
func TestOrchestrator_E2E_HTTPBackend(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"ok":true,"id":42}`)
	}))
	defer srv.Close()

	// Build a real composite handler with one real HTTP backend wired to one endpoint.
	ep := tools.HTTPEndpointDef{
		ID: "orders.create", Name: "orders_create", Method: "POST",
		Path: "/api/orders",
	}
	backend := tools.NewHTTPEndpointBackend(
		"api",        // backend name
		"backend-1",  // backend id
		tools.HTTPBackendCfg{BaseURL: srv.URL},
		[]tools.HTTPEndpointDef{ep},
		map[string]string{"orders.create": "backend-1"},
	)
	composite := tools.NewComposite([]tools.Backend{backend})

	// Script the LLM to emit one tool_use for orders_create, then end the turn.
	flm := &fakeLLM{
		name: "fake",
		script: [][]llm.Event{
			{
				llm.ToolUseStartEvent{ID: "tu1", Name: "orders_create"},
				llm.ToolUseInputEvent{ID: "tu1", JSONFragment: `{"amount":99}`},
				llm.ToolUseEndEvent{ID: "tu1"},
				llm.MessageStopEvent{StopReason: "tool_use"},
			},
			// Second round: LLM sees the tool result and ends the turn.
			{
				llm.TextDeltaEvent{Delta: "done"},
				llm.MessageStopEvent{StopReason: "end_turn"},
			},
		},
	}

	version := &types.AgentVersion{
		ID:     "version-1",
		Bundle: []byte(`{"main_prompt":"sys","tools":[{"id":"orders.create","name":"orders_create","method":"POST","path":"/api/orders"}]}`),
	}
	fakeAgent := &fakeAgentRepo{version: version}
	fakeChat := &fakeChatRepo{}

	o := NewOrchestrator(fakeAgent, fakeChat, flm, &fakeBuilder{handler: composite}, slog.Default())
	sink := &recordingSink{}
	err := o.Turn(context.Background(), "version-1", "session-1", []llm.Block{llm.TextBlock{Text: "hi"}}, sink)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	if gotPath != "/api/orders" {
		t.Fatalf("server got path %q, want /api/orders", gotPath)
	}
	if !strings.Contains(gotBody, `"amount":99`) {
		t.Fatalf("server body did not contain amount=99: %q", gotBody)
	}
}
