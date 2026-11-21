// Package tools defines a provider-agnostic tool handler for the LLM
// orchestrator. v1 ships MockHandler — the Skill meta-tool returns real
// skill prompts from the agent's bundle; capability tools return stub
// JSON. Real MCP wiring replaces MockHandler in a follow-up.
package tools

import (
	"context"
	"encoding/json"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
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

// Handler builds the LLM-visible tool list for one agent version and routes
// tool invocations to their backend (real or mocked).
//
// arch is the agent's architecture (endpoint tools, skills_meta); skills is
// the version's joined skill list (types.JoinSkills of skills_meta and
// prompts.skill_prompts), which drives the built-in Skill meta-tool.
type Handler interface {
	// Tools returns the tool defs the LLM will see. The Skill tool is first
	// when at least one skill is loadable, and omitted otherwise.
	Tools(arch json.RawMessage, skills []types.BundleSkill) ([]llm.ToolDef, error)

	// Call routes a tool invocation. A Skill call is always answered from
	// skills, whether or not the Skill tool was offered.
	Call(ctx context.Context, arch json.RawMessage, skills []types.BundleSkill, call Call) (Result, error)
}
