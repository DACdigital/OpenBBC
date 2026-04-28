// open-bbcd/internal/types/agent.go
package types

import (
	"encoding/json"
	"time"
)

type Agent struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	Prompt          string          `json:"prompt"`
	Status          string          `json:"status"`
	ParentVersionID *string         `json:"parent_version_id,omitempty"`
	WizardInput     json.RawMessage `json:"wizard_input,omitempty"`
	SchemaVersion   string          `json:"schema_version,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// AgentVersion pairs an Agent with its computed version number within a chain.
type AgentVersion struct {
	Agent      *Agent
	VersionNum int
}

// AgentChain groups versions of the same named agent. Versions are ordered newest first.
type AgentChain struct {
	Name     string
	Versions []AgentVersion
}

type CreateAgentOpts struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
}

type CreateAgentFromWizardOpts struct {
	Name          string
	WizardInput   map[string]string
	SchemaVersion string
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
		Status:      "DRAFT",
	}, nil
}
