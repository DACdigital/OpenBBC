package bifrost

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maximhq/bifrost/core/schemas"
)

// slogLogger bridges Bifrost's printf-style Logger onto the daemon's slog
// logger, so Bifrost lines share open-bbcd's JSON stdout. Lines are tagged
// component=bifrost. Fatal logs at error level and never exits: an embedded
// library must not terminate the daemon. Level and output type are owned by
// the slog handler, so SetLevel and SetOutputType are no-ops.
type slogLogger struct{ l *slog.Logger }

var _ schemas.Logger = (*slogLogger)(nil)

func newSlogLogger(l *slog.Logger) *slogLogger {
	return &slogLogger{l: l.With(slog.String("component", "bifrost"))}
}

func (s *slogLogger) log(level slog.Level, msg string, args []any) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	s.l.Log(context.Background(), level, msg)
}

func (s *slogLogger) Debug(msg string, args ...any)          { s.log(slog.LevelDebug, msg, args) }
func (s *slogLogger) Info(msg string, args ...any)           { s.log(slog.LevelInfo, msg, args) }
func (s *slogLogger) Warn(msg string, args ...any)           { s.log(slog.LevelWarn, msg, args) }
func (s *slogLogger) Error(msg string, args ...any)          { s.log(slog.LevelError, msg, args) }
func (s *slogLogger) Fatal(msg string, args ...any)          { s.log(slog.LevelError, msg, args) }
func (s *slogLogger) SetLevel(schemas.LogLevel)              {}
func (s *slogLogger) SetOutputType(schemas.LoggerOutputType) {}

func (s *slogLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return &slogEvent{l: s.l, level: slogLevel(level), msg: msg}
}

func slogLevel(l schemas.LogLevel) slog.Level {
	switch l {
	case schemas.LogLevelDebug:
		return slog.LevelDebug
	case schemas.LogLevelWarn:
		return slog.LevelWarn
	case schemas.LogLevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// slogEvent collects typed fields until Send.
type slogEvent struct {
	l     *slog.Logger
	level slog.Level
	msg   string
	attrs []slog.Attr
}

func (e *slogEvent) Str(k, v string) schemas.LogEventBuilder {
	e.attrs = append(e.attrs, slog.String(k, v))
	return e
}
func (e *slogEvent) Int(k string, v int) schemas.LogEventBuilder {
	e.attrs = append(e.attrs, slog.Int(k, v))
	return e
}
func (e *slogEvent) Int64(k string, v int64) schemas.LogEventBuilder {
	e.attrs = append(e.attrs, slog.Int64(k, v))
	return e
}
func (e *slogEvent) Send() { e.l.LogAttrs(context.Background(), e.level, e.msg, e.attrs...) }
