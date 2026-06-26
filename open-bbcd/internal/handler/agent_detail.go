package handler

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"sort"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// AgentDetailStore is the minimal set of repo methods AgentDetailHandler
// needs. Architecture + finalized_at come from the agent row; the wizard
// inputs (flow_map_config) come from the agent's root version.
type AgentDetailStore interface {
	GetByID(ctx context.Context, agentID string) (*types.Agent, error)
	ListGrouped(ctx context.Context) ([]types.AgentGroup, error)
	GetFlowMapConfigForAgent(ctx context.Context, agentID string) ([]byte, string, error)
}

// AgentDetailHandler serves the tabbed agent detail page at
// /agents/{agent_id}/configure/* — Versions / Inputs / Architecture. All
// content here is read-only: architecture is frozen post-finalize and the
// editable parts (prompts, MCP) live on the version detail page.
type AgentDetailHandler struct {
	store           AgentDetailStore
	backendRepo     *repository.ToolBackendRepository
	agentWiringRepo *repository.AgentWiringRepository
	schema          *types.WizardSchema
	tmpl            *template.Template
}

func NewAgentDetailHandler(
	store AgentDetailStore,
	backendRepo *repository.ToolBackendRepository,
	agentWiringRepo *repository.AgentWiringRepository,
	schema *types.WizardSchema,
	webFS fs.FS,
) (*AgentDetailHandler, error) {
	funcs := template.FuncMap{
		"statusClass": statusClass,
		"dict":        tplDict,
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/agent-detail/layout.html",
		"templates/agent-detail/versions.html",
		"templates/agent-detail/inputs.html",
		"templates/agent-detail/architecture.html",
	)
	if err != nil {
		return nil, err
	}
	return &AgentDetailHandler{
		store:           store,
		backendRepo:     backendRepo,
		agentWiringRepo: agentWiringRepo,
		schema:          schema,
		tmpl:            tmpl,
	}, nil
}

// agentDetailPageData is the shape passed to the agent-detail layout.
// One field set covers all three top-level tabs; the active tab and any
// per-tab payload are stitched in by each handler.
type agentDetailPageData struct {
	Active  string // top nav highlight (always "agents")
	Agent   *types.Agent
	Tab     string // "versions" | "inputs" | "architecture"
	SubTab  string // architecture sub-tabs: "flows" | "skills" | "endpoints"

	// Versions tab payload.
	Versions                  []types.AgentVersionListItem
	CurrentDeployedVersionNum int
	CurrentDeployedVersionID  string

	// Inputs tab payload.
	WizardFields []wizardFieldView

	// Architecture tab payload (decoded from agent.architecture).
	Architecture     archView
	SelectedFlowIdx  int      // -1 when none selected
	SelectedSkillIdx int      // -1 when none selected
	SelectedEPIdx    int      // -1 when none selected
	HTTPBackends     []*types.ToolBackend
	EndpointBackends map[string]string // endpoint_id → backend_id
}

// archView is the decoded shape of agents.architecture for templates.
// We define it inline (not in types) because it's a UI projection,
// not a wire format.
type archView struct {
	Tools  []archToolView         `json:"tools"`
	Flows  []archFlowView         `json:"flows"`
	Skills []types.SkillMetaEntry `json:"skills_meta"`
}

type archFlowView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Included    bool   `json:"included"`
}

// archToolView is the per-endpoint projection. The bundle's tools[] block
// carries more (path_params, body_shape, etc.) — those aren't needed by
// the read-only UI, so we capture just the columns templates display.
type archToolView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Auth        string `json:"auth,omitempty"`
	Source      string `json:"source,omitempty"`
}

// Index handles GET /agents/{agent_id}/configure → 302 to the default tab.
func (h *AgentDetailHandler) Index(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	http.Redirect(w, r, "/agents/"+agentID+"/configure/versions", http.StatusFound)
}

// Versions renders the default "Versions" tab — the list of versions for
// this agent with deploy/undeploy controls. Paginated.
func (h *AgentDetailHandler) Versions(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	agent, err := h.store.GetByID(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}

	// Reuse the grouped query and pull out this agent's chain. The full
	// ListGrouped pulls every agent — fine at BO scale; can be tuned
	// later with a per-agent ListVersions if perf becomes a concern.
	groups, err := h.store.ListGrouped(r.Context())
	if err != nil {
		Error(w, err)
		return
	}
	var versions []types.AgentVersionListItem
	for _, g := range groups {
		if g.AgentID == agentID {
			versions = g.Versions
			break
		}
	}

	data := agentDetailPageData{
		Active:   "agents",
		Agent:    agent,
		Tab:      "versions",
		Versions: versions,
	}
	for _, v := range versions {
		if v.Version != nil && v.Version.Status == "DEPLOYED" {
			data.CurrentDeployedVersionNum = v.VersionNum
			data.CurrentDeployedVersionID = v.Version.ID
			break
		}
	}
	renderTemplate(w, h.tmpl, "layout", data)
}

