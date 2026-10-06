// Package transport defines the wire-format-agnostic chat event stream.
// Orchestrator emits internal Events; transports map them to a wire format
// (AG-UI over SSE is the default; jsonl is shipped for tests; custom impls
// can implement Factory + Sink).
package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// Event is the orchestrator's internal event taxonomy. Transports translate
// these to their own wire format (AG-UI events, JSON lines, etc.).
type Event interface{ isEvent() }

// SessionStartEvent: emitted once at the start of a turn. Carries the
// session + agent identifiers so transports can include them in framing
// (AG-UI's RUN_STARTED, for instance).
type SessionStartEvent struct {
	SessionID string
	AgentID   string
}

// TextStartEvent: a new assistant text message begins.
type TextStartEvent struct {
	MessageID string
}

// TextDeltaEvent: a fragment of streamed assistant text. Belongs to the
// TextStartEvent's MessageID.
type TextDeltaEvent struct {
	MessageID string
	Delta     string
}

// TextEndEvent: the assistant text message is complete.
type TextEndEvent struct {
	MessageID string
}

// ToolCallStartEvent: the model has decided to call a tool.
type ToolCallStartEvent struct {
	ToolCallID string
	Name       string
	// ChildSessionID is set when the event comes from a sub-agent child
	// session; empty for the session's own events.
	ChildSessionID string `json:"child_session_id,omitempty"`
}

// ToolCallArgsEvent: streamed JSON fragment of the tool call's arguments.
type ToolCallArgsEvent struct {
	ToolCallID string
	ArgsJSON   string
	// ChildSessionID is set when the event comes from a sub-agent child
	// session; empty for the session's own events.
	ChildSessionID string `json:"child_session_id,omitempty"`
}

// ToolCallEndEvent: the tool call's arguments are complete.
type ToolCallEndEvent struct {
	ToolCallID string
	// ChildSessionID is set when the event comes from a sub-agent child
	// session; empty for the session's own events.
	ChildSessionID string `json:"child_session_id,omitempty"`
}

// ToolResultEvent: the orchestrator (not the model) has executed the tool
// and emits its result. Sent BEFORE the next round of model generation.
type ToolResultEvent struct {
	ToolCallID string
	Result     json.RawMessage
	IsError    bool
	// ChildSessionID is set when the event comes from a sub-agent child
	// session; empty for the session's own events.
	ChildSessionID string `json:"child_session_id,omitempty"`
}

// StepStartedEvent brackets one sub-agent run on the parent stream (spec §
// AG-UI stream). ToolCallID is the wire id of the parent's agent call (never
// re-prefixed by transports); ChildSessionID is the child session.
type StepStartedEvent struct {
	StepName       string // binding name
	ToolCallID     string
	ChildSessionID string
	Description    string
}

// StepFinishedEvent closes the StepStartedEvent with the same ToolCallID.
type StepFinishedEvent struct {
	StepName       string
	ToolCallID     string
	ChildSessionID string
	IsError        bool
}

// ArtifactRefEvent: a tool result produced an artifact. Sent after the
// round's tool-role message (and its artifact rows) are persisted, so the
// ref is resolvable as soon as the client receives it. Never sent for user
// uploads. The payload only ever changes additively.
type ArtifactRefEvent struct {
	ToolCallID string
	StoreID    string
	URI        string
	MIME       string
	SizeBytes  int64
	Sha256     string
	Filename   string // empty when unknown
}

// TurnEndEvent: the turn is complete. StopReason matches the LLM adapter's
// emitted reason ("end_turn", "tool_use", "max_tokens", etc.).
type TurnEndEvent struct {
	StopReason string
	UsageIn    int
	UsageOut   int
}

// ErrorEvent: a non-terminal stream-level error (the connection stays open
// so the client sees the failure in-band rather than as a broken HTTP
// response). Transports should map this to their error-event equivalent
// (AG-UI's RUN_ERROR).
type ErrorEvent struct {
	Code    string
	Message string
}

func (SessionStartEvent) isEvent()  {}
func (TextStartEvent) isEvent()     {}
func (TextDeltaEvent) isEvent()     {}
func (TextEndEvent) isEvent()       {}
func (ToolCallStartEvent) isEvent() {}
func (ToolCallArgsEvent) isEvent()  {}
func (ToolCallEndEvent) isEvent()   {}
func (ToolResultEvent) isEvent()    {}
func (StepStartedEvent) isEvent()   {}
func (StepFinishedEvent) isEvent()  {}
func (ArtifactRefEvent) isEvent()   {}
func (TurnEndEvent) isEvent()       {}
func (ErrorEvent) isEvent()         {}

// Sink serializes Events to an io.Writer (typically an http.ResponseWriter).
// Implementations own per-event framing (SSE event blocks, JSON lines,
// websocket frames, etc.) but NOT the HTTP connection lifecycle — the
// handler that constructed the Sink manages the connection.
type Sink interface {
	Send(ctx context.Context, ev Event) error
	Close() error
}

// Factory builds a Sink for an HTTP response. Implementations should set
// any framing-specific response headers via the writer before returning
// the Sink (or expose ContentType() so the handler can do it consistently).
type Factory interface {
	NewSink(w http.ResponseWriter) (Sink, error)
	ContentType() string
}

// WriterFactory is an optional extended interface for transports that can
// also serialize to a raw io.Writer (not just an http.ResponseWriter).
// Used by the test-only JSONL transport.
type WriterFactory interface {
	Factory
	NewWriterSink(w io.Writer) (Sink, error)
}
