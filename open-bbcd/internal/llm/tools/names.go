package tools

import "encoding/json"

// AgentToolName is the LLM-visible name of the built-in sub-agent tool.
const AgentToolName = "agent"

// ArchitectureHasEndpointTool reports whether any endpoint tool in the
// agent architecture blob ({"tools":[{"name":…}]}) has the given LLM-visible
// name after SanitizeToolName — the same name Builder exposes. HTTP endpoint
// tools are not prefixed, so such a tool would collide with a built-in tool
// of that name. A blob that does not parse has no tools.
func ArchitectureHasEndpointTool(architecture json.RawMessage, name string) bool {
	var snap struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if len(architecture) == 0 || json.Unmarshal(architecture, &snap) != nil {
		return false
	}
	for _, t := range snap.Tools {
		if SanitizeToolName(t.Name) == name {
			return true
		}
	}
	return false
}
