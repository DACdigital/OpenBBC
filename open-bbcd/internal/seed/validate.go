package seed

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/staticid"
)

const (
	kindMCP  = "mcp_client"
	kindHTTP = "http_endpoint"
)

// Same literal as repository/subagent.go bindingNameRe (mirrors the DB CHECK).
var bindingNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

// Validate runs every structural rule of spec §2 "Validation" (no DB). The
// result joins messages of the form `<source>: <kind> <key>: <problem>`.
func Validate(s *Set) error {
	var errs []error
	add := func(src, kind, key, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s %s: %s", src, kind, key, fmt.Sprintf(format, args...)))
	}

	backends := map[string]*Backend{}
	for i := range s.Backends {
		b := &s.Backends[i]
		if prev, dup := backends[b.Name]; dup {
			add(b.Source, "backend", b.Name, "duplicate name (also in %s)", prev.Source)
			continue
		}
		backends[b.Name] = b
		if b.Name == "" {
			add(b.Source, "backend", b.Name, "name is required")
		}
		if b.Kind != kindMCP && b.Kind != kindHTTP {
			add(b.Source, "backend", b.Name, "unknown kind %q (want %s or %s)", b.Kind, kindMCP, kindHTTP)
		}
	}

	agents := map[string]*Agent{}
	agentTools := map[string]map[string]bool{}
	for i := range s.Agents {
		a := &s.Agents[i]
		if err := staticid.Validate(a.StaticID); err != nil {
			add(a.Source, "agent", a.StaticID, "invalid static_id")
		}
		if prev, dup := agents[a.StaticID]; dup {
			add(a.Source, "agent", a.StaticID, "duplicate static_id (also in %s)", prev.Source)
			continue
		}
		agents[a.StaticID] = a
		agentTools[a.StaticID] = architectureToolIDs(a.Architecture)
		for toolID, backend := range a.EndpointBackends {
			if !agentTools[a.StaticID][toolID] {
				add(a.Source, "agent", a.StaticID, "endpoint_backends key %q is not an endpoint tool in the architecture", toolID)
			}
			b, ok := backends[backend]
			switch {
			case !ok:
				add(a.Source, "agent", a.StaticID, "endpoint_backends %q references unknown backend %q", toolID, backend)
			case b.Kind != kindHTTP:
				add(a.Source, "agent", a.StaticID, "endpoint_backends %q references backend %q of kind %s (want %s)", toolID, backend, b.Kind, kindHTTP)
			}
		}
	}

	versions := map[string]*Version{}
	for i := range s.Versions {
		v := &s.Versions[i]
		if prev, dup := versions[v.StaticID]; dup {
			add(v.Source, "version", v.StaticID, "duplicate static_id (also in %s)", prev.Source)
			continue
		}
		versions[v.StaticID] = v
	}

	hasVersion := map[string]bool{}
	deployMarkers := map[string][]string{}
	for i := range s.Versions {
		v := &s.Versions[i]
		if versions[v.StaticID] != v {
			continue // duplicate, already reported
		}
		if err := staticid.Validate(v.StaticID); err != nil {
			add(v.Source, "version", v.StaticID, "invalid static_id")
		}
		switch v.Deploy {
		case "", DeployNone:
		case DeployIfNone, DeployForce:
			deployMarkers[v.Agent] = append(deployMarkers[v.Agent], v.StaticID)
		default:
			add(v.Source, "version", v.StaticID, "invalid deploy %q (want none, if_none or force)", v.Deploy)
		}

		agent, ok := agents[v.Agent]
		if !ok {
			add(v.Source, "version", v.StaticID, "references unknown agent %q", v.Agent)
		} else {
			hasVersion[v.Agent] = true
		}

		attached := map[string]bool{}
		for _, m := range v.MCPBackends {
			if attached[m.Backend] {
				add(v.Source, "version", v.StaticID, "mcp_backends backend %q appears more than once", m.Backend)
				continue
			}
			attached[m.Backend] = true
			b, ok := backends[m.Backend]
			switch {
			case !ok:
				add(v.Source, "version", v.StaticID, "mcp_backends references unknown backend %q", m.Backend)
			case b.Kind != kindMCP:
				add(v.Source, "version", v.StaticID, "mcp_backends references backend %q of kind %s (want %s)", m.Backend, b.Kind, kindMCP)
			}
		}

		names := map[string]bool{}
		targets := map[string]bool{}
		for _, sa := range v.SubAgents {
			if !bindingNameRe.MatchString(sa.Name) {
				add(v.Source, "version", v.StaticID, "sub_agents name %q is invalid (want %s)", sa.Name, bindingNameRe)
			}
			if names[sa.Name] {
				add(v.Source, "version", v.StaticID, "sub_agents name %q is duplicated", sa.Name)
			}
			names[sa.Name] = true
			if targets[sa.Target] {
				add(v.Source, "version", v.StaticID, "sub_agents target %q appears more than once", sa.Target)
			}
			targets[sa.Target] = true
			if sa.Target == v.StaticID {
				add(v.Source, "version", v.StaticID, "sub_agents %q: caller and target are the same version", sa.Name)
			} else if _, ok := versions[sa.Target]; !ok {
				add(v.Source, "version", v.StaticID, "sub_agents %q references unknown target version %q", sa.Name, sa.Target)
			}
		}

		if ok && (v.AgentToolEnabled || len(v.SubAgents) > 0) && architectureHasTool(agent.Architecture, tools.AgentToolName) {
			add(v.Source, "version", v.StaticID, "agent %q architecture exposes an endpoint tool named %q, which collides with the built-in agent tool", v.Agent, tools.AgentToolName)
		}
	}

	for i := range s.Agents {
		a := &s.Agents[i]
		if agents[a.StaticID] != a {
			continue
		}
		if !hasVersion[a.StaticID] {
			add(a.Source, "agent", a.StaticID, "has no versions in the seed set")
		}
		if ids := deployMarkers[a.StaticID]; len(ids) > 1 {
			add(a.Source, "agent", a.StaticID, "more than one version has a deploy marker: %v", ids)
		}
	}

	errs = append(errs, malformedPlaceholderErrors(s)...)
	errs = append(errs, cycleErrors(s, versions)...)
	return errors.Join(errs...)
}

