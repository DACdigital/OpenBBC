package handler

import (
	"log/slog"
	"net/http"
	"time"
)

// responseRecorder wraps ResponseWriter to capture the written status code.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// RequestLogger is HTTP middleware that logs every request after it completes.
func RequestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rw, r)

		level := slog.LevelInfo
		if rw.status >= 500 {
			level = slog.LevelError
		} else if rw.status >= 400 {
			level = slog.LevelWarn
		}

		logger.Log(r.Context(), level, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rw.status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("remote_addr", r.RemoteAddr),
		)
	})
}
