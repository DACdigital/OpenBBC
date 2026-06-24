// Package flowmap parses a .flow-map zip into a structured types.FlowMapConfig.
package flowmap

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"gopkg.in/yaml.v3"
)

// Parse reads a .flow-map zip and returns a populated FlowMapConfig.
// Phase-1 fields (Name, Scope, etc.) are NOT set here — callers
// (the wizard handler) merge those in from the form values.
func Parse(r io.Reader) (types.FlowMapConfig, error) {
	cfg := types.FlowMapConfig{SchemaVersion: 2}

	body, err := io.ReadAll(r)
	if err != nil {
		return cfg, fmt.Errorf("%w: read upload: %v", types.ErrFlowMapInvalid, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return cfg, fmt.Errorf("%w: not a zip: %v", types.ErrFlowMapInvalid, err)
	}

	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Strip a leading "<root>/" if the zip wrapped contents in a
		// top-level directory (some users zip the parent directory).
		key := stripLeadingDir(f.Name)
		fc, err := f.Open()
		if err != nil {
			return cfg, fmt.Errorf("%w: open %s: %v", types.ErrFlowMapInvalid, f.Name, err)
		}
		b, err := io.ReadAll(fc)
		fc.Close()
		if err != nil {
			return cfg, fmt.Errorf("%w: read %s: %v", types.ErrFlowMapInvalid, f.Name, err)
		}
		files[key] = b
	}

	required := []string{"AGENTS.md", "APP.md", "glossary.md"}
	for _, rq := range required {
		if _, ok := files[rq]; !ok {
			return cfg, fmt.Errorf("%w: missing %s", types.ErrFlowMapInvalid, rq)
		}
	}

	if err := parseAgentsMD(files["AGENTS.md"], &cfg); err != nil {
		return cfg, err
	}
	for name, b := range files {
		switch {
		case strings.HasPrefix(name, "endpoints/") && strings.HasSuffix(name, ".md"):
			ep, err := parseEndpoint(name, b)
			if err != nil {
				return cfg, err
			}
			cfg.Endpoints = append(cfg.Endpoints, ep)
		case strings.HasPrefix(name, "skills/") && strings.HasSuffix(name, ".md"):
			sk, err := parseSkill(name, b)
			if err != nil {
				return cfg, err
			}
			cfg.Skills = append(cfg.Skills, sk)
		case strings.HasPrefix(name, "flows/") && strings.HasSuffix(name, ".md"):
			fl, err := parseFlow(name, b)
			if err != nil {
				return cfg, err
			}
			cfg.Flows = append(cfg.Flows, fl)
		}
	}

	sort.Slice(cfg.Endpoints, func(i, j int) bool { return cfg.Endpoints[i].ID < cfg.Endpoints[j].ID })
	sort.Slice(cfg.Skills, func(i, j int) bool { return cfg.Skills[i].ID < cfg.Skills[j].ID })
	sort.Slice(cfg.Flows, func(i, j int) bool { return cfg.Flows[i].ID < cfg.Flows[j].ID })

	if err := validate(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func stripLeadingDir(p string) string {
	p = path.Clean(p)
	if strings.HasPrefix(p, ".flow-map/") {
		return strings.TrimPrefix(p, ".flow-map/")
	}
	return p
}

func splitFrontmatter(b []byte) (front []byte, body []byte, err error) {
	s := string(b)
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return nil, nil, fmt.Errorf("missing leading --- frontmatter delimiter")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(s, "---\r\n"), "---\n")
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return nil, nil, fmt.Errorf("missing closing --- frontmatter delimiter")
	}
	frontStr := rest[:idx]
	bodyStr := strings.TrimPrefix(strings.TrimPrefix(rest[idx+len("\n---"):], "\r\n"), "\n")
	return []byte(frontStr), []byte(bodyStr), nil
}

