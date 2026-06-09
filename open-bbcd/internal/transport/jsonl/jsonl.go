// Package jsonl is a test-only Transport implementation that writes each
// internal event as one JSON line. Used by orchestrator + handler tests
// so they don't need an SSE parser. Also doubles as proof that the
// transport.Factory interface admits non-AG-UI impls.
package jsonl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
)

type Factory struct{}

func NewFactory() *Factory { return &Factory{} }

func (Factory) ContentType() string { return "application/x-ndjson" }

func (f *Factory) NewSink(w http.ResponseWriter) (transport.Sink, error) {
	return f.NewWriterSink(w)
}

// NewWriterSink builds a Sink against a raw io.Writer. Used by tests
// that don't have an http.ResponseWriter.
func (Factory) NewWriterSink(w io.Writer) (transport.Sink, error) {
	return &sink{w: w, enc: json.NewEncoder(w)}, nil
}

type sink struct {
	mu  sync.Mutex
	w   io.Writer
	enc *json.Encoder
}

// Send writes one JSON line: {"type":"<event_name>","data":<event>}.
func (s *sink) Send(ctx context.Context, ev transport.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	typ, payload := serialize(ev)
	return s.enc.Encode(map[string]any{"type": typ, "data": payload})
}

func (s *sink) Close() error { return nil }

// serialize returns the discriminator string and the payload to embed
// under "data". Falls back to type name for any event the switch hasn't
// covered (should never happen — keeps the function total).
func serialize(ev transport.Event) (string, any) {
	switch e := ev.(type) {
	case transport.SessionStartEvent:
		return "session_start", e
	case transport.TextStartEvent:
		return "text_start", e
	case transport.TextDeltaEvent:
		return "text_delta", e
	case transport.TextEndEvent:
		return "text_end", e
	case transport.ToolCallStartEvent:
		return "tool_call_start", e
	case transport.ToolCallArgsEvent:
		return "tool_call_args", e
	case transport.ToolCallEndEvent:
		return "tool_call_end", e
	case transport.ToolResultEvent:
		return "tool_call_result", e
	case transport.TurnEndEvent:
		return "run_finished", e
	case transport.ErrorEvent:
		return "run_error", e
	default:
		return fmt.Sprintf("%T", ev), ev
	}
}

// Compile-time interface conformance checks.
var (
	_ transport.Factory       = (*Factory)(nil)
	_ transport.WriterFactory = (*Factory)(nil)
	_ transport.Sink          = (*sink)(nil)
)
