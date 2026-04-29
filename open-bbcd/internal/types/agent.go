// open-bbcd/internal/types/agent.go
package types

import (
	"encoding/json"
	"time"
)

type AgentStatus string

const (
	AgentStatusInitializing AgentStatus = "INITIALIZING"
	AgentStatusDraft        AgentStatus = "DRAFT"
	AgentStatusTested       AgentStatus = "TESTED"
	AgentStatusDeployed     AgentStatus = "DEPLOYED"
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
	Agent      *Agent `json:"agent"`
	VersionNum int    `json:"version_num"`
}

// AgentChain groups versions of the same named agent. Versions are ordered newest first.
type AgentChain struct {
	RootID   string         `json:"root_id"`
	Name     string         `json:"name"`
	Versions []AgentVersion `json:"versions"`
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

// CreateVersionOpts holds the only editable field for a new version; name and description are inherited.
type CreateVersionOpts struct {
	Prompt string `json:"prompt"`
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
		Status:      string(AgentStatusDraft),
	}, nil
}
