package config

import (
	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
}

type ServerConfig struct {
	Host string `env:"SERVER_HOST" envDefault:"0.0.0.0"`
	Port int    `env:"SERVER_PORT" envDefault:"8080"`
}

type DatabaseConfig struct {
	URL string `env:"DATABASE_URL,required"`
}

func Load() (*Config, error) {
	// Load .env if exists (ignore error if missing)
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
