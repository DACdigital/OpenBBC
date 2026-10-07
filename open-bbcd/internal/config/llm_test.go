package config

import (
	"strings"
	"testing"
)

var anthropicDefaults = AnthropicConfig{APIKey: "ak", DefaultModel: "claude-sonnet-4-6", MaxTokens: 4096}

func TestParseLLM_DefaultIsAnthropic(t *testing.T) {
	cfg, err := parseLLMFromEnv(nil, anthropicDefaults)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.Adapter != LLMAdapterAnthropic || cfg.Model != "claude-sonnet-4-6" || cfg.Provider != "" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseLLM_ExplicitAnthropic(t *testing.T) {
	cfg, err := parseLLMFromEnv([]string{"OPENBBC_LLM_ADAPTER=anthropic"}, anthropicDefaults)
	if err != nil || cfg.Adapter != LLMAdapterAnthropic {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestParseLLM_BifrostValid(t *testing.T) {
	cfg, err := parseLLMFromEnv([]string{
		"OPENBBC_LLM_ADAPTER=bifrost",
		"OPENBBC_DEFAULT_MODEL=openai/gpt-4o",
		"OPENAI_API_KEY=sk-1",
		"GROQ_API_KEY=ignored",
	}, anthropicDefaults)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := LLMConfig{Adapter: LLMAdapterBifrost, Provider: "openai", Model: "gpt-4o", APIKey: "sk-1"}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestParseLLM_BifrostSplitsOnFirstSlash(t *testing.T) {
	cfg, err := parseLLMFromEnv([]string{
		"OPENBBC_LLM_ADAPTER=bifrost",
		"OPENBBC_DEFAULT_MODEL=openrouter/meta-llama/llama-3.1-70b",
	}, anthropicDefaults)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.Provider != "openrouter" || cfg.Model != "meta-llama/llama-3.1-70b" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseLLM_BifrostMissingKeyIsNotABootError(t *testing.T) {
	cfg, err := parseLLMFromEnv([]string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=groq/llama-3.3-70b"}, anthropicDefaults)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.APIKey != "" {
		t.Fatalf("APIKey = %q, want empty", cfg.APIKey)
	}
}

func TestParseLLM_Errors(t *testing.T) {
	cases := []struct {
		name    string
		env     []string
		wantVar string
	}{
		{"bad adapter", []string{"OPENBBC_LLM_ADAPTER=foo"}, "OPENBBC_LLM_ADAPTER"},
		{"bifrost model unset", []string{"OPENBBC_LLM_ADAPTER=bifrost"}, "OPENBBC_DEFAULT_MODEL"},
		{"no slash", []string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=gpt-4o"}, "OPENBBC_DEFAULT_MODEL"},
		{"empty provider", []string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=/gpt-4o"}, "OPENBBC_DEFAULT_MODEL"},
		{"empty model", []string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=openai/"}, "OPENBBC_DEFAULT_MODEL"},
		{"not allow-listed", []string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=bedrock/claude"}, "OPENBBC_DEFAULT_MODEL"},
		{"base url unsupported provider", []string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=groq/x", "GROQ_BASE_URL=https://p.example"}, "GROQ_BASE_URL"},
		{"base url not a url", []string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=openai/x", "OPENAI_BASE_URL=not a url"}, "OPENAI_BASE_URL"},
		{"base url plain http remote", []string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=openai/x", "OPENAI_BASE_URL=http://proxy.internal:8080"}, "OPENAI_BASE_URL"},
		{"base url ftp", []string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=openai/x", "OPENAI_BASE_URL=ftp://p.example"}, "OPENAI_BASE_URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseLLMFromEnv(c.env, anthropicDefaults)
			if err == nil || !strings.Contains(err.Error(), c.wantVar) {
				t.Fatalf("err = %v, want mention of %s", err, c.wantVar)
			}
		})
	}
}

func TestParseLLM_NotAllowListedErrorListsProviders(t *testing.T) {
	_, err := parseLLMFromEnv([]string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=vertex/gemini"}, anthropicDefaults)
	if err == nil || !strings.Contains(err.Error(), "anthropic, cerebras, cohere, deepseek, gemini, groq, mistral, openai, openrouter, xai") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseLLM_BaseURLAccepted(t *testing.T) {
	for _, u := range []string{"https://proxy.example/v1", "http://127.0.0.1:9999", "http://localhost:8080", "http://[::1]:9"} {
		cfg, err := parseLLMFromEnv([]string{"OPENBBC_LLM_ADAPTER=bifrost", "OPENBBC_DEFAULT_MODEL=openai/gpt-4o", "OPENAI_BASE_URL=" + u}, anthropicDefaults)
		if err != nil || cfg.BaseURL != u {
			t.Fatalf("%s: cfg = %+v, err = %v", u, cfg, err)
		}
	}
}

func TestAPIKeyEnv(t *testing.T) {
	if got := APIKeyEnv("xai"); got != "XAI_API_KEY" {
		t.Fatalf("APIKeyEnv(xai) = %q", got)
	}
	if got := APIKeyEnv("github-copilot"); got != "GITHUB_COPILOT_API_KEY" {
		t.Fatalf("APIKeyEnv(github-copilot) = %q", got)
	}
}

func TestLLMModel_FallsBackToAnthropicDefault(t *testing.T) {
	c := &Config{Anthropic: AnthropicConfig{DefaultModel: "claude-sonnet-4-6"}}
	if got := c.LLMModel(); got != "claude-sonnet-4-6" {
		t.Fatalf("LLMModel() = %q", got)
	}
	c.LLM.Model = "gpt-4o"
	if got := c.LLMModel(); got != "gpt-4o" {
		t.Fatalf("LLMModel() = %q", got)
	}
}

func TestLoad_BadAdapterFailsBoot(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("OPENBBC_LLM_ADAPTER", "nope")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPENBBC_LLM_ADAPTER") {
		t.Fatalf("Load() err = %v", err)
	}
}
