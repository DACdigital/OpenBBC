package config

import (
	"os"
	"testing"
)

func TestLoad_DefaultValues(t *testing.T) {
	// Clear env to test defaults
	os.Unsetenv("SERVER_HOST")
	os.Unsetenv("SERVER_PORT")
	os.Unsetenv("DATABASE_URL")

	cfg, err := Load()
	if err == nil {
		t.Fatal("Load() should error when DATABASE_URL is missing")
	}
	_ = cfg
}

func TestLoad_FromEnv(t *testing.T) {
	os.Setenv("SERVER_PORT", "9000")
	os.Setenv("SERVER_HOST", "127.0.0.1")
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	defer func() {
		os.Unsetenv("SERVER_PORT")
		os.Unsetenv("SERVER_HOST")
		os.Unsetenv("DATABASE_URL")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9000 {
		t.Errorf("Server.Port = %d, want 9000", cfg.Server.Port)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("Server.Host = %q, want \"127.0.0.1\"", cfg.Server.Host)
	}
	if cfg.Database.URL != "postgres://localhost/test" {
		t.Errorf("Database.URL = %q, want \"postgres://localhost/test\"", cfg.Database.URL)
	}
}
