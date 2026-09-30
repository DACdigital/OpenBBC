// Package llm defines a provider-agnostic chat LLM interface used by the
// run-agent orchestrator. The orchestrator depends only on this package;
// concrete provider adapters live in sub-packages (internal/llm/anthropic,
// etc.).
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"time"
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

// ArtifactRefBlock is a pointer to a blob stored in the deployer's
// artifact store. Framework code puts these on:
//   - user-role messages when the admin attaches a file to a turn;
//   - tool-role messages when an MCP tool result carries an
//     ImageContent or inline EmbeddedResource, which the orchestrator
//     unpacks into an artifact_ref block before persistence.
//
// The block is JSON-persisted verbatim on chat_messages.content (via
// types.ChatMessage.Content). Providers that natively support the ref's
// MIME (via MultimodalRenderer, below) inline the bytes into their
// wire format; providers that don't get a text-surrogate fallback
// rendered by the framework.
//
// Bytes never live in this struct — StoreID + URI is the only handle
// the framework carries around. Fetching bytes goes through the
// artifacts.ArtifactStore adapter selected by StoreID.
type ArtifactRefBlock struct {
	StoreID   string
	URI       string
	MIME      string
	SizeBytes int64
	Sha256    string
	// Filename is optional per-ref display metadata; UI renders as
	// the user-facing label. Never used to construct storage URIs.
	Filename string
}

func (ArtifactRefBlock) isBlock() {}

// InlineMediaBlock carries raw bytes plus their MIME, produced by a
// provider's MultimodalRenderer when it supports the ref's MIME natively.
// The adapter's message-conversion switch (e.g. anthropic.convertMessage)
// unpacks the bytes into the provider-native inline-media block type
// (Anthropic image / document, OpenAI image_url, etc.).
//
// Not persisted to chat_messages.content — this block only exists during
// in-flight request assembly. The persisted form of the reference is the
// original ArtifactRefBlock; renderers materialise InlineMediaBlock on
// demand at completion time.
type InlineMediaBlock struct {
	MIME string
	Data []byte // raw bytes, NOT base64-encoded — adapter encodes as needed
}

func (InlineMediaBlock) isBlock() {}

// MultimodalRenderer is an OPTIONAL interface an LLM provider may
// implement in addition to LLM. Providers that natively support one
// or more MIMEs (Anthropic → image/*, application/pdf; others per
// their own follow-on adapter work) implement this and return a
// provider-native block for supported MIMEs. Providers that return
// ErrUnsupported for a MIME — or that do not implement the interface
// at all — get a text-surrogate substitution rendered by the framework
// via TextSurrogate below.
//
// The interface is separate from LLM (not merged in) so existing
// provider adapters compile without change; opt-in via a type
// assertion at the caller.
type MultimodalRenderer interface {
	// RenderArtifactAsBlock converts an ArtifactRefBlock into a
	// provider-native Block for inclusion in the completion request
	// message list. The store handle is provided so the renderer can
	// fetch bytes via Get() or a signed URL via Sign() depending on
	// the store's PreferredDelivery.
	//
	// Return ErrUnsupported if this provider does not support the
	// ref's MIME natively — the caller substitutes a text surrogate.
	// Return any other error to abort the completion setup entirely.
	RenderArtifactAsBlock(ctx context.Context, ref ArtifactRefBlock, fetch ArtifactFetcher) (Block, error)
}

// ArtifactFetcher is the narrow slice of artifacts.ArtifactStore that
// RenderArtifactAsBlock needs. Declared here (not imported from the
// artifacts package) so llm has no import cycle risk; the handler
// wraps the adapter to satisfy this shape.
type ArtifactFetcher interface {
	Get(ctx context.Context, uri string) (io.ReadCloser, error)
	Sign(ctx context.Context, uri string, ttl time.Duration) (string, error)
	PreferredDelivery() int
}

// ErrUnsupported is returned by MultimodalRenderer.RenderArtifactAsBlock
// when the provider does not support the ref's MIME natively. Callers
// substitute a text-surrogate block (TextSurrogate) in place of the
// artifact_ref.
var ErrUnsupported = errors.New("llm: provider does not support this MIME natively")

// TextSurrogate produces a human-readable text block that stands in
// for an artifact_ref when the provider cannot render the ref natively.
// The pattern matches the spec's acceptance criteria:
//
//	[Attachment: <filename> (<mime>, <size>)]
//
// When ref.Filename is empty, the surrogate uses the URI's last path
// segment as a fallback label so the LLM still has something to refer
// to when it wants the user to look at the file.
func TextSurrogate(ref ArtifactRefBlock) TextBlock {
	label := ref.Filename
	if label == "" {
		// URI is "sha256/<hex>" — the tail is the hex, which is the
		// most stable identifier when no filename is present.
		if i := len(ref.URI) - 1; i >= 0 {
			for j := len(ref.URI) - 1; j >= 0; j-- {
				if ref.URI[j] == '/' {
					label = ref.URI[j+1:]
					break
				}
			}
		}
		if label == "" {
			label = ref.URI
		}
	}
	return TextBlock{Text: "[Attachment: " + label + " (" + ref.MIME + ", " + HumanBytes(ref.SizeBytes) + ")]"}
}

// HumanBytes renders a byte count in a compact human-readable form:
// bytes, KB, MB, GB. Deliberately small and dependency-free — pulling
// in github.com/dustin/go-humanize just for one call would be overkill.
func HumanBytes(n int64) string {
	const (
		kb = int64(1) << 10
		mb = int64(1) << 20
		gb = int64(1) << 30
	)
	switch {
	case n >= gb:
		return formatDecimal(n, gb) + " GB"
	case n >= mb:
		return formatDecimal(n, mb) + " MB"
	case n >= kb:
		return formatDecimal(n, kb) + " KB"
	default:
		return itoa(n) + " B"
	}
}

func formatDecimal(n, unit int64) string {
	whole := n / unit
	frac := (n % unit) * 10 / unit // one decimal place, truncated
	if frac == 0 {
		return itoa(whole)
	}
	return itoa(whole) + "." + itoa(frac)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

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
