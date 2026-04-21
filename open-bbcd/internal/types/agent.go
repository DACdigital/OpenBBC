package types

import (
	"time"
)

type Agent struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Prompt      string    `json:"prompt"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateAgentInput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
}

func NewAgent(input CreateAgentInput) (*Agent, error) {
	if input.Name == "" {
		return nil, ErrNameRequired
	}
	if input.Prompt == "" {
		return nil, ErrPromptRequired
	}
	return &Agent{
		Name:        input.Name,
		Description: input.Description,
		Prompt:      input.Prompt,
	}, nil
}
