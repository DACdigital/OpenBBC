//go:build mcp_smoke

package tools

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPSmoke_ConnectAndListTools exercises the official SDK against a real
// public MCP demo server. Run with:
//
//	go test -tags=mcp_smoke ./internal/llm/tools -run MCPSmoke -v
//
// Skipped by default (build tag) so CI without network access stays green.
func TestMCPSmoke_ConnectAndListTools(t *testing.T) {
	ctx := context.Background()

	// 1. Build the streamable-HTTP transport pointing at a live MCP server.
	//    Replace the URL with a real endpoint before running.
	transport := &mcp.StreamableClientTransport{
		Endpoint:             "https://demo.mcp.run/mcp",
		HTTPClient:           http.DefaultClient,
		DisableStandaloneSSE: true,
	}

	// 2. Create a client and connect (handshake happens inside Connect).
	c := mcp.NewClient(&mcp.Implementation{
		Name:    "open-bbcd-smoke",
		Version: "0.0.1",
	}, nil)

	session, err := c.Connect(ctx, transport, nil)
	if err != nil {
		t.Skipf("Connect: %v", err)
	}
	defer session.Close()

	// 3. List tools.
	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Skipf("ListTools: %v", err)
	}
	t.Logf("got %d tools", len(result.Tools))
	for _, tool := range result.Tools {
		t.Logf("  tool: %s — %s", tool.Name, tool.Description)
	}
}
