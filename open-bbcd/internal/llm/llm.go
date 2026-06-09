// Package llm defines a provider-agnostic chat LLM interface used by the
// run-agent orchestrator. The orchestrator depends only on this package;
// concrete provider adapters live in sub-packages (internal/llm/anthropic,
// etc.).
package llm

import (
	"context"
	"encoding/json"
	"iter"
)

// Role identifies the speaker in a Message. The Anthropic-style "system"
// role is not used in Message.Role — system text travels in Request.System.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Block is one content block within a Message.
type Block interface{ isBlock() }

type TextBlock struct {
	Text string
}

func (TextBlock) isBlock() {}

type ToolUseBlock struct {
	ID    string
	Name  string
	Input json.RawMessage
}

func (ToolUseBlock) isBlock() {}

type ToolResultBlock struct {
	ToolUseID string
	Result    json.RawMessage
	IsError   bool
}

func (ToolResultBlock) isBlock() {}

// Message is a single role-tagged content payload.
type Message struct {
	Role    Role
	Content []Block
}

// ToolDef describes a tool the model can call. InputSchema is a JSON Schema
// describing the expected input shape; permissive schemas are fine for v1.
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Request is the input to LLM.Generate. System holds the system prompt as
// a string (not a Message — Anthropic's API treats system as a separate
// field). Tools may be empty.
type Request struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolDef
	MaxTokens   int
	Temperature float64
}

// Event is a streaming event emitted by an LLM provider adapter. Adapters
// normalize provider-native chunks into this taxonomy.
type Event interface{ isEvent() }

// TextDeltaEvent: a fragment of streamed assistant text.
type TextDeltaEvent struct{ Delta string }

// ToolUseStartEvent: the model has decided to call a tool. ID + Name are
// known at start; arguments arrive as ToolUseInputEvent fragments.
type ToolUseStartEvent struct{ ID, Name string }

// ToolUseInputEvent: a partial JSON fragment of the tool's input.
// Concatenating all fragments for a given ID yields the full input JSON.
type ToolUseInputEvent struct{ ID, JSONFragment string }

// ToolUseEndEvent: the tool call's input is complete.
type ToolUseEndEvent struct{ ID string }

// MessageStopEvent: the model has stopped emitting tokens for this turn.
// StopReason is provider-specific; common values: "end_turn", "tool_use",
// "max_tokens", "stop_sequence".
type MessageStopEvent struct{ StopReason string }

// UsageEvent: token usage. Adapters may emit multiple Usage events per
// turn; orchestrator should keep the highest cumulative value.
type UsageEvent struct{ InputTokens, OutputTokens int }

func (TextDeltaEvent) isEvent()    {}
func (ToolUseStartEvent) isEvent() {}
func (ToolUseInputEvent) isEvent() {}
func (ToolUseEndEvent) isEvent()   {}
func (MessageStopEvent) isEvent()  {}
func (UsageEvent) isEvent()        {}

// LLM is the provider-agnostic chat model interface. Generate streams events
// for one model turn; cancellation via ctx. Adapters may emit error via the
// iter.Seq2 second value at any point.
type LLM interface {
	Name() string
	Generate(ctx context.Context, req Request) iter.Seq2[Event, error]
}
