package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUnmappedBackend_Tools_AdvertisesEndpointsWithWarning(t *testing.T) {
	ep := HTTPEndpointDef{
		ID: "products.list", Name: "products_list",
		Description: "List products in the catalog.",
		Method:      "GET", Path: "/api/products",
	}
	be := newUnmappedBackend([]HTTPEndpointDef{ep})
	defs, err := be.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "products_list" {
		t.Fatalf("want 1 tool products_list, got %+v", defs)
	}
	if !strings.Contains(defs[0].Description, "NOT YET CONFIGURED") {
		t.Fatalf("description should warn the LLM, got %q", defs[0].Description)
	}
}

func TestUnmappedBackend_Call_ReturnsIsErrorWithGuidance(t *testing.T) {
	ep := HTTPEndpointDef{
		ID: "products.list", Name: "products_list",
		Method: "GET", Path: "/api/products",
	}
	be := newUnmappedBackend([]HTTPEndpointDef{ep})
	res, _ := be.Call(context.Background(), "products_list", json.RawMessage(`{}`))
	if !res.IsError {
		t.Fatal("expected IsError true")
	}
	var payload map[string]any
	if err := json.Unmarshal(res.Output, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["error"] != "endpoint_not_connected" {
		t.Fatalf("want error=endpoint_not_connected, got %v", payload["error"])
	}
	if payload["endpoint"] != "products_list" {
		t.Fatalf("want endpoint=products_list, got %v", payload["endpoint"])
	}
	if payload["method"] != "GET" || payload["path"] != "/api/products" {
		t.Fatalf("missing method/path on payload: %+v", payload)
	}
}

func TestBuilder_AddsUnmappedBackendForUnconfiguredEndpoints(t *testing.T) {
	store := &fakeBackendStore{
		endpointMap: map[string]string{
			"orders.create": "backend-1",
			// products.list is intentionally NOT mapped
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
			{"id": "orders.create", "name": "orders_create", "method": "POST", "path": "/api/orders"},
			{"id": "products.list", "name": "products_list", "method": "GET", "path": "/api/products"}
		]
	}`)
	b := NewBuilder(store)
	handler, err := b.Build(context.Background(), "agent-1", "v1", bundle)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defs, err := handler.Tools(bundle)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	// Should see: Skill + orders_create (mapped) + products_list (unmapped) = 3
	names := map[string]string{}
	for _, d := range defs {
		names[d.Name] = d.Description
	}
	if _, ok := names["products_list"]; !ok {
		t.Fatalf("products_list should be exposed even when unmapped; got %v", names)
	}
	if !strings.Contains(names["products_list"], "NOT YET CONFIGURED") {
		t.Fatalf("unmapped tool description should warn: %q", names["products_list"])
	}

	// Calling the unmapped tool should return IsError with guidance.
	res, err := handler.Call(context.Background(), bundle, Call{ID: "x", Name: "products_list", Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("unmapped tool should IsError, got %s", string(res.Output))
	}
	if !strings.Contains(string(res.Output), "endpoint_not_connected") {
		t.Fatalf("expected guidance payload, got %s", string(res.Output))
	}
}
