package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// MockHandler is the v1 tool handler.
//
//   - The "Skill" meta-tool is REAL: Call looks up bundle.skills[<name>].prompt
//     and returns it. The LLM then folds the skill prompt into its working
//     context for subsequent turns. This mirrors Claude Code's Skill loading.
//   - Each capability tool is MOCKED: Call echoes the input with a _mocked
//     marker. The orchestrator's tool-call loop still runs end-to-end; only
//     the actual backend call is stubbed. Real MCP wiring replaces this
//     handler in a follow-up.
type MockHandler struct{}

func NewMockHandler() *MockHandler { return &MockHandler{} }

// bundleShape is the minimum bundle slice MockHandler needs to do its
// job. Defined here (not types/) because it's an implementation detail
// of how the handler reads the bundle JSON.
type bundleShape struct {
	Skills []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	} `json:"skills"`
	Capabilities []struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		ProposedTool string `json:"proposed_tool"`
	} `json:"capabilities"`
}

func parseBundle(bundle json.RawMessage) (bundleShape, error) {
	var b bundleShape
	if len(bundle) == 0 {
		return b, fmt.Errorf("tools: empty bundle")
	}
	if err := json.Unmarshal(bundle, &b); err != nil {
		return b, fmt.Errorf("tools: parse bundle: %w", err)
	}
	return b, nil
}

func (h *MockHandler) Tools(bundle json.RawMessage) ([]llm.ToolDef, error) {
	b, err := parseBundle(bundle)
	if err != nil {
		return nil, err
	}

	out := make([]llm.ToolDef, 0, 1+len(b.Capabilities))

	// The Skill meta-tool's input schema enumerates the skill names so the
	// LLM gets autocomplete-style guidance and can't hallucinate skill names.
	skillNames := make([]string, len(b.Skills))
	for i, s := range b.Skills {
		skillNames[i] = s.Name
	}
	skillSchema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type": "string",
				"enum": skillNames,
			},
		},
		"required": []string{"name"},
	})
	if err != nil {
		return nil, fmt.Errorf("tools: marshal Skill schema: %w", err)
	}
	out = append(out, llm.ToolDef{
		Name:        "Skill",
		Description: "Load the prompt for a named skill into your working context. Use when the user's intent matches a skill in skills_index.",
		InputSchema: skillSchema,
	})

	// Permissive schema for capability tools — real schemas arrive with real
	// MCP wiring. additionalProperties=true accepts any object.
	permissiveSchema, err := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": true,
	})
	if err != nil {
		return nil, fmt.Errorf("tools: marshal capability schema: %w", err)
	}
	for _, c := range b.Capabilities {
		if c.ProposedTool == "" {
			continue
		}
		out = append(out, llm.ToolDef{
			Name:        c.ProposedTool,
			Description: c.Description,
			InputSchema: permissiveSchema,
		})
	}
	return out, nil
}

func (h *MockHandler) Call(ctx context.Context, bundle json.RawMessage, call Call) (Result, error) {
	b, err := parseBundle(bundle)
	if err != nil {
		return Result{}, err
	}

	// Skill meta-tool — real lookup.
	if call.Name == "Skill" {
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(call.Input, &args); err != nil {
			return Result{}, fmt.Errorf("tools: parse Skill input: %w", err)
		}
		for _, s := range b.Skills {
			if s.Name == args.Name {
				out, _ := json.Marshal(map[string]string{"prompt": s.Prompt})
				return Result{ToolUseID: call.ID, Output: out, IsError: false}, nil
			}
		}
		out, _ := json.Marshal(map[string]string{"error": "unknown skill: " + args.Name})
		return Result{ToolUseID: call.ID, Output: out, IsError: true}, nil
	}

	// Capability tool — mocked echo. Input echoed back as raw JSON so the
	// LLM sees what it sent (useful for debugging during BO testing).
	out, _ := json.Marshal(map[string]any{
		"_mocked":    true,
		"capability": call.Name,
		"input":      json.RawMessage(call.Input),
		"note":       "MCP wiring lands in a follow-up; this is a stub.",
	})
	return Result{ToolUseID: call.ID, Output: out, IsError: false}, nil
}

// Compile-time interface conformance check.
var _ Handler = (*MockHandler)(nil)
