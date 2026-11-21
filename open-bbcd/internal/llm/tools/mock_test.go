package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMockHandler_Tools_FromBundle(t *testing.T) {
	bundle := []byte(`{
        "skills_meta":[
            {"name":"place_order","description":"d"},
            {"name":"check_rewards","description":"d2"}
        ],
        "tools":[
            {"name":"query_inventory","description":"List stock.","method":"GET","path":"/api/inventory","capability":"inventory"},
            {"name":"submit_order","description":"Place an order.","method":"POST","path":"/api/orders","capability":"orders"}
        ]
    }`)
	skills := prodSkills(t, string(bundle), `{"main_prompt":"M","skill_prompts":{"place_order":"P","check_rewards":"P2"}}`)
	h := NewMockHandler()
	defs, err := h.Tools(bundle, skills)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(defs) != 3 {
		t.Fatalf("expected 3 tool defs (1 Skill + 2 atomic tools), got %d", len(defs))
	}
	if defs[0].Name != "Skill" {
		t.Fatalf("first tool should be Skill, got %q", defs[0].Name)
	}
	// Skill schema enumerates names.
	var schema struct {
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(defs[0].InputSchema, &schema); err != nil {
		t.Fatalf("schema unmarshal: %v", err)
	}
	if len(schema.Properties.Name.Enum) != 2 {
		t.Fatalf("expected 2 enum values, got %d", len(schema.Properties.Name.Enum))
	}
	// Tool names come straight from bundle.tools[].name (no proposed_tool layer).
	if defs[1].Name != "query_inventory" || defs[2].Name != "submit_order" {
		t.Fatalf("unexpected tool names: %q, %q", defs[1].Name, defs[2].Name)
	}
}

func TestMockHandler_Tools_SkipsToolWithEmptyName(t *testing.T) {
	bundle := []byte(`{"skills_meta":[{"name":"s"}],"tools":[{"name":"","description":"d","method":"GET","path":"/x"},{"name":"kept","method":"GET","path":"/y"}]}`)
	skills := prodSkills(t, string(bundle), `{"main_prompt":"M","skill_prompts":{"s":"P"}}`)
	defs, err := NewMockHandler().Tools(bundle, skills)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	// Skill + kept — the tool with empty name is skipped.
	if len(defs) != 2 || defs[0].Name != "Skill" || defs[1].Name != "kept" {
		t.Fatalf("expected [Skill kept], got %+v", defs)
	}
}

func TestMockHandler_Tools_NoLoadableSkills_OmitsSkill(t *testing.T) {
	bundle := []byte(`{"skills_meta":[{"name":"s"}],"tools":[{"name":"kept","method":"GET","path":"/y"}]}`)
	defs, err := NewMockHandler().Tools(bundle, prodSkills(t, string(bundle), `{"main_prompt":"M"}`))
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "kept" {
		t.Fatalf("expected [kept], got %+v", defs)
	}
}

func TestMockHandler_Call_Skill_LookupSucceeds(t *testing.T) {
	bundle := []byte(`{"skills_meta":[{"name":"place_order","description":"d"}]}`)
	skills := prodSkills(t, string(bundle), `{"main_prompt":"M","skill_prompts":{"place_order":"SKILL_PROMPT"}}`)
	h := NewMockHandler()
	res, err := h.Call(context.Background(), bundle, skills, Call{
		ID:    "tu_1",
		Name:  "Skill",
		Input: []byte(`{"name":"place_order"}`),
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected IsError=false")
	}
	if res.ToolUseID != "tu_1" {
		t.Fatalf("ToolUseID mismatch: got %q", res.ToolUseID)
	}
	var out struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("Output unmarshal: %v", err)
	}
	if out.Prompt != "SKILL_PROMPT" {
		t.Fatalf("expected SKILL_PROMPT, got %q", out.Prompt)
	}
}

func TestMockHandler_Call_Skill_UnknownReturnsIsError(t *testing.T) {
	bundle := []byte(`{"skills_meta":[{"name":"place_order","description":"d"}]}`)
	skills := prodSkills(t, string(bundle), `{"main_prompt":"M","skill_prompts":{"place_order":"P"}}`)
	res, err := NewMockHandler().Call(context.Background(), bundle, skills, Call{
		ID:    "tu_2",
		Name:  "Skill",
		Input: []byte(`{"name":"nonexistent"}`),
	})
	if err != nil {
		t.Fatalf("Call returned error (expected IsError=true result): %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for unknown skill")
	}
}

func TestMockHandler_Call_ToolEcho(t *testing.T) {
	bundle := []byte(`{"tools":[{"name":"submit_order","description":"d","method":"POST","path":"/api/orders","capability":"orders"}]}`)
	res, _ := NewMockHandler().Call(context.Background(), bundle, nil, Call{
		ID:    "tu_1",
		Name:  "submit_order",
		Input: []byte(`{"item":"latte"}`),
	})
	if res.IsError {
		t.Fatalf("expected IsError=false")
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("Output unmarshal: %v", err)
	}
	if out["_mocked"] != true {
		t.Fatalf("expected _mocked: true")
	}
	if out["tool"] != "submit_order" {
		t.Fatalf("expected tool: submit_order, got %v", out["tool"])
	}
}