// Inputs renders the wizard-inputs tab. Read-only — values come from the
// agent's root version's flow_map_config (per-agent in practice).
func (h *AgentDetailHandler) Inputs(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	agent, err := h.store.GetByID(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}
	cfgBytes, _, err := h.store.GetFlowMapConfigForAgent(r.Context(), agentID)
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		Error(w, err)
		return
	}
	var cfg types.FlowMapConfig
	if len(cfgBytes) > 0 {
		_ = json.Unmarshal(cfgBytes, &cfg)
	}
	data := agentDetailPageData{
		Active:       "agents",
		Agent:        agent,
		Tab:          "inputs",
		WizardFields: h.buildWizardFieldViews(cfg),
	}
	renderTemplate(w, h.tmpl, "layout", data)
}

// buildWizardFieldViews mirrors ConfiguratorHandler.buildWizardFieldViews
// — kept duplicated here so AgentDetailHandler doesn't depend on the
// configurator package. Agent-level fields (name, discovery_file) skipped.
func (h *AgentDetailHandler) buildWizardFieldViews(cfg types.FlowMapConfig) []wizardFieldView {
	if h.schema == nil {
		return nil
	}
	values := map[string]string{
		"scope":           cfg.Scope,
		"should_do":       cfg.ShouldDo,
		"should_not_do":   cfg.ShouldNotDo,
		"business_domain": cfg.BusinessDomain,
	}
	out := make([]wizardFieldView, 0, len(h.schema.Wizard))
	for _, of := range h.schema.OrderedFields() {
		if agentLevelWizardKeys[of.Key] {
			continue
		}
		out = append(out, wizardFieldView{
			Key:   of.Key,
			Label: of.Field.Label,
			Type:  of.Field.Type,
			Value: values[of.Key],
		})
	}
	return out
}

// Architecture renders the architecture tab — sub-tab determined by the
// URL: /architecture/{flows|skills|endpoints}[/{id}].
func (h *AgentDetailHandler) Architecture(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	subTab := r.PathValue("subtab")
	if subTab == "" {
		subTab = "flows"
	}
	selectedID := r.PathValue("selectedID")

	agent, err := h.store.GetByID(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}

	var arch archView
	if len(agent.Architecture) > 0 {
		_ = json.Unmarshal(agent.Architecture, &arch)
	}

	data := agentDetailPageData{
		Active:           "agents",
		Agent:            agent,
		Tab:              "architecture",
		SubTab:           subTab,
		Architecture:     arch,
		SelectedFlowIdx:  -1,
		SelectedSkillIdx: -1,
		SelectedEPIdx:    -1,
	}

	switch subTab {
	case "flows":
		if selectedID != "" {
			for i, f := range arch.Flows {
				if f.ID == selectedID {
					data.SelectedFlowIdx = i
					break
				}
			}
		}
	case "skills":
		if selectedID != "" {
			for i, s := range arch.Skills {
				if s.Name == selectedID {
					data.SelectedSkillIdx = i
					break
				}
			}
		}
	case "endpoints":
		if selectedID != "" {
			for i, ep := range arch.Tools {
				if ep.ID == selectedID {
					data.SelectedEPIdx = i
					break
				}
			}
		}
		// Load HTTP backends (only http_endpoint kind) for the dropdown,
		// plus the current endpoint→backend wiring.
		if h.backendRepo != nil {
			all, err := h.backendRepo.List(r.Context())
			if err != nil {
				Error(w, err)
				return
			}
			http := []*types.ToolBackend{}
			for _, b := range all {
				if b.Kind == types.ToolBackendKindHTTPEndpoint {
					http = append(http, b)
				}
			}
			data.HTTPBackends = http
		}
		if h.agentWiringRepo != nil {
			m, err := h.agentWiringRepo.ListEndpointBackends(r.Context(), agentID)
			if err == nil {
				data.EndpointBackends = m
			}
		}
		if data.EndpointBackends == nil {
			data.EndpointBackends = map[string]string{}
		}
		// Stable order of skills (architecture is from a map iteration in
		// some bundle producers).
		sort.SliceStable(arch.Skills, func(i, j int) bool { return arch.Skills[i].Name < arch.Skills[j].Name })
		data.Architecture = arch
	default:
		http.NotFound(w, r)
		return
	}

	renderTemplate(w, h.tmpl, "layout", data)
}

// SetEndpointBackend handles the agent-scoped endpoint→backend dropdown
// POST. Mirrors ConfiguratorHandler.SetEndpointBackend but keyed by
// agent_id directly (no version-to-agent indirection).
func (h *AgentDetailHandler) SetEndpointBackend(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	endpointID := r.PathValue("endpointID")
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	backendID := r.FormValue("backend_id")
	if h.agentWiringRepo == nil {
		http.Error(w, "wiring repo not configured", http.StatusInternalServerError)
		return
	}
	if backendID == "" {
		if err := h.agentWiringRepo.UnsetEndpointBackend(r.Context(), agentID, endpointID); err != nil {
			Error(w, err)
			return
		}
	} else {
		if err := h.agentWiringRepo.SetEndpointBackend(r.Context(), agentID, endpointID, backendID); err != nil {
			Error(w, err)
			return
		}
	}
	// Re-render the architecture endpoints page so the dropdown reflects
	// the saved state. htmx swaps the whole content area.
	r.SetPathValue("subtab", "endpoints")
	r.SetPathValue("selectedID", endpointID)
	h.Architecture(w, r)
}
