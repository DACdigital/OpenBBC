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
	Tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Method      string `json:"method"`
		Path        string `json:"path"`
		Auth        string `json:"auth"`
	} `json:"tools"`
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

	out := make([]llm.ToolDef, 0, 1+len(b.Tools))

	// The Skill meta-tool's input schema enumerates the skill names so the
	// LLM gets autocomplete-style guidance and can't hallucinate skill names.
	skillDef, err := buildSkillToolDef(bundle)
	if err != nil {
		return nil, err
	}
	out = append(out, skillDef)

	// Permissive schema for capability tools — real schemas arrive with real
	// MCP wiring. additionalProperties=true accepts any object.
	permissiveSchema, err := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": true,
	})
	if err != nil {
		return nil, fmt.Errorf("tools: marshal tool schema: %w", err)
	}
	for _, t := range b.Tools {
		if t.Name == "" {
			continue
		}
		out = append(out, llm.ToolDef{
			Name:        sanitizeToolName(t.Name),
			Description: t.Description,
			InputSchema: permissiveSchema,
		})
	}
	return out, nil
}

// sanitizeToolName makes a tool name conform to Anthropic's required
// regex `^[a-zA-Z0-9_-]{1,128}$`. The discovery skill emits dotted names
// like "orders.list"; the API rejects those. Replace each disallowed
// rune with '_' and truncate to 128. Idempotent — already-clean names
// pass through unchanged.
func sanitizeToolName(name string) string {
	const max = 128
	out := make([]byte, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_' || r == '-':
			out = append(out, byte(r))
		default:
			out = append(out, '_')
		}
		if len(out) >= max {
			break
		}
	}
	if len(out) == 0 {
		return "tool"
	}
	return string(out)
}

func (h *MockHandler) Call(ctx context.Context, bundle json.RawMessage, call Call) (Result, error) {
	if _, err := parseBundle(bundle); err != nil {
		return Result{}, err
	}

	// Skill meta-tool — real lookup via shared helper.
	if call.Name == "Skill" {
		return callSkillMetaTool(bundle, call)
	}

	// Capability-backed tool — mocked echo. Input echoed back as raw JSON
	// so the LLM (and the BO observer) sees what it sent — useful for
	// debugging during testing.
	out, _ := json.Marshal(map[string]any{
		"_mocked": true,
		"tool":    call.Name,
		"input":   json.RawMessage(call.Input),
		"note":    "MCP wiring lands in a follow-up; this is a stub.",
	})
	return Result{ToolUseID: call.ID, Output: out, IsError: false}, nil
}

// Compile-time interface conformance check.
var _ Handler = (*MockHandler)(nil)