func parseAgentsMD(b []byte, cfg *types.FlowMapConfig) error {
	front, _, err := splitFrontmatter(b)
	if err != nil {
		return fmt.Errorf("%w: AGENTS.md: %v", types.ErrFlowMapInvalid, err)
	}
	var fm struct {
		SchemaVersion    int               `yaml:"schema_version"`
		GeneratedFromSHA string            `yaml:"generated_from_sha"`
		AppName          string            `yaml:"app_name"`
		Stack            map[string]string `yaml:"stack"`
	}
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return fmt.Errorf("%w: AGENTS.md frontmatter: %v", types.ErrFlowMapInvalid, err)
	}
	if fm.SchemaVersion != 2 {
		return fmt.Errorf("%w: unsupported schema_version %d; this build requires 2",
			types.ErrFlowMapInvalid, fm.SchemaVersion)
	}
	cfg.Source = types.FlowMapSource{
		CompilerSchemaVersion: fm.SchemaVersion,
		GeneratedFromSHA:      fm.GeneratedFromSHA,
		AppName:               fm.AppName,
		Stack:                 fm.Stack,
	}
	return nil
}


func parseEndpoint(name string, b []byte) (types.Endpoint, error) {
	front, body, err := splitFrontmatter(b)
	if err != nil {
		return types.Endpoint{}, fmt.Errorf("%w: %s: %v", types.ErrFlowMapInvalid, name, err)
	}
	var fm struct {
		SchemaVersion      int               `yaml:"schema_version"`
		ID                 string            `yaml:"id"`
		Proposed           bool              `yaml:"proposed"`
		Method             string            `yaml:"method"`
		Path               string            `yaml:"path"`
		PathParams         []types.ParamSpec `yaml:"path_params"`
		QueryParams        []types.ParamSpec `yaml:"query_params"`
		BodyShape          any               `yaml:"body_shape"`
		ResponseShape      any               `yaml:"response_shape"`
		Auth               string            `yaml:"auth"`
		Source             string            `yaml:"source"`
		UsedBySkills       []string          `yaml:"used_by_skills"`
		Confidence         string            `yaml:"confidence"`
		OpenapiOperationID string            `yaml:"openapi_operation_id"`
	}
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return types.Endpoint{}, fmt.Errorf("%w: %s frontmatter: %v", types.ErrFlowMapInvalid, name, err)
	}
	if fm.ID == "" {
		return types.Endpoint{}, fmt.Errorf("%w: %s: missing id in frontmatter", types.ErrFlowMapInvalid, name)
	}
	return types.Endpoint{
		ID:            fm.ID,
		Proposed:      fm.Proposed,
		Method:        fm.Method,
		Path:          fm.Path,
		PathParams:    fm.PathParams,
		QueryParams:   fm.QueryParams,
		BodyShape:     fm.BodyShape,
		ResponseShape: fm.ResponseShape,
		Auth:          fm.Auth,
		Source:        fm.Source,
		UsedBySkills:  fm.UsedBySkills,
		Confidence:    fm.Confidence,
		ProseMD:       string(body),
	}, nil
}

func parseSkill(name string, b []byte) (types.Skill, error) {
	front, body, err := splitFrontmatter(b)
	if err != nil {
		return types.Skill{}, fmt.Errorf("%w: %s: %v", types.ErrFlowMapInvalid, name, err)
	}
	var fm struct {
		SchemaVersion      int                      `yaml:"schema_version"`
		ID                 string                   `yaml:"id"`
		Name               string                   `yaml:"name"`
		Description        string                   `yaml:"description"`
		Domain             string                   `yaml:"domain"`
		UserPhrases        []string                 `yaml:"user_phrases"`
		SuggestedEndpoints []types.SkillEndpointRef `yaml:"suggested_endpoints"`
		External           bool                     `yaml:"external"`
		ExternalNote       string                   `yaml:"external_note"`
		Confidence         string                   `yaml:"confidence"`
	}
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return types.Skill{}, fmt.Errorf("%w: %s frontmatter: %v", types.ErrFlowMapInvalid, name, err)
	}
	if fm.ID == "" {
		return types.Skill{}, fmt.Errorf("%w: %s: missing id in frontmatter", types.ErrFlowMapInvalid, name)
	}
	return types.Skill{
		ID:                 fm.ID,
		Origin:             "discovered",
		Name:               fm.Name,
		Description:        fm.Description,
		Domain:             fm.Domain,
		UserPhrases:        fm.UserPhrases,
		SuggestedEndpoints: fm.SuggestedEndpoints,
		External:           fm.External,
		ExternalNote:       fm.ExternalNote,
		Confidence:         fm.Confidence,
		ProseMD:            string(body),
	}, nil
}

