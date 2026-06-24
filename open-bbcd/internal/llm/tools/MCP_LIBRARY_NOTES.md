# MCP Client Library Notes

## Library chosen

**`github.com/mark3labs/mcp-go` v0.55.0**

This is the most complete Go MCP SDK available. It ships a `client` package with
multiple transport back-ends (stdio, SSE, streamable-HTTP, in-process) and a
full server-side package that we don't need today. It is actively maintained and
covers the 2025-03-26 MCP specification.

Alternatives evaluated:

- **`github.com/metoro-io/mcp-golang`** — exists on go.dev but has far fewer
  stars and significantly less transport coverage (stdio-only as of mid-2025).
  Not worth switching unless mark3labs breaks.
- **Hand-rolled JSON-RPC over HTTP+SSE** — would give us full control but
  replicates a non-trivial amount of protocol work (session IDs, ping, protocol
  version negotiation, content-block parsing) that mark3labs already covers
  correctly.

## Key API surface

### Transport construction (streamable-HTTP)

```go
import "github.com/mark3labs/mcp-go/client/transport"

trans, err := transport.NewStreamableHTTP(url, transport.WithHTTPHeaders(headers))
```

`NewStreamableHTTP` returns `(*StreamableHTTP, error)`. Available options:
- `WithHTTPHeaders(map[string]string)` — static headers (auth tokens, etc.)
- `WithHTTPHeaderFunc(func() map[string]string)` — dynamic headers
- `WithHTTPBasicClient(*http.Client)` — inject a custom http.Client
- `WithHTTPTimeout(time.Duration)`
- `WithContinuousListening()` — long-lived GET for server→client notifications
- `WithHTTPOAuth(OAuthConfig)`

### Client construction

```go
import "github.com/mark3labs/mcp-go/client"

c := client.NewClient(trans)          // no error — always succeeds
err = c.Start(ctx)                    // opens the transport connection
defer c.Close()
```

`NewClient` takes the transport plus optional `ClientOption` variadic args.

### Initialization (MCP handshake)

```go
import "github.com/mark3labs/mcp-go/mcp"

initReq := mcp.InitializeRequest{}
initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
initReq.Params.ClientInfo = mcp.Implementation{Name: "open-bbcd", Version: "0.1"}
initReq.Params.Capabilities = mcp.ClientCapabilities{}

result, err := c.Initialize(ctx, initReq)
// result.ServerInfo.Name, result.Capabilities.Tools != nil → has tools
```

**Differs from plan's assumption**: the plan assumed `c.Initialize(ctx)` (no args).
The real signature is `c.Initialize(ctx, mcp.InitializeRequest)`. The request
must carry `ProtocolVersion`, `ClientInfo`, and `Capabilities`.

### Listing tools

```go
toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
// toolsResult.Tools []mcp.Tool
// each tool: .Name string, .Description string, .InputSchema mcp.ToolInputSchema
```

**Differs from plan's assumption**: the plan assumed `c.ListTools(ctx)` (context only).
Real signature is `c.ListTools(ctx, mcp.ListToolsRequest{})`. The empty struct
is fine for default pagination.

### Calling a tool

```go
callReq := mcp.CallToolRequest{}
callReq.Params.Name = "tool_name"
callReq.Params.Arguments = map[string]any{ ... } // or json.RawMessage

result, err := c.CallTool(ctx, callReq)
// result.Content []mcp.Content  — interface; concrete types:
//   mcp.TextContent{Type:"text", Text: string}
//   mcp.ImageContent{Type:"image", Data: string, MimeType: string}
//   mcp.EmbeddedResource{...}
// result.IsError bool
```

**Content-block handling note**: `result.Content` is `[]mcp.Content` where
`Content` is an interface. Concrete types are `TextContent`, `ImageContent`,
`AudioContent`, `EmbeddedResource`. Task 13 will need to type-switch over these
and flatten to a JSON blob (simplest strategy: marshal the slice to JSON and
treat it as the tool result output, or concatenate `TextContent.Text` values
for the common text-only case).

### InputSchema shape

`mcp.Tool.InputSchema` is typed as `mcp.ToolInputSchema` (not raw JSON). To
pass it as `llm.ToolDef.InputSchema json.RawMessage`, Task 13 will need to
`json.Marshal(tool.InputSchema)` for each tool. The field is a struct with
`Type string` and `Properties map[string]any` — it marshals cleanly to standard
JSON Schema.

Alternatively, `tool.RawInputSchema json.RawMessage` is available but tagged
`json:"-"` so it is only populated when explicitly set server-side; don't rely on it.

## Smoke test status

The smoke test in `mcp_smoke_test.go` is written with the actual library API
(not the plan's stub). It has NOT been run against a real server yet — the URL
`https://demo.mcp.run/mcp` is a placeholder; that public playground may or may
not require auth. The test uses `t.Skipf` on any network error so it degrades
gracefully. Run with:

```bash
go test -tags=mcp_smoke ./internal/llm/tools -run MCPSmoke -v
```

## Summary for Task 13

The two biggest divergences from the plan's assumed API:

1. `Initialize` requires a full `mcp.InitializeRequest{}` struct, not just `ctx`.
2. `ListTools` requires `mcp.ListToolsRequest{}` as second arg, not just `ctx`.
3. `CallTool` (not `Call`) takes `mcp.CallToolRequest{}` with `.Params.Name` and
   `.Params.Arguments` — then returns `*mcp.CallToolResult` with a `Content []mcp.Content`
   interface slice that needs flattening.

The library is well-suited for our needs. No blocking issues before Task 13.
