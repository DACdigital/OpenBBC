package types_test

import (
	"encoding/json"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestFlowMapConfig_JSONRoundTrip(t *testing.T) {
	cfg := types.FlowMapConfig{
		SchemaVersion:  1,
		Name:           "test-agent",
		Scope:          "support",
		ShouldDo:       "answer",
		ShouldNotDo:    "guess",
		BusinessDomain: "saas",
		Source: types.FlowMapSource{
			CompilerSchemaVersion: 1,
			GeneratedFromSHA:      "deadbeef",
			AppName:               "test-app",
			Stack:                 map[string]string{"framework": "react"},
		},
		Capabilities: []types.Capability{
			{
				Name:    "users",
				Summary: "user resource",
				Tools:   []map[string]any{{"tool": "users.getMe", "method": "GET"}},
				ProseMD: "# Users",
			},
		},
		Skills: []types.Skill{
			{
				ID: "read-self-profile", Origin: "discovered",
				Name: "Read self profile", Role: "read",
				CapabilityRef: "users", External: false,
				ProposedTool: "users.getMe",
				ProseMD:      "# Read self profile",
				UserPhrases:  []string{"who am I"},
			},
		},
		Flows: []types.Flow{
			{
				ID: "update-profile", Origin: "discovered", Included: true,
				Name: "Update profile", Confidence: "high",
				UserPhrases:    []string{"change my email"},
				Preconditions:  []string{"signed in"},
				Postconditions: []string{"profile saved"},
				SideEffects:    []string{"audit-log-entry"},
				Workflow: types.Workflow{
					Mermaid: "flowchart TD\n  start([start]) --> s_x[read-self-profile] --> e([end])",
					Layout:  map[string]types.Position{"start": {X: 40, Y: 40}},
				},
				ProseMD: "# Update profile",
			},
		},
	}

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded types.FlowMapConfig
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Name != cfg.Name {
		t.Errorf("Name = %q, want %q", decoded.Name, cfg.Name)
	}
	if len(decoded.Flows) != 1 || decoded.Flows[0].Workflow.Mermaid != cfg.Flows[0].Workflow.Mermaid {
		t.Errorf("Workflow mermaid not preserved: %+v", decoded.Flows[0].Workflow)
	}
	if decoded.Flows[0].Workflow.Layout["start"].X != 40 {
		t.Errorf("layout x = %d, want 40", decoded.Flows[0].Workflow.Layout["start"].X)
	}
}
