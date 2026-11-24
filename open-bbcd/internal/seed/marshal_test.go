package seed

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func marshalFixture() *Set {
	return &Set{
		Format: FormatV1,
		Backends: []Backend{
			{Name: "z-api", Kind: "http_endpoint", Config: map[string]any{"base_url": "https://z"}},
			{Name: "crm-mcp", Kind: "mcp_client", Config: map[string]any{
				"url":             "https://mcp",
				"default_headers": map[string]any{"Authorization": "${CRM_MCP_AUTHORIZATION}"},
			}},
		},
		Agents: []Agent{
			{StaticID: "sales", Name: "Sales", Description: "d", Architecture: map[string]any{"tools": []any{map[string]any{"id": "get", "name": "get"}}},
				FlowMapConfig: map[string]any{"name": "sales"}, EndpointBackends: map[string]string{"get": "z-api"}},
			{StaticID: "research", Name: "Research", Architecture: map[string]any{"n": 1}},
		},
		Versions: []Version{
			{StaticID: "sales_v2", Agent: "sales", Prompts: map[string]any{"main_prompt": "line1\nline2 ${NOT_A_VAR}"},
				AgentToolEnabled: true, MCPBackends: []MCPRef{{Backend: "crm-mcp", Note: "n"}},
				SubAgents: []SubAgentRef{{Name: "researcher", Target: "research_v1", Note: "x"}}, Deploy: DeployIfNone},
			{StaticID: "research_v1", Agent: "research", Prompts: map[string]any{"main_prompt": "r"}, Deploy: DeployNone},
		},
	}
}

func TestMarshal_LoadRoundTrip(t *testing.T) {
	src := marshalFixture()
	out, err := Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.seed.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, out)
	}
	if err := Validate(got); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Backends[0].Name != "crm-mcp" || got.Backends[1].Name != "z-api" {
		t.Errorf("backends not sorted by name: %v", got.Backends)
	}
	if got.Agents[0].StaticID != "sales" || got.Versions[0].StaticID != "sales_v2" {
		t.Errorf("agent/version order not kept")
	}
	if got.Versions[0].Prompts["main_prompt"] != "line1\nline2 ${NOT_A_VAR}" {
		t.Errorf("prompt = %q", got.Versions[0].Prompts["main_prompt"])
	}
	if !reflect.DeepEqual(got.Versions[0].SubAgents, src.Versions[0].SubAgents) ||
		!reflect.DeepEqual(got.Versions[0].MCPBackends, src.Versions[0].MCPBackends) {
		t.Errorf("bindings differ: %+v", got.Versions[0])
	}
	if got.Versions[0].Deploy != DeployIfNone || got.Versions[1].Deploy != "" {
		t.Errorf("deploy = %q / %q", got.Versions[0].Deploy, got.Versions[1].Deploy)
	}
}

func TestMarshal_Omissions(t *testing.T) {
	out, err := Marshal(marshalFixture())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Count(s, "description:") != 1 {
		t.Errorf("empty description must be omitted:\n%s", s)
	}
	if strings.Count(s, "flow_map_config:") != 1 {
		t.Errorf("absent flow_map_config must be omitted:\n%s", s)
	}
	if strings.Count(s, "deploy:") != 1 || strings.Contains(s, "deploy: none") {
		t.Errorf("deploy none must be omitted:\n%s", s)
	}
}

func TestMarshal_Deterministic(t *testing.T) {
	a, err := Marshal(marshalFixture())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		b, err := Marshal(marshalFixture())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("output differs between runs:\n%s\n---\n%s", a, b)
		}
	}
}

func TestEscapeLiteral(t *testing.T) {
	cases := map[string]string{
		"plain":          "plain",
		"${x}":           "$${x}",
		"$${x}":          "$$${x}",
		"a ${B} c $${D}": "a $${B} c $$${D}",
		"$$${x}":         "$$$${x}",
		"$x {y}":         "$x {y}",
	}
	for in, want := range cases {
		if got := EscapeLiteral(in); got != want {
			t.Errorf("EscapeLiteral(%q) = %q, want %q", in, got, want)
		}
		// Interpolating the escaped value restores the literal, with no env.
		s := &Set{Backends: []Backend{{Name: "b", Config: map[string]any{"v": EscapeLiteral(in)}}}}
		if err := Interpolate(s, func(string) (string, bool) { return "", false }); err != nil {
			t.Errorf("Interpolate(%q): %v", EscapeLiteral(in), err)
		}
		if got := s.Backends[0].Config["v"]; got != in {
			t.Errorf("round trip of %q = %q", in, got)
		}
	}
}
