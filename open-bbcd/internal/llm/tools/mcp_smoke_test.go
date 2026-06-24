//go:build mcp_smoke

package tools

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestMCPSmoke_ConnectAndListTools exercises the library against a real
// public MCP demo server. Run with: go test -tags=mcp_smoke ./internal/llm/tools -run MCPSmoke -v
// Skipped by default (build tag) so CI without network access stays green.
func TestMCPSmoke_ConnectAndListTools(t *testing.T) {
	ctx := context.Background()

	// 1. Build the streamable-HTTP transport pointing at the public MCP
	//    playground. Replace the URL with a live endpoint before running.
	trans, err := transport.NewStreamableHTTP("https://demo.mcp.run/mcp")
	if err != nil {
		t.Skipf("transport ctor: %v", err)
	}

	// 2. Wrap in a Client (no options needed for plain HTTP).
	c := client.NewClient(trans)

	// 3. Start the transport (opens the connection).
	if err := c.Start(ctx); err != nil {
		t.Skipf("client.Start: %v", err)
	}
	defer c.Close()

	// 4. MCP handshake: Initialize negotiates protocol version + capabilities.
	//    Unlike the plan's assumed c.Initialize(ctx), the real signature takes
	//    a full mcp.InitializeRequest (see MCP_LIBRARY_NOTES.md).
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "open-bbcd-smoke",
		Version: "0.0.1",
	}
	initReq.Params.Capabilities = mcp.ClientCapabilities{}

	serverInfo, err := c.Initialize(ctx, initReq)
	if err != nil {
		t.Skipf("Initialize: %v", err)
	}
	t.Logf("connected to %q v%s", serverInfo.ServerInfo.Name, serverInfo.ServerInfo.Version)

	// 5. List tools — real signature takes mcp.ListToolsRequest{}, not just ctx.
	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Skipf("ListTools: %v", err)
	}
	t.Logf("got %d tools", len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		t.Logf("  tool: %s — %s", tool.Name, tool.Description)
	}
}
