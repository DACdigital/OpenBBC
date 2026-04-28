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

type CreateAgentOpts struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
}

func NewAgent(opts CreateAgentOpts) (*Agent, error) {
	if opts.Name == "" {
		return nil, ErrNameRequired
	}
	if opts.Prompt == "" {
		return nil, ErrPromptRequired
	}
	return &Agent{
		Name:        opts.Name,
		Description: opts.Description,
		Prompt:      opts.Prompt,
	}, nil
}
