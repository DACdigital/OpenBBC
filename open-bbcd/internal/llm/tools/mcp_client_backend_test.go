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

func TestMCPClient_SessionOverrideAppliedAtConnect(t *testing.T) {
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Record the Authorization header from every request.
		if v := r.Header.Get("Authorization"); v != "" {
			gotAuth.Store(v)
		}
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
			respond(w, req.ID, map[string]any{"tools": []map[string]any{}})
		default:
			http.Error(w, "unknown method "+req.Method, 400)
		}
	}))
	defer srv.Close()

	be := NewMCPClientBackend("test", "b1", MCPBackendCfg{
		URL:            srv.URL,
		Transport:      "streamable_http",
		DefaultHeaders: map[string]string{"Authorization": "Bearer default"},
	})
	defer be.Close()

	ctx := WithSessionHeaderOverrides(context.Background(), SessionHeaderOverrides{
		"b1": {"Authorization": "Bearer SESSION"},
	})

	_, _ = be.Tools(ctx)

	if got := gotAuth.Load(); got != "Bearer SESSION" {
		t.Fatalf("want Bearer SESSION, got %v", got)
	}
}

// newMCPHeaderSpy returns a test server that records the Authorization header
// and responds to the minimal MCP protocol handshake (no tools). The atomic
// is written on every request so the caller can check after Tools() returns.
func newMCPHeaderSpy(t *testing.T, gotAuth *atomic.Value) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("Authorization"); v != "" {
			gotAuth.Store(v)
		}
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
			respond(w, req.ID, map[string]any{"tools": []map[string]any{}})
		default:
			http.Error(w, "unknown method "+req.Method, 400)
		}
	}))
}

// TestMCPClient_ForwardsLiveFEHeaders verifies that headers stashed on ctx
// via WithForwardedHeaders flow through to the MCP server on every JSON-RPC
// request when the backend is opted into _all in the routing envelope.
func TestMCPClient_ForwardsLiveFEHeaders(t *testing.T) {
	var gotAuth, gotUserID atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("Authorization"); v != "" {
			gotAuth.Store(v)
		}
		if v := r.Header.Get("X-User-Id"); v != "" {
			gotUserID.Store(v)
		}
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
			// no response
		case "tools/list":
			respond(w, req.ID, map[string]any{"tools": []map[string]any{}})
		default:
			http.Error(w, "unknown method "+req.Method, 400)
		}
	}))
	defer srv.Close()

	be := NewMCPClientBackend("test", "b1", MCPBackendCfg{
		URL:       srv.URL,
		Transport: "streamable_http",
	})
	defer be.Close()

	// Opt "test" backend into _all so live FE headers are forwarded.
	ctx := WithForwardedHeaders(context.Background(), http.Header{
		"Authorization": {"Bearer USER-TOKEN"},
		"X-User-Id":     {"user-42"},
		"Host":          {"should-be-stripped.example"}, // hop-by-hop; must not be forwarded
	})
	ctx = WithBackendHeaderRouting(ctx, BackendHeaderRouting{
		ByName: map[string]BackendRoutingBlock{
			"test": {All: true},
		},
	})

	_, _ = be.Tools(ctx)

	if got := gotAuth.Load(); got != "Bearer USER-TOKEN" {
		t.Fatalf("Authorization: want USER-TOKEN, got %v", got)
	}
	if got := gotUserID.Load(); got != "user-42" {
		t.Fatalf("X-User-Id: want user-42, got %v", got)
	}
}

// TestMCPClient_NoEnvelope_DoesNotForwardLiveHeaders verifies that live FE
// headers are NOT forwarded when no routing envelope is present on the context.
func TestMCPClient_NoEnvelope_DoesNotForwardLiveHeaders(t *testing.T) {
	var gotAuth atomic.Value
	srv := newMCPHeaderSpy(t, &gotAuth)
	defer srv.Close()

	be := NewMCPClientBackend("test", "b1", MCPBackendCfg{
		URL:       srv.URL,
		Transport: "streamable_http",
	})
	defer be.Close()

	// Live FE has Authorization but no routing envelope → must not be forwarded.
	ctx := WithForwardedHeaders(context.Background(), http.Header{
		"Authorization": {"Bearer LIVE"},
	})
	_, _ = be.Tools(ctx)

	if got := gotAuth.Load(); got != nil && got != "" {
		t.Fatalf("Authorization leaked without envelope: %v", got)
	}
}

// TestMCPClient_EnvelopeExplicitHeaderForwarded verifies that an explicitly
// listed header in the routing envelope reaches the MCP server.
func TestMCPClient_EnvelopeExplicitHeaderForwarded(t *testing.T) {
	var gotAuth atomic.Value
	srv := newMCPHeaderSpy(t, &gotAuth)
	defer srv.Close()

	be := NewMCPClientBackend("test", "b1", MCPBackendCfg{
		URL:       srv.URL,
		Transport: "streamable_http",
	})
	defer be.Close()

	ctx := WithBackendHeaderRouting(context.Background(), BackendHeaderRouting{
		ByName: map[string]BackendRoutingBlock{
			"test": {Headers: map[string]string{"Authorization": "Bearer ROUTED"}},
		},
	})
	_, _ = be.Tools(ctx)

	if got := gotAuth.Load(); got != "Bearer ROUTED" {
		t.Fatalf("want Bearer ROUTED, got %v", got)
	}
}

// TestMCPClient_EnvelopeNotMatched_NoLiveHeaders verifies that when the
// envelope addresses a different backend, no live FE headers are forwarded.
func TestMCPClient_EnvelopeNotMatched_NoLiveHeaders(t *testing.T) {
	var gotAuth atomic.Value
	srv := newMCPHeaderSpy(t, &gotAuth)
	defer srv.Close()

	be := NewMCPClientBackend("test", "b1", MCPBackendCfg{
		URL:       srv.URL,
		Transport: "streamable_http",
	})
	defer be.Close()

	// Envelope is for "other" backend; "test" should receive nothing.
	ctx := WithForwardedHeaders(context.Background(), http.Header{
		"Authorization": {"Bearer LIVE"},
	})
	ctx = WithBackendHeaderRouting(ctx, BackendHeaderRouting{
		ByName: map[string]BackendRoutingBlock{"other": {All: true}},
	})
	_, _ = be.Tools(ctx)

	if got := gotAuth.Load(); got != nil && got != "" {
		t.Fatalf("Authorization leaked despite no envelope match: %v", got)
	}
}
