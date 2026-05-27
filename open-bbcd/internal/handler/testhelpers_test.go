package handler

import (
	"io"
	"log/slog"
)

// testLogger returns a slog.Logger that discards output, suitable for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
