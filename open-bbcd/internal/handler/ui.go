package handler

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/storage"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type GroupedAgentRepository interface {
	ListGrouped(ctx context.Context) ([]types.AgentGroup, error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
}

type UIHandler struct {
	agentRepo         GroupedAgentRepository
	versions          DeployVersionRepository
	storage           storage.Storage
	schema            *types.WizardSchema
	logger            *slog.Logger
	agentsTmpl        *template.Template
	agentVersionsTmpl *template.Template
	wizardTmpl        *template.Template
	stepTmpl          *template.Template
	deployModalTmpl   *template.Template
	undeployModalTmpl *template.Template
}

func statusClass(status string) string {
	switch status {
	case "DEPLOYED":
		return "deployed"
	case "READY":
		return "ready"
	case "TRAINING":
		return "training"
	case "INITIALIZING":
		return "initializing"
	default:
		return "draft"
	}
}

func NewUIHandler(agentRepo GroupedAgentRepository, versions DeployVersionRepository, store storage.Storage, schema *types.WizardSchema, webFS fs.FS, logger *slog.Logger) (*UIHandler, error) {
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

	deployModalTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/agent-deploy-modal.html",
	)
	if err != nil {
		return nil, err
	}
	undeployModalTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/agent-undeploy-modal.html",
	)
	if err != nil {
		return nil, err
	}

	return &UIHandler{
		agentRepo:         agentRepo,
		versions:          versions,
		storage:           store,
		schema:            schema,
		logger:            logger,
		agentsTmpl:        agentsTmpl,
		agentVersionsTmpl: agentVersionsTmpl,
		wizardTmpl:        wizardTmpl,
		stepTmpl:          stepTmpl,
		deployModalTmpl:   deployModalTmpl,
		undeployModalTmpl: undeployModalTmpl,
	}, nil
}

// renderTemplate buffers template execution so errors don't corrupt a partial response.
func renderTemplate(w http.ResponseWriter, tmpl *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("template execution failed", slog.String("template", name), slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

type agentsPageData struct {
	Active   string
	Groups   []types.AgentGroup
	Page     PageView
	BasePath string
}

// AgentsPage serves either the agents list or a single agent chain's version
// history, depending on whether the ?agent= query param is present. The query
// param value is the chain root agent ID — the stable identifier for a chain
// across version additions.
//
// Both views are paginated via ?page= and ?size= (defaults 1/50). The list
// view paginates at the agent level; the versions view paginates the
// version list inside one agent. Slicing happens app-side — repo fetches
// remain unpaginated, which is fine at BO scale.
func (h *UIHandler) AgentsPage(w http.ResponseWriter, r *http.Request) {
	groups, err := h.agentRepo.ListGrouped(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	pr := ParsePageRequest(r)

	if agentID := r.URL.Query().Get("agent"); agentID != "" {
		// /agents/ui?agent=X redirects to the new tabbed agent detail page.
		// Preserve any &page=… and &size=… on the redirect target so deep
		// links into a specific page of the versions list still land
		// correctly.
		qs := ""
		if r.URL.RawQuery != "" {
			vals := r.URL.Query()
			vals.Del("agent")
			if encoded := vals.Encode(); encoded != "" {
				qs = "?" + encoded
			}
		}
		http.Redirect(w, r, "/agents/"+agentID+"/configure/versions"+qs, http.StatusFound)
		return
	}

	total := len(groups)
	pageGroups := slicePage(groups, pr.Offset(), pr.Limit())
	renderTemplate(w, h.agentsTmpl, "layout", agentsPageData{
		Active:   "agents",
		Groups:   pageGroups,
		Page:     NewPageView(pr, total),
		BasePath: r.URL.Path,
	})
}

// slicePage returns the sub-slice of items for one page, with bounds
// clamped to len(items). Generic helper for app-side pagination.
func slicePage[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

type agentVersionsPageData struct {
	Active                    string
	AgentID                   string
	Name                      string
	Description               string
	DiscoveryFilePath         string
	CreatedAt                 time.Time
	CurrentDeployedVersionNum int    // 0 if no version is deployed
	CurrentDeployedVersionID  string // empty if none
	Versions                  []types.AgentVersionListItem
	Page                      PageView
	BasePath                  string // e.g. "/agents/ui" — pagination links append ?agent=…&page=…
	AgentQS                   string // e.g. "agent=<id>" — preserved across pagination links
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

// DeployConfirm renders the deploy confirmation modal partial for one version.
func (u *UIHandler) DeployConfirm(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	versionID := r.URL.Query().Get("version_id")
	if versionID == "" {
		http.Error(w, "version_id query param required", http.StatusBadRequest)
		return
	}
	version, err := u.versions.GetByID(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	if version.AgentID != agentID {
		http.NotFound(w, r)
		return
	}
	curID, err := u.versions.CurrentDeployedID(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	var current *types.AgentVersion
	if curID != "" && curID != versionID {
		current, _ = u.versions.GetByID(r.Context(), curID)
	}
	renderTemplate(w, u.deployModalTmpl, "agent-deploy-modal", struct {
		AgentID         string
		Version         *types.AgentVersion
		CurrentDeployed *types.AgentVersion
	}{agentID, version, current})
}

// UndeployConfirm renders the undeploy confirmation modal partial.
func (u *UIHandler) UndeployConfirm(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	curID, err := u.versions.CurrentDeployedID(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	if curID == "" {
		Error(w, types.ErrAgentNotDeployed)
		return
	}
	cur, err := u.versions.GetByID(r.Context(), curID)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, u.undeployModalTmpl, "agent-undeploy-modal", struct {
		AgentID string
		Version *types.AgentVersion
	}{agentID, cur})
}

// DiscoveryDownload streams the discovery zip referenced by the agent's
// discovery_file_path. Returns 404 when the agent does not exist, has no
// discovery_file_path, or the underlying blob is missing.
func (h *UIHandler) DiscoveryDownload(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	agent, err := h.agentRepo.GetByID(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}
	if agent.DiscoveryFilePath == "" {
		http.NotFound(w, r)
		return
	}
	rc, err := h.storage.Open(r.Context(), agent.DiscoveryFilePath)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			h.logger.Info("discovery file missing on disk",
				slog.String("agent_id", agentID),
				slog.String("key", agent.DiscoveryFilePath),
			)
			http.NotFound(w, r)
			return
		}
		h.logger.Error("storage.Open", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/zip")
	// Filename derives from agent.ID (always a UUID) rather than the
	// discovery_file_path column — keeps header-injection-safe even if the
	// path column ever holds untrusted strings.
	w.Header().Set("Content-Disposition", `attachment; filename="`+agent.ID+`.zip"`)
	if _, err := io.Copy(w, rc); err != nil {
		h.logger.Warn("discovery download copy failed", slog.Any("error", err))
	}
}
