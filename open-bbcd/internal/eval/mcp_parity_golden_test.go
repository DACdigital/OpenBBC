package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
)

// mcpParityCase is one tools/call scenario. raw is the JSON-RPC result the
// fake server returns; transportError makes the server answer HTTP 500.
type mcpParityCase struct {
	name, tool     string
	raw            map[string]any
	transportError bool
}

var mcpParityCases = []mcpParityCase{
	{name: "single_text", tool: "search", raw: map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "3 hits"}}}},
	{name: "multiple_text", tool: "search", raw: map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "first"},
			map[string]any{"type": "text", "text": "second"},
		}}},
	{name: "text_and_image", tool: "render", raw: map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "chart:"},
			map[string]any{"type": "image", "data": "iVBORw0KGgo=", "mimeType": "image/png"},
		}}},
	{name: "is_error", tool: "search", raw: map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "index unavailable"}},
		"isError": true}},
	// Last: the server answers HTTP 500, which may leave the session unusable.
	{name: "transport_error", tool: "boom", transportError: true},
}

// mcpParityServer serves a fixed tools/list and answers tools/call with the
// raw result of the case whose name is the "case" argument.
func mcpParityServer(t *testing.T) *httptest.Server {
	t.Helper()
	byName := map[string]mcpParityCase{}
	for _, c := range mcpParityCases {
		byName[c.name] = c
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "kb", "version": "0.1"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "search", "description": "Search the knowledge base",
					"inputSchema": map[string]any{"type": "object",
						"properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []any{"q"}},
					"outputSchema": map[string]any{"type": "object",
						"properties": map[string]any{"hits": map[string]any{"type": "integer"}}}},
				map[string]any{"name": "render", "description": "Render a chart"},
				map[string]any{"name": "boom", "description": "Always fails at the transport",
					"inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			var p struct {
				Arguments struct {
					Case string `json:"case"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			c := byName[p.Arguments.Case]
			if c.transportError {
				http.Error(w, "upstream exploded", http.StatusInternalServerError)
				return
			}
			result = c.raw
		default:
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestMCPParityGolden writes aikdm/tests/fixtures/mcp_parity.json (spec
// eval-mcp-tools In → 9): the runtime's model-visible tool definitions, the
// export's, and for each tools/call case the raw server result next to the
// runtime's model-visible output. aikdm's test_mcp_parity_golden.py replays
// the raw results through MCPToolCaller's flattening.
func TestMCPParityGolden(t *testing.T) {
	srv := mcpParityServer(t)
	ref := MCPBackendRef{ID: "b-kb", Name: "kb", Config: tools.MCPBackendCfg{URL: srv.URL, Transport: "streamable_http"}}
	ctx := context.Background()

	runtime := tools.NewMCPClientBackend(ref.Name, ref.ID, ref.Config)
	defer func() { _ = runtime.Close() }()
	defs, err := runtime.Tools(ctx)
	if err != nil {
		t.Fatalf("runtime Tools: %v", err)
	}
	runtimeTools := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		var schema any
		if err := json.Unmarshal(d.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		runtimeTools = append(runtimeTools, map[string]any{
			"name": d.Name, "description": d.Description, "input_schema": schema,
		})
	}

	exported, err := DiscoverMCPTools(ctx, ref, nil)
	if err != nil {
		t.Fatalf("export discovery: %v", err)
	}
	exportTools := make([]map[string]any, 0, len(exported))
	for i, et := range exported {
		m := map[string]any{"name": et.Name, "description": et.Description, "input_schema": et.InputSchema}
		if !reflect.DeepEqual(m, runtimeTools[i]) {
			t.Fatalf("export tool %d = %#v, runtime offers %#v", i, m, runtimeTools[i])
		}
		if et.OutputSchema != nil {
			m["output_schema"] = et.OutputSchema
		}
		exportTools = append(exportTools, m)
	}
	if len(exported) != len(runtimeTools) {
		t.Fatalf("export has %d tools, runtime %d", len(exported), len(runtimeTools))
	}

	prefix := ref.Name + "__"
	cases := make([]map[string]any, 0, len(mcpParityCases))
	for _, c := range mcpParityCases {
		input, _ := json.Marshal(map[string]any{"case": c.name})
		res, err := runtime.Call(ctx, c.tool, input)
		if err != nil {
			t.Fatalf("%s: Call: %v", c.name, err)
		}
		var expected map[string]any
		if err := json.Unmarshal(res.Output, &expected); err != nil {
			t.Fatalf("%s: output %s: %v", c.name, res.Output, err)
		}
		entry := map[string]any{
			"name": c.name, "tool": prefix + c.tool,
			"expected": expected, "is_error": res.IsError,
		}
		if c.transportError {
			// The runtime reports "mcp <backend>: call <tool>: <cause>"; the
			// cause text is SDK-specific, so the fixture pins the template
			// with the Go cause and aikdm formats the same cause.
			msg, _ := expected["error"].(string)
			head := "mcp " + ref.Name + ": call " + c.tool + ": "
			if !strings.HasPrefix(msg, head) || !res.IsError {
				t.Fatalf("transport error = %q (is_error=%v), want prefix %q", msg, res.IsError, head)
			}
			entry["transport_error"] = strings.TrimPrefix(msg, head)
		} else {
			entry["raw"] = c.raw
		}
		cases = append(cases, entry)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"backend":       ref.Name,
		"runtime_tools": runtimeTools,
		"export_tools":  exportTools,
		"cases":         cases,
	}); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "mcp_parity.json", buf.Bytes())
}
