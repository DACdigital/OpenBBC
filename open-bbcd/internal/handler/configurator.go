package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/yuin/goldmark"
)

// ConfigStore is the narrow interface the configurator depends on.
type ConfigStore interface {
	GetFlowMapConfig(ctx context.Context, agentID string) (cfg []byte, parseErr string, err error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
	UpdateFlowMapConfig(ctx context.Context, agentID string, cfg []byte) error
}

type ConfiguratorHandler struct {
	repo                                    ConfigStore
	flowsTmpl, skillsTmpl, capabilitiesTmpl *template.Template
}

func NewConfiguratorHandler(repo ConfigStore, webFS fs.FS) (*ConfiguratorHandler, error) {
	funcs := template.FuncMap{
		"renderMarkdown": renderMarkdown,
		"dict":           tplDict,
		"selectedFlowID": func(f *types.Flow) string {
			if f == nil {
				return ""
			}
			return f.ID
		},
		"selectedSkillID": func(s *types.Skill) string {
			if s == nil {
				return ""
			}
			return s.ID
		},
		"selectedCapName": func(c *types.Capability) string {
			if c == nil {
				return ""
			}
			return c.Name
		},
	}
	parse := func(name string) (*template.Template, error) {
		return template.New("").Funcs(funcs).ParseFS(webFS,
			"templates/layout.html",
			"templates/configurator/layout.html",
			"templates/configurator/partials.html",
			"templates/configurator/"+name+".html",
		)
	}
	flowsTmpl, err := parse("flows")
	if err != nil {
		return nil, err
	}
	skillsTmpl, err := parse("skills")
	if err != nil {
		return nil, err
	}
	capabilitiesTmpl, err := parse("capabilities")
	if err != nil {
		return nil, err
	}
	return &ConfiguratorHandler{
		repo:             repo,
		flowsTmpl:        flowsTmpl,
		skillsTmpl:       skillsTmpl,
		capabilitiesTmpl: capabilitiesTmpl,
	}, nil
}

// renderMarkdown is a template func that converts markdown prose to HTML.
// Trusted source: prose came from the discovery skill, not user input.
func renderMarkdown(md string) template.HTML {
	if md == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(md), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(md))
	}
	return template.HTML(buf.String())
}

type configPageData struct {
	Active        string
	AgentID       string
	AgentName     string
	Tab           string // "flows" | "skills" | "capabilities"
	Config        types.FlowMapConfig
	ParseError    string
	SelectedFlow  *types.Flow
	SelectedSkill *types.Skill
	SelectedCap   *types.Capability
}

func (h *ConfiguratorHandler) load(r *http.Request) (configPageData, error) {
	agentID := r.PathValue("id")
	agent, err := h.repo.GetByID(r.Context(), agentID)
	if err != nil {
		return configPageData{}, err
	}
	cfgBytes, parseErr, err := h.repo.GetFlowMapConfig(r.Context(), agentID)
	if err != nil {
		return configPageData{}, err
	}
	var cfg types.FlowMapConfig
	if len(cfgBytes) > 0 {
		if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
			return configPageData{}, err
		}
	}
	return configPageData{
		Active:     "agents",
		AgentID:    agentID,
		AgentName:  agent.Name,
		Config:     cfg,
		ParseError: parseErr,
	}, nil
}

// Index redirects /agents/{id}/configure to the default Flows tab content.
func (h *ConfiguratorHandler) Index(w http.ResponseWriter, r *http.Request) {
	h.Flows(w, r)
}

func (h *ConfiguratorHandler) Flows(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Tab = "flows"
	if flowID := r.PathValue("flowId"); flowID != "" {
		for i := range data.Config.Flows {
			if data.Config.Flows[i].ID == flowID {
				data.SelectedFlow = &data.Config.Flows[i]
				break
			}
		}
		if data.SelectedFlow == nil {
			http.NotFound(w, r)
			return
		}
	}
	renderTemplate(w, h.flowsTmpl, "layout", data)
}

