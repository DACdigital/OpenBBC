package tools

import (
	"context"
	"encoding/json"

	mcpclient "github.com/mark3labs/mcp-go/client"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// MCPBackendCfg is the persisted configuration for an MCP client backend.
// Stored as JSONB in the tool_backends row.
type MCPBackendCfg struct {
	URL            string            `json:"url"`
	Transport      string            `json:"transport"` // "streamable_http" | "sse"
	DefaultHeaders map[string]string `json:"default_headers,omitempty"`
}

// MCPClientBackend connects to an external MCP server and exposes its tools
// to the LLM. Lazy: the client connection opens on the first Tools() or
// Call() within a chat session.
//
// Task 13 will fill in the JSON-RPC plumbing.
type MCPClientBackend struct {
	name string
	id   string
	cfg  MCPBackendCfg
	// c is the lazily-initialized MCP client (mark3labs/mcp-go).
	// Nil until first Tools() or Call() within a session.
	c *mcpclient.Client
	// cachedTools caches the result of tools/list across a session.
	cachedTools []llm.ToolDef
}

func NewMCPClientBackend(name, id string, cfg MCPBackendCfg) *MCPClientBackend {
	return &MCPClientBackend{name: name, id: id, cfg: cfg}
}

func (b *MCPClientBackend) Name() string { return b.name }

func (b *MCPClientBackend) Tools(ctx context.Context) ([]llm.ToolDef, error) {
	// Task 13: connect (lazy), call tools/list, cache result, prefix names
	// with b.name + "__" before returning.
	return nil, nil
}

func (b *MCPClientBackend) Call(ctx context.Context, unprefixed string, input json.RawMessage) (Result, error) {
	// Task 13: connect (lazy), call tools/call with name=unprefixed +
	// arguments=input, flatten content blocks to a JSON blob, return as Output.
	return Result{}, nil
}

// Compile-time conformance check.
var _ Backend = (*MCPClientBackend)(nil)
