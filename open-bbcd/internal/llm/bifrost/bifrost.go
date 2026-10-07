// Package bifrost adapts the embedded Bifrost Go SDK
// (github.com/maximhq/bifrost/core) to the open-bbcd internal/llm interface.
// One adapter instance talks to the single provider selected by
// OPENBBC_DEFAULT_MODEL=<provider>/<model>, over Bifrost's OpenAI-shaped
// chat-completions API.
package bifrost

import (
	"context"
	"fmt"
	"iter"
	"log/slog"

	bifrostcore "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// LLM is the Bifrost provider adapter.
type LLM struct {
	cfg    config.LLMConfig
	client *bifrostcore.Bifrost
}

// New initialises the embedded Bifrost client with an in-memory account
// holding cfg's provider, key and optional base URL. A missing key is not an
// error here: Generate fails lazily, matching the anthropic adapter.
func New(ctx context.Context, cfg config.LLMConfig, logger *slog.Logger) (*LLM, error) {
	client, err := bifrostcore.Init(ctx, schemas.BifrostConfig{
		Account: &account{provider: schemas.ModelProvider(cfg.Provider), apiKey: cfg.APIKey, baseURL: cfg.BaseURL},
		Logger:  newSlogLogger(logger),
	})
	if err != nil {
		return nil, fmt.Errorf("bifrost: init: %w", err)
	}
	return &LLM{cfg: cfg, client: client}, nil
}

// Name implements llm.LLM.
func (l *LLM) Name() string { return "bifrost:" + l.cfg.Provider }

// Shutdown releases the Bifrost client's workers and connections.
func (l *LLM) Shutdown() { l.client.Shutdown() }

// Generate implements llm.LLM: one streamed chat completion, translated to
// llm.Events.
func (l *LLM) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		if l.cfg.APIKey == "" {
			yield(nil, fmt.Errorf("bifrost: %s not configured", config.APIKeyEnv(l.cfg.Provider)))
			return
		}
		bctx, cancel := schemas.NewBifrostContextWithCancel(ctx)
		defer cancel()
		stream, berr := l.client.ChatCompletionStreamRequest(bctx, buildChatRequest(req, schemas.ModelProvider(l.cfg.Provider), l.cfg.Model))
		if berr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				yield(nil, ctxErr)
				return
			}
			yield(nil, providerError(l.cfg.Provider, berr))
			return
		}
		// However we return, keep draining so Bifrost's producer never
		// blocks on a send nobody reads (it closes the channel once bctx is
		// cancelled or the stream ends).
		defer func() { go drain(stream) }()

		// After cancellation Bifrost may still deliver a "Request cancelled"
		// error chunk or close the channel early; the caller's ctx error wins
		// so cancellation always surfaces as ctx.Err().
		fail := func(err error) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
			yield(nil, err)
		}

		t := newStreamTranslator(l.cfg.Provider)
		for {
			select {
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			case chunk, ok := <-stream:
				if !ok {
					if err := t.done(); err != nil {
						fail(err)
					}
					return
				}
				evs, err := t.translate(chunk)
				for _, ev := range evs {
					if !yield(ev, nil) {
						return
					}
				}
				if err != nil {
					fail(err)
					return
				}
			}
		}
	}
}

func drain(ch chan *schemas.BifrostStreamChunk) {
	for range ch {
	}
}
