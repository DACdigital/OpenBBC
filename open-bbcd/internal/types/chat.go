package types

import (
	"encoding/json"
	"time"
)

type ChatSession struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Title     string    `json:"title,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ChatRole string

const (
	ChatRoleUser      ChatRole = "user"
	ChatRoleAssistant ChatRole = "assistant"
	ChatRoleTool      ChatRole = "tool"
)

// ChatMessage holds an array of content blocks as raw JSON. Typed parsing
// happens at the LLM-adapter layer where Anthropic-style block shapes
// (TextBlock / ToolUseBlock / ToolResultBlock) are known.
type ChatMessage struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Role      ChatRole        `json:"role"`
	Content   json.RawMessage `json:"content"`
	Seq       int             `json:"seq"`
	CreatedAt time.Time       `json:"created_at"`
}
