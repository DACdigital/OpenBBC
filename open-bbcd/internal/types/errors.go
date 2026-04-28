package types

import "errors"

var (
	ErrNameRequired   = errors.New("name is required")
	ErrPromptRequired = errors.New("prompt is required")
	ErrAgentRequired  = errors.New("agent_id is required")
	ErrNotFound       = errors.New("not found")
)
