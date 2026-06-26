package tools

import (
	"context"
	"encoding/json"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// unmappedBackend exposes endpoints declared in the bundle but not yet
// wired to any HTTP backend. It still advertises them as tools to the LLM
// so the model emits real tool_use calls (visible debug boxes in chat)
// rather than hallucinating results in prose. Each Call returns a clear
// IsError result explaining the misconfiguration, which the LLM and the
// operator both see in the chat tool_result block.
type unmappedBackend struct {
	endpoints []HTTPEndpointDef
}

func newUnmappedBackend(endpoints []HTTPEndpointDef) *unmappedBackend {
	return &unmappedBackend{endpoints: endpoints}
}

// Name returns a stable internal identifier. It doesn't collide with
// real backend names because user-facing names can't start with an
// underscore (per the tool_backends.name validation) and the Composite
// handler routes by tool name (unprefixed scan), not by backend name.
func (u *unmappedBackend) Name() string { return "_unmapped" }

func (u *unmappedBackend) Tools(ctx context.Context) ([]llm.ToolDef, error) {
	out := make([]llm.ToolDef, 0, len(u.endpoints))
	for _, ep := range u.endpoints {
		schema, err := BuildEndpointSchema(EndpointSchemaInput{
			PathParams:  ep.PathParams,
			QueryParams: ep.QueryParams,
			BodyShape:   ep.BodyShape,
		})
		if err != nil {
			return nil, err
		}
		desc := ep.Description
		if desc == "" {
			desc = ep.Method + " " + ep.Path
		}
		desc += " (NOT YET CONFIGURED — calling this will return an error explaining the missing backend wiring)"
		out = append(out, llm.ToolDef{
			Name:        ep.Name,
			Description: desc,
			InputSchema: schema,
		})
	}
	return out, nil
}

func (u *unmappedBackend) Call(ctx context.Context, name string, input json.RawMessage) (Result, error) {
	var ep *HTTPEndpointDef
	for i := range u.endpoints {
		if u.endpoints[i].Name == name {
			ep = &u.endpoints[i]
			break
		}
	}
	payload := map[string]any{
		"error":  "endpoint_not_connected",
		"detail": "This endpoint is declared in the agent's bundle but no HTTP backend is assigned in this version. The agent cannot make a real call until an operator wires it.",
		"how_to_fix": "Open the configurator's Endpoints subtab and pick a backend for this endpoint. If no backends exist yet, create one at /mcp.",
		"endpoint": name,
	}
	if ep != nil {
		payload["method"] = ep.Method
		payload["path"] = ep.Path
		payload["endpoint_id"] = ep.ID
	}
	out, _ := json.Marshal(payload)
	return Result{Output: out, IsError: true}, nil
}

var _ Backend = (*unmappedBackend)(nil)