func (h *ConfiguratorHandler) Skills(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Tab = "skills"
	if skillID := r.PathValue("skillId"); skillID != "" {
		for i := range data.Config.Skills {
			if data.Config.Skills[i].ID == skillID {
				data.SelectedSkill = &data.Config.Skills[i]
				break
			}
		}
		if data.SelectedSkill == nil {
			http.NotFound(w, r)
			return
		}
	}
	renderTemplate(w, h.skillsTmpl, "layout", data)
}

func (h *ConfiguratorHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Tab = "capabilities"
	if capName := r.PathValue("capName"); capName != "" {
		for i := range data.Config.Capabilities {
			if data.Config.Capabilities[i].Name == capName {
				data.SelectedCap = &data.Config.Capabilities[i]
				break
			}
		}
		if data.SelectedCap == nil {
			http.NotFound(w, r)
			return
		}
	}
	renderTemplate(w, h.capabilitiesTmpl, "layout", data)
}

// tplDict builds a map[string]any from alternating key/value template args.
// Used to pass multiple named values into a sub-template invocation:
//
//	{{template "flow_row" (dict "AgentID" $.AgentID "Flow" .)}}
func tplDict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("dict: odd number of args")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			return nil, errors.New("dict: keys must be strings")
		}
		m[key] = kv[i+1]
	}
	return m, nil
}

// loadConfig fetches the agent's flow_map_config and unmarshals into a
// FlowMapConfig. Returns ErrNotFound if the agent does not exist or has no
// config persisted (the configurator pages assume the wizard already ran).
func (h *ConfiguratorHandler) loadConfig(ctx context.Context, agentID string) (types.FlowMapConfig, error) {
	cfgBytes, _, err := h.repo.GetFlowMapConfig(ctx, agentID)
	if err != nil {
		return types.FlowMapConfig{}, err
	}
	if len(cfgBytes) == 0 {
		return types.FlowMapConfig{}, types.ErrNotFound
	}
	var cfg types.FlowMapConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		return types.FlowMapConfig{}, err
	}
	return cfg, nil
}

// saveConfig marshals cfg to JSON and writes it via the repository.
func (h *ConfiguratorHandler) saveConfig(ctx context.Context, agentID string, cfg types.FlowMapConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return h.repo.UpdateFlowMapConfig(ctx, agentID, b)
}

// FlowIncluded toggles a flow's `included` boolean. Body: "included=true"
// or "included=false". Responds with the updated flow_row HTML fragment so
// htmx can swap the list row in place.
func (h *ConfiguratorHandler) FlowIncluded(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	included := r.FormValue("included") == "true"

	agentID := r.PathValue("id")
	flowID := r.PathValue("flowId")
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	idx := -1
	for i := range cfg.Flows {
		if cfg.Flows[i].ID == flowID {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.NotFound(w, r)
		return
	}
	cfg.Flows[idx].Included = included

	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	// Re-render the flow row so htmx can swap it in place.
	renderTemplate(w, h.flowsTmpl, "flow_row", map[string]any{
		"AgentID":    agentID,
		"Flow":       cfg.Flows[idx],
		"SelectedID": "",
	})
}

// parseSkillForm reads form values shared by SkillUpdate and SkillCreate.
// Validates role; clears capability_ref/external_note when external is the
// other state. Splits user_phrases on newlines or commas.
func parseSkillForm(r *http.Request, capabilities []types.Capability) (types.Skill, error) {
	role := strings.TrimSpace(r.FormValue("role"))
	if role != "read" && role != "write" {
		return types.Skill{}, types.ErrInvalidSkillRole
	}
	external := r.FormValue("external") == "true"
	cap := strings.TrimSpace(r.FormValue("capability"))
	note := strings.TrimSpace(r.FormValue("external_note"))
	if external {
		cap = ""
	} else {
		note = ""
		if cap != "" {
			ok := false
			for _, c := range capabilities {
				if c.Name == cap {
					ok = true
					break
				}
			}
			if !ok {
				return types.Skill{}, fmt.Errorf("%w: capability %q not present in this agent's discovery snapshot",
					types.ErrFlowMapInvalid, cap)
			}
		}
	}

	rawPhrases := r.FormValue("user_phrases")
	var phrases []string
	for _, line := range strings.FieldsFunc(rawPhrases, func(r rune) bool { return r == '\n' || r == ',' }) {
		if s := strings.TrimSpace(line); s != "" {
			phrases = append(phrases, s)
		}
	}

	return types.Skill{
		Name:          strings.TrimSpace(r.FormValue("name")),
		Description:   strings.TrimSpace(r.FormValue("description")),
		Role:          role,
		CapabilityRef: cap,
		External:      external,
		ExternalNote:  note,
		ProposedTool:  strings.TrimSpace(r.FormValue("proposed_tool")),
		UserPhrases:   phrases,
	}, nil
}

// SkillCreate adds a new custom skill from form values. The id is server-
// assigned via SlugifySkillName + UniqueSkillID. Returns the rendered
// skill_row partial so htmx can append it to the list.
func (h *ConfiguratorHandler) SkillCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	agentID := r.PathValue("id")
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	parsed, err := parseSkillForm(r, cfg.Capabilities)
	if err != nil {
		Error(w, err)
		return
	}
	if parsed.Name == "" {
		Error(w, types.ErrCustomSkillNameRequired)
		return
	}

	slug := flowmap.SlugifySkillName(parsed.Name)
	if slug == "" {
		Error(w, types.ErrCustomSkillNameRequired)
		return
	}
	taken := make(map[string]struct{}, len(cfg.Skills))
	for _, s := range cfg.Skills {
		taken[s.ID] = struct{}{}
	}
	parsed.ID = flowmap.UniqueSkillID(slug, taken)
	parsed.Origin = "custom"

	cfg.Skills = append(cfg.Skills, parsed)
	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	renderTemplate(w, h.skillsTmpl, "skill_row", map[string]any{
		"AgentID":    agentID,
		"Skill":      parsed,
		"SelectedID": "",
	})
}

