package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// BundleSnapshot is the minimum bundle slice the builder reads, scoped to
// what backends need at construction time.
type BundleSnapshot struct {
	Tools []BundleToolSnap `json:"tools"`
}
type BundleToolSnap struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Method      string      `json:"method"`
	Path        string      `json:"path"`
	PathParams  []ParamSpec `json:"path_params"`
	QueryParams []ParamSpec `json:"query_params"`
	BodyShape   any         `json:"body_shape"`
}

// BackendStore is the minimum repo dep the builder needs. Implemented in
// handler/api.go by toolBackendStoreAdapter wrapping the two repositories.
type BackendStore interface {
	GetBackend(ctx context.Context, id string) (kind string, name string, configJSON json.RawMessage, err error)
	EndpointBackends(ctx context.Context, versionID string) (map[string]string, error)
	MCPAttachments(ctx context.Context, versionID string) (map[string]string, error) // backend_id → note
}

// Builder constructs the Composite handler for one chat session.
type Builder struct{ store BackendStore }

func NewBuilder(store BackendStore) *Builder { return &Builder{store: store} }

// Build returns a tools.Handler (concretely a *Composite). Returning the
// interface lets the orchestrator depend only on ToolHandlerBuilder without
// a leak of the impl type.
func (b *Builder) Build(ctx context.Context, versionID string, bundle json.RawMessage) (Handler, error) {
	mapping, err := b.store.EndpointBackends(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("builder: load endpoint mapping: %w", err)
	}
	mcp, err := b.store.MCPAttachments(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("builder: load MCP attachments: %w", err)
	}

	var snap BundleSnapshot
	if len(bundle) > 0 {
		if err := json.Unmarshal(bundle, &snap); err != nil {
			return nil, fmt.Errorf("builder: parse bundle: %w", err)
		}
	}

	// Group endpoints by their assigned backend. Endpoints with no mapping
	// go into an "unmapped" bucket and are exposed via a synthetic backend
	// that fails loudly on call — this surfaces the misconfiguration in the
	// chat as a real tool_result block instead of silent LLM hallucination.
	byBackend := map[string][]HTTPEndpointDef{}
	var unmapped []HTTPEndpointDef
	for _, t := range snap.Tools {
		// Sanitize the LLM-visible name to the Anthropic-required charset
		// (^[a-zA-Z0-9_-]{1,128}$). The stable id (t.ID) is preserved
		// separately for FK lookup against the wiring table.
		ep := HTTPEndpointDef{
			ID: t.ID, Name: sanitizeToolName(t.Name), Description: t.Description,
			Method: t.Method, Path: t.Path,
			PathParams: t.PathParams, QueryParams: t.QueryParams, BodyShape: t.BodyShape,
		}
		if bid, ok := mapping[t.ID]; ok {
			byBackend[bid] = append(byBackend[bid], ep)
		} else {
			unmapped = append(unmapped, ep)
		}
	}

	backends := []Backend{}
	for bid, eps := range byBackend {
		kind, name, cfgJSON, err := b.store.GetBackend(ctx, bid)
		if err != nil {
			return nil, fmt.Errorf("builder: load backend %s: %w", bid, err)
		}
		if kind != "http_endpoint" {
			continue
		}
		var cfg HTTPBackendCfg
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			return nil, fmt.Errorf("builder: parse config for backend %s: %w", bid, err)
		}
		m := map[string]string{}
		for _, ep := range eps {
			m[ep.ID] = bid
		}
		backends = append(backends, NewHTTPEndpointBackend(name, bid, cfg, eps, m))
	}

	// Build MCP backends from the attachment list.
	for bid := range mcp {
		kind, name, cfgJSON, err := b.store.GetBackend(ctx, bid)
		if err != nil {
			return nil, fmt.Errorf("builder: load backend %s: %w", bid, err)
		}
		if kind != "mcp_client" {
			continue
		}
		var cfg MCPBackendCfg
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			return nil, fmt.Errorf("builder: parse mcp config for backend %s: %w", bid, err)
		}
		backends = append(backends, NewMCPClientBackend(name, bid, cfg))
	}

	// Synthetic backend for unmapped endpoints. Always last so explicit
	// HTTP backends win on prefix-less name routing in Composite.Call.
	if len(unmapped) > 0 {
		backends = append(backends, newUnmappedBackend(unmapped))
	}

	return NewComposite(backends), nil
}
