// Package agui implements transport.Factory using the AG-UI protocol over
// Server-Sent Events. Wraps the community Go SDK's SSE writer + event types;
// translates orchestrator internal events to AG-UI events.
package agui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	sse "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"github.com/google/uuid"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/transport"
)

// Factory creates AG-UI SSE sinks.
type Factory struct{}

// NewFactory returns a new Factory.
func NewFactory() *Factory { return &Factory{} }

// ContentType returns the MIME type for AG-UI SSE streams.
func (Factory) ContentType() string { return "text/event-stream" }

// NewSink builds an AG-UI sink on top of an HTTP response. Requires the
// writer to implement http.Flusher (every modern http.ResponseWriter does).
func (Factory) NewSink(w http.ResponseWriter) (transport.Sink, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("agui: ResponseWriter does not implement http.Flusher")
	}
	return newSink(w, flusher), nil
}

// newWriterSink is a test-only constructor for raw writers (bypasses HTTP
// plumbing). Uses a no-op flusher.
func newWriterSink(w io.Writer) transport.Sink {
	return newSink(w, noopFlusher{})
}

type noopFlusher struct{}

func (noopFlusher) Flush() {}

// flushedWriter wraps an io.Writer with an http.Flusher so the SSEWriter can
// flush after each event. The underlying writer is always an io.Writer; the
// flusher is invoked separately after each WriteEvent call.
type flushedWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func newSink(w io.Writer, f http.Flusher) *sink {
	return &sink{
		fw:    &flushedWriter{w: w, flusher: f},
		sw:    sse.NewSSEWriter(),
		runID: uuid.NewString(),
	}
}

type sink struct {
	mu       sync.Mutex
	closed   bool
	fw       *flushedWriter
	sw       *sse.SSEWriter
	runID    string
	threadID string // set on SessionStartEvent
}

// Send maps the internal event to an AG-UI event and writes it to the stream.
func (s *sink) Send(ctx context.Context, ev transport.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("agui: sink closed")
	}

	aguiEv, err := s.translate(ev)
	if err != nil {
		return err
	}
	if aguiEv == nil {
		return nil // nothing to send
	}

	if err := s.sw.WriteEvent(ctx, s.fw.w, aguiEv); err != nil {
		return err
	}
	s.fw.flusher.Flush()
	return nil
}

// Close marks the sink as closed. Subsequent Send calls return an error.
// Idempotent — multiple closes are safe.
func (s *sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// translate maps an internal Event to an AG-UI event per spec §8.3.
// Returns (nil, nil) if no event should be sent.
func (s *sink) translate(ev transport.Event) (events.Event, error) {
	switch e := ev.(type) {
	case transport.SessionStartEvent:
		s.threadID = e.SessionID
		return events.NewRunStartedEvent(e.SessionID, s.runID), nil

	case transport.TextStartEvent:
		return events.NewTextMessageStartEvent(
			e.MessageID,
			events.WithRole("assistant"),
		), nil

	case transport.TextDeltaEvent:
		return events.NewTextMessageContentEvent(e.MessageID, e.Delta), nil

	case transport.TextEndEvent:
		return events.NewTextMessageEndEvent(e.MessageID), nil

	case transport.ToolCallStartEvent:
		return events.NewToolCallStartEvent(e.ToolCallID, e.Name), nil

	case transport.ToolCallArgsEvent:
		return events.NewToolCallArgsEvent(e.ToolCallID, e.ArgsJSON), nil

	case transport.ToolCallEndEvent:
		return events.NewToolCallEndEvent(e.ToolCallID), nil

	case transport.ToolResultEvent:
		// Serialize result + is_error flag as JSON content string.
		payload, _ := json.Marshal(map[string]any{
			"result":   json.RawMessage(e.Result),
			"is_error": e.IsError,
		})
		// NewToolCallResultEvent(messageID, toolCallID, content): the AG-UI spec
		// requires a non-empty messageID to associate the result with the assistant
		// turn. We use the toolCallID as a proxy — it uniquely identifies the call.
		return events.NewToolCallResultEvent(e.ToolCallID, e.ToolCallID, string(payload)), nil

	case transport.TurnEndEvent:
		return events.NewRunFinishedEvent(s.threadID, s.runID), nil

	case transport.ErrorEvent:
		return events.NewRunErrorEvent(
			e.Message,
			events.WithErrorCode(e.Code),
			events.WithRunID(s.runID),
		), nil
	}

	return nil, fmt.Errorf("agui: unhandled internal event type %T", ev)
}

// Compile-time interface conformance.
var _ transport.Factory = (*Factory)(nil)
var _ transport.Sink = (*sink)(nil)
