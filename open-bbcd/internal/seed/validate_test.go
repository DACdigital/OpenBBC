package seed

import (
	"strings"
	"testing"
)

func validSet() *Set {
	return &Set{
		Format: FormatV1,
		Backends: []Backend{
			{Name: "mcp1", Kind: "mcp_client", Source: "s.yaml"},
			{Name: "http1", Kind: "http_endpoint", Source: "s.yaml"},
		},
		Agents: []Agent{
			{StaticID: "a", Source: "s.yaml", Architecture: map[string]any{"tools": []any{map[string]any{"id": "t1", "name": "t1"}}},
				EndpointBackends: map[string]string{"t1": "http1"}},
			{StaticID: "b", Source: "s.yaml"},
		},
		Versions: []Version{
			{StaticID: "a_v1", Agent: "a", Source: "s.yaml", MCPBackends: []MCPRef{{Backend: "mcp1"}}, Deploy: DeployIfNone},
			{StaticID: "b_v1", Agent: "b", Source: "s.yaml"},
		},
	}
}

func TestValidate_OK(t *testing.T) {
	if err := Validate(validSet()); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	agentToolArch := map[string]any{"tools": []any{map[string]any{"id": "agent", "name": "agent"}}}
	cases := []struct {
		name   string
		mutate func(s *Set)
		want   string
	}{
		{"unknown agent", func(s *Set) { s.Versions[0].Agent = "ghost" }, "ghost"},
		{"unknown target", func(s *Set) { s.Versions[0].SubAgents = []SubAgentRef{{Name: "r", Target: "nowhere_v1"}} }, "nowhere_v1"},
		{"unknown backend", func(s *Set) { s.Versions[0].MCPBackends = []MCPRef{{Backend: "nobackend"}} }, "nobackend"},
		{"uuid static id", func(s *Set) {
			s.Agents[1].StaticID = "123e4567-e89b-12d3-a456-426614174000"
			s.Versions[1].Agent = s.Agents[1].StaticID
		}, "123e4567-e89b-12d3-a456-426614174000"},
		{"32hex static id", func(s *Set) { s.Versions[1].StaticID = "123e4567e89b12d3a456426614174000" }, "123e4567e89b12d3a456426614174000"},
		{"duplicate static ids", func(s *Set) { s.Versions[1].StaticID = "a_v1" }, "a_v1"},
		{"bad binding name", func(s *Set) { s.Versions[0].SubAgents = []SubAgentRef{{Name: "Bad Name", Target: "b_v1"}} }, "Bad Name"},
		{"dup binding name", func(s *Set) {
			s.Versions = append(s.Versions, Version{StaticID: "b_v2", Agent: "b", Source: "s.yaml"})
			s.Versions[0].SubAgents = []SubAgentRef{{Name: "dupname", Target: "b_v1"}, {Name: "dupname", Target: "b_v2"}}
		}, `"dupname" is duplicated`},
		{"dup target", func(s *Set) {
			s.Versions[0].SubAgents = []SubAgentRef{{Name: "x", Target: "b_v1"}, {Name: "y", Target: "b_v1"}}
		}, `target "b_v1" appears more than once`},
		{"self target", func(s *Set) { s.Versions[0].SubAgents = []SubAgentRef{{Name: "me", Target: "a_v1"}} }, "caller and target"},
		{"agent tool collision enabled", func(s *Set) {
			s.Agents[0].Architecture = agentToolArch
			s.Versions[0].AgentToolEnabled = true
			s.Agents[0].EndpointBackends = nil
		}, "collides"},
		{"agent tool collision sub_agents", func(s *Set) {
			s.Agents[0].Architecture = agentToolArch
			s.Agents[0].EndpointBackends = nil
			s.Versions[0].SubAgents = []SubAgentRef{{Name: "x", Target: "b_v1"}}
		}, "collides"},
		{"mcp ref to http backend", func(s *Set) { s.Versions[0].MCPBackends = []MCPRef{{Backend: "http1"}} }, `backend "http1" of kind http_endpoint`},
		{"endpoint ref to mcp backend", func(s *Set) { s.Agents[0].EndpointBackends = map[string]string{"t1": "mcp1"} }, `backend "mcp1" of kind mcp_client`},
		{"endpoint key not in architecture", func(s *Set) { s.Agents[0].EndpointBackends = map[string]string{"nope_tool": "http1"} }, "nope_tool"},
		{"cycle", func(s *Set) {
			s.Versions[0].SubAgents = []SubAgentRef{{Name: "x", Target: "b_v1"}}
			s.Versions[1].SubAgents = []SubAgentRef{{Name: "y", Target: "a_v1"}}
		}, "a_v1 -> b_v1 -> a_v1"},
		{"two deploy markers", func(s *Set) {
			s.Versions = append(s.Versions, Version{StaticID: "a_v2", Agent: "a", Source: "s.yaml", Deploy: DeployForce})
		}, "more than one version has a deploy marker"},
		{"agent without versions", func(s *Set) { s.Versions = s.Versions[:1] }, "b: has no versions"},
		{"invalid deploy", func(s *Set) { s.Versions[1].Deploy = "sometimes" }, "sometimes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSet()
			tc.mutate(s)
			err := Validate(s)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "s.yaml: ") {
				t.Errorf("error lacks source prefix: %q", err)
			}
		})
	}
}
