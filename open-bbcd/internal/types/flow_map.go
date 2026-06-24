package types

// FlowMapConfig is the agent's full configuration: phase-1 wizard fields
// at root, plus the parsed and edited v2 discovery snapshot. Stored in
// the agents.flow_map_config JSONB column; rendered as YAML on demand.
type FlowMapConfig struct {
	SchemaVersion int `json:"schema_version" yaml:"schema_version"` // must be 2

	// Phase-1 wizard answers.
	Name           string `json:"name" yaml:"name"`
	Scope          string `json:"scope,omitempty" yaml:"scope,omitempty"`
	ShouldDo       string `json:"should_do,omitempty" yaml:"should_do,omitempty"`
	ShouldNotDo    string `json:"should_not_do,omitempty" yaml:"should_not_do,omitempty"`
	BusinessDomain string `json:"business_domain,omitempty" yaml:"business_domain,omitempty"`

	// Phase-2 discovery snapshot.
	Source    FlowMapSource `json:"source" yaml:"source"`
	Endpoints []Endpoint    `json:"endpoints" yaml:"endpoints"`
	Skills    []Skill       `json:"skills" yaml:"skills"`
	Flows     []Flow        `json:"flows" yaml:"flows"`

	// AttachedMCPs is populated at YAML-serve time only (NOT persisted in
	// the DB). Drives aikdm's <external_mcps> prompt section.
	AttachedMCPs []AttachedMCP `json:"attached_mcps,omitempty" yaml:"attached_mcps,omitempty"`
}

// AttachedMCP is an operator-attached MCP server for this agent version.
// Carries the server identity + a per-attachment note that aikdm folds into
// the main prompt as guidance for when to use the server.
//
// Sourced at serialization time (DownloadYAML) by joining
// agent_version_mcp_backend with tool_backends. NOT stored on the
// FlowMapConfig row in the DB — `attached_mcps` exists only in the YAML
// served to aikdm.
type AttachedMCP struct {
	Name string `json:"name" yaml:"name"`
	URL  string `json:"url"  yaml:"url"`
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
}

type FlowMapSource struct {
	CompilerSchemaVersion int               `json:"compiler_schema_version" yaml:"compiler_schema_version"`
	GeneratedFromSHA      string            `json:"generated_from_sha" yaml:"generated_from_sha"`
	AppName               string            `json:"app_name" yaml:"app_name"`
	Stack                 map[string]string `json:"stack,omitempty" yaml:"stack,omitempty"`
}

type Endpoint struct {
	ID            string      `json:"id" yaml:"id"`
	Proposed      bool        `json:"proposed" yaml:"proposed"`
	Method        string      `json:"method" yaml:"method"`
	Path          string      `json:"path" yaml:"path"`
	PathParams    []ParamSpec `json:"path_params,omitempty" yaml:"path_params,omitempty"`
	QueryParams   []ParamSpec `json:"query_params,omitempty" yaml:"query_params,omitempty"`
	BodyShape     any         `json:"body_shape,omitempty" yaml:"body_shape,omitempty"`
	ResponseShape any         `json:"response_shape,omitempty" yaml:"response_shape,omitempty"`
	Auth          string      `json:"auth" yaml:"auth"`
	Source        string      `json:"source,omitempty" yaml:"source,omitempty"`
	UsedBySkills  []string    `json:"used_by_skills" yaml:"used_by_skills"`
	Confidence    string      `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	ProseMD       string      `json:"prose_md,omitempty" yaml:"prose_md,omitempty"`
}

type ParamSpec struct {
	Name     string `json:"name" yaml:"name"`
	Type     string `json:"type,omitempty" yaml:"type,omitempty"`
	Required bool   `json:"required" yaml:"required"`
}

type Skill struct {
	ID                 string             `json:"id" yaml:"id"`
	Origin             string             `json:"origin" yaml:"origin"`
	Name               string             `json:"name" yaml:"name"`
	Description        string             `json:"description,omitempty" yaml:"description,omitempty"`
	Domain             string             `json:"domain,omitempty" yaml:"domain,omitempty"`
	UserPhrases        []string           `json:"user_phrases,omitempty" yaml:"user_phrases,omitempty"`
	SuggestedEndpoints []SkillEndpointRef `json:"suggested_endpoints,omitempty" yaml:"suggested_endpoints,omitempty"`
	External           bool               `json:"external" yaml:"external"`
	ExternalNote       string             `json:"external_note,omitempty" yaml:"external_note,omitempty"`
	Confidence         string             `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	ProseMD            string             `json:"prose_md,omitempty" yaml:"prose_md,omitempty"`
}

type SkillEndpointRef struct {
	Endpoint string `json:"endpoint" yaml:"endpoint"`
	Role     string `json:"role" yaml:"role"`
	When     string `json:"when,omitempty" yaml:"when,omitempty"`
}

type Flow struct {
	ID             string   `json:"id" yaml:"id"`
	Origin         string   `json:"origin" yaml:"origin"`
	Included       bool     `json:"included" yaml:"included"`
	Name           string   `json:"name" yaml:"name"`
	Description    string   `json:"description,omitempty" yaml:"description,omitempty"`
	Intent         string   `json:"intent,omitempty" yaml:"intent,omitempty"`
	UserPhrases    []string `json:"user_phrases,omitempty" yaml:"user_phrases,omitempty"`
	Preconditions  []string `json:"preconditions,omitempty" yaml:"preconditions,omitempty"`
	Postconditions []string `json:"postconditions,omitempty" yaml:"postconditions,omitempty"`
	SideEffects    []string `json:"side_effects,omitempty" yaml:"side_effects,omitempty"`
	Confidence     string   `json:"confidence,omitempty" yaml:"confidence,omitempty"`
	Workflow       Workflow `json:"workflow" yaml:"workflow"`
	ProseMD        string   `json:"prose_md,omitempty" yaml:"prose_md,omitempty"`
}

type Workflow struct {
	Mermaid string              `json:"mermaid" yaml:"mermaid"`
	Layout  map[string]Position `json:"layout,omitempty" yaml:"layout,omitempty"`
}

type Position struct {
	X int `json:"x" yaml:"x"`
	Y int `json:"y" yaml:"y"`
}
