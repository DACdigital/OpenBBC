// Package tools defines a provider-agnostic tool handler for the LLM
// orchestrator. v1 ships MockHandler — the Skill meta-tool returns real
// skill prompts from the agent's bundle; capability tools return stub
// JSON. Real MCP wiring replaces MockHandler in a follow-up.
package tools

import (
	"context"
	"encoding/json"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// Call is a single tool invocation from the model.
type Call struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Result is what the handler produces for a Call. Output is serialized
// into a tool_result content block on the next turn. IsError signals
// to the model that the call failed; the model decides whether to retry
// or surface the failure.
type Result struct {
	ToolUseID string
	Output    json.RawMessage
	IsError   bool
}

// Handler builds the LLM-visible tool list from an agent's bundle and
// routes tool invocations to their backend (real or mocked).
type Handler interface {
	// Tools returns the tool defs the LLM will see, derived from the agent's bundle.
	Tools(bundle json.RawMessage) ([]llm.ToolDef, error)

	// Call routes a tool invocation. The bundle is passed in so handlers
	// that depend on bundle content (e.g., the Skill meta-tool looking up
	// bundle.skills[X].prompt) have access without separate state.
	Call(ctx context.Context, bundle json.RawMessage, call Call) (Result, error)
}
