package tools

import (
	"encoding/json"
	"testing"
)

func TestBuildEndpointSchema_PathAndQueryAndBody(t *testing.T) {
	ep := EndpointSchemaInput{
		PathParams:  []ParamSpec{{Name: "id", Type: "string", Required: true}},
		QueryParams: []ParamSpec{{Name: "expand", Type: "string"}},
		BodyShape:   map[string]any{"type": "object", "properties": map[string]any{"amount": map[string]any{"type": "number"}}, "required": []any{"amount"}},
	}
	s, err := BuildEndpointSchema(ep)
	if err != nil {
		t.Fatalf("BuildEndpointSchema: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(s, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := got["properties"].(map[string]any)
	if _, ok := props["id"]; !ok {
		t.Fatalf("missing id in properties")
	}
	if _, ok := props["expand"]; !ok {
		t.Fatalf("missing expand in properties")
	}
	if _, ok := props["amount"]; !ok {
		t.Fatalf("missing amount in properties")
	}
	req := got["required"].([]any)
	if len(req) != 2 {
		t.Fatalf("want [id, amount] required, got %v", req)
	}
}

func TestBuildEndpointSchema_PermissiveWhenEmpty(t *testing.T) {
	s, err := BuildEndpointSchema(EndpointSchemaInput{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	json.Unmarshal(s, &got)
	if got["additionalProperties"] != true {
		t.Fatalf("expected permissive schema when empty")
	}
}

// Discovery pipelines sometimes emit body_shape as a TS-style type literal
// string instead of a JSON Schema object. Without the additionalProperties
// fallback the LLM is handed a tool that accepts nothing and calls it with
// `{}`, which then 400s on the backend. Regression guard.
func TestBuildEndpointSchema_StringBodyShapeFallsBackToAdditionalProperties(t *testing.T) {
	ep := EndpointSchemaInput{
		BodyShape: "{ items: { productId: string, quantity: number }[] }",
	}
	s, err := BuildEndpointSchema(ep)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(s, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["additionalProperties"] != true {
		t.Fatalf("expected additionalProperties:true for string body_shape, got %v", got)
	}
}

// Same fallback when body_shape is a map but lacks "properties" (e.g.
// discovery emitted a structural fragment that doesn't follow JSON Schema).
func TestBuildEndpointSchema_MapBodyShapeWithoutPropertiesFallsBack(t *testing.T) {
	ep := EndpointSchemaInput{
		PathParams: []ParamSpec{{Name: "id", Type: "string", Required: true}},
		BodyShape:  map[string]any{"type": "object"},
	}
	s, err := BuildEndpointSchema(ep)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	json.Unmarshal(s, &got)
	if got["additionalProperties"] != true {
		t.Fatalf("expected additionalProperties:true when body_shape lacks properties, got %v", got)
	}
	props := got["properties"].(map[string]any)
	if _, ok := props["id"]; !ok {
		t.Fatalf("path param should still be declared, got %v", props)
	}
}

// When body_shape is a proper JSON Schema, additionalProperties stays
// unset so the LLM gets a strict, structured schema.
func TestBuildEndpointSchema_ProperBodyShapeNoAdditionalProperties(t *testing.T) {
	ep := EndpointSchemaInput{
		BodyShape: map[string]any{
			"type":       "object",
			"properties": map[string]any{"amount": map[string]any{"type": "number"}},
		},
	}
	s, err := BuildEndpointSchema(ep)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	json.Unmarshal(s, &got)
	if _, set := got["additionalProperties"]; set {
		t.Fatalf("additionalProperties should be unset for structured body, got %v", got)
	}
}
