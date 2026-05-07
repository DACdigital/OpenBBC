package config

import (
	"os"
	"testing"
)

func TestLoad_DefaultValues(t *testing.T) {
	t.Chdir(t.TempDir())
	os.Unsetenv("SERVER_HOST")
	os.Unsetenv("SERVER_PORT")
	os.Unsetenv("DATABASE_URL")
	os.Unsetenv("DISCOVERY_STORAGE_DIR")
	os.Unsetenv("DISCOVERY_MAX_UPLOAD_MB")

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

func TestLoad_DiscoveryDefaults(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	os.Unsetenv("DISCOVERY_STORAGE_DIR")
	os.Unsetenv("DISCOVERY_MAX_UPLOAD_MB")
	defer os.Unsetenv("DATABASE_URL")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Discovery.StorageDir != "./data/discovery" {
		t.Errorf("Discovery.StorageDir = %q, want \"./data/discovery\"", cfg.Discovery.StorageDir)
	}
	if cfg.Discovery.MaxUploadMB != 50 {
		t.Errorf("Discovery.MaxUploadMB = %d, want 50", cfg.Discovery.MaxUploadMB)
	}
}

func TestLoad_DiscoveryFromEnv(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	os.Setenv("DISCOVERY_STORAGE_DIR", "/var/lib/openbbcd/discovery")
	os.Setenv("DISCOVERY_MAX_UPLOAD_MB", "200")
	defer func() {
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("DISCOVERY_STORAGE_DIR")
		os.Unsetenv("DISCOVERY_MAX_UPLOAD_MB")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Discovery.StorageDir != "/var/lib/openbbcd/discovery" {
		t.Errorf("Discovery.StorageDir = %q", cfg.Discovery.StorageDir)
	}
	if cfg.Discovery.MaxUploadMB != 200 {
		t.Errorf("Discovery.MaxUploadMB = %d", cfg.Discovery.MaxUploadMB)
	}
}
