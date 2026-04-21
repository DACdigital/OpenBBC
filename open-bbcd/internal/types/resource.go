package types

import (
	"time"
)

type Resource struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Prompt      string    `json:"prompt"`
	MCPEndpoint string    `json:"mcp_endpoint,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateResourceOpts struct {
	AgentID     string `json:"agent_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
	MCPEndpoint string `json:"mcp_endpoint,omitempty"`
}

func NewResource(opts CreateResourceOpts) (*Resource, error) {
	if opts.AgentID == "" {
		return nil, ErrAgentRequired
	}
	if opts.Name == "" {
		return nil, ErrNameRequired
	}
	if opts.Prompt == "" {
		return nil, ErrPromptRequired
	}
	return &Resource{
		AgentID:     opts.AgentID,
		Name:        opts.Name,
		Description: opts.Description,
		Prompt:      opts.Prompt,
		MCPEndpoint: opts.MCPEndpoint,
	}, nil
}