// SkillDelete removes a custom skill. Discovered skills cannot be deleted
// (409). Custom skills cannot be deleted while referenced by any flow's
// workflow (409). On success returns 200 with an empty body so htmx can
// remove the row in place.
func (h *ConfiguratorHandler) SkillDelete(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	skillID := r.PathValue("skillId")
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	idx := -1
	for i := range cfg.Skills {
		if cfg.Skills[i].ID == skillID {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.NotFound(w, r)
		return
	}
	if cfg.Skills[idx].Origin != "custom" {
		Error(w, types.ErrSkillReferenced)
		return
	}
	for _, f := range cfg.Flows {
		if flowmap.WorkflowReferencesSkill(f.Workflow.Mermaid, skillID) {
			Error(w, types.ErrSkillReferenced)
			return
		}
	}

	cfg.Skills = append(cfg.Skills[:idx], cfg.Skills[idx+1:]...)
	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// SkillUpdate applies form values to an existing skill in place.
func (h *ConfiguratorHandler) SkillUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	agentID := r.PathValue("id")
	skillID := r.PathValue("skillId")
	cfg, err := h.loadConfig(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	idx := -1
	for i := range cfg.Skills {
		if cfg.Skills[i].ID == skillID {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.NotFound(w, r)
		return
	}

	parsed, err := parseSkillForm(r, cfg.Capabilities)
	if err != nil {
		Error(w, err)
		return
	}
	if parsed.Name == "" {
		Error(w, types.ErrCustomSkillNameRequired)
		return
	}

	// Preserve immutable fields: id, origin, prose_md.
	cur := &cfg.Skills[idx]
	cur.Name = parsed.Name
	cur.Description = parsed.Description
	cur.Role = parsed.Role
	cur.CapabilityRef = parsed.CapabilityRef
	cur.External = parsed.External
	cur.ExternalNote = parsed.ExternalNote
	cur.ProposedTool = parsed.ProposedTool
	cur.UserPhrases = parsed.UserPhrases

	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	// Re-render skill_detail so the htmx swap shows the saved state.
	renderTemplate(w, h.skillsTmpl, "skill_detail", map[string]any{
		"AgentID":      agentID,
		"Skill":        *cur,
		"Capabilities": cfg.Capabilities,
	})
}
