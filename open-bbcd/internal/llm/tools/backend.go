// Package tools — Backend is a single source of tools the LLM can call.
// A real composite Handler fans out across Backends + the built-in Skill
// meta-tool.
package tools

import (
	"context"
	"encoding/json"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm"
)

// Backend is a per-session tool source. Implementations are constructed by
// the BackendBuilder at chat-session start and live for the duration of the
// session. Tools() may make remote calls (e.g., MCP tools/list); Call()
// dispatches one tool invocation.
type Backend interface {
	// Name is the stable identifier used for routing + log/error scoping.
	// For MCP backends, it is also the display prefix in LLM-visible tool
	// names (e.g., "slack" → "slack__send_message").
	Name() string

	// Tools returns the tool definitions this backend exposes to the LLM.
	// May be cached after first call within a session.
	Tools(ctx context.Context) ([]llm.ToolDef, error)

	// Call dispatches one tool invocation. unprefixedName is the tool name
	// after the composite handler has stripped any backend prefix.
	Call(ctx context.Context, unprefixedName string, input json.RawMessage) (Result, error)
}