func parseFlow(name string, b []byte) (types.Flow, error) {
	front, body, err := splitFrontmatter(b)
	if err != nil {
		return types.Flow{}, fmt.Errorf("%w: %s: %v", types.ErrFlowMapInvalid, name, err)
	}
	var fm struct {
		ID             string   `yaml:"id"`
		Name           string   `yaml:"name"`
		Description    string   `yaml:"description"`
		Intent         string   `yaml:"intent"`
		UserPhrases    []string `yaml:"user_phrases"`
		Preconditions  []string `yaml:"preconditions"`
		Postconditions []string `yaml:"postconditions"`
		SideEffects    []string `yaml:"side_effects"`
		Confidence     string   `yaml:"confidence"`
		Workflow       string   `yaml:"workflow"`
		SkillsUsed     []struct {
			Skill string `yaml:"skill"`
			Role  string `yaml:"role"`
		} `yaml:"skills_used"`
	}
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return types.Flow{}, fmt.Errorf("%w: %s frontmatter: %v", types.ErrFlowMapInvalid, name, err)
	}

	wf := strings.TrimSpace(fm.Workflow)
	if wf == "" {
		wf = linearFallback(fm.SkillsUsed)
	}
	normalized, err := NormalizeMermaid(wf)
	if err != nil {
		return types.Flow{}, fmt.Errorf("%w: %s: %v", types.ErrFlowMapInvalid, name, err)
	}
	wf = normalized

	return types.Flow{
		ID:             fm.ID,
		Origin:         "discovered",
		Included:       true,
		Name:           fm.Name,
		Description:    fm.Description,
		Intent:         fm.Intent,
		UserPhrases:    fm.UserPhrases,
		Preconditions:  fm.Preconditions,
		Postconditions: fm.Postconditions,
		SideEffects:    fm.SideEffects,
		Confidence:     fm.Confidence,
		Workflow: types.Workflow{
			Mermaid: wf,
			Layout:  map[string]types.Position{},
		},
		ProseMD: string(body),
	}, nil
}

// linearFallback emits a deterministic mermaid flowchart connecting all
// skills_used entries in declared order: start → s_<id1> → s_<id2> → ... → end.
func linearFallback(skills []struct {
	Skill string `yaml:"skill"`
	Role  string `yaml:"role"`
}) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n  start([start])")
	for _, s := range skills {
		nodeID := "s_" + strings.ReplaceAll(s.Skill, "-", "_")
		fmt.Fprintf(&b, " --> %s[%s]\n  %s", nodeID, s.Skill, nodeID)
	}
	b.WriteString(" --> e([end])\n")
	return b.String()
}

func validate(cfg *types.FlowMapConfig) error {
	endpointIDs := make(map[string]struct{}, len(cfg.Endpoints))
	for _, e := range cfg.Endpoints {
		endpointIDs[e.ID] = struct{}{}
	}
	for _, s := range cfg.Skills {
		if s.External {
			continue
		}
		for _, ref := range s.SuggestedEndpoints {
			if _, ok := endpointIDs[ref.Endpoint]; !ok {
				return fmt.Errorf("%w: skill %q references unknown endpoint %q",
					types.ErrFlowMapInvalid, s.ID, ref.Endpoint)
			}
		}
	}
	skillIDs := make(map[string]struct{}, len(cfg.Skills))
	for _, s := range cfg.Skills {
		skillIDs[s.ID] = struct{}{}
	}
	for _, f := range cfg.Flows {
		if err := ValidateWorkflowSkillRefs(f.Workflow.Mermaid, skillIDs); err != nil {
			return fmt.Errorf("%w: flow %q: %v", types.ErrFlowMapInvalid, f.ID, err)
		}
	}
	return nil
}
