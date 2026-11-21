package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
)

// DefaultMCPDiscoveryTimeout bounds tools/list per backend at export time
// (OPENBBC_EVAL_MCP_DISCOVERY_TIMEOUT overrides it).
const DefaultMCPDiscoveryTimeout = 15 * time.Second

// InputMCPBackend is one entry of the eval-input-v1 mcp_backends key, keyed
// by tool_backends.name. URL, Transport and Headers are only written in real
// mode (mock_mcp_tools=false); aikdm never connects in mock mode.
type InputMCPBackend struct {
	URL       string            `yaml:"url,omitempty"`
	Transport string            `yaml:"transport,omitempty"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	Tools     []InputMCPTool    `yaml:"tools"`
}

// InputMCPTool is one discovered tool exactly as the runtime offers it to
// the model ({backend}__{tool}, description, input schema with the runtime
// fallback), plus the server-declared output schema when there is one.
type InputMCPTool struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	InputSchema  any    `yaml:"input_schema"`
	OutputSchema any    `yaml:"output_schema,omitempty"`
}

// MCPBackendRef is an mcp_client backend attached to the evaluated version.
type MCPBackendRef struct {
	ID     string
	Name   string
	Config tools.MCPBackendCfg
}

// MCPDiscoverFunc runs tools/list against one backend with overrides
// applied over the backend's default_headers.
type MCPDiscoverFunc func(ctx context.Context, b MCPBackendRef, overrides map[string]string) ([]InputMCPTool, error)

// MCPDiscoveryError is returned by Build when any attached backend cannot be
// connected or listed. There is no partial tool set.
type MCPDiscoveryError struct {
	Backend string
	Details string
}

func (e *MCPDiscoveryError) Error() string {
	return "mcp_discovery_failed: " + e.Backend + ": " + e.Details
}

// Options tunes Build. Zero values mean DiscoverMCPTools and
// DefaultMCPDiscoveryTimeout.
type Options struct {
	Discover MCPDiscoverFunc
	Timeout  time.Duration
}

// DiscoverMCPTools builds the runtime's MCP client (tools.MCPClientBackend)
// for b, so naming, the input-schema fallback and transport behaviour are
// the runtime's own. overrides travel as the backend's session overrides,
// which the runtime transport applies over default_headers per header.
func DiscoverMCPTools(ctx context.Context, b MCPBackendRef, overrides map[string]string) ([]InputMCPTool, error) {
	be := tools.NewMCPClientBackend(b.Name, b.ID, b.Config)
	defer func() { _ = be.Close() }()
	if len(overrides) > 0 {
		ctx = tools.WithSessionHeaderOverrides(ctx, tools.SessionHeaderOverrides{b.ID: overrides})
	}
	discovered, err := be.DiscoverTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InputMCPTool, 0, len(discovered))
	for _, t := range discovered {
		var in any
		if err := json.Unmarshal(t.Def.InputSchema, &in); err != nil {
			return nil, fmt.Errorf("tool %s: input schema: %w", t.Def.Name, err)
		}
		tool := InputMCPTool{Name: t.Def.Name, Description: t.Def.Description, InputSchema: in}
		if len(t.OutputSchema) > 0 {
			var outSchema any
			if err := json.Unmarshal(t.OutputSchema, &outSchema); err != nil {
				return nil, fmt.Errorf("tool %s: output schema: %w", t.Def.Name, err)
			}
			tool.OutputSchema = outSchema
		}
		out = append(out, tool)
	}
	return out, nil
}

// buildMCPBackends discovers every attached mcp_client backend. nil when
// none is attached, so the export stays byte-identical for such versions.
func buildMCPBackends(ctx context.Context, refs []MCPBackendRef, mock bool, overrides map[string]map[string]string, opts Options) (map[string]InputMCPBackend, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	discover := opts.Discover
	if discover == nil {
		discover = DiscoverMCPTools
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultMCPDiscoveryTimeout
	}
	out := make(map[string]InputMCPBackend, len(refs))
	for _, ref := range refs {
		ovr := overrides[ref.Name]
		dctx, cancel := context.WithTimeout(ctx, timeout)
		discovered, err := discover(dctx, ref, ovr)
		cancel()
		if err != nil {
			return nil, &MCPDiscoveryError{Backend: ref.Name, Details: err.Error()}
		}
		entry := InputMCPBackend{Tools: discovered}
		if entry.Tools == nil {
			entry.Tools = []InputMCPTool{}
		}
		if !mock {
			entry.URL = ref.Config.URL
			entry.Transport = ref.Config.Transport
			headers := map[string]string{}
			maps.Copy(headers, ref.Config.DefaultHeaders)
			maps.Copy(headers, ovr)
			if len(headers) > 0 {
				entry.Headers = headers
			}
		}
		out[ref.Name] = entry
	}
	return out, nil
}
