package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoad_DefaultValues(t *testing.T) {
	t.Chdir(t.TempDir())
	os.Unsetenv("SERVER_HOST")
	os.Unsetenv("SERVER_PORT")
	os.Unsetenv("DATABASE_URL")
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
	os.Unsetenv("DISCOVERY_MAX_UPLOAD_MB")
	defer os.Unsetenv("DATABASE_URL")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Discovery.MaxUploadMB != 50 {
		t.Errorf("Discovery.MaxUploadMB = %d, want 50", cfg.Discovery.MaxUploadMB)
	}
}

func TestLoad_DiscoveryFromEnv(t *testing.T) {
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	os.Setenv("DISCOVERY_MAX_UPLOAD_MB", "200")
	defer func() {
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("DISCOVERY_MAX_UPLOAD_MB")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Discovery.MaxUploadMB != 200 {
		t.Errorf("Discovery.MaxUploadMB = %d", cfg.Discovery.MaxUploadMB)
	}
}

func TestLoad_AgentToolLimits(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv("DATABASE_URL", "postgres://localhost/test")
		// t.Setenv records the original value and restores it on cleanup;
		// the Unsetenv that follows then makes the var truly absent.
		for _, name := range []string{"AGENT_TOOL_MAX_DEPTH", "AGENT_TOOL_MAX_PARALLEL"} {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Chat.AgentToolMaxDepth != 3 || cfg.Chat.AgentToolMaxParallel != 4 {
			t.Fatalf("defaults = %d/%d, want 3/4", cfg.Chat.AgentToolMaxDepth, cfg.Chat.AgentToolMaxParallel)
		}
	})
	for _, name := range []string{"AGENT_TOOL_MAX_DEPTH", "AGENT_TOOL_MAX_PARALLEL"} {
		for _, bad := range []string{"0", "-1", "abc", "1.5"} {
			t.Run(name+"="+bad, func(t *testing.T) {
				t.Chdir(t.TempDir())
				t.Setenv("DATABASE_URL", "postgres://localhost/test")
				t.Setenv(name, bad)
				_, err := Load()
				if err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("Load err = %v, want error mentioning %s", err, name)
				}
			})
		}
		t.Run(name+"=1", func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("DATABASE_URL", "postgres://localhost/test")
			t.Setenv(name, "1")
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := cfg.Chat.AgentToolMaxDepth
			if name == "AGENT_TOOL_MAX_PARALLEL" {
				got = cfg.Chat.AgentToolMaxParallel
			}
			if got != 1 {
				t.Fatalf("%s = %d, want 1 (cfg %+v)", name, got, cfg.Chat)
			}
		})
	}
}
