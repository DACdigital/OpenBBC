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
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Description       string          `json:"description,omitempty"`
	Bundle            json.RawMessage `json:"bundle,omitempty"`
	Status            string          `json:"status"`
	ParentVersionID   *string         `json:"parent_version_id,omitempty"`
	FlowMapConfig     json.RawMessage `json:"flow_map_config,omitempty"`
	FlowMapParseError string          `json:"flow_map_parse_error,omitempty"`
	DiscoveryFilePath string          `json:"discovery_file_path,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
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
}

type CreateAgentFromWizardOpts struct {
	ID                string
	Name              string
	FlowMapConfig     json.RawMessage // pre-marshaled JSONB; nil if parse failed
	FlowMapParseError string          // empty when parse succeeded
	DiscoveryFilePath string
}

func NewAgent(opts CreateAgentOpts) (*Agent, error) {
	if opts.Name == "" {
		return nil, ErrNameRequired
	}
	return &Agent{
		Name:        opts.Name,
		Description: opts.Description,
		Status:      string(AgentStatusDraft),
	}, nil
}
