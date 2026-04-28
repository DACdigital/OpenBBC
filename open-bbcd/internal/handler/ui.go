package handler

import (
	"bytes"
	"context"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type GroupedAgentRepository interface {
	ListGrouped(ctx context.Context) ([]types.AgentChain, error)
}

type UIHandler struct {
	agentRepo       GroupedAgentRepository
	schema          *types.WizardSchema
	agentsTmpl      *template.Template
	agentVersionsTmpl *template.Template
	wizardTmpl      *template.Template
	stepTmpl        *template.Template
}

func statusClass(status string) string {
	switch status {
	case "DEPLOYED":
		return "deployed"
	case "TESTED":
		return "tested"
	case "INITIALIZING":
		return "initializing"
	default:
		return "draft"
	}
}

func NewUIHandler(agentRepo GroupedAgentRepository, schema *types.WizardSchema, webFS fs.FS) (*UIHandler, error) {
	funcs := template.FuncMap{
		"statusClass": statusClass,
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
		"urlEncode":   url.PathEscape,
	}

	agentsTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/agents.html",
	)
	if err != nil {
		return nil, err
	}

	agentVersionsTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/agent-versions.html",
	)
	if err != nil {
		return nil, err
	}

	wizardTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/wizard/wizard.html",
	)
	if err != nil {
		return nil, err
	}

	stepTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/wizard/step.html",
	)
	if err != nil {
		return nil, err
	}

	return &UIHandler{
		agentRepo:         agentRepo,
		schema:            schema,
		agentsTmpl:        agentsTmpl,
		agentVersionsTmpl: agentVersionsTmpl,
		wizardTmpl:        wizardTmpl,
		stepTmpl:          stepTmpl,
	}, nil
}

// renderTemplate buffers template execution so errors don't corrupt a partial response.
func renderTemplate(w http.ResponseWriter, tmpl *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("template %q error: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

type agentsPageData struct {
	Active string
	Chains []types.AgentChain
}

func (h *UIHandler) AgentsList(w http.ResponseWriter, r *http.Request) {
	chains, err := h.agentRepo.ListGrouped(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	renderTemplate(w, h.agentsTmpl, "layout", agentsPageData{Active: "agents", Chains: chains})
}

type agentVersionsPageData struct {
	Active   string
	Name     string
	Versions []types.AgentVersion
}

func (h *UIHandler) AgentVersions(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	chains, err := h.agentRepo.ListGrouped(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, chain := range chains {
		if chain.Name == name {
			renderTemplate(w, h.agentVersionsTmpl, "layout", agentVersionsPageData{
				Active:   "agents",
				Name:     chain.Name,
				Versions: chain.Versions,
			})
			return
		}
	}
	http.NotFound(w, r)
}

type wizardPageData struct {
	Active string
}

func (h *UIHandler) WizardPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, h.wizardTmpl, "layout", wizardPageData{Active: "agents"})
}

type stepData struct {
	Field        types.OrderedField
	CurrentStep  int
	TotalSteps   int
	Values       map[string]string
	AllFields    []types.OrderedField
	IsLast       bool
	CurrentValue string
}

func (h *UIHandler) WizardStep(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	fields := h.schema.OrderedFields()
	if n < 1 || n > len(fields) {
		http.NotFound(w, r)
		return
	}

	allValues := make(map[string]string)
	for _, of := range fields {
		if v := r.URL.Query().Get(of.Key); v != "" {
			allValues[of.Key] = v
		}
	}

	currentKey := fields[n-1].Key
	currentValue := allValues[currentKey]

	hiddenValues := make(map[string]string, len(allValues))
	for k, v := range allValues {
		if k != currentKey {
			hiddenValues[k] = v
		}
	}

	data := stepData{
		Field:        fields[n-1],
		CurrentStep:  n,
		TotalSteps:   len(fields),
		Values:       hiddenValues,
		AllFields:    fields,
		IsLast:       n == len(fields),
		CurrentValue: currentValue,
	}

	renderTemplate(w, h.stepTmpl, "step", data)
}
