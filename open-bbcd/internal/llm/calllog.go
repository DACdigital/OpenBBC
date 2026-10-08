package llm

import (
	"context"
	"iter"
	"log/slog"
	"time"
)

// WithCallLog wraps an adapter so every Generate call writes one debug-level
// "llm call" line naming the adapter that served it (Name(), e.g. "anthropic"
// or "bifrost:openai"), the model, request size, stop reason, token usage,
// duration and any error. The line is written when the call ends, including
// when the consumer stops iterating early. Run with LOG_LEVEL=debug to see it.
//
// The wrapper keeps MultimodalRenderer visible when the inner adapter
// implements it: callers detect native media support by type assertion.
func WithCallLog(inner LLM, logger *slog.Logger) LLM {
	l := &callLogged{inner: inner, logger: logger}
	if r, ok := inner.(MultimodalRenderer); ok {
		return &callLoggedRenderer{callLogged: l, MultimodalRenderer: r}
	}
	return l
}

type callLogged struct {
	inner  LLM
	logger *slog.Logger
}

type callLoggedRenderer struct {
	*callLogged
	MultimodalRenderer
}

func (c *callLogged) Name() string { return c.inner.Name() }

func (c *callLogged) Generate(ctx context.Context, req Request) iter.Seq2[Event, error] {
	if !c.logger.Enabled(ctx, slog.LevelDebug) {
		return c.inner.Generate(ctx, req)
	}
	return func(yield func(Event, error) bool) {
		start := time.Now()
		var (
			stopReason    string
			inTok, outTok int
			callErr       error
		)
		defer func() {
			attrs := []slog.Attr{
				slog.String("adapter", c.inner.Name()),
				slog.String("model", req.Model),
				slog.Int("messages", len(req.Messages)),
				slog.Int("tools", len(req.Tools)),
				slog.String("stop_reason", stopReason),
				slog.Int("input_tokens", inTok),
				slog.Int("output_tokens", outTok),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			}
			if callErr != nil {
				attrs = append(attrs, slog.String("error", callErr.Error()))
			}
			c.logger.LogAttrs(ctx, slog.LevelDebug, "llm call", attrs...)
		}()
		for ev, err := range c.inner.Generate(ctx, req) {
			if err != nil {
				callErr = err
			}
			switch e := ev.(type) {
			case MessageStopEvent:
				stopReason = e.StopReason
			case UsageEvent:
				// Same rule as the orchestrator: keep the highest value seen.
				inTok = max(inTok, e.InputTokens)
				outTok = max(outTok, e.OutputTokens)
			}
			if !yield(ev, err) {
				return
			}
		}
	}
}
