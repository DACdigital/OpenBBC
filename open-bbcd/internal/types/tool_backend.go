package types

import (
	"encoding/json"
	"time"
)

type ToolBackendKind string

const (
	ToolBackendKindHTTPEndpoint ToolBackendKind = "http_endpoint"
	ToolBackendKindMCPClient    ToolBackendKind = "mcp_client"
)

type ToolBackend struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Kind      ToolBackendKind `json:"kind"`
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type HTTPBackendConfig struct {
	BaseURL          string            `json:"base_url"`
	DefaultHeaders   map[string]string `json:"default_headers,omitempty"`
	ForwardedHeaders []string          `json:"forwarded_headers,omitempty"`
}

type MCPBackendConfig struct {
	URL            string            `json:"url"`
	Transport      string            `json:"transport"` // "streamable_http" | "sse"
	DefaultHeaders map[string]string `json:"default_headers,omitempty"`
}
