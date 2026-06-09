package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMockHandler_Tools_FromBundle(t *testing.T) {
	bundle := []byte(`{
        "skills":[
            {"name":"place_order","description":"d","prompt":"P"},
            {"name":"check_rewards","description":"d2","prompt":"P2"}
        ],
        "capabilities":[
            {"name":"inventory","description":"Read stock.","proposed_tool":"query_inventory"},
            {"name":"orders","description":"Submit orders.","proposed_tool":"submit_order"}
        ]
    }`)
	h := NewMockHandler()
	defs, err := h.Tools(bundle)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(defs) != 3 {
		t.Fatalf("expected 3 tool defs (1 Skill + 2 capabilities), got %d", len(defs))
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
	// Capability tool names from proposed_tool field.
	if defs[1].Name != "query_inventory" || defs[2].Name != "submit_order" {
		t.Fatalf("unexpected capability tool names: %q, %q", defs[1].Name, defs[2].Name)
	}
}

func TestMockHandler_Tools_SkipsCapabilityWithoutProposedTool(t *testing.T) {
	bundle := []byte(`{"skills":[],"capabilities":[{"name":"x","description":"d","proposed_tool":""}]}`)
	defs, err := NewMockHandler().Tools(bundle)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	// Only the Skill meta-tool — the capability with empty proposed_tool is skipped.
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool (Skill only), got %d", len(defs))
	}
}

func TestMockHandler_Call_Skill_LookupSucceeds(t *testing.T) {
	bundle := []byte(`{"skills":[{"name":"place_order","description":"d","prompt":"SKILL_PROMPT"}]}`)
	h := NewMockHandler()
	res, err := h.Call(context.Background(), bundle, Call{
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
	bundle := []byte(`{"skills":[{"name":"place_order","description":"d","prompt":"P"}]}`)
	res, err := NewMockHandler().Call(context.Background(), bundle, Call{
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

func TestMockHandler_Call_CapabilityEcho(t *testing.T) {
	bundle := []byte(`{"capabilities":[{"name":"orders","description":"d","proposed_tool":"submit_order"}]}`)
	res, _ := NewMockHandler().Call(context.Background(), bundle, Call{
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
	if out["capability"] != "submit_order" {
		t.Fatalf("expected capability: submit_order, got %v", out["capability"])
	}
}
