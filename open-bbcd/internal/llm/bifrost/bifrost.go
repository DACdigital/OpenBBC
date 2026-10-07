// Package bifrost adapts the embedded Bifrost Go SDK
// (github.com/maximhq/bifrost/core) to the open-bbcd internal/llm interface.
// One adapter instance talks to the single provider selected by
// OPENBBC_DEFAULT_MODEL=<provider>/<model>, over Bifrost's OpenAI-shaped
// chat-completions API.
package bifrost

import (
	bifrostcore "github.com/maximhq/bifrost/core"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
)

// LLM is the Bifrost provider adapter.
type LLM struct {
	cfg    config.LLMConfig
	client *bifrostcore.Bifrost
}
