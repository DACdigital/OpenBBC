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

	// Group endpoints by their assigned backend.
	byBackend := map[string][]HTTPEndpointDef{}
	for _, t := range snap.Tools {
		bid, ok := mapping[t.ID]
		if !ok {
			continue
		}
		byBackend[bid] = append(byBackend[bid], HTTPEndpointDef{
			ID: t.ID, Name: t.Name, Description: t.Description,
			Method: t.Method, Path: t.Path,
			PathParams: t.PathParams, QueryParams: t.QueryParams, BodyShape: t.BodyShape,
		})
	}

	backends := []Backend{}
	for bid, eps := range byBackend {
		kind, name, cfgJSON, err := b.store.GetBackend(ctx, bid)
		if err != nil {
			return nil, err
		}
		if kind != "http_endpoint" {
			continue
		}
		var cfg HTTPBackendCfg
		if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
			return nil, err
		}
		m := map[string]string{}
		for _, ep := range eps {
			m[ep.ID] = bid
		}
		backends = append(backends, NewHTTPEndpointBackend(name, bid, cfg, eps, m))
	}

	// MCP backends built in Task 13 — until then, log + skip.
	_ = mcp

	return NewComposite(backends), nil
}
