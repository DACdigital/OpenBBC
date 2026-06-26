package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// fakeBackendStore implements BackendStore for builder tests.
type fakeBackendStore struct {
	endpointMap map[string]string
	mcpMap      map[string]string
	backends    map[string]fakeBackend
}

type fakeBackend struct {
	kind   string
	name   string
	config json.RawMessage
}

func (f *fakeBackendStore) GetBackend(ctx context.Context, id string) (string, string, json.RawMessage, error) {
	b := f.backends[id]
	return b.kind, b.name, b.config, nil
}

func (f *fakeBackendStore) EndpointBackends(ctx context.Context, versionID string) (map[string]string, error) {
	return f.endpointMap, nil
}

func (f *fakeBackendStore) MCPAttachments(ctx context.Context, versionID string) (map[string]string, error) {
	return f.mcpMap, nil
}

// TestBuilder_Build_ProducesRealJSONSchemaFromEnrichedBundle verifies that
// when the aikdm bundle carries path_params + body_shape (post-Task 10), the
// resulting HTTPEndpointBackend advertises a JSON Schema with the correct
// "required" and "properties" fields — not the permissive fallback.
func TestBuilder_Build_ProducesRealJSONSchemaFromEnrichedBundle(t *testing.T) {
	store := &fakeBackendStore{
		endpointMap: map[string]string{
			"orders.create": "backend-1",
		},
		backends: map[string]fakeBackend{
			"backend-1": {
				kind:   "http_endpoint",
				name:   "api",
				config: json.RawMessage(`{"base_url":"https://api.example.com"}`),
			},
		},
	}

	bundle := json.RawMessage(`{
		"tools": [
			{
				"id": "orders.create",
				"name": "orders_create",
				"description": "Create an order",
				"method": "POST",
				"path": "/api/orders/{customer_id}",
				"path_params": [{"name":"customer_id","type":"string","required":true}],
				"query_params": [],
				"body_shape": {
					"type": "object",
					"properties": {"amount": {"type":"number"}},
					"required": ["amount"]
				}
			}
		]
	}`)

	b := NewBuilder(store)
	handler, err := b.Build(context.Background(), "v1", bundle)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	defs, err := handler.Tools(bundle)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}

	// Should be 2 tools: Skill meta-tool + orders_create from the HTTP backend.
	if len(defs) != 2 {
		t.Fatalf("want 2 tools (Skill + orders_create), got %d", len(defs))
	}

	var orders llm.ToolDef
	for _, d := range defs {
		if d.Name == "orders_create" {
			orders = d
			break
		}
	}
	if orders.Name == "" {
		t.Fatalf("orders_create tool not found in: %+v", defs)
	}

	var schema map[string]any
	if err := json.Unmarshal(orders.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	// Permissive fallback would have only "type" + "additionalProperties".
	// Real schema must have a "required" list including both customer_id (path) + amount (body).
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("expected real schema, got permissive: %v", schema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("missing properties")
	}
	if _, ok := props["customer_id"]; !ok {
		t.Fatalf("customer_id missing from properties: %v", props)
	}
	if _, ok := props["amount"]; !ok {
		t.Fatalf("amount missing from properties: %v", props)
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 2 {
		t.Fatalf("expected required = [customer_id, amount], got %v", schema["required"])
	}
}
