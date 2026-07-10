package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// AgentDetailStore is the minimal set of repo methods AgentDetailHandler
// needs. Reads and writes to flow_map_config are keyed by agent — the
// agent-scoped architecture UI is editable pre-finalize and read-only
// post-finalize, but always speaks to the agent's root version's config.
type AgentDetailStore interface {
	GetByID(ctx context.Context, agentID string) (*types.Agent, error)
	ListGrouped(ctx context.Context) ([]types.AgentGroup, error)
	GetFlowMapConfigForAgent(ctx context.Context, agentID string) ([]byte, string, error)
	UpdateFlowMapConfigForAgent(ctx context.Context, agentID string, cfg []byte) error
	GetRootVersion(ctx context.Context, agentID string) (versionID, status string, err error)
	UpdateVersionStatus(ctx context.Context, versionID, expectedFrom, to string) error
	Delete(ctx context.Context, agentID string) error
}

// AgentDetailHandler serves the tabbed agent detail page at
// /agents/{agent_id}/configure/* — Versions / Inputs / Architecture.
// Architecture is editable when the agent is pre-finalize (reads/writes
// the root version's flow_map_config) and read-only once agent.FinalizedAt
// is set (renders the frozen agent.architecture blob). Prompts and MCP
// remain version-scoped on the ConfiguratorHandler.
type AgentDetailHandler struct {
	store           AgentDetailStore
	backendRepo     *repository.ToolBackendRepository
	agentWiringRepo *repository.AgentWiringRepository
	evalRepo        *repository.EvalRepository
	schema          *types.WizardSchema
	tmpl            *template.Template
}

func NewAgentDetailHandler(
	store AgentDetailStore,
	backendRepo *repository.ToolBackendRepository,
	agentWiringRepo *repository.AgentWiringRepository,
	evalRepo *repository.EvalRepository,
	schema *types.WizardSchema,
	webFS fs.FS,
) (*AgentDetailHandler, error) {
	funcs := template.FuncMap{
		"statusClass":           statusClass,
		"dict":                  tplDict,
		"renderMarkdown":        renderMarkdown,
		"cssID":                 cssID,
		"selectedFlowID":        tplSelectedFlowID,
		"selectedSkillID":       tplSelectedSkillID,
		"selectedEndpointID":    tplSelectedEndpointID,
		"skillSuggestsEndpoint": tplSkillSuggestsEndpoint,
		"json":                  tplJSON,
		"skillIds":              tplSkillIDs,
		"workflowState":         tplWorkflowState,
		"workflowNodeCount":     tplWorkflowNodeCount,
		"includedFlowsCount":    tplIncludedFlowsCount,
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/agent-detail/layout.html",
		"templates/agent-detail/versions.html",
		"templates/agent-detail/inputs.html",
		"templates/agent-detail/architecture.html",
		"templates/agent-detail/architecture_partials.html",
		"templates/agent-detail/finalize.html",
		"templates/agent-detail/bulk_backend_modal.html",
		"templates/agent-detail/delete_confirm_modal.html",
	)
	if err != nil {
		return nil, err
	}
	return &AgentDetailHandler{
		store:           store,
		backendRepo:     backendRepo,
		agentWiringRepo: agentWiringRepo,
		evalRepo:        evalRepo,
		schema:          schema,
		tmpl:            tmpl,
	}, nil
}

