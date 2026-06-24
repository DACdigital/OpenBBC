package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPBackendCfg is the persisted configuration for an MCP client backend.
// Stored as JSONB in the tool_backends row.
type MCPBackendCfg struct {
	URL            string            `json:"url"`
	Transport      string            `json:"transport"` // "streamable_http" | "sse" — currently only streamable_http supported
	DefaultHeaders map[string]string `json:"default_headers,omitempty"`
}

// MCPClientBackend connects to an external MCP server and exposes its tools
// to the LLM. Lazy: the connection opens on the first Tools() or Call()
// within a chat session, and is reused across calls in the same session.
type MCPClientBackend struct {
	name string
	id   string
	cfg  MCPBackendCfg

	mu       sync.Mutex
	client   *client.Client
	toolDefs []llm.ToolDef // cached after first list
	rawTools []mcp.Tool    // cached, indexed by Name for Call routing
	connErr  error         // sticky: if init fails, the backend is dead for the session
}

func NewMCPClientBackend(name, id string, cfg MCPBackendCfg) *MCPClientBackend {
	return &MCPClientBackend{name: name, id: id, cfg: cfg}
}

func (b *MCPClientBackend) Name() string { return b.name }

// ensure connects, initializes, and caches tools list. Idempotent — calling
// more than once is a no-op unless the previous attempt failed.
func (b *MCPClientBackend) ensure(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil || b.connErr != nil {
		return b.connErr
	}

	// Merge: default + session overrides for this backend's id.
	headers := map[string]string{}
	for k, v := range b.cfg.DefaultHeaders {
		headers[k] = v
	}
	if sess := sessionHeaderOverridesFromContext(ctx); sess != nil {
		if mine, ok := sess[b.id]; ok {
			for k, v := range mine {
				headers[k] = v
			}
		}
	}

	trans, err := transport.NewStreamableHTTP(b.cfg.URL, transport.WithHTTPHeaders(headers))
	if err != nil {
		b.connErr = fmt.Errorf("mcp %s: transport: %w", b.name, err)
		return b.connErr
	}
	c := client.NewClient(trans)
	if err := c.Start(ctx); err != nil {
		b.connErr = fmt.Errorf("mcp %s: start: %w", b.name, err)
		return b.connErr
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "open-bbcd", Version: "0.1"}
	initReq.Params.Capabilities = mcp.ClientCapabilities{}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		b.connErr = fmt.Errorf("mcp %s: initialize: %w", b.name, err)
		return b.connErr
	}

	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		_ = c.Close()
		b.connErr = fmt.Errorf("mcp %s: list_tools: %w", b.name, err)
		return b.connErr
	}

	// Cache prefixed tool defs.
	defs := make([]llm.ToolDef, 0, len(toolsResult.Tools))
	for _, t := range toolsResult.Tools {
		schemaBytes, err := json.Marshal(t.InputSchema)
		if err != nil {
			// Fall back to permissive schema; don't block the whole backend on one bad schema.
			schemaBytes = []byte(`{"type":"object","additionalProperties":true}`)
		}
		defs = append(defs, llm.ToolDef{
			Name:        b.name + "__" + t.Name,
			Description: t.Description,
			InputSchema: schemaBytes,
		})
	}

	b.client = c
	b.toolDefs = defs
	b.rawTools = toolsResult.Tools
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

	// Parse arguments — MCP servers expect map[string]any.
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return errResult(fmt.Sprintf("mcp: invalid arguments json: %s", err)), nil
		}
	}

	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = unprefixedName
	callReq.Params.Arguments = args

	result, err := b.client.CallTool(ctx, callReq)
	if err != nil {
		return errResult(fmt.Sprintf("mcp %s: call %s: %s", b.name, unprefixedName, err)), nil
	}

	// Flatten content blocks: extract Text from TextContent entries; for
	// non-text content, marshal the entry verbatim. Wrap everything in a
	// JSON object so the LLM sees a structured result.
	flattened := flattenMCPContent(result.Content)
	out, err := json.Marshal(flattened)
	if err != nil {
		return errResult(fmt.Sprintf("mcp: marshal result: %s", err)), nil
	}
	return Result{Output: out, IsError: result.IsError}, nil
}

// flattenMCPContent reduces []mcp.Content (interface) to a JSON-serializable
// shape: { "text": "...", "content": [<raw content blocks>] }. The "text"
// field concatenates TextContent entries (the common case for tool results);
// the full content list is preserved under "content" for richer consumers.
func flattenMCPContent(blocks []mcp.Content) map[string]any {
	var textParts []string
	rawBlocks := make([]any, 0, len(blocks))
	for _, blk := range blocks {
		if tc, ok := blk.(mcp.TextContent); ok {
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

// Close releases the client connection if open. Safe to call multiple times.
func (b *MCPClientBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client == nil {
		return nil
	}
	err := b.client.Close()
	b.client = nil
	return err
}

// Compile-time conformance check.
var _ Backend = (*MCPClientBackend)(nil)
