package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/yuin/goldmark"
)

// ConfigGetter is the narrow interface the configurator depends on.
type ConfigGetter interface {
	GetFlowMapConfig(ctx context.Context, agentID string) (cfg []byte, parseErr string, err error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
}

type ConfiguratorHandler struct {
	repo                                    ConfigGetter
	flowsTmpl, skillsTmpl, capabilitiesTmpl *template.Template
}

func NewConfiguratorHandler(repo ConfigGetter, webFS fs.FS) (*ConfiguratorHandler, error) {
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
