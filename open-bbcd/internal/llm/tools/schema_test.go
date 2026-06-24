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
