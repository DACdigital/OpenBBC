package anthropic

import (
	"testing"

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
