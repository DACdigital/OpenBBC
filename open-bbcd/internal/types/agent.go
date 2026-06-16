// open-bbcd/internal/types/agent.go
package types

import (
	"encoding/json"
	"time"
)

type AgentStatus string

// Lifecycle: INITIALIZING (wizard) -> DRAFT (finalized config) ->
// TRAINING (aikdm generating/iterating) -> READY (bundle finalized for this
// version) -> DEPLOYED.
const (
	AgentStatusInitializing AgentStatus = "INITIALIZING"
	AgentStatusDraft        AgentStatus = "DRAFT"
	AgentStatusTraining     AgentStatus = "TRAINING"
	AgentStatusReady        AgentStatus = "READY"
	AgentStatusDeployed     AgentStatus = "DEPLOYED"
)

type Agent struct {
	ID                string          `json:"id"`
	ChainRootID       string          `json:"chain_root_id"`
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
// RootID is the chain's stable identifier: the ID of the oldest version (the only
// row in the chain with parent_version_id = NULL). Use it for routing.
type AgentChain struct {
	RootID   string
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
