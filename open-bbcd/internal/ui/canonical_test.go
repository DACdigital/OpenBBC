package ui

import (
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
)

func TestCanonicalModel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"claude-haiku-4-5", "anthropic/claude-haiku-4-5"},
		{"gpt-4o", "openai/gpt-4o"},
		{"openai-o3", "openai/openai-o3"},
		{"gemini-2.0-flash", "gemini/gemini-2.0-flash"},
		{"anthropic/claude-haiku-4-5", "anthropic/claude-haiku-4-5"},
		{"openai/gpt-4o", "openai/gpt-4o"},
		{"openrouter/anthropic/claude-3.5", "openrouter/anthropic/claude-3.5"},
		{"mistral-large", "mistral-large"},
		{"", ""},
	}
	for _, c := range cases {
		if got := CanonicalModel(c.in); got != c.want {
			t.Errorf("CanonicalModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRuntimeModel(t *testing.T) {
	cases := []struct {
		name           string
		cfg            config.Config
		raw, canonical string
	}{
		{
			name:      "anthropic adapter, bare id",
			cfg:       config.Config{LLM: config.LLMConfig{Adapter: config.LLMAdapterAnthropic, Model: "claude-sonnet-4-6"}},
			raw:       "claude-sonnet-4-6",
			canonical: "anthropic/claude-sonnet-4-6",
		},
		{
			name:      "anthropic adapter, already prefixed",
			cfg:       config.Config{LLM: config.LLMConfig{Adapter: config.LLMAdapterAnthropic, Model: "anthropic/claude-sonnet-4-6"}},
			raw:       "anthropic/claude-sonnet-4-6",
			canonical: "anthropic/claude-sonnet-4-6",
		},
		{
			name:      "empty LLM block falls back to Anthropic.DefaultModel",
			cfg:       config.Config{Anthropic: config.AnthropicConfig{DefaultModel: "claude-sonnet-4-6"}},
			raw:       "claude-sonnet-4-6",
			canonical: "anthropic/claude-sonnet-4-6",
		},
		{
			name:      "bifrost adapter",
			cfg:       config.Config{LLM: config.LLMConfig{Adapter: config.LLMAdapterBifrost, Provider: "openai", Model: "gpt-4o"}},
			raw:       "openai/gpt-4o",
			canonical: "openai/gpt-4o",
		},
		{
			name:      "bifrost adapter, anthropic provider",
			cfg:       config.Config{LLM: config.LLMConfig{Adapter: config.LLMAdapterBifrost, Provider: "anthropic", Model: "claude-haiku-4-5"}},
			raw:       "anthropic/claude-haiku-4-5",
			canonical: "anthropic/claude-haiku-4-5",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RuntimeModel(&c.cfg)
			if got != c.raw {
				t.Fatalf("RuntimeModel = %q, want %q", got, c.raw)
			}
			if can := CanonicalModel(got); can != c.canonical {
				t.Fatalf("CanonicalModel(RuntimeModel) = %q, want %q", can, c.canonical)
			}
		})
	}
}

func TestTargetDiffers(t *testing.T) {
	cases := []struct {
		target, runtime string
		want            bool
	}{
		{"claude-haiku-4-5", "anthropic/claude-haiku-4-5", false},
		{"anthropic/claude-haiku-4-5", "claude-haiku-4-5", false},
		{"claude-haiku-4-5", "claude-haiku-4-5", false},
		{"gpt-4o", "openai/gpt-4o", false},
		{"claude-haiku-4-5", "claude-sonnet-4-6", true},
		{"claude-haiku-4-5", "openai/gpt-4o", true},
		{"", "claude-sonnet-4-6", false},
		{"claude-haiku-4-5", "", false},
	}
	for _, c := range cases {
		if got := TargetDiffers(c.target, c.runtime); got != c.want {
			t.Errorf("TargetDiffers(%q, %q) = %v, want %v", c.target, c.runtime, got, c.want)
		}
	}
}
