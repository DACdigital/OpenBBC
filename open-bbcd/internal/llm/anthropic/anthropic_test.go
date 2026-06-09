package anthropic

import (
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

func TestLLM_Name(t *testing.T) {
	l := New(config.AnthropicConfig{})
	if l.Name() != "anthropic" {
		t.Fatalf("Name() = %q, want %q", l.Name(), "anthropic")
	}
}

func TestBuildMessageNewParams_BasicFields(t *testing.T) {
	req := llm.Request{
		Model:     "claude-sonnet-4-6",
		System:    "you are helpful",
		MaxTokens: 1024,
	}
	params := buildMessageNewParams(req)

	// Model is a type alias for string in sdk v1.49.0 — cast directly.
	if string(params.Model) != "claude-sonnet-4-6" {
		t.Fatalf("Model = %q, want claude-sonnet-4-6", params.Model)
	}
	if params.MaxTokens != 1024 {
		t.Fatalf("MaxTokens = %d, want 1024", params.MaxTokens)
	}
	if len(params.System) == 0 {
		t.Fatalf("System should be non-empty when req.System is set")
	}
}

func TestBuildMessageNewParams_EmptySystem(t *testing.T) {
	req := llm.Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 512,
	}
	params := buildMessageNewParams(req)
	if len(params.System) != 0 {
		t.Fatalf("System should be empty when req.System is empty, got %d blocks", len(params.System))
	}
}

func TestConvertMessage_UserText(t *testing.T) {
	m := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.Block{llm.TextBlock{Text: "hi"}},
	}
	p := convertMessage(m)
	if p.Role != sdk.MessageParamRoleUser {
		t.Fatalf("role mismatch: got %v", p.Role)
	}
	if len(p.Content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(p.Content))
	}
	text := p.Content[0].GetText()
	if text == nil || *text != "hi" {
		t.Fatalf("expected text block with 'hi', got %v", text)
	}
}

func TestConvertMessage_AssistantWithToolUse(t *testing.T) {
	m := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.Block{
			llm.TextBlock{Text: "let me check"},
			llm.ToolUseBlock{ID: "tu_1", Name: "query", Input: []byte(`{"q":"x"}`)},
		},
	}
	p := convertMessage(m)
	if p.Role != sdk.MessageParamRoleAssistant {
		t.Fatalf("role mismatch: got %v", p.Role)
	}
	if len(p.Content) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(p.Content))
	}
}

func TestConvertMessage_ToolRole_BecomesUser(t *testing.T) {
	m := llm.Message{
		Role: llm.RoleTool,
		Content: []llm.Block{
			llm.ToolResultBlock{ToolUseID: "tu_1", Result: []byte(`{"ok":true}`), IsError: false},
		},
	}
	p := convertMessage(m)
	if p.Role != sdk.MessageParamRoleUser {
		t.Fatalf("tool-role messages should map to user role; got %v", p.Role)
	}
	if len(p.Content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(p.Content))
	}
}

func TestConvertTool(t *testing.T) {
	td := llm.ToolDef{
		Name:        "Skill",
		Description: "Load a skill prompt",
		InputSchema: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	}
	p := convertTool(td)
	if p.OfTool == nil {
		t.Fatalf("expected OfTool branch to be set")
	}
	if p.OfTool.Name != "Skill" {
		t.Fatalf("name mismatch: got %q", p.OfTool.Name)
	}
	if !p.OfTool.Description.Valid() {
		t.Fatalf("expected Description to be set")
	}
	if p.OfTool.Description.Value != "Load a skill prompt" {
		t.Fatalf("description mismatch: got %q", p.OfTool.Description.Value)
	}
}

func TestBuildMessageNewParams_TemperaturePassthrough(t *testing.T) {
	req := llm.Request{
		Model:       "claude-sonnet-4-6",
		MaxTokens:   100,
		Temperature: 0.7,
	}
	params := buildMessageNewParams(req)
	if !params.Temperature.Valid() {
		t.Fatalf("expected Temperature to be set, but it was omitted")
	}
	if params.Temperature.Value != 0.7 {
		t.Fatalf("Temperature = %v, want 0.7", params.Temperature.Value)
	}
}

func TestBuildMessageNewParams_ZeroTemperatureOmitted(t *testing.T) {
	req := llm.Request{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 100,
		// Temperature zero (default) — should be omitted so SDK uses its own default.
	}
	params := buildMessageNewParams(req)
	if params.Temperature.Valid() {
		t.Fatalf("zero Temperature should be omitted, but it was set to %v", params.Temperature.Value)
	}
}
