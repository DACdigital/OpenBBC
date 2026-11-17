package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
)

type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Discovery DiscoveryConfig
	Anthropic AnthropicConfig
	LLM       LLMConfig
	Chat      ChatConfig
	Artifacts ArtifactsConfig
}

type ServerConfig struct {
	Host string `env:"SERVER_HOST" envDefault:"0.0.0.0"`
	Port int    `env:"SERVER_PORT" envDefault:"8080"`
}

type DatabaseConfig struct {
	URL string `env:"DATABASE_URL,required"`
}

type DiscoveryConfig struct {
	MaxUploadMB int `env:"DISCOVERY_MAX_UPLOAD_MB" envDefault:"50"`
}

// AnthropicConfig holds runtime LLM provider settings. APIKey is NOT marked
// required — open-bbcd boots without it; chat endpoints lazy-fail at first
// request when missing. This lets the rest of the service work in environments
// without an Anthropic key (development, CI for non-chat features).
type AnthropicConfig struct {
	APIKey string `env:"ANTHROPIC_API_KEY"`
	// DefaultModel is the anthropic adapter's model. On the bifrost adapter the
	// raw OPENBBC_DEFAULT_MODEL (<provider>/<model>) is parsed by parseLLMFromEnv.
	DefaultModel string `env:"OPENBBC_DEFAULT_MODEL" envDefault:"claude-sonnet-4-6"`
	MaxTokens    int    `env:"OPENBBC_MAX_TOKENS" envDefault:"4096"`
}

// ChatConfig controls the chat runtime: transport choice + per-turn loop bounds.
type ChatConfig struct {
	Transport     string `env:"OPENBBC_CHAT_TRANSPORT" envDefault:"agui"`
	MaxToolRounds int    `env:"OPENBBC_MAX_TOOL_ROUNDS" envDefault:"10"`
	// AgentToolMaxDepth caps sub-agent nesting (root = 0); AgentToolMaxParallel
	// caps concurrent agent calls per assistant step per session node. Both
	// are validated as integers >= 1 in Load (AGENT_TOOL_MAX_DEPTH /
	// AGENT_TOOL_MAX_PARALLEL); defaults 3 and 4.
	AgentToolMaxDepth    int
	AgentToolMaxParallel int
}

// positiveIntEnv reads name as an integer >= 1, returning def when unset.
func positiveIntEnv(name string, def int) (int, error) {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s: expected integer >= 1, got %q", name, v)
	}
	return n, nil
}

// ArtifactsConfig captures deployer-declared artifact-store registry state
// plus the two global tuning knobs (per-upload cap, presigned-URL TTL).
//
// Registry semantics: len(Stores) == 0 means artifact routes are not wired
// (feature-gated off — analogous to how AnthropicConfig.APIKey==""  keeps
// chat endpoints from failing at boot). When Stores is non-empty,
// DefaultID must resolve to one of them or Load() fails at boot.
type ArtifactsConfig struct {
	// Stores is keyed by <ID> — the uppercase slug that appeared in
	// ARTIFACT_STORE_<ID>_KIND. That slug is also the store_id embedded
	// in every artifact_ref block; it must be stable across deploys or
	// historical refs stop resolving.
	Stores map[string]ArtifactStoreConfig

	// DefaultID is the value of ARTIFACT_STORE_DEFAULT. All new uploads
	// route to Stores[DefaultID]. Reads route via the store_id on each
	// ref, so old refs keep working after DefaultID flips across
	// redeploys.
	DefaultID string

	// MaxUploadMB caps per-artifact upload size at the multipart
	// boundary — 413 before any store call is made. No default is
	// shipped; deployers set this explicitly.
	MaxUploadMB int

	// MaxPending caps pending (uploaded, not yet claimed) artifacts per
	// session. ARTIFACT_MAX_PENDING, default 10, must be >= 1.
	MaxPending int

	// SignedURLTTL is passed to ArtifactStore.Sign when the retrieval
	// route serves a 302 redirect. Default 300s.
	SignedURLTTL time.Duration
}

