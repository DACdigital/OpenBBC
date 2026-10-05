package tools

import (
	"encoding/json"
	"testing"
)

func TestArchitectureHasEndpointTool(t *testing.T) {
	cases := []struct {
		name string
		arch string
		want bool
	}{
		{"exact", `{"tools":[{"name":"agent"}]}`, true},
		{"sanitised differs", `{"tools":[{"name":"agent!"}]}`, false},
		{"case differs", `{"tools":[{"name":"Agent"}]}`, false},
		{"empty", ``, false},
		{"null", `null`, false},
		{"garbage", `{not json`, false},
	}
	for _, c := range cases {
		if got := ArchitectureHasEndpointTool(json.RawMessage(c.arch), AgentToolName); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
