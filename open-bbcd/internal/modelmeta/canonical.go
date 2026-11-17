// Package modelmeta parses the model and eval-mode metadata aikdm records
// (an eval's aikdm_meta, a training report's "models" block) and compares
// the scoring target model with the model this open-bbcd runs agents on.
// Nothing here does I/O and it imports nothing internal, so both handler and
// repository may import it.
package modelmeta

import "strings"

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

// TargetDiffers reports whether the scoring target model and the runtime
// model differ once both are canonicalized. It is false when either side is
// unknown (""), so no mismatch notice is shown for missing data.
func TargetDiffers(target, runtime string) bool {
	if target == "" || runtime == "" {
		return false
	}
	return CanonicalModel(target) != CanonicalModel(runtime)
}

// View is the model metadata a detail page shows: the parsed Models, the
// runtime model, and whether the scoring target differs from it.
type View struct {
	Models   Models
	Runtime  string
	Mismatch bool
}

// NewView builds a View. Mismatch has TargetDiffers semantics: false when
// the target or the runtime model is unknown ("").
func NewView(m Models, runtime string) View {
	return View{Models: m, Runtime: runtime, Mismatch: TargetDiffers(m.Target, runtime)}
}
