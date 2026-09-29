package config

import (
	"strings"
	"testing"
	"time"
)

// envSlice turns a KEY=VALUE map into an os.Environ()-shaped slice so
// tests can drive the parser without touching the real process env.
func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func TestParseArtifacts_EmptyEnvironIsDisabled(t *testing.T) {
	cfg, err := parseArtifactsFromEnv(nil)
	if err != nil {
		t.Fatalf("parseArtifactsFromEnv(nil) = err %v, want nil", err)
	}
	if cfg.Enabled() {
		t.Errorf("Enabled() = true, want false when no ARTIFACT_STORE_* vars are set")
	}
	if len(cfg.Stores) != 0 {
		t.Errorf("len(Stores) = %d, want 0", len(cfg.Stores))
	}
	if cfg.SignedURLTTL != 300*time.Second {
		t.Errorf("SignedURLTTL = %v, want 300s default", cfg.SignedURLTTL)
	}
}

func TestParseArtifacts_MinimalSingleStore(t *testing.T) {
	cfg, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN_KIND":       "s3_compatible",
		"ARTIFACT_STORE_MAIN_ENDPOINT":   "https://s3.example.com",
		"ARTIFACT_STORE_MAIN_BUCKET":     "artifacts",
		"ARTIFACT_STORE_MAIN_REGION":     "eu-west-1",
		"ARTIFACT_STORE_MAIN_ACCESS_KEY": "AKIA",
		"ARTIFACT_STORE_MAIN_SECRET_KEY": "xxx",
		"ARTIFACT_STORE_DEFAULT":         "MAIN",
		"ARTIFACT_MAX_UPLOAD_MB":         "25",
	}))
	if err != nil {
		t.Fatalf("parseArtifactsFromEnv = err %v, want nil", err)
	}
	if !cfg.Enabled() {
		t.Fatal("Enabled() = false, want true")
	}
	if cfg.DefaultID != "MAIN" {
		t.Errorf("DefaultID = %q, want %q", cfg.DefaultID, "MAIN")
	}
	if cfg.MaxUploadMB != 25 {
		t.Errorf("MaxUploadMB = %d, want 25", cfg.MaxUploadMB)
	}
	main, ok := cfg.Stores["MAIN"]
	if !ok {
		t.Fatal(`Stores["MAIN"] missing`)
	}
	if main.Kind != "s3_compatible" {
		t.Errorf("Stores[MAIN].Kind = %q, want %q", main.Kind, "s3_compatible")
	}
	// Multi-word field (ACCESS_KEY) must land in Options under its full name,
	// not split as ACCESS + KEY.
	if got, want := main.Options["ACCESS_KEY"], "AKIA"; got != want {
		t.Errorf("Options[ACCESS_KEY] = %q, want %q", got, want)
	}
	if got, want := main.Options["SECRET_KEY"], "xxx"; got != want {
		t.Errorf("Options[SECRET_KEY] = %q, want %q", got, want)
	}
	if got, want := main.Options["ENDPOINT"], "https://s3.example.com"; got != want {
		t.Errorf("Options[ENDPOINT] = %q, want %q", got, want)
	}
}

func TestParseArtifacts_SignedURLTTLOverride(t *testing.T) {
	cfg, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN_KIND":        "s3_compatible",
		"ARTIFACT_STORE_MAIN_ENDPOINT":    "https://s3.example.com",
		"ARTIFACT_STORE_MAIN_BUCKET":      "artifacts",
		"ARTIFACT_STORE_DEFAULT":          "MAIN",
		"ARTIFACT_MAX_UPLOAD_MB":          "10",
		"ARTIFACT_SIGNED_URL_TTL_SECONDS": "900",
	}))
	if err != nil {
		t.Fatalf("parseArtifactsFromEnv = err %v", err)
	}
	if cfg.SignedURLTTL != 900*time.Second {
		t.Errorf("SignedURLTTL = %v, want 900s", cfg.SignedURLTTL)
	}
}

