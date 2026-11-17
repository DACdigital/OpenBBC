// Package ui holds small display helpers shared by the back-office handlers
// and the repositories that feed them. Nothing here does I/O. It imports
// only internal/config, so both handler and repository may import it.
package ui

import (
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
)

// CanonicalModel returns s in provider/model form. It is used only to
// compare model strings; pages always display the configured string.
// Mirrors aikdm's provider inference (_normalize_model_string in
// aikdm/models.py plus the openai* branch of _provider_of in
// aikdm/config.py):
//   - a string containing "/" is returned unchanged;
//   - bare claude* → anthropic/<s>; gpt* or openai* → openai/<s>;
//     gemini* → gemini/<s>;
//   - anything else is returned unchanged.
func CanonicalModel(s string) string {
	switch {
	case strings.Contains(s, "/"):
		return s
	case strings.HasPrefix(s, "claude"):
		return "anthropic/" + s
	case strings.HasPrefix(s, "gpt"), strings.HasPrefix(s, "openai"):
		return "openai/" + s
	case strings.HasPrefix(s, "gemini"):
		return "gemini/" + s
	}
	return s
}

// RuntimeModel is the model this open-bbcd runs agents on, as configured.
// Bifrost adapter: "<provider>/<model>". Anthropic adapter, or a hand-built
// config with an empty LLM block: cfg.LLMModel() as is. It is never
// prefixed here; comparisons go through CanonicalModel, so an
// OPENBBC_DEFAULT_MODEL that already carries "anthropic/" is not prefixed
// twice.
func RuntimeModel(cfg *config.Config) string {
	if cfg.LLM.Adapter == config.LLMAdapterBifrost {
		return cfg.LLM.Provider + "/" + cfg.LLM.Model
	}
	return cfg.LLMModel()
}

// TargetDiffers reports whether the scoring target model and the runtime
// model differ once both are canonicalized. It is false when either side is
// unknown (""), so no mismatch notice is shown for missing data.
func TargetDiffers(target, runtime string) bool {
	if target == "" || runtime == "" {
		return false
	}
	return CanonicalModel(target) != CanonicalModel(runtime)
}
