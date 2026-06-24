package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// Composite is the wired-in tools.Handler. It owns:
//  1. The built-in Skill meta-tool (bundle-driven, identical to MockHandler).
//  2. N runtime Backends constructed per chat session.
//
// Composite is NOT safe for concurrent use across sessions: each chat session
// builds its own Composite via BackendBuilder.
type Composite struct {
	backends []Backend
}

func NewComposite(backends []Backend) *Composite { return &Composite{backends: backends} }

func (c *Composite) Tools(bundle json.RawMessage) ([]llm.ToolDef, error) {
	out := []llm.ToolDef{}

	// Skill meta-tool first (consistent placement for the LLM).
	skill, err := buildSkillToolDef(bundle)
	if err != nil {
		return nil, err
	}
	out = append(out, skill)

	for _, be := range c.backends {
		defs, err := be.Tools(context.Background())
		if err != nil {
			return nil, fmt.Errorf("tools: backend %s Tools(): %w", be.Name(), err)
		}
		out = append(out, defs...)
	}
	return out, nil
}

func (c *Composite) Call(ctx context.Context, bundle json.RawMessage, call Call) (Result, error) {
	if call.Name == "Skill" {
		return callSkillMetaTool(bundle, call)
	}
	for _, be := range c.backends {
		prefix := be.Name() + "__"
		if strings.HasPrefix(call.Name, prefix) {
			return be.Call(ctx, strings.TrimPrefix(call.Name, prefix), call.Input)
		}
	}
	// No prefix → try each backend's Tools() to find an owner (HTTP backend
	// tools aren't prefixed, since each tool name is unique across endpoints
	// within the agent version).
	for _, be := range c.backends {
		defs, _ := be.Tools(ctx)
		for _, d := range defs {
			if d.Name == call.Name {
				return be.Call(ctx, call.Name, call.Input)
			}
		}
	}
	return Result{ToolUseID: call.ID}, fmt.Errorf("tools: no backend owns %q", call.Name)
}

var _ Handler = (*Composite)(nil)

// --- Skill meta-tool shared helpers ---

// bundleSkillShape is the minimum bundle shape needed to resolve skills.
// It is intentionally separate from bundleShape (MockHandler's full shape)
// so the two handlers remain independently evolvable.
type bundleSkillShape struct {
	Skills []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	} `json:"skills"`
}

func buildSkillToolDef(bundle json.RawMessage) (llm.ToolDef, error) {
	var b bundleSkillShape
	if len(bundle) > 0 {
		if err := json.Unmarshal(bundle, &b); err != nil {
			return llm.ToolDef{}, fmt.Errorf("tools: parse bundle for Skill: %w", err)
		}
	}
	names := make([]string, len(b.Skills))
	for i, s := range b.Skills {
		names[i] = s.Name
	}
	schema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "enum": names},
		},
		"required": []string{"name"},
	})
	if err != nil {
		return llm.ToolDef{}, err
	}
	return llm.ToolDef{
		Name:        "Skill",
		Description: "Load the prompt for a named skill into your working context. Use when the user's intent matches a skill in skills_index.",
		InputSchema: schema,
	}, nil
}

func callSkillMetaTool(bundle json.RawMessage, call Call) (Result, error) {
	var b bundleSkillShape
	if err := json.Unmarshal(bundle, &b); err != nil {
		return Result{ToolUseID: call.ID}, fmt.Errorf("tools: parse bundle for Skill call: %w", err)
	}
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return Result{ToolUseID: call.ID}, fmt.Errorf("tools: parse Skill input: %w", err)
	}
	for _, s := range b.Skills {
		if s.Name == args.Name {
			out, _ := json.Marshal(map[string]string{"prompt": s.Prompt})
			return Result{ToolUseID: call.ID, Output: out}, nil
		}
	}
	out, _ := json.Marshal(map[string]string{"error": "unknown skill: " + args.Name})
	return Result{ToolUseID: call.ID, Output: out, IsError: true}, nil
}
