package tools

import (
	"encoding/json"
	"fmt"
)

// ParamSpec is the runtime view of an endpoint parameter, matching the
// aikdm BundleTool's enriched fields. Kept here (not in types/) so the
// tools package has no inbound dep on a versioned schema package.
type ParamSpec struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// EndpointSchemaInput is the structural input to schema construction —
// derived from BundleTool. body_shape / response_shape are kept as raw any
// (they are arbitrary nested JSON Schema fragments emitted by discovery).
type EndpointSchemaInput struct {
	PathParams    []ParamSpec
	QueryParams   []ParamSpec
	BodyShape     any
	ResponseShape any
}

// BuildEndpointSchema returns a JSON Schema describing the union of path,
// query, and body params. When all three are empty (e.g., aikdm output
// hasn't been enriched yet), returns a permissive object schema so the
// LLM can still call the tool with arbitrary inputs.
//
// BodyShape is expected to be a JSON-Schema-shaped map (`{type, properties,
// required}`). Some discovery pipelines (notably older flow-map-compiler
// runs) instead emit a free-form TypeScript type literal string like
// `"{ items: { productId: string }[] }"`. When BodyShape is present but
// not a usable JSON Schema object, we still declare any path/query params
// we know about and flip `additionalProperties: true` so the LLM can pass
// arbitrary body fields rather than being handed a tool that accepts
// nothing.
func BuildEndpointSchema(ep EndpointSchemaInput) (json.RawMessage, error) {
	if len(ep.PathParams) == 0 && len(ep.QueryParams) == 0 && ep.BodyShape == nil {
		return json.Marshal(map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		})
	}
	props := map[string]any{}
	required := []string{}
	addParam := func(p ParamSpec) {
		t := p.Type
		if t == "" {
			t = "string"
		}
		props[p.Name] = map[string]any{"type": t}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	for _, p := range ep.PathParams {
		addParam(p)
	}
	for _, p := range ep.QueryParams {
		addParam(p)
	}
	bodyDeclared := false
	if ep.BodyShape != nil {
		if bs, ok := ep.BodyShape.(map[string]any); ok {
			if bp, ok := bs["properties"].(map[string]any); ok {
				for k, v := range bp {
					props[k] = v
				}
				bodyDeclared = true
			}
			if br, ok := bs["required"].([]any); ok {
				for _, r := range br {
					if s, ok := r.(string); ok {
						required = append(required, s)
					}
				}
			}
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	if ep.BodyShape != nil && !bodyDeclared {
		schema["additionalProperties"] = true
	}
	out, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("schema marshal: %w", err)
	}
	return out, nil
}
