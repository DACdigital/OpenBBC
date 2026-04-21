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

type CreateResourceInput struct {
	AgentID     string `json:"agent_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
	MCPEndpoint string `json:"mcp_endpoint,omitempty"`
}

func NewResource(input CreateResourceInput) (*Resource, error) {
	if input.AgentID == "" {
		return nil, ErrAgentRequired
	}
	if input.Name == "" {
		return nil, ErrNameRequired
	}
	if input.Prompt == "" {
		return nil, ErrPromptRequired
	}
	return &Resource{
		AgentID:     input.AgentID,
		Name:        input.Name,
		Description: input.Description,
		Prompt:      input.Prompt,
		MCPEndpoint: input.MCPEndpoint,
	}, nil
}
