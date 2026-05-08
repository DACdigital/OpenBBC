package types

// FlowMapConfig is the agent's full configuration: phase-1 wizard fields
// at root, plus the parsed and edited discovery snapshot. Stored in the
// agents.flow_map_config JSONB column; rendered as YAML on demand.
type FlowMapConfig struct {
	SchemaVersion int `json:"schema_version" yaml:"schema_version"`

	// Phase-1 wizard answers.
	Name           string `json:"name" yaml:"name"`
	Scope          string `json:"scope,omitempty" yaml:"scope,omitempty"`
	ShouldDo       string `json:"should_do,omitempty" yaml:"should_do,omitempty"`
	ShouldNotDo    string `json:"should_not_do,omitempty" yaml:"should_not_do,omitempty"`
	BusinessDomain string `json:"business_domain,omitempty" yaml:"business_domain,omitempty"`

	// Phase-2 discovery snapshot.
	Source       FlowMapSource `json:"source" yaml:"source"`
	Capabilities []Capability  `json:"capabilities" yaml:"capabilities"`
	Skills       []Skill       `json:"skills" yaml:"skills"`
	Flows        []Flow        `json:"flows" yaml:"flows"`
}

type FlowMapSource struct {
	CompilerSchemaVersion int               `json:"compiler_schema_version" yaml:"compiler_schema_version"`
	GeneratedFromSHA      string            `json:"generated_from_sha" yaml:"generated_from_sha"`
	AppName               string            `json:"app_name" yaml:"app_name"`
	Stack                 map[string]string `json:"stack,omitempty" yaml:"stack,omitempty"`
}

type Capability struct {
	Name    string           `json:"name" yaml:"name"`
	Summary string           `json:"summary,omitempty" yaml:"summary,omitempty"`
	Tools   []map[string]any `json:"tools,omitempty" yaml:"tools,omitempty"`
	ProseMD string           `json:"prose_md,omitempty" yaml:"prose_md,omitempty"`
}

type Skill struct {
	ID            string   `json:"id" yaml:"id"`
	Origin        string   `json:"origin" yaml:"origin"` // "discovered" | "custom"
	Name          string   `json:"name" yaml:"name"`
	Description   string   `json:"description,omitempty" yaml:"description,omitempty"`
	UserPhrases   []string `json:"user_phrases,omitempty" yaml:"user_phrases,omitempty"`
	Role          string   `json:"role" yaml:"role"` // "read" | "write"
	CapabilityRef string   `json:"capability_ref,omitempty" yaml:"capability_ref,omitempty"`
	External      bool     `json:"external" yaml:"external"`
	ExternalNote  string   `json:"external_note,omitempty" yaml:"external_note,omitempty"`
	ProposedTool  string   `json:"proposed_tool,omitempty" yaml:"proposed_tool,omitempty"`
	ProseMD       string   `json:"prose_md,omitempty" yaml:"prose_md,omitempty"`
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
