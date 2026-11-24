package seed

import (
	"bytes"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Marshal encodes s as an openbbc-seed-v1 YAML document. Output is
// deterministic: backends are sorted by name, agents and versions keep the
// order of s (the export emits them in closure order, requested version
// first), and map keys are sorted. Empty descriptions, absent (nil)
// flow_map_config and `deploy: none` are omitted; an empty flow_map_config
// is kept as {}.
func Marshal(s *Set) ([]byte, error) {
	out := marshalSet{Format: s.Format}
	if out.Format == "" {
		out.Format = FormatV1
	}
	backends := append([]Backend(nil), s.Backends...)
	sort.SliceStable(backends, func(i, j int) bool { return backends[i].Name < backends[j].Name })
	for _, b := range backends {
		cfg := b.Config
		if cfg == nil {
			cfg = map[string]any{}
		}
		out.Backends = append(out.Backends, marshalBackend{Name: b.Name, Kind: b.Kind, Config: cfg})
	}
	for _, a := range s.Agents {
		arch := a.Architecture
		if arch == nil {
			arch = map[string]any{}
		}
		var flowMap *map[string]any
		if a.FlowMapConfig != nil {
			fm := a.FlowMapConfig
			flowMap = &fm
		}
		out.Agents = append(out.Agents, marshalAgent{
			StaticID:         a.StaticID,
			Name:             a.Name,
			Description:      a.Description,
			Architecture:     arch,
			FlowMapConfig:    flowMap,
			EndpointBackends: a.EndpointBackends,
		})
	}
	for _, v := range s.Versions {
		prompts := v.Prompts
		if prompts == nil {
			prompts = map[string]any{}
		}
		deploy := v.Deploy
		if deploy == DeployNone {
			deploy = ""
		}
		out.Versions = append(out.Versions, marshalVersion{
			StaticID:         v.StaticID,
			Agent:            v.Agent,
			Prompts:          prompts,
			AgentToolEnabled: v.AgentToolEnabled,
			MCPBackends:      v.MCPBackends,
			SubAgents:        v.SubAgents,
			Deploy:           deploy,
		})
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EscapeLiteral escapes s so that Interpolate returns it unchanged: every
// `${` gains one leading `$` (so `${` → `$${` and `$${` → `$$${`). This is a
// single pass; applying the two rewrites one after the other would escape
// `$${` twice.
func EscapeLiteral(s string) string {
	return strings.ReplaceAll(s, "${", "$${")
}

// The marshal* types mirror the model with the export's omission rules; the
// keys must stay identical to the model's yaml tags so Load accepts the output.
type marshalSet struct {
	Format   string           `yaml:"format"`
	Backends []marshalBackend `yaml:"backends,omitempty"`
	Agents   []marshalAgent   `yaml:"agents,omitempty"`
	Versions []marshalVersion `yaml:"versions,omitempty"`
}

type marshalBackend struct {
	Name   string         `yaml:"name"`
	Kind   string         `yaml:"kind"`
	Config map[string]any `yaml:"config"`
}

type marshalAgent struct {
	StaticID         string            `yaml:"static_id"`
	Name             string            `yaml:"name"`
	Description      string            `yaml:"description,omitempty"`
	Architecture     map[string]any    `yaml:"architecture"`
	FlowMapConfig    *map[string]any   `yaml:"flow_map_config,omitempty"` // nil omitted; empty kept as {}
	EndpointBackends map[string]string `yaml:"endpoint_backends,omitempty"`
}

type marshalVersion struct {
	StaticID         string         `yaml:"static_id"`
	Agent            string         `yaml:"agent"`
	Prompts          map[string]any `yaml:"prompts"`
	AgentToolEnabled bool           `yaml:"agent_tool_enabled"`
	MCPBackends      []MCPRef       `yaml:"mcp_backends,omitempty"`
	SubAgents        []SubAgentRef  `yaml:"sub_agents,omitempty"`
	Deploy           DeployMode     `yaml:"deploy,omitempty"`
}
