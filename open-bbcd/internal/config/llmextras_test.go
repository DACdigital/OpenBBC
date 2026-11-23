package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadEnv runs Load in an empty directory (no .env) with exactly the given
// LLM-related variables set; every other LLM variable is unset.
func loadEnv(t *testing.T, vars map[string]string) (*Config, error) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, k := range []string{"OPENBBC_LLM_ADAPTER", "OPENBBC_DEFAULT_MODEL", "OPENAI_API_KEY", "OPENAI_BASE_URL",
		"OPENBBC_LLM_REQUEST_EXTRAS", "OPENBBC_LLM_REQUEST_TIMEOUT_SECONDS", "OPENBBC_LLM_STREAM_IDLE_TIMEOUT_SECONDS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
	return Load()
}

const glmRule = `[{"match":"*/glm-*","headers":{"x-bf-passthrough-extra-params":"true"},"body":{"chat_template_kwargs":{"reasoning_effort":"low"}}}]`

func TestLoad_ExtrasResolvedAgainstBifrostModel(t *testing.T) {
	cfg, err := loadEnv(t, map[string]string{
		"OPENBBC_LLM_ADAPTER":        "bifrost",
		"OPENBBC_DEFAULT_MODEL":      "openai/vllm/glm-5.3-flash",
		"OPENBBC_LLM_REQUEST_EXTRAS": glmRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.LLMRequestExtras) != 1 || cfg.LLM.Extras == nil || cfg.LLM.Extras.Index != 0 {
		t.Fatalf("rules = %+v, active = %+v", cfg.LLMRequestExtras, cfg.LLM.Extras)
	}
}

func TestLoad_ExtrasNoMatch(t *testing.T) {
	cfg, err := loadEnv(t, map[string]string{
		"OPENBBC_LLM_ADAPTER":        "bifrost",
		"OPENBBC_DEFAULT_MODEL":      "openai/gpt-4o-mini",
		"OPENBBC_LLM_REQUEST_EXTRAS": glmRule,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.LLMRequestExtras) != 1 || cfg.LLM.Extras != nil {
		t.Fatalf("rules = %+v, active = %+v", cfg.LLMRequestExtras, cfg.LLM.Extras)
	}
}

func TestLoad_ExtrasUnsetMeansNone(t *testing.T) {
	cfg, err := loadEnv(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLMRequestExtras != nil || cfg.LLM.Extras != nil || cfg.LLM.RequestTimeoutSeconds != 0 || cfg.LLM.StreamIdleTimeoutSeconds != 0 {
		t.Fatalf("cfg.LLM = %+v, rules = %+v", cfg.LLM, cfg.LLMRequestExtras)
	}
}

func TestLoad_InvalidExtrasFailsBoot(t *testing.T) {
	_, err := loadEnv(t, map[string]string{"OPENBBC_LLM_REQUEST_EXTRAS": `[{"match":"*","headers":{"Authorization":"x"}}]`})
	if err == nil || !strings.HasPrefix(err.Error(), "OPENBBC_LLM_REQUEST_EXTRAS: rule 0: headers.Authorization") {
		t.Fatalf("err = %v", err)
	}
}

// The shared matching fixture's unset-model case: on the anthropic adapter
// an unset OPENBBC_DEFAULT_MODEL falls back to claude-sonnet-4-6, and the
// rules match against that effective value. (On bifrost an unset model
// already fails boot, so there is no such case there.)
func TestLoad_ExtrasMatchEffectiveDefaultModel(t *testing.T) {
	dir, _ := os.Getwd()
	var raw []byte
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "testdata", "llm-request-extras", "matching.json")); err == nil {
			raw = b
			break
		}
		if filepath.Dir(dir) == dir {
			t.Fatal("testdata/llm-request-extras/matching.json not found")
		}
		dir = filepath.Dir(dir)
	}
	var doc struct {
		Cases []struct {
			Name            string          `json:"name"`
			Patterns        []string        `json:"patterns"`
			Model           *string         `json:"model"`
			ExpectByService map[string]*int `json:"expect_by_service"`
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, c := range doc.Cases {
		if c.Model != nil {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			var rules []map[string]any
			for _, p := range c.Patterns {
				rules = append(rules, map[string]any{"match": p, "headers": map[string]string{"x-rule": p}})
			}
			js, _ := json.Marshal(rules)
			cfg, err := loadEnv(t, map[string]string{"OPENBBC_LLM_REQUEST_EXTRAS": string(js)})
			if err != nil {
				t.Fatal(err)
			}
			want := c.ExpectByService["open-bbcd"]
			got := cfg.LLM.Extras
			if (want == nil) != (got == nil) || (want != nil && got.Index != *want) {
				t.Fatalf("active rule = %+v, want %v (model %q)", got, want, cfg.RuntimeModel())
			}
		})
	}
	if ran == 0 {
		t.Fatal("matching.json has no unset-model case")
	}
}

func TestLoad_Timeouts(t *testing.T) {
	cfg, err := loadEnv(t, map[string]string{
		"OPENBBC_LLM_REQUEST_TIMEOUT_SECONDS":     "900",
		"OPENBBC_LLM_STREAM_IDLE_TIMEOUT_SECONDS": "3600",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.RequestTimeoutSeconds != 900 || cfg.LLM.StreamIdleTimeoutSeconds != 3600 {
		t.Fatalf("cfg.LLM = %+v", cfg.LLM)
	}
}

func TestLoad_InvalidTimeoutsFailBoot(t *testing.T) {
	bad := []struct{ name, value string }{
		{"OPENBBC_LLM_REQUEST_TIMEOUT_SECONDS", "0"},
		{"OPENBBC_LLM_REQUEST_TIMEOUT_SECONDS", "-5"},
		{"OPENBBC_LLM_REQUEST_TIMEOUT_SECONDS", "1.5"},
		{"OPENBBC_LLM_REQUEST_TIMEOUT_SECONDS", "10s"},
		{"OPENBBC_LLM_REQUEST_TIMEOUT_SECONDS", "86401"},
		{"OPENBBC_LLM_REQUEST_TIMEOUT_SECONDS", "9223372036854775807"},
		{"OPENBBC_LLM_STREAM_IDLE_TIMEOUT_SECONDS", "0"},
		{"OPENBBC_LLM_STREAM_IDLE_TIMEOUT_SECONDS", "3601"},
		{"OPENBBC_LLM_STREAM_IDLE_TIMEOUT_SECONDS", "abc"},
	}
	for _, b := range bad {
		t.Run(b.name+"="+b.value, func(t *testing.T) {
			_, err := loadEnv(t, map[string]string{b.name: b.value})
			if err == nil || !strings.HasPrefix(err.Error(), b.name+": ") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
