package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPBackendCfg is the persisted configuration for an MCP client backend.
// Stored as JSONB in the tool_backends row.
type MCPBackendCfg struct {
	URL            string            `json:"url"`
	Transport      string            `json:"transport"` // "streamable_http" (only supported transport today)
	DefaultHeaders map[string]string `json:"default_headers,omitempty"`
}

// MCPClientBackend connects to an external MCP server and exposes its tools
// to the LLM. Lazy: the session opens on the first Tools() or Call() within
// a chat session.
type MCPClientBackend struct {
	name string
	id   string
	cfg  MCPBackendCfg

	mu       sync.Mutex
	session  *mcp.ClientSession
	toolDefs []llm.ToolDef
	connErr  error // sticky: if init fails, the backend is dead for the session
}

func NewMCPClientBackend(name, id string, cfg MCPBackendCfg) *MCPClientBackend {
	return &MCPClientBackend{name: name, id: id, cfg: cfg}
}

func (b *MCPClientBackend) Name() string { return b.name }

// headerInjectingTransport applies headers to every outgoing MCP request,
// since the official SDK's StreamableClientTransport has no headers option.
//
// Headers are merged per-request (read from req.Context()) in the same
// precedence order as HTTPEndpointBackend.applyHeaders:
//
//  1. defaultHeaders    — service-level config on the backend row (lowest)
//  2. routing envelope  — _all flag copies live FE headers; explicit map overrides
//  3. session overrides — BO testing modal (highest)
type headerInjectingTransport struct {
	base           http.RoundTripper
	backendID      string
	backendName    string
	defaultHeaders map[string]string
}

func (h *headerInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// 1. Default headers.
	for k, v := range h.defaultHeaders {
		req.Header.Set(k, v)
	}

	// 2/3. Routing envelope.
	if routing, ok := backendHeaderRoutingFromContext(ctx); ok {
		if block, found := routing.LookupByBackendName(h.backendName); found {
			if block.All {
				if live := forwardedHeadersFromContext(ctx); live != nil {
					for name, vals := range live {
						lc := strings.ToLower(name)
						if hopByHopHeaders[lc] || lc == strings.ToLower(RoutingEnvelopeHeader) {
							continue
						}
						if len(vals) > 0 {
							req.Header.Set(name, vals[0])
						}
					}
				}
			}
			for k, v := range block.Headers {
				req.Header.Set(k, v)
			}
		}
		// backend not in envelope → no FE headers forwarded
	}

	// 4. Session overrides.
	if sess := sessionHeaderOverridesFromContext(ctx); sess != nil {
		if mine, ok := sess[h.backendID]; ok {
			for k, v := range mine {
				req.Header.Set(k, v)
			}
		}
	}

	return h.base.RoundTrip(req)
}

// ensure connects, initializes the session, lists tools, caches the result.
// Idempotent. Sticky on error.
func (b *MCPClientBackend) ensure(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.session != nil || b.connErr != nil {
		return b.connErr
	}

	// Headers are evaluated per-request inside the RoundTripper from
	// req.Context(), so routing-envelope-gated FE headers propagate to every
	// MCP call — important for per-user MCP servers that key off
	// user-identifying headers.
	httpClient := &http.Client{
		Transport: &headerInjectingTransport{
			base:           http.DefaultTransport,
			backendID:      b.id,
			backendName:    b.name,
			defaultHeaders: b.cfg.DefaultHeaders,
		},
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:             b.cfg.URL,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true, // we don't consume server-initiated notifications today
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "open-bbcd",
		Version: "0.1",
	}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		b.connErr = fmt.Errorf("mcp %s: connect: %w", b.name, err)
		return b.connErr
	}

	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		_ = session.Close()
		b.connErr = fmt.Errorf("mcp %s: list_tools: %w", b.name, err)
		return b.connErr
	}

	defs := make([]llm.ToolDef, 0, len(result.Tools))
	for _, t := range result.Tools {
		schemaBytes, err := json.Marshal(t.InputSchema)
		if err != nil || len(schemaBytes) == 0 {
			schemaBytes = []byte(`{"type":"object","additionalProperties":true}`)
		}
		defs = append(defs, llm.ToolDef{
			Name:        b.name + "__" + t.Name,
			Description: t.Description,
			InputSchema: schemaBytes,
		})
	}

	b.session = session
	b.toolDefs = defs
	return nil
}

func (b *MCPClientBackend) Tools(ctx context.Context) ([]llm.ToolDef, error) {
	if err := b.ensure(ctx); err != nil {
		return nil, err
	}
	return b.toolDefs, nil
}

func (b *MCPClientBackend) Call(ctx context.Context, unprefixedName string, input json.RawMessage) (Result, error) {
	if err := b.ensure(ctx); err != nil {
		return errResult(err.Error()), nil
	}

	var args any
	if len(input) > 0 {
		var m map[string]any
		if err := json.Unmarshal(input, &m); err != nil {
			return errResult(fmt.Sprintf("mcp: invalid arguments json: %s", err)), nil
		}
		args = m
	}

	result, err := b.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      unprefixedName,
		Arguments: args,
	})
	if err != nil {
		return errResult(fmt.Sprintf("mcp %s: call %s: %s", b.name, unprefixedName, err)), nil
	}

	flattened := flattenMCPContent(result.Content)
	out, err := json.Marshal(flattened)
	if err != nil {
		return errResult(fmt.Sprintf("mcp: marshal result: %s", err)), nil
	}
	return Result{Output: out, IsError: result.IsError}, nil
}

// flattenMCPContent reduces []mcp.Content (interface; concrete types are
// pointers in the official SDK) to a JSON-serializable shape:
// { "text": "...", "content": [<raw content blocks>] }.
// The "text" field concatenates TextContent entries (common case for tool
// results); the full content list is preserved under "content" for richer
// consumers.
func flattenMCPContent(blocks []mcp.Content) map[string]any {
	var textParts []string
	rawBlocks := make([]any, 0, len(blocks))
	for _, blk := range blocks {
		if tc, ok := blk.(*mcp.TextContent); ok {
			textParts = append(textParts, tc.Text)
		}
		rawBlocks = append(rawBlocks, blk)
	}
	out := map[string]any{
		"content": rawBlocks,
	}
	if len(textParts) > 0 {
		out["text"] = strings.Join(textParts, "\n")
	}
	return out
}

// Close releases the session if open. Safe to call multiple times.
func (b *MCPClientBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session == nil {
		return nil
	}
	err := b.session.Close()
	b.session = nil
	return err
}

// Compile-time conformance check.
var _ Backend = (*MCPClientBackend)(nil)
