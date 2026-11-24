package seed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// CanonicalJSON marshals v with sorted object keys and no insignificant
// whitespace. Numbers keep their literal text.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil { // map keys are sorted by encoding/json
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// VersionContent is the immutable content of a version, as hashed.
type VersionContent struct {
	Prompts          json.RawMessage
	AgentToolEnabled bool
	MCP              []MCPRef
	SubAgents        []SubAgentRef
}

// Content returns the hashable content of v.
func (v Version) Content() (VersionContent, error) {
	prompts := v.Prompts
	if prompts == nil {
		prompts = map[string]any{}
	}
	raw, err := json.Marshal(normalizeMap(prompts))
	if err != nil {
		return VersionContent{}, err
	}
	return VersionContent{
		Prompts:          raw,
		AgentToolEnabled: v.AgentToolEnabled,
		MCP:              append([]MCPRef(nil), v.MCPBackends...),
		SubAgents:        append([]SubAgentRef(nil), v.SubAgents...),
	}, nil
}

// ContentHash is the hex SHA-256 of the canonical JSON of c. MCP and
// sub-agent lists are sorted first so list order never matters.
func ContentHash(c VersionContent) string {
	mcp := append([]MCPRef{}, c.MCP...)
	sort.Slice(mcp, func(i, j int) bool {
		if mcp[i].Backend != mcp[j].Backend {
			return mcp[i].Backend < mcp[j].Backend
		}
		return mcp[i].Note < mcp[j].Note
	})
	subs := append([]SubAgentRef{}, c.SubAgents...)
	sort.Slice(subs, func(i, j int) bool {
		a, b := subs[i], subs[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.Note < b.Note
	})
	prompts := c.Prompts
	if len(prompts) == 0 {
		prompts = json.RawMessage("{}")
	}
	b, err := CanonicalJSON(map[string]any{
		"prompts":            prompts,
		"agent_tool_enabled": c.AgentToolEnabled,
		"mcp_backends":       mcp,
		"sub_agents":         subs,
	})
	if err != nil {
		// Inputs are plain data; an invalid Prompts blob is a programming error.
		panic("seed: canonical JSON: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