// ArtifactStoreConfig is one entry in the boot-time registry. Options
// carries kind-specific config (endpoint, bucket, credentials, etc.) as
// raw strings; each adapter kind interprets its own keys. This keeps
// this package free of kind-specific dependencies.
type ArtifactStoreConfig struct {
	ID      string
	Kind    string
	Options map[string]string
}

// Enabled reports whether artifact routes should be wired. When false,
// callers skip artifact registration entirely and the daemon boots with
// no artifact surface — matching the feature-gate pattern used for the
// Anthropic API key.
func (a ArtifactsConfig) Enabled() bool { return len(a.Stores) > 0 }

// artifactIDPattern validates <ID> slugs in env-var group names.
// Uppercase letter start, then uppercase alphanumeric only — deliberately
// no underscore, so that ARTIFACT_STORE_MAIN_ACCESS_KEY unambiguously
// splits as <ID>=MAIN + <FIELD>=ACCESS_KEY (first underscore after
// the ARTIFACT_STORE_ prefix). Rejects lowercase, dashes, leading digits,
// and multi-word IDs — all of which would silently produce a confusing
// store_id in every artifact_ref (flagged by /spec-review).
var artifactIDPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*$`)

// reservedArtifactIDs are <ID> slugs that would collide with global
// artifact env vars. DEFAULT is the only actual collision under the
// ARTIFACT_STORE_ prefix; MAX_UPLOAD_MB and SIGNED_URL_TTL_SECONDS
// live under ARTIFACT_ (not ARTIFACT_STORE_) so cannot collide.
// Reserving them anyway matches spec-review's belt-and-braces advice.
var reservedArtifactIDs = map[string]bool{
	"DEFAULT":                true,
	"MAX_UPLOAD_MB":          true,
	"SIGNED_URL_TTL_SECONDS": true,
}

// parseArtifactsFromEnv reads os.Environ()-style KEY=VALUE strings and
// extracts the artifact-store configuration. Exposed for testing;
// production callers go through Load().
//
// Return values:
//   - nil error + Enabled()==false → no ARTIFACT_STORE_* vars found;
//     artifact routes should not be wired.
//   - nil error + Enabled()==true → registry is valid; caller can
//     construct adapters and hydrate the runtime registry.
//   - non-nil error → malformed configuration; boot must fail with
//     the error message surfaced verbatim.
func parseArtifactsFromEnv(environ []string) (ArtifactsConfig, error) {
	kv := make(map[string]string, len(environ))
	for _, e := range environ {
		i := strings.IndexByte(e, '=')
		if i < 0 {
			continue
		}
		kv[e[:i]] = e[i+1:]
	}

	cfg := ArtifactsConfig{
		Stores:       map[string]ArtifactStoreConfig{},
		DefaultID:    kv["ARTIFACT_STORE_DEFAULT"],
		SignedURLTTL: 300 * time.Second,
		MaxPending:   10,
	}

	if v := kv["ARTIFACT_MAX_UPLOAD_MB"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return ArtifactsConfig{}, fmt.Errorf("ARTIFACT_MAX_UPLOAD_MB: expected positive integer, got %q", v)
		}
		cfg.MaxUploadMB = n
	}

	if v := kv["ARTIFACT_SIGNED_URL_TTL_SECONDS"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return ArtifactsConfig{}, fmt.Errorf("ARTIFACT_SIGNED_URL_TTL_SECONDS: expected positive integer, got %q", v)
		}
		cfg.SignedURLTTL = time.Duration(n) * time.Second
	}

	if v := kv["ARTIFACT_MAX_PENDING"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return ArtifactsConfig{}, fmt.Errorf("ARTIFACT_MAX_PENDING: expected integer >= 1, got %q", v)
		}
		cfg.MaxPending = n
	}

	// Group per-store vars: ARTIFACT_STORE_<ID>_<FIELD>=<value>.
	// <ID> is validated against artifactIDPattern; <FIELD> is
	// preserved verbatim (all-uppercase) and stored in Options keyed
	// by the field name minus the ARTIFACT_STORE_<ID>_ prefix.
	const prefix = "ARTIFACT_STORE_"
	for k, v := range kv {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := k[len(prefix):]
		if rest == "DEFAULT" {
			continue // handled above
		}

		// Split rest into <ID>_<FIELD> at the FIRST underscore. Since
		// artifactIDPattern forbids underscores in IDs, this is
		// unambiguous — ARTIFACT_STORE_MAIN_ACCESS_KEY → id=MAIN,
		// field=ACCESS_KEY.
		firstUnder := strings.IndexByte(rest, '_')
		if firstUnder < 1 || firstUnder == len(rest)-1 {
			return ArtifactsConfig{}, fmt.Errorf("%s: malformed env var name (expected ARTIFACT_STORE_<ID>_<FIELD>)", k)
		}
		id := rest[:firstUnder]
		field := rest[firstUnder+1:]

		if !artifactIDPattern.MatchString(id) {
			return ArtifactsConfig{}, fmt.Errorf("%s: invalid store id %q — must be uppercase alphanumeric, starting with a letter, no underscores", k, id)
		}
		if reservedArtifactIDs[id] {
			return ArtifactsConfig{}, fmt.Errorf("%s: store id %q is reserved and cannot be used as a store slug", k, id)
		}

		entry, ok := cfg.Stores[id]
		if !ok {
			entry = ArtifactStoreConfig{ID: id, Options: map[string]string{}}
		}
		if field == "KIND" {
			entry.Kind = v
		} else {
			entry.Options[field] = v
		}
		cfg.Stores[id] = entry
	}

	// Semantic validation: every registered store must declare a KIND.
	// Missing KIND means the deployer set some options but forgot the
	// discriminator — fail loudly instead of silently constructing an
	// unusable entry.
	for id, s := range cfg.Stores {
		if s.Kind == "" {
			return ArtifactsConfig{}, fmt.Errorf("ARTIFACT_STORE_%s_KIND is required (found other %s vars but no KIND)", id, id)
		}
	}

	// Semantic validation: when at least one store is registered, the
	// DEFAULT must be set and must resolve. When zero stores are
	// registered, DEFAULT is allowed to be unset (feature off).
	if len(cfg.Stores) > 0 {
		if cfg.DefaultID == "" {
			return ArtifactsConfig{}, fmt.Errorf("ARTIFACT_STORE_DEFAULT is required when at least one ARTIFACT_STORE_<ID>_* group is configured")
		}
		if _, ok := cfg.Stores[cfg.DefaultID]; !ok {
			return ArtifactsConfig{}, fmt.Errorf("ARTIFACT_STORE_DEFAULT=%q does not match any configured ARTIFACT_STORE_<ID>_* group", cfg.DefaultID)
		}
		if cfg.MaxUploadMB <= 0 {
			return ArtifactsConfig{}, fmt.Errorf("ARTIFACT_MAX_UPLOAD_MB is required when at least one artifact store is configured (must be a positive integer)")
		}
	}

	return cfg, nil
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, err
	}

	var err error
	if cfg.Chat.AgentToolMaxDepth, err = positiveIntEnv("AGENT_TOOL_MAX_DEPTH", 3); err != nil {
		return nil, err
	}
	if cfg.Chat.AgentToolMaxParallel, err = positiveIntEnv("AGENT_TOOL_MAX_PARALLEL", 4); err != nil {
		return nil, err
	}

	artifacts, err := parseArtifactsFromEnv(os.Environ())
	if err != nil {
		return nil, err
	}
	cfg.Artifacts = artifacts

	llmCfg, err := parseLLMFromEnv(os.Environ(), cfg.Anthropic)
	if err != nil {
		return nil, err
	}
	cfg.LLM = llmCfg

	return cfg, nil
}
