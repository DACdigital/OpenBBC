package handler

import "github.com/DACdigital/OpenBBC/open-bbcd/internal/types"

// Shared template helpers used by the agent-detail architecture views
// (edit-mode partials moved out of the ConfiguratorHandler) and by the
// configurator's remaining templates. Kept package-level so both handler
// files can reference the same identifiers in their FuncMap.

// cssID makes a string safe for use inside a CSS selector (e.g. htmx's
// hx-target="#..."). Replaces any char outside [A-Za-z0-9_-] with '-'.
// Endpoint IDs from discovery often contain dots (orders.create), which
// are valid in HTML id attributes but in CSS selectors are interpreted as
// class separators — silently breaking htmx swaps.
func cssID(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' {
			b = append(b, c)
		} else {
			b = append(b, '-')
		}
	}
	return string(b)
}

func tplSelectedFlowID(f *types.Flow) string {
	if f == nil {
		return ""
	}
	return f.ID
}

func tplSelectedSkillID(s *types.Skill) string {
	if s == nil {
		return ""
	}
	return s.ID
}

func tplSelectedEndpointID(e *types.Endpoint) string {
	if e == nil {
		return ""
	}
	return e.ID
}

func tplSkillSuggestsEndpoint(s any, id string) bool {
	var refs []types.SkillEndpointRef
	switch v := s.(type) {
	case *types.Skill:
		if v == nil {
			return false
		}
		refs = v.SuggestedEndpoints
	case types.Skill:
		refs = v.SuggestedEndpoints
	default:
		return false
	}
	for _, ref := range refs {
		if ref.Endpoint == id {
			return true
		}
	}
	return false
}
