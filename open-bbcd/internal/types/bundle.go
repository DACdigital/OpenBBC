package types

import (
	"encoding/json"
	"fmt"
)

// Bundle is the shape emitted by aikdm. The Go side never edits a bundle —
// it only reads (to render configurator UI) and splits (when landing the
// bundle onto the agent + version pair). Fields are json.RawMessage so the
// split is shape-preserving: anything aikdm puts under a known key passes
// through verbatim into Architecture or Prompts.
type Bundle struct {
	Metadata        json.RawMessage   `json:"metadata,omitempty"`
	MainPrompt      string            `json:"main_prompt"`
	Tools           json.RawMessage   `json:"tools,omitempty"`
	Skills          []BundleSkill     `json:"skills,omitempty"`
	Flows           json.RawMessage   `json:"flows,omitempty"`
	ExternalActions json.RawMessage   `json:"external_actions,omitempty"`
}

// BundleSkill mirrors the per-skill block in the aikdm bundle. Description
// is metadata (goes to Architecture.SkillsMeta); Prompt is the
// version-editable text (goes to Prompts.SkillPrompts).
type BundleSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
}

// Architecture is the agent-level frozen payload: structural metadata that
// can't change across versions of the same agent. JSON shape matches the
// agents.architecture JSONB column.
type Architecture struct {
	Metadata        json.RawMessage  `json:"metadata,omitempty"`
	Tools           json.RawMessage  `json:"tools,omitempty"`
	Flows           json.RawMessage  `json:"flows,omitempty"`
	ExternalActions json.RawMessage  `json:"external_actions,omitempty"`
	SkillsMeta      []SkillMetaEntry `json:"skills_meta,omitempty"`
}

// SkillMetaEntry is the per-skill architecture row (name + description).
// The skill's prompt lives on Prompts.SkillPrompts keyed by Name.
type SkillMetaEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Prompts is the version-level editable payload. JSON shape matches the
// agent_versions.prompts JSONB column.
type Prompts struct {
	MainPrompt   string            `json:"main_prompt"`
	SkillPrompts map[string]string `json:"skill_prompts,omitempty"`
}

// SplitBundle decomposes an aikdm bundle JSON blob into the
// agent-architecture and version-prompts payloads. This is the canonical
// split point — every code path that lands a bundle (HTTP endpoint, seed
// script, future background job) must go through here (or the seed script
// must mirror the same logic in Python).
func SplitBundle(bundleJSON json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	var b Bundle
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		return nil, nil, fmt.Errorf("parse bundle: %w", err)
	}

	arch := Architecture{
		Metadata:        b.Metadata,
		Tools:           b.Tools,
		Flows:           b.Flows,
		ExternalActions: b.ExternalActions,
		SkillsMeta:      make([]SkillMetaEntry, 0, len(b.Skills)),
	}
	prompts := Prompts{
		MainPrompt:   b.MainPrompt,
		SkillPrompts: map[string]string{},
	}
	for _, s := range b.Skills {
		arch.SkillsMeta = append(arch.SkillsMeta, SkillMetaEntry{Name: s.Name, Description: s.Description})
		if s.Prompt != "" {
			prompts.SkillPrompts[s.Name] = s.Prompt
		}
	}

	archJSON, err := json.Marshal(arch)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal architecture: %w", err)
	}
	promptsJSON, err := json.Marshal(prompts)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal prompts: %w", err)
	}
	return archJSON, promptsJSON, nil
}
