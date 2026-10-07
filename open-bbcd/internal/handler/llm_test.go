package handler

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/bifrost"
)

func TestNewLLM_DefaultsToAnthropic(t *testing.T) {
	client, shutdown, err := NewLLM(&config.Config{Anthropic: config.AnthropicConfig{DefaultModel: "claude-sonnet-4-6"}}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()
	if client.Name() != "anthropic" {
		t.Fatalf("Name() = %q", client.Name())
	}
}

func TestNewLLM_Bifrost(t *testing.T) {
	cfg := &config.Config{LLM: config.LLMConfig{Adapter: config.LLMAdapterBifrost, Provider: "openai", Model: "gpt-4o", APIKey: "sk"}}
	client, shutdown, err := NewLLM(cfg, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if client.Name() != "bifrost:openai" {
		t.Fatalf("Name() = %q", client.Name())
	}
	shutdown()
}

func TestNewLLM_BifrostInitErrorPropagates(t *testing.T) {
	orig := newBifrost
	t.Cleanup(func() { newBifrost = orig })
	newBifrost = func(context.Context, config.LLMConfig, *slog.Logger) (*bifrost.LLM, error) {
		return nil, errors.New("bifrost: init: boom")
	}
	_, _, err := NewLLM(&config.Config{LLM: config.LLMConfig{Adapter: config.LLMAdapterBifrost, Provider: "openai", Model: "m"}}, testLogger())
	if err == nil || err.Error() != "bifrost: init: boom" {
		t.Fatalf("err = %v", err)
	}
}