func TestParseArtifacts_TwoStores(t *testing.T) {
	cfg, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN_KIND":   "s3_compatible",
		"ARTIFACT_STORE_MAIN_BUCKET": "prod",
		"ARTIFACT_STORE_BACKUP_KIND": "s3_compatible",
		"ARTIFACT_STORE_BACKUP_BUCKET": "backup",
		"ARTIFACT_STORE_DEFAULT":     "MAIN",
		"ARTIFACT_MAX_UPLOAD_MB":     "50",
	}))
	if err != nil {
		t.Fatalf("parseArtifactsFromEnv = err %v", err)
	}
	if len(cfg.Stores) != 2 {
		t.Fatalf("len(Stores) = %d, want 2", len(cfg.Stores))
	}
	if cfg.Stores["MAIN"].Options["BUCKET"] != "prod" {
		t.Errorf("MAIN bucket = %q, want prod", cfg.Stores["MAIN"].Options["BUCKET"])
	}
	if cfg.Stores["BACKUP"].Options["BUCKET"] != "backup" {
		t.Errorf("BACKUP bucket = %q, want backup", cfg.Stores["BACKUP"].Options["BUCKET"])
	}
}

func TestParseArtifacts_MissingDefaultWithStores(t *testing.T) {
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN_KIND":   "s3_compatible",
		"ARTIFACT_STORE_MAIN_BUCKET": "prod",
		"ARTIFACT_MAX_UPLOAD_MB":     "10",
	}))
	if err == nil {
		t.Fatal("expected error for missing ARTIFACT_STORE_DEFAULT")
	}
	if !strings.Contains(err.Error(), "ARTIFACT_STORE_DEFAULT") {
		t.Errorf("error should mention ARTIFACT_STORE_DEFAULT, got: %v", err)
	}
}

func TestParseArtifacts_DefaultPointsAtMissingStore(t *testing.T) {
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN_KIND":   "s3_compatible",
		"ARTIFACT_STORE_MAIN_BUCKET": "prod",
		"ARTIFACT_STORE_DEFAULT":     "TYPO",
		"ARTIFACT_MAX_UPLOAD_MB":     "10",
	}))
	if err == nil {
		t.Fatal("expected error for DEFAULT pointing at a store that does not exist")
	}
	if !strings.Contains(err.Error(), "TYPO") {
		t.Errorf("error should mention the missing id %q, got: %v", "TYPO", err)
	}
}

func TestParseArtifacts_MissingKindOnGroup(t *testing.T) {
	// Deployer set bucket/access_key etc. but forgot KIND — the group
	// has no discriminator, so the parser can't dispatch to an adapter.
	// This must fail loud rather than silently drop the group.
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN_BUCKET":     "prod",
		"ARTIFACT_STORE_MAIN_ACCESS_KEY": "AKIA",
		"ARTIFACT_STORE_DEFAULT":         "MAIN",
		"ARTIFACT_MAX_UPLOAD_MB":         "10",
	}))
	if err == nil {
		t.Fatal("expected error for group missing KIND")
	}
	if !strings.Contains(err.Error(), "KIND") {
		t.Errorf("error should mention KIND, got: %v", err)
	}
}

func TestParseArtifacts_MissingMaxUploadMBWithStores(t *testing.T) {
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN_KIND":   "s3_compatible",
		"ARTIFACT_STORE_MAIN_BUCKET": "prod",
		"ARTIFACT_STORE_DEFAULT":     "MAIN",
	}))
	if err == nil {
		t.Fatal("expected error for missing ARTIFACT_MAX_UPLOAD_MB")
	}
	if !strings.Contains(err.Error(), "ARTIFACT_MAX_UPLOAD_MB") {
		t.Errorf("error should mention ARTIFACT_MAX_UPLOAD_MB, got: %v", err)
	}
}

