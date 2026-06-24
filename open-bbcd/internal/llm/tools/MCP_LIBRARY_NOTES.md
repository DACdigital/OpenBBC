# MCP Client Library Notes

## Library chosen

**`github.com/modelcontextprotocol/go-sdk` v1.6.1** (official SDK)

This is the official Go SDK for the Model Context Protocol, published by the
`modelcontextprotocol` organisation on GitHub. It reached a stable v1.x API in
May 2026 and aligns tightly with the MCP specification. It ships built-in
support for OAuth flows, retry semantics, and the full 2025 spec revisions.

### Why we switched from mark3labs

We previously used `github.com/mark3labs/mcp-go` v0.55.0 (a community
implementation). The switch was made at the cheapest point (before a PR opened)
for the following reasons:

- mark3labs remains v0.x; the official SDK is v1.x with a stable API contract.
- Better long-term maintenance guarantee from the protocol authors.
- Built-in OAuth and retry support that mark3labs lacked.
- Tighter alignment with the spec as it evolves.

## Key API surface

### Transport construction (streamable-HTTP)

```go
import "github.com/modelcontextprotocol/go-sdk/mcp"

transport := &mcp.StreamableClientTransport{
    Endpoint:             url,         // string
    HTTPClient:           httpClient,  // *http.Client (nil → http.DefaultClient)
    DisableStandaloneSSE: true,        // see note below
}
```

**No built-in headers option.** To inject static headers (e.g. auth tokens), wrap
`http.Client` with a custom `RoundTripper`:

```go
type headerInjectingTransport struct {
    base    http.RoundTripper
    headers map[string]string
}

func (h *headerInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    for k, v := range h.headers {
        req.Header.Set(k, v)
    }
    return h.base.RoundTrip(req)
}
```

#### `DisableStandaloneSSE: true`

The official SDK normally opens a long-lived GET SSE stream after the POST
initialize handshake to receive server-initiated notifications. We set this flag
to `true` because open-bbcd is a simple request-response client — it never
consumes server-initiated notifications today. Disabling it simplifies the
protocol exchange and makes stub-server unit tests much easier to write (the stub
doesn't have to speak SSE).

### Client construction and session lifecycle

```go
client := mcp.NewClient(&mcp.Implementation{
    Name:    "open-bbcd",
    Version: "0.1",
}, nil)

// Connect performs the MCP handshake (initialize → initialized notification).
session, err := client.Connect(ctx, transport, nil)
// session.Close() to disconnect
```

A single `*mcp.Client` can open many sessions. In our code we maintain one
session per MCPClientBackend per chat session (lazy, cached).

### Listing tools

```go
result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
// result.Tools []*mcp.Tool   ← pointer slice
// each tool: .Name string, .Description string, .InputSchema any
```

`InputSchema` is `any` (already JSON-Schema-shaped). `json.Marshal(t.InputSchema)`
gives you schema bytes directly.

### Calling a tool

```go
result, err := session.CallTool(ctx, &mcp.CallToolParams{
    Name:      "tool_name",
    Arguments: args,  // any (typically map[string]any)
})
// result.Content []mcp.Content — interface; concrete types are POINTERS
// result.IsError bool
```

### Content blocks

Content block types are **pointer** receivers in the official SDK:

```go
for _, blk := range result.Content {
    if tc, ok := blk.(*mcp.TextContent); ok {
        // tc.Text string
    }
    // *mcp.ImageContent, *mcp.AudioContent, *mcp.EmbeddedResource also available
}
```

This differs from mark3labs where the same types were concrete (value receivers).

## Stub-server protocol notes (for unit tests)

The official SDK speaks standard JSON-RPC 2.0 over HTTP POST. With
`DisableStandaloneSSE: true` the stub only needs to handle:

1. `initialize` → respond with `{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{...}}`
2. `tools/list` → respond with `{"tools":[...]}`
3. `tools/call` → respond with `{"content":[...],"isError":false}`

No `notifications/initialized` response is expected — the SDK sends it as a
fire-and-forget notification and does not wait for a reply.

## Smoke test status

`mcp_smoke_test.go` (build tag `mcp_smoke`) is updated for the official SDK API.
Run against a live server with:

```bash
go test -tags=mcp_smoke ./internal/llm/tools -run MCPSmoke -v
```

The test uses `t.Skipf` on network errors so it degrades gracefully in CI.
