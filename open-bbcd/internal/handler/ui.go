package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type GroupedAgentRepository interface {
	ListGrouped(ctx context.Context) ([]types.AgentChain, error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
	CreateVersion(ctx context.Context, parentID string, opts types.CreateVersionOpts) (*types.Agent, error)
	GetVersionChain(ctx context.Context, agentID string) (types.AgentChain, error)
}

type UIHandler struct {
	agentRepo              GroupedAgentRepository
	schema                 *types.WizardSchema
	logger                 *slog.Logger
	agentsTmpl             *template.Template
	agentVersionsTmpl      *template.Template
	agentVersionDetailTmpl *template.Template
	editTmpl               *template.Template
	wizardTmpl             *template.Template
	stepTmpl               *template.Template
}

func statusClass(status string) string {
	switch types.AgentStatus(status) {
	case types.AgentStatusDeployed:
		return "deployed"
	case types.AgentStatusTested:
		return "tested"
	case types.AgentStatusInitializing:
		return "initializing"
	default:
		return "draft"
	}
}

func NewUIHandler(agentRepo GroupedAgentRepository, schema *types.WizardSchema, webFS fs.FS, logger *slog.Logger) (*UIHandler, error) {
	funcs := template.FuncMap{
		"statusClass": statusClass,
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
		"urlEncode":   url.QueryEscape,
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

	agentVersionDetailTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/agent-version-detail.html",
	)
	if err != nil {
		return nil, err
	}

	editTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/agent-edit.html",
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
		agentRepo:              agentRepo,
		schema:                 schema,
		logger:                 logger,
		agentsTmpl:             agentsTmpl,
		agentVersionsTmpl:      agentVersionsTmpl,
		agentVersionDetailTmpl: agentVersionDetailTmpl,
		editTmpl:               editTmpl,
		wizardTmpl:             wizardTmpl,
		stepTmpl:               stepTmpl,
	}, nil
}

// renderTemplate buffers template execution so errors don't corrupt a partial response.
func (h *UIHandler) renderTemplate(w http.ResponseWriter, tmpl *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		h.logger.Error("render template", slog.String("template", name), slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := buf.WriteTo(w); err != nil {
		h.logger.Error("write template response", slog.String("template", name), slog.Any("error", err))
	}
}

type agentsPageData struct {
	Active string
	Chains []types.AgentChain
}

type agentVersionsPageData struct {
	Active   string
	RootID   string
	Name     string
	Versions []types.AgentVersion
}

type wizardAnswer struct {
	Label string
	Value string
}

type agentVersionDetailPageData struct {
	Active        string
	ChainName     string
	RootID        string
	VersionNum    int
	Agent         *types.Agent
	WizardAnswers []wizardAnswer
}

func (h *UIHandler) buildWizardAnswers(agent *types.Agent) []wizardAnswer {
	if len(agent.WizardInput) == 0 {
		return nil
	}
	var raw map[string]string
	if err := json.Unmarshal(agent.WizardInput, &raw); err != nil {
		return nil
	}
	fields := h.schema.OrderedFields()
	answers := make([]wizardAnswer, 0, len(fields))
	for _, f := range fields {
		if f.Field.Type == "file" {
			continue
		}
		if val, ok := raw[f.Key]; ok {
			answers = append(answers, wizardAnswer{Label: f.Field.Label, Value: val})
		}
	}
	return answers
}

// AgentsPage dispatches to the agents list, version history, or version detail
// based on query params: ?agent_id= for version history, ?version_id= for detail.
func (h *UIHandler) AgentsPage(w http.ResponseWriter, r *http.Request) {
	chains, err := h.agentRepo.ListGrouped(r.Context())
	if err != nil {
		h.logger.Error("list grouped agents", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if agentID := r.URL.Query().Get("agent_id"); agentID != "" {
		for _, chain := range chains {
			if chain.RootID == agentID {
				h.renderTemplate(w, h.agentVersionsTmpl, "layout", agentVersionsPageData{
					Active:   "agents",
					RootID:   chain.RootID,
					Name:     chain.Name,
					Versions: chain.Versions,
				})
				return
			}
		}
		http.NotFound(w, r)
		return
	}

	if versionID := r.URL.Query().Get("version_id"); versionID != "" {
		for _, chain := range chains {
			for _, v := range chain.Versions {
				if v.Agent.ID == versionID {
					h.renderTemplate(w, h.agentVersionDetailTmpl, "layout", agentVersionDetailPageData{
						Active:        "agents",
						ChainName:     chain.Name,
						RootID:        chain.RootID,
						VersionNum:    v.VersionNum,
						Agent:         v.Agent,
						WizardAnswers: h.buildWizardAnswers(v.Agent),
					})
					return
				}
			}
		}
		http.NotFound(w, r)
		return
	}

	h.renderTemplate(w, h.agentsTmpl, "layout", agentsPageData{Active: "agents", Chains: chains})
}

type wizardPageData struct {
	Active string
}

func (h *UIHandler) WizardPage(w http.ResponseWriter, r *http.Request) {
	h.renderTemplate(w, h.wizardTmpl, "layout", wizardPageData{Active: "agents"})
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

	h.renderTemplate(w, h.stepTmpl, "step", data)
}

type agentEditPageData struct {
	Active     string
	ChainName  string
	RootID     string
	VersionNum int
	Agent      *types.Agent
}

func (h *UIHandler) EditPage(w http.ResponseWriter, r *http.Request) {
	versionID := r.URL.Query().Get("version_id")
	if versionID == "" {
		http.NotFound(w, r)
		return
	}

	agent, err := h.agentRepo.GetByID(r.Context(), versionID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	chain, err := h.agentRepo.GetVersionChain(r.Context(), versionID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var versionNum int
	for _, v := range chain.Versions {
		if v.Agent.ID == versionID {
			versionNum = v.VersionNum
			break
		}
	}

	h.renderTemplate(w, h.editTmpl, "layout", agentEditPageData{
		Active:     "agents",
		ChainName:  chain.Name,
		RootID:     chain.RootID,
		VersionNum: versionNum,
		Agent:      agent,
	})
}

func (h *UIHandler) EditSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	parentID := r.FormValue("parent_version_id")
	if parentID == "" {
		http.Error(w, "parent_version_id is required", http.StatusBadRequest)
		return
	}

	opts := types.CreateVersionOpts{
		Prompt: r.FormValue("prompt"),
	}

	newAgent, err := h.agentRepo.CreateVersion(r.Context(), parentID, opts)
	if err != nil {
		h.logger.Error("ui: create version", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/agents/ui?version_id="+url.QueryEscape(newAgent.ID), http.StatusSeeOther)
}