func architectureToolIDs(arch map[string]any) map[string]bool {
	ids := map[string]bool{}
	tl, _ := arch["tools"].([]any)
	for _, t := range tl {
		if m, ok := t.(map[string]any); ok {
			if id, ok := m["id"].(string); ok && id != "" {
				ids[id] = true
			}
		}
	}
	return ids
}

func architectureHasTool(arch map[string]any, name string) bool {
	raw, err := json.Marshal(normalizeMap(arch))
	if err != nil {
		return false
	}
	return tools.ArchitectureHasEndpointTool(raw, name)
}

// cycleErrors reports each cycle in the sub-agent binding graph (DFS,
// self-loops excluded: they are reported as caller == target).
func cycleErrors(s *Set, versions map[string]*Version) []error {
	const (
		white, grey, black = 0, 1, 2
	)
	color := map[string]int{}
	var stack []string
	var errs []error
	var visit func(id string)
	visit = func(id string) {
		color[id] = grey
		stack = append(stack, id)
		v := versions[id]
		targets := make([]string, 0, len(v.SubAgents))
		for _, sa := range v.SubAgents {
			if sa.Target != id && versions[sa.Target] != nil {
				targets = append(targets, sa.Target)
			}
		}
		sort.Strings(targets)
		for _, t := range targets {
			switch color[t] {
			case white:
				visit(t)
			case grey:
				start := 0
				for i, n := range stack {
					if n == t {
						start = i
						break
					}
				}
				path := ""
				for _, n := range stack[start:] {
					path += n + " -> "
				}
				path += t
				errs = append(errs, fmt.Errorf("%s: version %s: sub-agent cycle: %s", v.Source, id, path))
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
	}
	ids := make([]string, 0, len(versions))
	for id := range versions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if color[id] == white {
			visit(id)
		}
	}
	return errs
}
