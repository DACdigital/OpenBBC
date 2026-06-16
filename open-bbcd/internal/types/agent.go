// open-bbcd/internal/types/agent.go
package types

import (
	"encoding/json"
	"time"
)

type AgentStatus string

// Lifecycle is the same as before — it lives on AgentVersion now.
const (
	AgentStatusInitializing AgentStatus = "INITIALIZING"
	AgentStatusDraft        AgentStatus = "DRAFT"
	AgentStatusTraining     AgentStatus = "TRAINING"
	AgentStatusReady        AgentStatus = "READY"
	AgentStatusDeployed     AgentStatus = "DEPLOYED"
)

// Agent is one logical agent (the integrator's stable identity). The flow-map
// source, name, and description live here; per-version state (bundle, status,
// parent pointer) lives on AgentVersion.
type Agent struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Description       string          `json:"description,omitempty"`
	FlowMapConfig     json.RawMessage `json:"flow_map_config,omitempty"`
	FlowMapParseError string          `json:"flow_map_parse_error,omitempty"`
	DiscoveryFilePath string          `json:"discovery_file_path,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

// AgentVersion is one version row. It carries lifecycle (status), the compiled
// bundle, and the linked-list parent pointer. Per-agent metadata is on Agent.
type AgentVersion struct {
	ID              string          `json:"id"`
	AgentID         string          `json:"agent_id"`
	ParentVersionID *string         `json:"parent_version_id,omitempty"`
	Status          string          `json:"status"`
	Bundle          json.RawMessage `json:"bundle,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// AgentVersionListItem is an AgentVersion plus its 1-based positional number
// within an agent's version history. Used by AgentGroup for UI listings.
type AgentVersionListItem struct {
	Version    *AgentVersion `json:"version"`
	VersionNum int           `json:"version_num"`
}

// AgentGroup groups all versions of a single agent. AgentID is the stable
// identifier; Name is the agent's name (copied from Agent for template convenience).
type AgentGroup struct {
	AgentID  string                 `json:"agent_id"`
	Name     string                 `json:"name"`
	Versions []AgentVersionListItem `json:"versions"`
}

// CreateAgentOpts is the input for AgentRepository.Create (REST path).
type CreateAgentOpts struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CreateAgentFromWizardOpts is the input for AgentRepository.CreateFromWizard
// (wizard path). The repository creates the agents row + the initial INITIALIZING
// AgentVersion row in a single transaction.
type CreateAgentFromWizardOpts struct {
	ID                string          // optional pre-generated agent id
	Name              string
	FlowMapConfig     json.RawMessage // pre-marshaled JSONB; nil if parse failed
	FlowMapParseError string
	DiscoveryFilePath string
}

func NewAgent(opts CreateAgentOpts) (*Agent, error) {
	if opts.Name == "" {
		return nil, ErrNameRequired
	}
	return &Agent{Name: opts.Name, Description: opts.Description}, nil
}
