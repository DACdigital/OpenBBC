package config

import (
	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
)

type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Discovery DiscoveryConfig
	Anthropic AnthropicConfig
	Chat      ChatConfig
}

type ServerConfig struct {
	Host string `env:"SERVER_HOST" envDefault:"0.0.0.0"`
	Port int    `env:"SERVER_PORT" envDefault:"8080"`
}

type DatabaseConfig struct {
	URL string `env:"DATABASE_URL,required"`
}

type DiscoveryConfig struct {
	StorageDir  string `env:"DISCOVERY_STORAGE_DIR" envDefault:"./data/discovery"`
	MaxUploadMB int    `env:"DISCOVERY_MAX_UPLOAD_MB" envDefault:"50"`
}

// AnthropicConfig holds runtime LLM provider settings. APIKey is NOT marked
// required — open-bbcd boots without it; chat endpoints lazy-fail at first
// request when missing. This lets the rest of the service work in environments
// without an Anthropic key (development, CI for non-chat features).
type AnthropicConfig struct {
	APIKey       string `env:"ANTHROPIC_API_KEY"`
	DefaultModel string `env:"OPENBBC_DEFAULT_MODEL" envDefault:"claude-sonnet-4-6"`
	MaxTokens    int    `env:"OPENBBC_MAX_TOKENS" envDefault:"4096"`
}

// ChatConfig controls the chat runtime: transport choice + per-turn loop bounds.
type ChatConfig struct {
	Transport     string `env:"OPENBBC_CHAT_TRANSPORT" envDefault:"agui"`
	MaxToolRounds int    `env:"OPENBBC_MAX_TOOL_ROUNDS" envDefault:"10"`
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
