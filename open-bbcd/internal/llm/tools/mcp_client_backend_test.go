package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// jsonrpcRequest is the inbound shape we parse from MCP wire traffic.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// newMCPTestServer returns an httptest.Server that responds to initialize,
// tools/list, and tools/call with canned responses.
func newMCPTestServer(t *testing.T, listToolsCount *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonrpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", 400)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch req.Method {
		case "initialize":
			respond(w, req.ID, map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "test", "version": "0.1"},
			})
		case "notifications/initialized":
			// no response for notifications
		case "tools/list":
			if listToolsCount != nil {
				atomic.AddInt32(listToolsCount, 1)
			}
			respond(w, req.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        "send_message",
						"description": "Send a Slack message",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"text": map[string]any{"type": "string"}},
							"required":   []string{"text"},
						},
					},
				},
			})
		case "tools/call":
			// echo the input as a text content block; flag isError if a magic field is set.
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			isError := strings.Contains(string(p.Arguments), `"_fail":true`)
			respond(w, req.ID, map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "called " + p.Name + " with " + string(p.Arguments)},
				},
				"isError": isError,
			})
		default:
			http.Error(w, "unknown method "+req.Method, 400)
		}
	}))
}

func respond(w http.ResponseWriter, id any, result any) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func TestMCPClient_Tools_PrefixesNames(t *testing.T) {
	srv := newMCPTestServer(t, nil)
	defer srv.Close()

	be := NewMCPClientBackend("slack", "b1", MCPBackendCfg{
		URL: srv.URL, Transport: "streamable_http",
	})
	defer be.Close()

	defs, err := be.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "slack__send_message" {
		t.Fatalf("want one tool named slack__send_message, got %+v", defs)
	}
}

func TestMCPClient_Tools_CachesAcrossCalls(t *testing.T) {
	var count int32
	srv := newMCPTestServer(t, &count)
	defer srv.Close()

	be := NewMCPClientBackend("x", "b1", MCPBackendCfg{URL: srv.URL, Transport: "streamable_http"})
	defer be.Close()

	for i := 0; i < 3; i++ {
		if _, err := be.Tools(context.Background()); err != nil {
			t.Fatalf("Tools[%d]: %v", i, err)
		}
	}
	if c := atomic.LoadInt32(&count); c != 1 {
		t.Fatalf("expected tools/list called exactly once across 3 Tools() calls, got %d", c)
	}
}

func TestMCPClient_Call_StripsPrefix_AndFlattensTextContent(t *testing.T) {
	srv := newMCPTestServer(t, nil)
	defer srv.Close()

	be := NewMCPClientBackend("slack", "b1", MCPBackendCfg{URL: srv.URL, Transport: "streamable_http"})
	defer be.Close()

	res, err := be.Call(context.Background(), "send_message", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", string(res.Output))
	}
	if !strings.Contains(string(res.Output), "called send_message") {
		t.Fatalf("output missing tool name: %s", string(res.Output))
	}
}

func TestMCPClient_Call_PropagatesIsError(t *testing.T) {
	srv := newMCPTestServer(t, nil)
	defer srv.Close()

	be := NewMCPClientBackend("x", "b1", MCPBackendCfg{URL: srv.URL, Transport: "streamable_http"})
	defer be.Close()

	res, _ := be.Call(context.Background(), "send_message", json.RawMessage(`{"_fail":true}`))
	if !res.IsError {
		t.Fatal("expected IsError true")
	}
}

func TestMCPClient_Tools_FailsOnUnreachableServer(t *testing.T) {
	be := NewMCPClientBackend("x", "b1", MCPBackendCfg{URL: "http://127.0.0.1:1", Transport: "streamable_http"})
	_, err := be.Tools(context.Background())
	if err == nil {
		t.Fatal("expected error from unreachable server")
	}
}
