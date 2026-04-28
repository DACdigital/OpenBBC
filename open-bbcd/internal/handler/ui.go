package handler

import (
	"context"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type GroupedAgentRepository interface {
	ListGrouped(ctx context.Context) ([]types.AgentChain, error)
}

type UIHandler struct {
	agentRepo  GroupedAgentRepository
	schema     *types.WizardSchema
	agentsTmpl *template.Template
	wizardTmpl *template.Template
	stepTmpl   *template.Template
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
	}

	agentsTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/agents.html",
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
		agentRepo:  agentRepo,
		schema:     schema,
		agentsTmpl: agentsTmpl,
		wizardTmpl: wizardTmpl,
		stepTmpl:   stepTmpl,
	}, nil
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.agentsTmpl.ExecuteTemplate(w, "layout", agentsPageData{Active: "agents", Chains: chains})
}

type wizardPageData struct {
	Active string
}

func (h *UIHandler) WizardPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.wizardTmpl.ExecuteTemplate(w, "layout", wizardPageData{Active: "agents"})
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.stepTmpl.ExecuteTemplate(w, "step", data)
}