func TestParseArtifacts_InvalidSlugLowercase(t *testing.T) {
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_main_KIND":   "s3_compatible", // lowercase id
		"ARTIFACT_STORE_main_BUCKET": "prod",
		"ARTIFACT_STORE_DEFAULT":     "main",
		"ARTIFACT_MAX_UPLOAD_MB":     "10",
	}))
	if err == nil {
		t.Fatal("expected error for lowercase store id")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "invalid store id") {
		t.Errorf("error should mention invalid store id, got: %v", err)
	}
}

func TestParseArtifacts_InvalidSlugLeadingDigit(t *testing.T) {
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_2MAIN_KIND":   "s3_compatible",
		"ARTIFACT_STORE_2MAIN_BUCKET": "prod",
		"ARTIFACT_STORE_DEFAULT":      "2MAIN",
		"ARTIFACT_MAX_UPLOAD_MB":      "10",
	}))
	if err == nil {
		t.Fatal("expected error for leading-digit store id")
	}
}

func TestParseArtifacts_ReservedSlugDefault(t *testing.T) {
	// <ID>=DEFAULT would collide with the global ARTIFACT_STORE_DEFAULT
	// selector — an env-level footgun spec-review flagged.
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_DEFAULT_KIND":   "s3_compatible",
		"ARTIFACT_STORE_DEFAULT_BUCKET": "prod",
		"ARTIFACT_STORE_DEFAULT":        "DEFAULT",
		"ARTIFACT_MAX_UPLOAD_MB":        "10",
	}))
	if err == nil {
		t.Fatal("expected error for store named DEFAULT")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error should mention reserved, got: %v", err)
	}
}

func TestParseArtifacts_InvalidMaxUploadMB(t *testing.T) {
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN_KIND":   "s3_compatible",
		"ARTIFACT_STORE_MAIN_BUCKET": "prod",
		"ARTIFACT_STORE_DEFAULT":     "MAIN",
		"ARTIFACT_MAX_UPLOAD_MB":     "not-a-number",
	}))
	if err == nil {
		t.Fatal("expected error for non-numeric ARTIFACT_MAX_UPLOAD_MB")
	}
}

func TestParseArtifacts_NegativeMaxUploadMB(t *testing.T) {
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN_KIND":   "s3_compatible",
		"ARTIFACT_STORE_MAIN_BUCKET": "prod",
		"ARTIFACT_STORE_DEFAULT":     "MAIN",
		"ARTIFACT_MAX_UPLOAD_MB":     "-1",
	}))
	if err == nil {
		t.Fatal("expected error for negative ARTIFACT_MAX_UPLOAD_MB")
	}
}

func TestParseArtifacts_MalformedEnvVarName(t *testing.T) {
	// ARTIFACT_STORE_ with nothing after the prefix.
	_, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_STORE_MAIN":    "orphan", // no _<FIELD>
		"ARTIFACT_STORE_DEFAULT": "MAIN",
		"ARTIFACT_MAX_UPLOAD_MB": "10",
	}))
	if err == nil {
		t.Fatal("expected error for malformed env var name")
	}
}

func TestParseArtifacts_DisabledWhenOnlyGlobalsSet(t *testing.T) {
	// User set globals but no per-store groups. Registry stays empty
	// (feature off) — matches the feature-gate pattern.
	cfg, err := parseArtifactsFromEnv(envSlice(map[string]string{
		"ARTIFACT_MAX_UPLOAD_MB":          "10",
		"ARTIFACT_SIGNED_URL_TTL_SECONDS": "600",
	}))
	if err != nil {
		t.Fatalf("parseArtifactsFromEnv = err %v", err)
	}
	if cfg.Enabled() {
		t.Error("Enabled() = true, want false when only global vars are set (no per-store groups)")
	}
	if cfg.MaxUploadMB != 10 {
		t.Errorf("MaxUploadMB = %d, want 10", cfg.MaxUploadMB)
	}
	if cfg.SignedURLTTL != 600*time.Second {
		t.Errorf("SignedURLTTL = %v, want 600s", cfg.SignedURLTTL)
	}
}
