package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Composite is the wired-in tools.Handler. It owns:
//  1. The built-in Skill meta-tool (resolved from the version's joined
//     skills, identical to MockHandler).
//  2. N runtime Backends constructed per chat session.
//
// Composite is NOT safe for concurrent use across sessions: each chat session
// builds its own Composite via BackendBuilder.
type Composite struct {
	backends []Backend
}

func NewComposite(backends []Backend) *Composite { return &Composite{backends: backends} }

// Tools ignores arch: each backend already holds its own endpoint list.
func (c *Composite) Tools(_ json.RawMessage, skills []types.BundleSkill) ([]llm.ToolDef, error) {
	out := []llm.ToolDef{}

	// Skill meta-tool first (consistent placement for the LLM), when any
	// skill is loadable.
	if skill, ok, err := buildSkillToolDef(skills); err != nil {
		return nil, err
	} else if ok {
		out = append(out, skill)
	}

	for _, be := range c.backends {
		defs, err := be.Tools(context.Background())
		if err != nil {
			return nil, fmt.Errorf("tools: backend %s Tools(): %w", be.Name(), err)
		}
		out = append(out, defs...)
	}
	return out, nil
}

func (c *Composite) Call(ctx context.Context, _ json.RawMessage, skills []types.BundleSkill, call Call) (Result, error) {
	// Intercepted before backend routing even when Skill was not offered.
	if call.Name == SkillToolName {
		return callSkillMetaTool(skills, call)
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
		defs, err := be.Tools(ctx)
		if err != nil {
			return Result{ToolUseID: call.ID}, fmt.Errorf("tools: backend %s Tools() during routing: %w", be.Name(), err)
		}
		for _, d := range defs {
			if d.Name == call.Name {
				return be.Call(ctx, call.Name, call.Input)
			}
		}
	}
	return Result{ToolUseID: call.ID}, fmt.Errorf("tools: no backend owns %q", call.Name)
}

// Close closes every backend that holds resources (MCP sessions). The
// orchestrator calls it once per turn, root or child (spec § Scope).
func (c *Composite) Close() error {
	var errs []error
	for _, be := range c.backends {
		if cl, ok := be.(io.Closer); ok {
			if err := cl.Close(); err != nil {
				errs = append(errs, fmt.Errorf("tools: close backend %s: %w", be.Name(), err))
			}
		}
	}
	return errors.Join(errs...)
}

var (
	_ Handler   = (*Composite)(nil)
	_ io.Closer = (*Composite)(nil)
)

// --- Skill meta-tool shared helpers ---

// skillToolDescription is the LLM-visible Skill description. aikdm's eval
// target mirrors it (aikdm/tests/fixtures/skill_tool_parity.json pins both).
const skillToolDescription = "Load the prompt for a named skill into your working context. Use when the user's intent matches a skill in skills_index."

// buildSkillToolDef returns the Skill tool def whose enum lists the loadable
// skills in order. ok is false when no skill is loadable: the tool is then
// not offered.
func buildSkillToolDef(skills []types.BundleSkill) (llm.ToolDef, bool, error) {
	loadable := types.LoadableSkills(skills)
	if len(loadable) == 0 {
		return llm.ToolDef{}, false, nil
	}
	names := make([]string, len(loadable))
	for i, s := range loadable {
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
		return llm.ToolDef{}, false, err
	}
	return llm.ToolDef{
		Name:        SkillToolName,
		Description: skillToolDescription,
		InputSchema: schema,
	}, true, nil
}

// callSkillMetaTool answers a Skill call: {"prompt": ...} for a loadable
// name, {"error": "unknown skill: <name>"} (IsError) for anything else,
// including a skills_meta name with an empty prompt.
func callSkillMetaTool(skills []types.BundleSkill, call Call) (Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return Result{ToolUseID: call.ID}, fmt.Errorf("tools: parse Skill input: %w", err)
	}
	for _, s := range types.LoadableSkills(skills) {
		if s.Name == args.Name {
			out, _ := json.Marshal(map[string]string{"prompt": s.Prompt})
			return Result{ToolUseID: call.ID, Output: out}, nil
		}
	}
	out, _ := json.Marshal(map[string]string{"error": "unknown skill: " + args.Name})
	return Result{ToolUseID: call.ID, Output: out, IsError: true}, nil
}
