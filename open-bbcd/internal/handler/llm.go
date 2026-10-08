package handler

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/anthropic"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/bifrost"
)

// newBifrost is a seam so tests can force an init failure.
var newBifrost = bifrost.New

// NewLLM builds the boot-selected LLM adapter (OPENBBC_LLM_ADAPTER). The
// shutdown func releases adapter resources; it is a no-op for the direct
// Anthropic adapter. One adapter serves both orchestrators and every
// sub-agent. Either adapter is wrapped by llm.WithCallLog, so with
// LOG_LEVEL=debug every LLM call logs the adapter that served it.
func NewLLM(cfg *config.Config, logger *slog.Logger) (llm.LLM, func(), error) {
	if cfg.LLM.Adapter == config.LLMAdapterBifrost {
		b, err := newBifrost(context.Background(), cfg.LLM, logger)
		if err != nil {
			return nil, nil, err
		}
		logger.Info("llm adapter ready", slog.String("adapter", b.Name()), slog.String("model", cfg.LLM.Model))
		return llm.WithCallLog(b, logger), b.Shutdown, nil
	}
	a := anthropic.New(cfg.Anthropic)
	logger.Info("llm adapter ready", slog.String("adapter", a.Name()), slog.String("model", cfg.LLMModel()))
	return llm.WithCallLog(a, logger), func() {}, nil
}

// NewAPIWithLLM is NewAPI around an adapter built by NewLLM.
func NewAPIWithLLM(db *sql.DB, cfg *config.Config, logger *slog.Logger, client llm.LLM) http.Handler {
	return newAPI(db, cfg, logger, client)
}
