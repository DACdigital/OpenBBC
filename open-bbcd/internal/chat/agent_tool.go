package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// agentToolDescriptionHead is the fixed part of the agent tool description
// (spec § LLM tool contract — agent); one "- <name>: <note>" line per
// binding follows it.
const agentToolDescriptionHead = "Delegate a self-contained task to a specialised sub-agent. The sub-agent starts with NO access to this conversation or its files — put everything it needs in `prompt`. It returns its final answer as text.\n\nAvailable sub-agents:\n"

// skillToolName is the Skill meta-tool's name (tools.Composite); the agent
// tool is listed right after it.
const skillToolName = "Skill"

// agentToolError is an agent-tool failure surfaced to the calling LLM as an
// is_error tool result "<code>: <details>" (spec § LLM tool contract).
type agentToolError struct {
	Code    string // max_depth_exceeded | unknown_subagent | invalid_input | session_locked | subagent_max_tool_rounds | subagent_failed | cancelled
	Details string
}

func (e *agentToolError) Error() string { return e.Code + ": " + e.Details }

// agentInput is the decoded tool_use input of an agent call.
type agentInput struct {
	Subagent    string `json:"subagent"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

type agentSchemaProp struct {
	Type        string   `json:"type"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description,omitempty"`
}

type agentSchema struct {
	Type                 string                     `json:"type"`
	Required             []string                   `json:"required"`
	AdditionalProperties bool                       `json:"additionalProperties"`
	Properties           map[string]agentSchemaProp `json:"properties"`
}

// agentToolDef builds the agent tool definition for a caller version's
// bindings. Bindings are listed by name. A binding with an empty (or
// whitespace-only) note renders as "- <name>" with no trailing colon.
func agentToolDef(bindings []types.SubAgentBinding) llm.ToolDef {
	sorted := make([]types.SubAgentBinding, len(bindings))
	copy(sorted, bindings)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var desc strings.Builder
	desc.WriteString(agentToolDescriptionHead)
	names := make([]string, 0, len(sorted))
	for i, b := range sorted {
		if i > 0 {
			desc.WriteByte('\n')
		}
		desc.WriteString("- ")
		desc.WriteString(b.Name)
		if note := strings.TrimSpace(b.Note); note != "" {
			desc.WriteString(": ")
			desc.WriteString(note)
		}
		names = append(names, b.Name)
	}

	schema, err := json.Marshal(agentSchema{
		Type:                 "object",
		Required:             []string{"subagent", "description", "prompt"},
		AdditionalProperties: false,
		Properties: map[string]agentSchemaProp{
			"subagent":    {Type: "string", Enum: names},
			"description": {Type: "string", Description: "3-10 word summary of the task, shown to the user as progress"},
			"prompt":      {Type: "string", Description: "Full task instructions for the sub-agent"},
		},
	})
	if err != nil {
		panic("chat: marshal agent tool schema: " + err.Error()) // static shape; cannot fail
	}
	return llm.ToolDef{Name: tools.AgentToolName, Description: desc.String(), InputSchema: schema}
}

// parseAgentInput decodes an agent call's tool_use input strictly: unknown
// properties, trailing data, wrong types and missing or blank fields are all
// invalid_input.
func parseAgentInput(raw json.RawMessage) (agentInput, error) {
	invalid := func(details string) error {
		return &agentToolError{Code: "invalid_input", Details: details}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return agentInput{}, invalid("input must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var in agentInput
	if err := dec.Decode(&in); err != nil {
		return agentInput{}, invalid(err.Error())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return agentInput{}, invalid("trailing data after input object")
	}
	for _, f := range []struct{ name, val string }{
		{"subagent", in.Subagent},
		{"description", in.Description},
		{"prompt", in.Prompt},
	} {
		if strings.TrimSpace(f.val) == "" {
			return agentInput{}, invalid(f.name + " is required")
		}
	}
	return in, nil
}

// insertAgentToolDef places def immediately after the Skill tool, or first
// when there is no Skill tool.
func insertAgentToolDef(defs []llm.ToolDef, def llm.ToolDef) []llm.ToolDef {
	at := 0
	for i, d := range defs {
		if d.Name == skillToolName {
			at = i + 1
			break
		}
	}
	out := make([]llm.ToolDef, 0, len(defs)+1)
	out = append(out, defs[:at]...)
	out = append(out, def)
	return append(out, defs[at:]...)
}

// agentErrorResult is the is_error tool result for a failed agent call:
// content is the JSON string "<code>: <details>".
func agentErrorResult(toolUseID string, err error) llm.ToolResultBlock {
	msg, _ := json.Marshal(err.Error())
	return llm.ToolResultBlock{ToolUseID: toolUseID, Result: msg, IsError: true}
}
