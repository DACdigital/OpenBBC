// Package seed implements the openbbc-seed-v1 declarative seed file model
// (declarative-seed spec §2): loading, ${VAR} interpolation for backend
// configs, structural validation and content hashing. It has no DB access.
package seed

// FormatV1 is the only accepted value of the top-level `format` key.
const FormatV1 = "openbbc-seed-v1"

// DeployMode is a version's `deploy` marker.
type DeployMode string

const (
	DeployNone   DeployMode = "none"
	DeployIfNone DeployMode = "if_none"
	DeployForce  DeployMode = "force"
)

// Set is a whole seed set (one or more merged files).
type Set struct {
	Format   string    `yaml:"format"`
	Backends []Backend `yaml:"backends,omitempty"`
	Agents   []Agent   `yaml:"agents,omitempty"`
	Versions []Version `yaml:"versions,omitempty"`
}

// Backend is a tool backend keyed by Name.
type Backend struct {
	Name   string         `yaml:"name"`
	Kind   string         `yaml:"kind"`
	Config map[string]any `yaml:"config"`
	Source string         `yaml:"-"` // file the entry came from
}

// Agent is an agent keyed by StaticID.
type Agent struct {
	StaticID         string            `yaml:"static_id"`
	Name             string            `yaml:"name"`
	Description      string            `yaml:"description"`
	Architecture     map[string]any    `yaml:"architecture"`
	FlowMapConfig    map[string]any    `yaml:"flow_map_config,omitempty"`
	EndpointBackends map[string]string `yaml:"endpoint_backends,omitempty"`
	Source           string            `yaml:"-"`
}

// Version is an agent version keyed by StaticID.
type Version struct {
	StaticID         string         `yaml:"static_id"`
	Agent            string         `yaml:"agent"`
	Prompts          map[string]any `yaml:"prompts"`
	AgentToolEnabled bool           `yaml:"agent_tool_enabled"`
	MCPBackends      []MCPRef       `yaml:"mcp_backends,omitempty"`
	SubAgents        []SubAgentRef  `yaml:"sub_agents,omitempty"`
	Deploy           DeployMode     `yaml:"deploy,omitempty"`
	Source           string         `yaml:"-"`
}

// MCPRef binds a version to an mcp_client backend.
type MCPRef struct {
	Backend string `yaml:"backend" json:"backend"`
	Note    string `yaml:"note,omitempty" json:"note"`
}

// SubAgentRef binds a version to a target version under a binding name.
type SubAgentRef struct {
	Name   string `yaml:"name" json:"name"`
	Target string `yaml:"target" json:"target"`
	Note   string `yaml:"note,omitempty" json:"note"`
}
