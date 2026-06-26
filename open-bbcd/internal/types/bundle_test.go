package types

import (
	"encoding/json"
	"testing"
)

func TestSplitBundle_ProducesArchitectureAndPrompts(t *testing.T) {
	bundle := []byte(`{
		"main_prompt": "<role>bot</role>",
		"tools": [{"id":"orders.create","name":"orders_create"}],
		"skills": [
			{"name":"place_order","description":"Take an order","prompt":"<role>order</role>"},
			{"name":"check_status","description":"Check status","prompt":""}
		],
		"external_actions": [{"skill_id":"escalate","external_note":"file in portal"}],
		"flows": [{"id":"buy"}]
	}`)

	archJSON, promptsJSON, err := SplitBundle(bundle)
	if err != nil {
		t.Fatalf("SplitBundle: %v", err)
	}

	var arch Architecture
	if err := json.Unmarshal(archJSON, &arch); err != nil {
		t.Fatalf("parse arch: %v", err)
	}
	if len(arch.SkillsMeta) != 2 {
		t.Fatalf("skills_meta len: want 2, got %d", len(arch.SkillsMeta))
	}
	if arch.SkillsMeta[0].Name != "place_order" || arch.SkillsMeta[0].Description != "Take an order" {
		t.Fatalf("skills_meta[0] wrong: %+v", arch.SkillsMeta[0])
	}
	if len(arch.Tools) == 0 {
		t.Fatalf("tools should be carried into architecture")
	}
	if len(arch.ExternalActions) == 0 {
		t.Fatalf("external_actions should be carried into architecture")
	}

	var prompts Prompts
	if err := json.Unmarshal(promptsJSON, &prompts); err != nil {
		t.Fatalf("parse prompts: %v", err)
	}
	if prompts.MainPrompt != "<role>bot</role>" {
		t.Fatalf("main_prompt wrong: %q", prompts.MainPrompt)
	}
	if prompts.SkillPrompts["place_order"] != "<role>order</role>" {
		t.Fatalf("skill_prompts[place_order] wrong: %q", prompts.SkillPrompts["place_order"])
	}
	// Empty skill prompts should not be persisted (avoid noise).
	if _, ok := prompts.SkillPrompts["check_status"]; ok {
		t.Fatalf("empty skill prompt should be omitted")
	}
}

func TestSplitBundle_HandlesEmptyBundle(t *testing.T) {
	archJSON, promptsJSON, err := SplitBundle([]byte(`{}`))
	if err != nil {
		t.Fatalf("SplitBundle empty: %v", err)
	}
	var arch Architecture
	if err := json.Unmarshal(archJSON, &arch); err != nil {
		t.Fatalf("parse arch: %v", err)
	}
	var prompts Prompts
	if err := json.Unmarshal(promptsJSON, &prompts); err != nil {
		t.Fatalf("parse prompts: %v", err)
	}
	if prompts.MainPrompt != "" {
		t.Fatalf("empty bundle should yield empty main_prompt, got %q", prompts.MainPrompt)
	}
}

func TestSplitBundle_RejectsMalformed(t *testing.T) {
	if _, _, err := SplitBundle([]byte("not json")); err == nil {
		t.Fatalf("expected error for malformed input")
	}
}
