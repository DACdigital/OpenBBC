package config

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// LLM adapter names accepted in OPENBBC_LLM_ADAPTER.
const (
	LLMAdapterAnthropic = "anthropic"
	LLMAdapterBifrost   = "bifrost"
)

// LLMConfig is the boot-selected LLM adapter. On the anthropic adapter only
// Adapter and Model are set (Model mirrors AnthropicConfig.DefaultModel); on
// bifrost, Provider/Model come from OPENBBC_DEFAULT_MODEL=<provider>/<model>
// and APIKey/BaseURL from the selected provider's <PROVIDER>_* vars. An empty
// APIKey is not a boot error: the adapter fails the first LLM call instead,
// matching ANTHROPIC_API_KEY.
type LLMConfig struct {
	Adapter  string
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
}

// bifrostProviders is the v1 key-only allow-list. Cloud-credential providers
// (azure, bedrock, vertex) and keyless self-hosted ones (ollama, vllm, sgl)
// are refused at boot.
var bifrostProviders = map[string]bool{
	"anthropic":  true,
	"openai":     true,
	"gemini":     true,
	"mistral":    true,
	"groq":       true,
	"cohere":     true,
	"openrouter": true,
	"deepseek":   true,
	"xai":        true,
	"cerebras":   true,
}

// bifrostBaseURLProviders are the providers for which Bifrost honours
// NetworkConfig.BaseURL.
var bifrostBaseURLProviders = map[string]bool{
	"openai":    true,
	"anthropic": true,
	"cohere":    true,
	"mistral":   true,
}

func sortedKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func providerEnvPrefix(provider string) string {
	return strings.ToUpper(strings.ReplaceAll(provider, "-", "_"))
}

// APIKeyEnv is the env var holding provider's API key, e.g. OPENAI_API_KEY.
func APIKeyEnv(provider string) string { return providerEnvPrefix(provider) + "_API_KEY" }

// BaseURLEnv is the env var overriding provider's endpoint, e.g. OPENAI_BASE_URL.
func BaseURLEnv(provider string) string { return providerEnvPrefix(provider) + "_BASE_URL" }

// LLMModel is the bare model id the orchestrators send. It falls back to
// AnthropicConfig.DefaultModel when LLM is unset (configs built by hand in
// tests never run parseLLMFromEnv).
func (c *Config) LLMModel() string {
	if c.LLM.Model != "" {
		return c.LLM.Model
	}
	return c.Anthropic.DefaultModel
}

// parseLLMFromEnv reads os.Environ()-style KEY=VALUE strings and resolves
// the LLM adapter config. Exposed for testing; production goes through Load.
func parseLLMFromEnv(environ []string, anthropic AnthropicConfig) (LLMConfig, error) {
	kv := make(map[string]string, len(environ))
	for _, e := range environ {
		if k, v, ok := strings.Cut(e, "="); ok {
			kv[k] = v
		}
	}

	adapter := kv["OPENBBC_LLM_ADAPTER"]
	if adapter == "" {
		adapter = LLMAdapterAnthropic
	}
	switch adapter {
	case LLMAdapterAnthropic:
		return LLMConfig{Adapter: adapter, Model: anthropic.DefaultModel}, nil
	case LLMAdapterBifrost:
	default:
		return LLMConfig{}, fmt.Errorf("OPENBBC_LLM_ADAPTER: expected %q or %q, got %q", LLMAdapterAnthropic, LLMAdapterBifrost, adapter)
	}

	// Read the raw env value, not AnthropicConfig.DefaultModel: on bifrost
	// the anthropic envDefault (a bare id) must not satisfy the requirement.
	raw := kv["OPENBBC_DEFAULT_MODEL"]
	provider, model, found := strings.Cut(raw, "/")
	if !found || provider == "" || model == "" {
		return LLMConfig{}, fmt.Errorf("OPENBBC_DEFAULT_MODEL: the bifrost adapter needs <provider>/<model> with provider one of %s, got %q", sortedKeys(bifrostProviders), raw)
	}
	if !bifrostProviders[provider] {
		return LLMConfig{}, fmt.Errorf("OPENBBC_DEFAULT_MODEL: provider %q is not supported by the bifrost adapter (allowed: %s)", provider, sortedKeys(bifrostProviders))
	}

	cfg := LLMConfig{Adapter: adapter, Provider: provider, Model: model, APIKey: kv[APIKeyEnv(provider)]}
	if base := kv[BaseURLEnv(provider)]; base != "" {
		name := BaseURLEnv(provider)
		if !bifrostBaseURLProviders[provider] {
			return LLMConfig{}, fmt.Errorf("%s: a base URL override is supported only for %s", name, sortedKeys(bifrostBaseURLProviders))
		}
		if err := validateBaseURL(base); err != nil {
			return LLMConfig{}, fmt.Errorf("%s: %w", name, err)
		}
		cfg.BaseURL = base
	}
	return cfg, nil
}

// validateBaseURL requires an absolute https URL; plain http is allowed only
// to a loopback host so a provider key never crosses the network unencrypted.
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("expected an absolute URL, got %q", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("plain http is allowed only to a loopback host (localhost, 127.0.0.0/8, ::1), got %q", raw)
	}
	return fmt.Errorf("scheme must be https (or http to a loopback host), got %q", raw)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
