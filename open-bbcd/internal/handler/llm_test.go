package handler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/bifrost"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/bifrost/bifrosttest"
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

func TestNewLLM_KeepsMultimodalRendererThroughCallLog(t *testing.T) {
	for _, cfg := range []*config.Config{
		{Anthropic: config.AnthropicConfig{DefaultModel: "claude-sonnet-4-6"}},
		{LLM: config.LLMConfig{Adapter: config.LLMAdapterBifrost, Provider: "openai", Model: "gpt-4o", APIKey: "sk"}},
	} {
		client, shutdown, err := NewLLM(cfg, testLogger())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := client.(llm.MultimodalRenderer); !ok {
			t.Errorf("%s: lost llm.MultimodalRenderer", client.Name())
		}
		shutdown()
	}
}

func TestNewLLM_EveryCallLogsItsAdapter(t *testing.T) {
	fake := bifrosttest.New(t)
	fake.Route("sys", bifrosttest.Text("hello"))
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, shutdown, err := NewLLM(&config.Config{LLM: config.LLMConfig{
		Adapter: config.LLMAdapterBifrost, Provider: "openai", Model: "gpt-test", APIKey: "sk", BaseURL: fake.URL(),
	}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()
	req := llm.Request{Model: "gpt-test", System: "sys", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{llm.TextBlock{Text: "hi"}}}}}
	for _, err := range client.Generate(context.Background(), req) {
		if err != nil {
			t.Fatal(err)
		}
	}
	out := buf.String()
	for _, want := range []string{`"msg":"llm call"`, `"adapter":"bifrost:openai"`, `"model":"gpt-test"`, `"stop_reason":"end_turn"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %s:\n%s", want, out)
		}
	}
}