// versionStat is the per-version eval rollup shown in the Versions tab.
// Avg is a plain mean of DONE eval scores; Count is the number of DONE
// evals contributing. Both are zero when no evals have completed yet.
type versionStat struct {
	Avg       float64
	Count     int
	LastScore float64
	HasLast   bool
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
	EvalStats                 map[string]versionStat

	// Inputs tab payload.
	WizardFields []wizardFieldView

	// Architecture tab payload.
	//
	// EditMode=true → pre-finalize view; Config carries the flow_map_config
	// (from the agent's root version) and the SelectedFlow/Skill/Endpoint
	// pointers dereference into it. Templates render the editable partials
	// (workflow editor, skill CRUD, backend dropdowns).
	//
	// EditMode=false → post-finalize view; Architecture carries the frozen
	// bundle blob. Templates render the read-only projection.
	EditMode         bool
	ParseError       string
	Config           types.FlowMapConfig
	SelectedFlow     *types.Flow
	SelectedSkill    *types.Skill
	SelectedEndpoint *types.Endpoint

	Architecture     archView
	SelectedFlowIdx  int // -1 when none selected (readonly view only)
	SelectedSkillIdx int // -1 when none selected (readonly view only)
	SelectedEPIdx    int // -1 when none selected (readonly view only)
	HTTPBackends     []*types.ToolBackend
	EndpointBackends map[string]string // endpoint_id → backend_id
	UnmappedCount    int               // count of endpoints without a backend (edit mode only)
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
	// Per-version eval rollup for the "Avg eval" column. A failed lookup
	// is not fatal — the row simply falls back to em-dash. evalRepo may
	// be nil in tests that don't wire it.
	stats := map[string]versionStat{}
	if h.evalRepo != nil {
		for _, v := range versions {
			if v.Version == nil {
				continue
			}
			avg, count, err := h.evalRepo.AverageScoreByAgentVersion(r.Context(), v.Version.ID)
			if err == nil {
				stats[v.Version.ID] = versionStat{Avg: avg, Count: count}
			}
			last, hasLast, err := h.evalRepo.LastScoreByAgentVersion(r.Context(), v.Version.ID)
			if err == nil {
				st := stats[v.Version.ID]
				st.LastScore = last
				st.HasLast = hasLast
				stats[v.Version.ID] = st
			}
		}
	}
	data.EvalStats = stats
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
// URL: /architecture/{flows|skills|endpoints}[/{id}]. Always renders from
// the root version's flow_map_config; EditMode is derived from the root
// version's status (INITIALIZING → editable; anything else → read-only).
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

	switch subTab {
	case "flows", "skills", "endpoints":
	default:
		http.NotFound(w, r)
		return
	}

	_, status, err := h.store.GetRootVersion(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	data := agentDetailPageData{
		Active:           "agents",
		Agent:            agent,
		Tab:              "architecture",
		SubTab:           subTab,
		EditMode:         status == "INITIALIZING",
		SelectedFlowIdx:  -1,
		SelectedSkillIdx: -1,
		SelectedEPIdx:    -1,
	}

	cfg, parseErr, err := h.loadConfigForAgent(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	data.Config = cfg
	data.ParseError = parseErr

	switch subTab {
	case "flows":
		if selectedID != "" {
			for i := range cfg.Flows {
				if cfg.Flows[i].ID == selectedID {
					data.SelectedFlow = &cfg.Flows[i]
					break
				}
			}
			if data.SelectedFlow == nil {
				http.NotFound(w, r)
				return
			}
		}
	case "skills":
		if selectedID != "" {
			for i := range cfg.Skills {
				if cfg.Skills[i].ID == selectedID {
					data.SelectedSkill = &cfg.Skills[i]
					break
				}
			}
			if data.SelectedSkill == nil {
				http.NotFound(w, r)
				return
			}
		}
	case "endpoints":
		if selectedID != "" {
			for i := range cfg.Endpoints {
				if cfg.Endpoints[i].ID == selectedID {
					data.SelectedEndpoint = &cfg.Endpoints[i]
					break
				}
			}
			if data.SelectedEndpoint == nil {
				http.NotFound(w, r)
				return
			}
		}
		if err := h.attachEndpointBackends(r.Context(), agentID, &data); err != nil {
			Error(w, err)
			return
		}
		data.UnmappedCount = 0
		for _, ep := range cfg.Endpoints {
			if data.EndpointBackends[ep.ID] == "" {
				data.UnmappedCount++
			}
		}
	}

	renderTemplate(w, h.tmpl, "layout", data)
}

// attachEndpointBackends populates data.HTTPBackends + data.EndpointBackends.
// Shared between edit-mode and read-only endpoint views since the wiring
// tables are agent-scoped in both cases.
func (h *AgentDetailHandler) attachEndpointBackends(ctx context.Context, agentID string, data *agentDetailPageData) error {
	if h.backendRepo != nil {
		all, err := h.backendRepo.List(ctx)
		if err != nil {
			return err
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
		m, err := h.agentWiringRepo.ListEndpointBackends(ctx, agentID)
		if err != nil {
			return err
		}
		data.EndpointBackends = m
	}
	if data.EndpointBackends == nil {
		data.EndpointBackends = map[string]string{}
	}
	return nil
}

// requireEditable returns ErrInvalidAgentStatus if the agent's root
// version is no longer INITIALIZING. All architecture write handlers call
// this first so a stale tab or hand-crafted request cannot mutate a
// post-finalize agent.
func (h *AgentDetailHandler) requireEditable(ctx context.Context, agentID string) error {
	_, status, err := h.store.GetRootVersion(ctx, agentID)
	if err != nil {
		return err
	}
	if status != "INITIALIZING" {
		return types.ErrInvalidAgentStatus
	}
	return nil
}

// loadConfigForAgent fetches the agent's flow_map_config + parse_error from
// its root version. Returns ErrNotFound if the agent has no config
// persisted (should not happen for wizard-created agents).
func (h *AgentDetailHandler) loadConfigForAgent(ctx context.Context, agentID string) (types.FlowMapConfig, string, error) {
	cfgBytes, parseErr, err := h.store.GetFlowMapConfigForAgent(ctx, agentID)
	if err != nil {
		return types.FlowMapConfig{}, "", err
	}
	if len(cfgBytes) == 0 {
		return types.FlowMapConfig{}, parseErr, types.ErrNotFound
	}
	var cfg types.FlowMapConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		return types.FlowMapConfig{}, parseErr, err
	}
	return cfg, parseErr, nil
}

// saveConfigForAgent marshals cfg to JSON and writes it via the store.
func (h *AgentDetailHandler) saveConfigForAgent(ctx context.Context, agentID string, cfg types.FlowMapConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return h.store.UpdateFlowMapConfigForAgent(ctx, agentID, b)
}

// FinalizeConfirm renders the confirmation page shown when the user clicks
// "Finalize →" in the agent-detail header. Submitting the page's form POSTs
// to /agents/{agent_id}/finalize.
func (h *AgentDetailHandler) FinalizeConfirm(w http.ResponseWriter, r *http.Request) {
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
	_, status, err := h.store.GetRootVersion(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	if status != "INITIALIZING" {
		Error(w, types.ErrInvalidAgentStatus)
		return
	}
	cfg, parseErr, err := h.loadConfigForAgent(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.tmpl, "layout", agentDetailPageData{
		Active:     "agents",
		Agent:      agent,
		Tab:        "finalize",
		EditMode:   true,
		Config:     cfg,
		ParseError: parseErr,
	})
}

// Finalize flips the root version's status INITIALIZING → DRAFT.
// Redirects back to the architecture flows tab so the user sees the new
// read-only mode take effect.
func (h *AgentDetailHandler) Finalize(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	versionID, _, err := h.store.GetRootVersion(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}
	if err := h.store.UpdateVersionStatus(r.Context(), versionID, "INITIALIZING", "DRAFT"); err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/agents/"+agentID+"/configure/architecture/flows", http.StatusSeeOther)
}

// FlowIncluded toggles a flow's `included` boolean. Body: "included=true"
// or "included=false". Responds with the updated flow_row HTML fragment so
// htmx can swap the list row in place.
func (h *AgentDetailHandler) FlowIncluded(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	included := r.FormValue("included") == "true"

	agentID := r.PathValue("agent_id")
	flowID := r.PathValue("flowId")
	if err := h.requireEditable(r.Context(), agentID); err != nil {
		Error(w, err)
		return
	}
	cfg, _, err := h.loadConfigForAgent(r.Context(), agentID)
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

	if err := h.saveConfigForAgent(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	renderTemplate(w, h.tmpl, "flow_row", map[string]any{
		"AgentID":    agentID,
		"Flow":       cfg.Flows[idx],
		"SelectedID": "",
	})
}

// WorkflowUpdate accepts a JSON body { "mermaid": "...", "layout": {...} }
// and persists it to the named flow's Workflow struct.
func (h *AgentDetailHandler) WorkflowUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mermaid string                    `json:"mermaid"`
		Layout  map[string]types.Position `json:"layout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	agentID := r.PathValue("agent_id")
	flowID := r.PathValue("flowId")
	if err := h.requireEditable(r.Context(), agentID); err != nil {
		Error(w, err)
		return
	}
	cfg, _, err := h.loadConfigForAgent(r.Context(), agentID)
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

	if _, err := flowmap.ParseWorkflow(body.Mermaid); err != nil {
		Error(w, fmt.Errorf("%w: %v", types.ErrFlowMapInvalid, err))
		return
	}
	skillIDs := make(map[string]struct{}, len(cfg.Skills))
	for _, s := range cfg.Skills {
		skillIDs[s.ID] = struct{}{}
	}
	if err := flowmap.ValidateWorkflowSkillRefs(body.Mermaid, skillIDs); err != nil {
		Error(w, fmt.Errorf("%w: %v", types.ErrFlowMapInvalid, err))
		return
	}

	cfg.Flows[idx].Workflow = types.Workflow{
		Mermaid: body.Mermaid,
		Layout:  body.Layout,
	}

	if err := h.saveConfigForAgent(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// SkillNew renders an empty skill_new_form for creating a custom skill.
func (h *AgentDetailHandler) SkillNew(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if err := h.requireEditable(r.Context(), agentID); err != nil {
		Error(w, err)
		return
	}
	cfg, _, err := h.loadConfigForAgent(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}
	blank := types.Skill{Origin: "custom"}
	renderTemplate(w, h.tmpl, "skill_new_form", map[string]any{
		"AgentID":   agentID,
		"Skill":     blank,
		"Endpoints": cfg.Endpoints,
	})
}

// SkillCreate adds a new custom skill from form values.
func (h *AgentDetailHandler) SkillCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	agentID := r.PathValue("agent_id")
	if err := h.requireEditable(r.Context(), agentID); err != nil {
		Error(w, err)
		return
	}
	cfg, _, err := h.loadConfigForAgent(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	parsed, err := parseSkillForm(r, cfg.Endpoints)
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
	if err := h.saveConfigForAgent(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	renderTemplate(w, h.tmpl, "skill_row", map[string]any{
		"AgentID":    agentID,
		"Skill":      parsed,
		"SelectedID": "",
	})
}

// SkillUpdate applies form values to an existing skill in place.
func (h *AgentDetailHandler) SkillUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	agentID := r.PathValue("agent_id")
	skillID := r.PathValue("skillId")
	if err := h.requireEditable(r.Context(), agentID); err != nil {
		Error(w, err)
		return
	}
	cfg, _, err := h.loadConfigForAgent(r.Context(), agentID)
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

	parsed, err := parseSkillForm(r, cfg.Endpoints)
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
	cur.Domain = parsed.Domain
	cur.External = parsed.External
	cur.ExternalNote = parsed.ExternalNote
	cur.SuggestedEndpoints = parsed.SuggestedEndpoints
	cur.UserPhrases = parsed.UserPhrases

	if err := h.saveConfigForAgent(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	renderTemplate(w, h.tmpl, "skill_detail", map[string]any{
		"AgentID":   agentID,
		"Skill":     cur,
		"Endpoints": cfg.Endpoints,
	})
}

// SkillDelete removes a custom skill. Discovered skills can't be deleted;
// referenced custom skills can't either.
func (h *AgentDetailHandler) SkillDelete(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	skillID := r.PathValue("skillId")
	if err := h.requireEditable(r.Context(), agentID); err != nil {
		Error(w, err)
		return
	}
	cfg, _, err := h.loadConfigForAgent(r.Context(), agentID)
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
	if err := h.saveConfigForAgent(r.Context(), agentID, cfg); err != nil {
		Error(w, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// parseSkillForm reads form values shared by SkillUpdate and SkillCreate.
// Splits user_phrases on newlines or commas. For non-external skills, reads
// suggested_endpoints multi-select values and validates each against the
// agent's endpoint inventory.
func parseSkillForm(r *http.Request, endpoints []types.Endpoint) (types.Skill, error) {
	external := r.FormValue("external") == "true"
	note := strings.TrimSpace(r.FormValue("external_note"))

	var suggested []types.SkillEndpointRef
	if !external {
		note = ""
		known := make(map[string]struct{}, len(endpoints))
		for _, e := range endpoints {
			known[e.ID] = struct{}{}
		}
		for _, epID := range r.Form["suggested_endpoints"] {
			epID = strings.TrimSpace(epID)
			if epID == "" {
				continue
			}
			if _, ok := known[epID]; !ok {
				return types.Skill{}, fmt.Errorf("%w: endpoint %q not present in this agent's discovery snapshot",
					types.ErrFlowMapInvalid, epID)
			}
			suggested = append(suggested, types.SkillEndpointRef{
				Endpoint: epID,
				Role:     "",
				When:     "",
			})
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
		Name:               strings.TrimSpace(r.FormValue("name")),
		Description:        strings.TrimSpace(r.FormValue("description")),
		Domain:             strings.TrimSpace(r.FormValue("domain")),
		External:           external,
		ExternalNote:       note,
		SuggestedEndpoints: suggested,
		UserPhrases:        phrases,
	}, nil
}

// DeleteConfirm renders the agent-delete confirmation modal. The modal
// requires the user to type the agent's name (GitHub-style) before the
// confirm button enables — the server re-validates on POST as a backstop.
func (h *AgentDetailHandler) DeleteConfirm(w http.ResponseWriter, r *http.Request) {
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
	renderTemplate(w, h.tmpl, "agent_delete_confirm_modal", map[string]any{
		"Agent": agent,
	})
}

// Delete drops the agent and (via FK CASCADE) every version, chat session,
// deployed session, message, and endpoint wiring that belongs to it.
// Validates the typed name matches; redirects to /agents/ui on success.
func (h *AgentDetailHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	if r.FormValue("confirm_name") != agent.Name {
		Error(w, types.ErrAgentNameMismatch)
		return
	}
	if err := h.store.Delete(r.Context(), agentID); err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/agents/ui", http.StatusSeeOther)
}

// BulkBackendModal renders the "connect all endpoints to one backend"
// modal fragment. Loaded via htmx and appended to <body>; submit is a
// plain POST that redirects back to the architecture endpoints view.
func (h *AgentDetailHandler) BulkBackendModal(w http.ResponseWriter, r *http.Request) {
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
	var arch archView
	if len(agent.Architecture) > 0 {
		_ = json.Unmarshal(agent.Architecture, &arch)
	}
	ids := make([]string, 0, len(arch.Tools))
	for _, ep := range arch.Tools {
		ids = append(ids, ep.ID)
	}
	var httpBackends []*types.ToolBackend
	if h.backendRepo != nil {
		all, err := h.backendRepo.List(r.Context())
		if err != nil {
			Error(w, err)
			return
		}
		for _, b := range all {
			if b.Kind == types.ToolBackendKindHTTPEndpoint {
				httpBackends = append(httpBackends, b)
			}
		}
	}
	data := map[string]any{
		"Agent":        agent,
		"EndpointIDs":  ids,
		"HTTPBackends": httpBackends,
	}
	renderTemplate(w, h.tmpl, "bulk_backend_modal", data)
}

// SetEndpointBackendBulk applies the chosen backend to every endpoint in
// the agent's architecture in one transaction, then 303s back to the
// architecture endpoints view so the dropdowns reflect the new state.
func (h *AgentDetailHandler) SetEndpointBackendBulk(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	backendID := r.FormValue("backend_id")
	if backendID == "" {
		http.Error(w, "backend_id is required", http.StatusBadRequest)
		return
	}
	if h.agentWiringRepo == nil {
		http.Error(w, "wiring repo not configured", http.StatusInternalServerError)
		return
	}
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
	ids := make([]string, 0, len(arch.Tools))
	for _, ep := range arch.Tools {
		ids = append(ids, ep.ID)
	}
	if err := h.agentWiringRepo.SetEndpointBackendBulk(r.Context(), agentID, backendID, ids); err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/agents/"+agentID+"/configure/architecture/endpoints", http.StatusSeeOther)
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
