package seed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
)

// CanonicalJSON marshals v with sorted object keys and no insignificant
// whitespace. Numbers are written in one canonical form per value (see
// canonicalNumber), so Go's exponent form (1e-07) and Postgres jsonb's plain
// form (0.0000001) of the same number produce the same bytes.
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
	generic = canonicalNumbers(generic)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil { // map keys are sorted by encoding/json
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// canonicalNumbers rewrites every json.Number in v (as decoded with
// UseNumber) to its canonical text.
func canonicalNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = canonicalNumbers(val)
		}
	case []any:
		for i, val := range t {
			t[i] = canonicalNumbers(val)
		}
	case json.Number:
		return canonicalNumber(t)
	}
	return v
}

// canonicalNumber formats n by value: an integer literal that fits int64 is
// kept exactly (beyond float64 precision too); anything else is parsed as a
// float64 and written in Go's shortest form. A value that fails to parse
// (out of float64 range) keeps its literal text.
func canonicalNumber(n json.Number) json.Number {
	if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
		return json.Number(strconv.FormatInt(i, 10))
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil {
		return n
	}
	return json.Number(strconv.FormatFloat(f, 'g', -1, 64))
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
