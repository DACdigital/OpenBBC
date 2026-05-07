package types

import "errors"

var (
	ErrNameRequired   = errors.New("name is required")
	ErrPromptRequired = errors.New("prompt is required")
	ErrAgentRequired  = errors.New("agent_id is required")
	ErrNotFound       = errors.New("not found")

	ErrDiscoveryFileRequired     = errors.New("discovery file is required")
	ErrDiscoveryFileTooLarge     = errors.New("discovery file is too large")
	ErrDiscoveryFileBadExtension = errors.New("discovery file must be a .zip")
)
