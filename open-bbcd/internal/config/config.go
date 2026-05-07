package config

import (
	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
)

type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Discovery DiscoveryConfig
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

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
