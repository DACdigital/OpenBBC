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

// Agent is one logical agent (the integrator's stable identity). Only the
// name, description, and discovery-file pointer live here — everything that
// changes per regeneration (flow-map source, parse error, status, bundle,
// parent pointer) lives on AgentVersion.
type Agent struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Description       string    `json:"description,omitempty"`
	DiscoveryFilePath string    `json:"discovery_file_path,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// AgentVersion is one version row. It carries lifecycle (status), the flow-map
// source the version was generated from (FlowMapConfig + FlowMapParseError),
// the compiled bundle, and the linked-list parent pointer. Per-agent metadata
// (name, description, discovery file path) is on Agent.
type AgentVersion struct {
	ID                string          `json:"id"`
	AgentID           string          `json:"agent_id"`
	ParentVersionID   *string         `json:"parent_version_id,omitempty"`
	Status            string          `json:"status"`
	Bundle            json.RawMessage `json:"bundle,omitempty"`
	FlowMapConfig     json.RawMessage `json:"flow_map_config,omitempty"`
	FlowMapParseError string          `json:"flow_map_parse_error,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
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
// AgentVersion row in a single transaction. The fields split across the two
// rows as follows:
//   - ID, Name, DiscoveryFilePath  → agents row
//   - FlowMapConfig, FlowMapParseError → first agent_versions row
type CreateAgentFromWizardOpts struct {
	ID                string          // optional pre-generated agent id (agents row)
	Name              string          // agents row
	FlowMapConfig     json.RawMessage // first agent_versions row; pre-marshaled JSONB, nil if parse failed
	FlowMapParseError string          // first agent_versions row
	DiscoveryFilePath string          // agents row
}

func NewAgent(opts CreateAgentOpts) (*Agent, error) {
	if opts.Name == "" {
		return nil, ErrNameRequired
	}
	return &Agent{Name: opts.Name, Description: opts.Description}, nil
}
