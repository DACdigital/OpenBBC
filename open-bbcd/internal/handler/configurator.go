package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/yuin/goldmark"
	"gopkg.in/yaml.v3"
)

// ConfigStore is the narrow interface the configurator depends on.
//
// GetWithAgent returns both the version row (for status/bundle/flow_map_config)
// and its owning Agent (for display fields like name + discovery file path).
// All other methods are scoped per-version: flow_map_config, parse error,
// and lifecycle status all live on AgentVersion after migration 014.
type ConfigStore interface {
	GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error)
	GetFlowMapConfig(ctx context.Context, versionID string) (cfg []byte, parseErr string, err error)
	UpdateFlowMapConfig(ctx context.Context, versionID string, cfg []byte) error
	UpdateStatus(ctx context.Context, versionID, expectedFrom, to string) error
	CreateVersionFromPrompts(ctx context.Context, parentVersionID string, promptsJSON []byte) (string, error)
}

// mcpBackendView wraps a ToolBackend and exposes its primary URL for template
// use without requiring the template to decode the Config JSON blob.
type mcpBackendView struct {
	*types.ToolBackend
	PrimaryURL string
}

type ConfiguratorHandler struct {
	repo                                                                                 ConfigStore
	backends                                                                             *repository.ToolBackendRepository
	wiring                                                                               *repository.VersionWiringRepository
	agentWiring                                                                          *repository.AgentWiringRepository
	schema                                                                               *types.WizardSchema
	flowsTmpl, skillsTmpl, endpointsTmpl, finalizeTmpl, inputsTmpl, promptsTmpl, mcpTmpl *template.Template
}

func NewConfiguratorHandler(
	repo ConfigStore,
	backends *repository.ToolBackendRepository,
	wiring *repository.VersionWiringRepository,
	agentWiring *repository.AgentWiringRepository,
	schema *types.WizardSchema,
	webFS fs.FS,
) (*ConfiguratorHandler, error) {
	funcs := template.FuncMap{
		"renderMarkdown": renderMarkdown,
		"statusClass":    statusClass,
		"dict":           tplDict,
		// cssID makes a string safe for use inside a CSS selector (e.g.
		// htmx's hx-target="#..."). Replaces any char outside [A-Za-z0-9_-]
		// with '-'. Endpoint IDs from discovery often contain dots
		// (orders.create), which are valid in HTML id attributes but in CSS
		// selectors are interpreted as class separators — silently breaking
		// htmx swaps.
		"cssID": func(s string) string {
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
		},
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
		"selectedEndpointID": func(e *types.Endpoint) string {
			if e == nil {
				return ""
			}
			return e.ID
		},
		"skillSuggestsEndpoint": func(s any, id string) bool {
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
		},
		"json":               tplJSON,
		"skillIds":           tplSkillIDs,
		"workflowState":      tplWorkflowState,
		"workflowNodeCount":  tplWorkflowNodeCount,
		"includedFlowsCount": tplIncludedFlowsCount,
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
	endpointsTmpl, err := parse("endpoints")
	if err != nil {
		return nil, err
	}
	finalizeTmpl, err := parse("finalize")
	if err != nil {
		return nil, err
	}
	inputsTmpl, err := parse("inputs")
	if err != nil {
		return nil, err
	}
	promptsTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/configurator/layout.html",
		"templates/configurator/partials.html",
		"templates/configurator/prompts.html",
		"templates/configurator/prompts_confirm_modal.html",
	)
	if err != nil {
		return nil, err
	}
	mcpTmpl, err := parse("mcp")
	if err != nil {
		return nil, err
	}
	return &ConfiguratorHandler{
		repo:          repo,
		backends:      backends,
		wiring:        wiring,
		agentWiring:   agentWiring,
		schema:        schema,
		flowsTmpl:     flowsTmpl,
		skillsTmpl:    skillsTmpl,
		endpointsTmpl: endpointsTmpl,
		finalizeTmpl:  finalizeTmpl,
		inputsTmpl:    inputsTmpl,
		promptsTmpl:   promptsTmpl,
		mcpTmpl:       mcpTmpl,
	}, nil
}

// proseRelLink matches `../<section>/<id>.md` inside a markdown link
// destination, where <section> is one of the .flow-map directory names
// the discovery skill emits.
var proseRelLink = regexp.MustCompile(`\.\./(skills|endpoints|flows)/([^)\s]+?)\.md`)

// renderMarkdown converts a prose markdown blob (from the discovery
// skill) to HTML. Relative links into the .flow-map directory layout
// are rewritten to the configurator routes (scoped by version_id) so
// they actually navigate. Flows / skills / endpoints live under the
// Architecture primary tab. Trusted input — prose is generated, not
// user-typed.
func renderMarkdown(versionID, md string) template.HTML {
	if md == "" {
		return ""
	}
	if versionID != "" {
		base := "/agent_versions/" + versionID + "/configure/architecture/"
		md = proseRelLink.ReplaceAllString(md, base+"$1/$2")
	}
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(md), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(md))
	}
	return template.HTML(buf.String())
}

type configPageData struct {
	Active           string
	VersionID        string // URL path param value (a version row's ID)
	AgentID          string // stable agent ID, used for back-link to the version list
	AgentName        string
	AgentStatus      string // version's status (lives on AgentVersion now)
	ReadOnly         bool   // true for non-INITIALIZING versions (DRAFT, TRAINING, READY, DEPLOYED)
	HasBundle        bool   // true when the agent has architecture AND this version has prompts (Run is enabled)
	Tab              string // primary tab: "inputs" | "architecture" | "prompts" | "finalize"
	SubTab           string // architecture sub-tab: "flows" | "skills" | "endpoints" (empty for other primary tabs)
	Config           types.FlowMapConfig
	ParseError       string
	Architecture     json.RawMessage // agent-level architecture blob (endpoints/flows/skills_meta); len()>0 once finalized
	Prompts          json.RawMessage // version-level prompts blob (main_prompt + skill_prompts)
	WizardFields     []wizardFieldView // populated for the Inputs tab
	SelectedFlow     *types.Flow
	SelectedSkill    *types.Skill
	SelectedEndpoint *types.Endpoint
}

// wizardFieldView is the read-only projection of a wizard answer for the
// Inputs tab. Value is the user-typed string for text/textarea fields, or
// the stored object key for file fields.
type wizardFieldView struct {
	Key   string
	Label string
	Type  string
	Value string
}

func (h *ConfiguratorHandler) load(r *http.Request) (configPageData, error) {
	versionID := r.PathValue("version_id")
	version, agent, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		return configPageData{}, err
	}
	cfgBytes, parseErr, err := h.repo.GetFlowMapConfig(r.Context(), version.ID)
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
		Active:       "agents",
		VersionID:    versionID,
		AgentID:      agent.ID,
		AgentName:    agent.Name,
		AgentStatus:  version.Status,
		ReadOnly:     version.Status != "INITIALIZING",
		HasBundle:    len(agent.Architecture) > 0 && len(version.Prompts) > 0,
		Config:       cfg,
		ParseError:   parseErr,
		Architecture: agent.Architecture,
		Prompts:      version.Prompts,
	}, nil
}

// Inputs renders the read-only wizard-inputs tab. Only meaningful for
// non-INITIALIZING agents; INITIALIZING users are still inside the wizard
// flow itself. The values come from the same FlowMapConfig the configurator
// edits (the wizard writes its answers there).
func (h *ConfiguratorHandler) Inputs(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Tab = "inputs"
	data.WizardFields = h.buildWizardFieldViews(data.Config)
	renderTemplate(w, h.inputsTmpl, "layout", data)
}

// agentLevelWizardKeys are wizard fields whose values are stored on the Agent
// row (immutable per-agent) and therefore not shown on the per-version Inputs
// tab — they appear on the agent detail header instead. A version cannot
// diverge from its agent on these fields, so rendering them per-version would
// be misleading.
var agentLevelWizardKeys = map[string]bool{
	"name":           true,
	"discovery_file": true,
}

// buildWizardFieldViews maps each per-version schema field to its current
// value pulled from the FlowMapConfig. Agent-level fields (name,
// discovery_file) are skipped — they're rendered on the agent detail page.
// Field order follows the schema's `order` so the layout matches the wizard.
func (h *ConfiguratorHandler) buildWizardFieldViews(cfg types.FlowMapConfig) []wizardFieldView {
	if h.schema == nil {
		return nil
	}
	wizardValues := map[string]string{
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
			Value: wizardValues[of.Key],
		})
	}
	return out
}

// skillPromptView is the projection of a bundle skill rendered in the Prompts
// tab. JSON tags match the bundle schema produced by aikdm.
type skillPromptView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// promptsPageData embeds configPageData and adds the prompt-specific fields.
// Renders as an empty state when MainPrompt and SkillPrompts are both empty
// (NULL bundle or unmarshal failure both land here).
type promptsPageData struct {
	configPageData
	MainPrompt   string
	SkillPrompts []skillPromptView
}

// Prompts renders a read-only view of the version's compiled bundle:
// main_prompt + each skill's prompt. Malformed or NULL bundle → empty state.
// Tools and external_actions are not shown here; they live under the
// Architecture tab.
func (h *ConfiguratorHandler) Prompts(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Tab = "prompts"

	page := promptsPageData{configPageData: data}
	if len(data.Prompts) > 0 {
		var p types.Prompts
		if err := json.Unmarshal(data.Prompts, &p); err == nil {
			page.MainPrompt = p.MainPrompt
			// Cross-reference skill_prompts with skills_meta from the agent's
			// architecture to keep the display ordered by skill, with
			// description alongside the prompt.
			var arch types.Architecture
			if uerr := json.Unmarshal(data.Architecture, &arch); uerr == nil {
				for _, s := range arch.SkillsMeta {
					page.SkillPrompts = append(page.SkillPrompts, skillPromptView{
						Name:        s.Name,
						Description: s.Description,
						Prompt:      p.SkillPrompts[s.Name],
					})
				}
			} else {
				// No architecture available: emit prompts in map-iteration
				// order so something still renders for debugging.
				for name, body := range p.SkillPrompts {
					page.SkillPrompts = append(page.SkillPrompts, skillPromptView{
						Name:   name,
						Prompt: body,
					})
				}
			}
		}
		// Malformed JSON falls through with both fields empty — template
		// renders empty state.
	}

	renderTemplate(w, h.promptsTmpl, "layout", page)
}

// parsePromptsForm extracts main_prompt + skill_prompt[<name>] from a
// posted form. Used by both the confirmation modal handler and the
// final save handler.
func parsePromptsForm(r *http.Request) (types.Prompts, error) {
	if err := r.ParseForm(); err != nil {
		return types.Prompts{}, err
	}
	skillPrompts := map[string]string{}
	for key, vals := range r.Form {
		if !strings.HasPrefix(key, "skill_prompt[") || !strings.HasSuffix(key, "]") {
			continue
		}
		name := key[len("skill_prompt[") : len(key)-1]
		if name == "" || len(vals) == 0 {
			continue
		}
		skillPrompts[name] = vals[0]
	}
	return types.Prompts{
		MainPrompt:   r.FormValue("main_prompt"),
		SkillPrompts: skillPrompts,
	}, nil
}

// promptDiffEntry is one row in the confirm modal: a field that
// differs between the loaded version's prompts and what the user
// submitted.
type promptDiffEntry struct {
	Field    string
	Old      string
	New      string
	OldBytes int
	NewBytes int
}

// ConfirmSavePrompts renders the "Save as new version" confirmation
// modal. It diffs the submitted form against the current version's
// stored prompts; if nothing changed, the modal says so and offers
// only Close. If anything changed, the modal lists the affected
// fields and provides a Confirm button that posts the same payload
// to SavePrompts (the actual writer).
func (h *ConfiguratorHandler) ConfirmSavePrompts(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	submitted, err := parsePromptsForm(r)
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	version, _, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	var current types.Prompts
	if len(version.Prompts) > 0 {
		_ = json.Unmarshal(version.Prompts, &current)
	}

	diffs := diffPrompts(current, submitted)

	parentShort := versionID
	if len(parentShort) > 8 {
		parentShort = parentShort[:8]
	}

	data := map[string]any{
		"VersionID":          versionID,
		"ParentVersionShort": parentShort,
		"NoChanges":          len(diffs) == 0,
		"ChangedCount":       len(diffs),
		"Diffs":              diffs,
		"MainPrompt":         submitted.MainPrompt,
		"SkillPromptsMap":    submitted.SkillPrompts,
	}
	_ = h.promptsTmpl.ExecuteTemplate(w, "prompts_confirm_modal", data)
}

// normalizePromptText folds CRLF → LF and strips a single trailing
// newline so values round-tripped through a <textarea> compare equal to
// the LF-stored DB version. Browsers POST textarea content as CRLF per
// HTML form spec; without this, an unmodified Save would always show a
// diff.
func normalizePromptText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, "\n")
}

// diffPrompts returns one entry per field whose value differs between
// current and submitted. Skill prompts that exist on one side but not
// the other are also flagged (using "" for the missing side).
// Line endings + trailing newlines are normalized before comparison.
func diffPrompts(current, submitted types.Prompts) []promptDiffEntry {
	out := []promptDiffEntry{}
	curMain := normalizePromptText(current.MainPrompt)
	subMain := normalizePromptText(submitted.MainPrompt)
	if curMain != subMain {
		out = append(out, promptDiffEntry{
			Field:    "main_prompt",
			Old:      curMain,
			New:      subMain,
			OldBytes: len(curMain),
			NewBytes: len(subMain),
		})
	}
	// Union of skill names from both sides; deterministic order via sort.
	names := map[string]struct{}{}
	for n := range current.SkillPrompts {
		names[n] = struct{}{}
	}
	for n := range submitted.SkillPrompts {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		o := normalizePromptText(current.SkillPrompts[name])
		n := normalizePromptText(submitted.SkillPrompts[name])
		if o != n {
			out = append(out, promptDiffEntry{
				Field:    "skill_prompts." + name,
				Old:      o,
				New:      n,
				OldBytes: len(o),
				NewBytes: len(n),
			})
		}
	}
	return out
}

// SavePrompts handles the prompts editor's "Save as new version" submit
// (called from the confirmation modal's Confirm button). Forks a new
// DRAFT version with MCP attachments copied forward, then 303-redirects
// to the new version's Prompts tab.
func (h *ConfiguratorHandler) SavePrompts(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	submitted, err := parsePromptsForm(r)
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	promptsJSON, err := json.Marshal(submitted)
	if err != nil {
		Error(w, err)
		return
	}
	newID, err := h.repo.CreateVersionFromPrompts(r.Context(), versionID, promptsJSON)
	if err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/agent_versions/"+newID+"/configure/prompts", http.StatusSeeOther)
}

// Index redirects /configure to the version's default tab. Post-PR #34 +
// the tab restructure: Inputs/Architecture moved to the agent detail page,
// so the version detail page's default lands on Prompts.
func (h *ConfiguratorHandler) Index(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	http.Redirect(w, r, "/agent_versions/"+versionID+"/configure/prompts", http.StatusFound)
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
	data.Tab = "architecture"
	data.SubTab = "flows"
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
	data.Tab = "architecture"
	data.SubTab = "skills"
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

func (h *ConfiguratorHandler) Endpoints(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data.Tab = "architecture"
	data.SubTab = "endpoints"
	if epID := r.PathValue("endpointID"); epID != "" {
		for i := range data.Config.Endpoints {
			if data.Config.Endpoints[i].ID == epID {
				data.SelectedEndpoint = &data.Config.Endpoints[i]
				break
			}
		}
		if data.SelectedEndpoint == nil {
			http.NotFound(w, r)
			return
		}
	}

	// Load available HTTP backends and current endpoint→backend wiring.
	var httpBackends []*types.ToolBackend
	var endpointBackends map[string]string
	if h.backends != nil {
		allBackends, err := h.backends.List(r.Context())
		if err != nil {
			Error(w, err)
			return
		}
		httpBackends = []*types.ToolBackend{}
		for _, b := range allBackends {
			if b.Kind == types.ToolBackendKindHTTPEndpoint {
				httpBackends = append(httpBackends, b)
			}
		}
	}
	if h.agentWiring != nil {
		endpointBackends, err = h.agentWiring.ListEndpointBackends(r.Context(), data.AgentID)
		if err != nil {
			Error(w, err)
			return
		}
	}
	if endpointBackends == nil {
		endpointBackends = map[string]string{}
	}

	unmappedCount := 0
	for _, ep := range data.Config.Endpoints {
		if endpointBackends[ep.ID] == "" {
			unmappedCount++
		}
	}

	type endpointsPageData struct {
		configPageData
		AvailableBackends []*types.ToolBackend
		EndpointBackends  map[string]string
		UnmappedCount     int
	}
	renderTemplate(w, h.endpointsTmpl, "layout", endpointsPageData{
		configPageData:    data,
		AvailableBackends: httpBackends,
		EndpointBackends:  endpointBackends,
		UnmappedCount:     unmappedCount,
	})
}

// SetEndpointBackend maps an endpoint to a backend (POST with backend_id="" unmaps).
// htmx fragment response: re-renders the endpoint_detail partial for the affected endpoint.
//
// Endpoint→backend wiring is agent-keyed (post-017), so we resolve the
// version_id path param to the underlying agent_id once and use that for
// every wiring call below.
func (h *ConfiguratorHandler) SetEndpointBackend(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("version_id")
	eid := r.PathValue("endpointID")
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	bid := r.FormValue("backend_id")

	_, agent, err := h.repo.GetWithAgent(r.Context(), vid)
	if err != nil {
		Error(w, err)
		return
	}

	if bid == "" {
		if err := h.agentWiring.UnsetEndpointBackend(r.Context(), agent.ID, eid); err != nil {
			Error(w, err)
			return
		}
	} else {
		if err := h.agentWiring.SetEndpointBackend(r.Context(), agent.ID, eid, bid); err != nil {
			Error(w, err)
			return
		}
	}

	// Re-render the endpoint_detail partial for this endpoint.
	cfgBytes, _, err := h.repo.GetFlowMapConfig(r.Context(), vid)
	if err != nil {
		Error(w, err)
		return
	}
	var cfg types.FlowMapConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		Error(w, err)
		return
	}
	var selected *types.Endpoint
	for i := range cfg.Endpoints {
		if cfg.Endpoints[i].ID == eid {
			selected = &cfg.Endpoints[i]
			break
		}
	}
	if selected == nil {
		http.Error(w, "endpoint not found", http.StatusNotFound)
		return
	}

	var httpBackends []*types.ToolBackend
	if h.backends != nil {
		allBackends, _ := h.backends.List(r.Context())
		httpBackends = []*types.ToolBackend{}
		for _, b := range allBackends {
			if b.Kind == types.ToolBackendKindHTTPEndpoint {
				httpBackends = append(httpBackends, b)
			}
		}
	}
	var endpointBackends map[string]string
	if h.agentWiring != nil {
		endpointBackends, _ = h.agentWiring.ListEndpointBackends(r.Context(), agent.ID)
	}
	if endpointBackends == nil {
		endpointBackends = map[string]string{}
	}

	renderTemplate(w, h.endpointsTmpl, "endpoint_detail", map[string]any{
		"VersionID":         vid,
		"Endpoint":          selected,
		"AvailableBackends": httpBackends,
		"EndpointBackends":  endpointBackends,
	})
}

// MCPSubtab renders the architecture/mcp subtab — list of all MCP backends
// globally with attach checkboxes + notes.
func (h *ConfiguratorHandler) MCPSubtab(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	version, agent, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	data, err := h.mcpSubtabData(r.Context(), version, agent)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.mcpTmpl, "layout", data)
}

func (h *ConfiguratorHandler) mcpSubtabData(ctx context.Context, version *types.AgentVersion, agent *types.Agent) (map[string]any, error) {
	var allBackends []mcpBackendView
	if h.backends != nil {
		all, err := h.backends.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range all {
			if b.Kind == types.ToolBackendKindMCPClient {
				var cfg types.MCPBackendConfig
				_ = json.Unmarshal(b.Config, &cfg)
				allBackends = append(allBackends, mcpBackendView{
					ToolBackend: b,
					PrimaryURL:  cfg.URL,
				})
			}
		}
	}
	var attMap map[string]*repository.MCPAttachment
	if h.wiring != nil {
		atts, err := h.wiring.ListMCPAttachments(ctx, version.ID)
		if err != nil {
			return nil, err
		}
		attMap = make(map[string]*repository.MCPAttachment, len(atts))
		for i := range atts {
			a := atts[i]
			attMap[a.BackendID] = &a
		}
	}
	return map[string]any{
		"VersionID":      version.ID,
		"AgentName":      agent.Name,
		"AgentID":        agent.ID,
		"AgentStatus":    version.Status,
		"ReadOnly":       version.Status != "INITIALIZING",
		"Tab":            "mcp",
		"Active":         "agents",
		"AllMCPBackends": allBackends,
		"Attachments":    attMap,
	}, nil
}

// ToggleMCPBackend attaches or detaches an MCP backend based on whether the
// "attached" form field is present. Returns the row's detail fragment.
func (h *ConfiguratorHandler) ToggleMCPBackend(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("version_id")
	bid := r.PathValue("backendID")
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	attached := r.FormValue("attached") != ""

	if h.wiring == nil {
		http.Error(w, "wiring repo not configured", http.StatusInternalServerError)
		return
	}

	if attached {
		if err := h.wiring.AttachMCP(r.Context(), vid, bid, ""); err != nil {
			Error(w, err)
			return
		}
	} else {
		if err := h.wiring.DetachMCP(r.Context(), vid, bid); err != nil {
			Error(w, err)
			return
		}
	}
	h.renderMCPRowFragment(w, r.Context(), vid, bid)
}

// UpdateAllMCPNotes processes the single bulk form on the MCP tab — one
// textarea per currently-attached backend, all submitted together. Form
// fields are named note[<backend_id>]. We upsert only currently-attached
// rows (silently ignore note[*] entries whose backend isn't attached) so a
// stale form can't accidentally re-attach a detached backend.
//
// Re-renders the whole MCP form via htmx outerHTML swap with Saved=true so
// the user sees a single "Saved ✓" pill next to the Save button.
func (h *ConfiguratorHandler) UpdateAllMCPNotes(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if h.wiring == nil {
		http.Error(w, "wiring repo not configured", http.StatusInternalServerError)
		return
	}

	version, agent, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}

	// Pull the current attachment set — we only update notes for backends
	// actually attached. The form may carry stale fields for backends the
	// user just unchecked via the htmx toggle.
	atts, err := h.wiring.ListMCPAttachments(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	attached := map[string]bool{}
	for _, a := range atts {
		attached[a.BackendID] = true
	}

	const prefix = "note["
	for key, vals := range r.Form {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, "]") {
			continue
		}
		bid := key[len(prefix) : len(key)-1]
		if !attached[bid] || len(vals) == 0 {
			continue
		}
		if err := h.wiring.AttachMCP(r.Context(), versionID, bid, vals[0]); err != nil {
			Error(w, err)
			return
		}
	}

	data, err := h.mcpSubtabData(r.Context(), version, agent)
	if err != nil {
		Error(w, err)
		return
	}
	data["Saved"] = true
	// Re-render just the form (tab_content), not the layout — htmx swap
	// is outerHTML on the form element.
	_ = h.mcpTmpl.ExecuteTemplate(w, "tab_content", data)
}

// renderMCPRowFragment renders just the #mcp-row-{bid} outer div for htmx
// outerHTML swap after a toggle or note update.
func (h *ConfiguratorHandler) renderMCPRowFragment(w http.ResponseWriter, ctx context.Context, vid, bid string) {
	if h.backends == nil || h.wiring == nil {
		http.Error(w, "repos not configured", http.StatusInternalServerError)
		return
	}
	be, err := h.backends.Get(ctx, bid)
	if err != nil {
		Error(w, err)
		return
	}
	atts, err := h.wiring.ListMCPAttachments(ctx, vid)
	if err != nil {
		Error(w, err)
		return
	}
	var att *repository.MCPAttachment
	for i := range atts {
		if atts[i].BackendID == bid {
			a := atts[i]
			att = &a
			break
		}
	}

	// Resolve PrimaryURL for the header — same projection mcpSubtabData uses.
	var primaryURL string
	if be.Kind == types.ToolBackendKindMCPClient {
		var cfg types.MCPBackendConfig
		_ = json.Unmarshal(be.Config, &cfg)
		primaryURL = cfg.URL
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.mcpTmpl.ExecuteTemplate(w, "mcp_card", map[string]any{
		"VersionID":  vid,
		"Backend":    be,
		"Attachment": att,
		"PrimaryURL": primaryURL,
	})
}

// tplDict builds a map[string]any from alternating key/value template args.
// Used to pass multiple named values into a sub-template invocation:
//
//	{{template "flow_row" (dict "VersionID" $.VersionID "Flow" .)}}
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

// requireEditable resolves the version_id to the (version, agent) pair and
// verifies the version is in INITIALIZING — the only state where the
// configurator accepts edits. Returns the version's ID so callers can route
// subsequent config CRUD against the per-version flow_map_config. Used as a
// guard at the top of every mutating handler so a stale tab or hand-crafted
// request can't change a finalized version.
func (h *ConfiguratorHandler) requireEditable(ctx context.Context, versionID string) (string, error) {
	version, _, err := h.repo.GetWithAgent(ctx, versionID)
	if err != nil {
		return "", err
	}
	if version.Status != "INITIALIZING" {
		return "", types.ErrInvalidAgentStatus
	}
	return version.ID, nil
}

// loadConfig fetches the version's flow_map_config and unmarshals into a
// FlowMapConfig. Returns ErrNotFound if the version does not exist or has no
// config persisted (the configurator pages assume the wizard already ran).
func (h *ConfiguratorHandler) loadConfig(ctx context.Context, versionID string) (types.FlowMapConfig, error) {
	cfgBytes, _, err := h.repo.GetFlowMapConfig(ctx, versionID)
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
func (h *ConfiguratorHandler) saveConfig(ctx context.Context, versionID string, cfg types.FlowMapConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return h.repo.UpdateFlowMapConfig(ctx, versionID, b)
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

	versionID := r.PathValue("version_id")
	flowID := r.PathValue("flowId")
	vID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	cfg, err := h.loadConfig(r.Context(), vID)
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

	if err := h.saveConfig(r.Context(), vID, cfg); err != nil {
		Error(w, err)
		return
	}

	// Re-render the flow row so htmx can swap it in place. Template's
	// VersionID drives the htmx URL for the toggle/back actions.
	renderTemplate(w, h.flowsTmpl, "flow_row", map[string]any{
		"VersionID":  versionID,
		"Flow":       cfg.Flows[idx],
		"SelectedID": "",
	})
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

// SkillCreate adds a new custom skill from form values. The id is server-
// assigned via SlugifySkillName + UniqueSkillID. Returns the rendered
// skill_row partial so htmx can append it to the list.
func (h *ConfiguratorHandler) SkillCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	versionID := r.PathValue("version_id")
	vID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	cfg, err := h.loadConfig(r.Context(), vID)
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
	if err := h.saveConfig(r.Context(), vID, cfg); err != nil {
		Error(w, err)
		return
	}

	renderTemplate(w, h.skillsTmpl, "skill_row", map[string]any{
		"VersionID":  versionID,
		"Skill":      parsed,
		"SelectedID": "",
	})
}

// SkillDelete removes a custom skill. Discovered skills cannot be deleted
// (409). Custom skills cannot be deleted while referenced by any flow's
// workflow (409). On success returns 200 with an empty body so htmx can
// remove the row in place.
func (h *ConfiguratorHandler) SkillDelete(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	skillID := r.PathValue("skillId")
	vID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	cfg, err := h.loadConfig(r.Context(), vID)
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
	if err := h.saveConfig(r.Context(), vID, cfg); err != nil {
		Error(w, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// SkillNew renders an empty skill_new_form for creating a custom skill.
// The form's submit URL is /skills (no skillId), creating a new row
// instead of updating an existing one.
func (h *ConfiguratorHandler) SkillNew(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	version, _, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	cfg, err := h.loadConfig(r.Context(), version.ID)
	if err != nil {
		Error(w, err)
		return
	}
	blank := types.Skill{Origin: "custom"}
	renderTemplate(w, h.skillsTmpl, "skill_new_form", map[string]any{
		"VersionID": versionID,
		"Skill":     blank,
		"Endpoints": cfg.Endpoints,
	})
}

// WorkflowUpdate accepts a JSON body { "mermaid": "...", "layout": { nodeId: {x, y} } }
// and persists it to the named flow's Workflow struct. Validates the mermaid:
//   - structural parse via flowmap.ParseWorkflow
//   - every id[<skill-id>] rectangle resolves to a known skill in this agent's config
//
// Returns 200 with empty body on success.
func (h *ConfiguratorHandler) WorkflowUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mermaid string                    `json:"mermaid"`
		Layout  map[string]types.Position `json:"layout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	versionID := r.PathValue("version_id")
	flowID := r.PathValue("flowId")
	vID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	cfg, err := h.loadConfig(r.Context(), vID)
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

	if err := h.saveConfig(r.Context(), vID, cfg); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// tplJSON marshals v to JSON suitable for embedding as a single-quoted HTML
// attribute value or a <script type="application/json"> body. Used by the
// workflow editor's data-skills attribute and data-obf-state element.
func tplJSON(v any) (template.JS, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return template.JS(b), nil
}

// tplSkillIDs extracts the id slice from a []types.Skill.
func tplSkillIDs(skills []types.Skill) []string {
	out := make([]string, len(skills))
	for i, s := range skills {
		out[i] = s.ID
	}
	return out
}

// tplIncludedFlowsCount returns the number of flows that are toggled
// on. Used by the finalize summary so the count matches what will
// actually ship downstream.
func tplIncludedFlowsCount(flows []types.Flow) int {
	n := 0
	for _, f := range flows {
		if f.Included {
			n++
		}
	}
	return n
}

// tplWorkflowNodeCount returns the number of nodes the editor will render
// for a workflow — i.e. the node count after normalization. Falls back to
// 0 when the mermaid can't be parsed.
func tplWorkflowNodeCount(wf types.Workflow) int {
	src := wf.Mermaid
	if normalized, err := flowmap.NormalizeMermaid(src); err == nil {
		src = normalized
	}
	pw, err := flowmap.ParseWorkflow(src)
	if err != nil {
		return 0
	}
	return len(pw.Nodes)
}

// tplWorkflowState marshals a Workflow as JSON for the inline state element
// the workflow editor reads.
//
// The mermaid is normalized best-effort so flows compiled against older
// dialect rules (parallel-fanout, pipe-labels) still render. Normalization
// failures are non-fatal — the raw mermaid is passed through and the
// editor will surface its own parse error.
func tplWorkflowState(wf types.Workflow) (template.JS, error) {
	if normalized, err := flowmap.NormalizeMermaid(wf.Mermaid); err == nil {
		wf.Mermaid = normalized
	}
	b, err := json.Marshal(wf)
	if err != nil {
		return "", err
	}
	return template.JS(b), nil
}

// DownloadYAML renders the agent's flow_map_config as YAML and serves it
// as a file attachment named "<agent-name>.yaml".
//
// Query parameter:
//   - clean=true: emit a filtered view — flows with included=false dropped;
//     in v2 endpoints are always preserved as the runtime tool inventory.
//     The full view (default) preserves everything for audit and round-trip.
//
// Available for any agent (no status gate at the handler level — the link
// in /agents/ui only appears for non-INITIALIZING agents). Returns 404 if
// the agent doesn't exist or has no config persisted yet.
func (h *ConfiguratorHandler) DownloadYAML(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	version, agent, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cfg, err := h.loadConfig(r.Context(), version.ID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Augment config with operator-attached MCP backends (not persisted on the
	// FlowMapConfig row — joined at serve time from the wiring tables).
	if h.wiring != nil && h.backends != nil {
		atts, err := h.wiring.ListMCPAttachments(r.Context(), version.ID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		mcps := make([]types.AttachedMCP, 0, len(atts))
		for _, a := range atts {
			be, err := h.backends.Get(r.Context(), a.BackendID)
			if err != nil {
				// A wiring row references a missing backend — defensive skip,
				// log and continue. The agent's bundle won't include this MCP.
				slog.Default().Warn("attached MCP backend missing",
					slog.String("version", version.ID),
					slog.String("backend_id", a.BackendID),
					slog.Any("err", err))
				continue
			}
			var mcpCfg types.MCPBackendConfig
			_ = json.Unmarshal(be.Config, &mcpCfg)
			mcps = append(mcps, types.AttachedMCP{
				Name: be.Name,
				URL:  mcpCfg.URL,
				Note: a.Note,
			})
		}
		cfg.AttachedMCPs = mcps
	}

	// Bump schema_version to 3 for aikdm consumption. The Go-side parser
	// stores v2 (discovery's emission); the YAML served to aikdm advertises v3
	// because of the AttachedMCPs additive field.
	cfg.SchemaVersion = 3

	if r.URL.Query().Get("clean") == "true" {
		cfg = filterAgentConfig(cfg)
	}

	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	yamlBytes = normalizeBlockScalarHeaders(yamlBytes)

	filename := sanitiseFilename(agent.Name) + ".yaml"
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(yamlBytes)
}

// filterAgentConfig returns a copy of cfg with curation noise stripped:
//   - flows where Included=false are dropped
//
// In v2 the endpoint inventory is always preserved (endpoints are the
// runtime tool catalog; skills only suggest a subset). Skills are not
// filtered either. The full config remains in the database; this
// function only shapes the YAML for export.
func filterAgentConfig(cfg types.FlowMapConfig) types.FlowMapConfig {
	keptFlows := make([]types.Flow, 0, len(cfg.Flows))
	for _, f := range cfg.Flows {
		if f.Included {
			keptFlows = append(keptFlows, f)
		}
	}
	cfg.Flows = keptFlows
	return cfg
}

// blockScalarIndentRE matches block-scalar headers with an explicit indent
// indicator (e.g. `|4`, `>+2`). yaml.v3 emits these conservatively for
// multi-line strings, but other YAML readers interpret the indicator
// differently from yaml.v3's emission, breaking interoperability.
// Stripping the digits leaves the bare indicator (`|`, `|+`, `|-`, `>`,
// etc.); auto-indent detection on the reader side handles the content
// correctly across implementations.
var blockScalarIndentRE = regexp.MustCompile(`(?m)([|>])([+-]?)\d+(\s*)$`)

// normalizeBlockScalarHeaders strips explicit indent indicators from block
// scalar headers (`|N` -> `|`). Safe because the digit-less form is what
// every YAML parser auto-detects from the content's actual indentation.
func normalizeBlockScalarHeaders(b []byte) []byte {
	return blockScalarIndentRE.ReplaceAll(b, []byte("$1$2$3"))
}

// sanitiseFilename produces a safe basename for the Content-Disposition header.
// Strips path separators and quotes; falls back to "agent" if the input is
// empty or has no allowed characters.
func sanitiseFilename(name string) string {
	if name == "" {
		return "agent"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		return "agent"
	}
	return out
}

// FinalizeConfirm renders the small confirmation page shown when the user
// clicks "Finalize →" in the configurator. Submitting the page's form
// POSTs to /agent_versions/{version_id}/finalize.
func (h *ConfiguratorHandler) FinalizeConfirm(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	version, agent, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cfgBytes, parseErr, err := h.repo.GetFlowMapConfig(r.Context(), version.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data := configPageData{
		Active:     "agents",
		VersionID:  versionID,
		AgentID:    agent.ID,
		AgentName:  agent.Name,
		Tab:        "finalize",
		ParseError: parseErr,
	}
	if len(cfgBytes) > 0 {
		_ = json.Unmarshal(cfgBytes, &data.Config)
	}
	renderTemplate(w, h.finalizeTmpl, "layout", data)
}

// Finalize flips the version's status INITIALIZING → DRAFT and redirects to
// /agents/ui. 409 (ErrInvalidAgentStatus) if the version isn't in INITIALIZING.
func (h *ConfiguratorHandler) Finalize(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	if err := h.repo.UpdateStatus(r.Context(), versionID, "INITIALIZING", "DRAFT"); err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/agents/ui", http.StatusSeeOther)
}

// SkillUpdate applies form values to an existing skill in place.
func (h *ConfiguratorHandler) SkillUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	versionID := r.PathValue("version_id")
	skillID := r.PathValue("skillId")
	vID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	cfg, err := h.loadConfig(r.Context(), vID)
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

	if err := h.saveConfig(r.Context(), vID, cfg); err != nil {
		Error(w, err)
		return
	}

	// Re-render skill_detail so the htmx swap shows the saved state.
	renderTemplate(w, h.skillsTmpl, "skill_detail", map[string]any{
		"VersionID": versionID,
		"Skill":     cur,
		"Endpoints": cfg.Endpoints,
	})
}

// RegisterConfiguratorRedirects mounts 301s for the pre-redesign tab URLs.
// Bookmarks against /configure/{flows,skills,endpoints} survive; the bare
// /configure/architecture path lands on the default Flows sub-tab.
func RegisterConfiguratorRedirects(mux *http.ServeMux) {
	for _, sub := range []string{"flows", "skills", "endpoints"} {
		sub := sub
		mux.HandleFunc("GET /agent_versions/{version_id}/configure/"+sub, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r,
				"/agent_versions/"+r.PathValue("version_id")+"/configure/architecture/"+sub,
				http.StatusMovedPermanently)
		})
	}
	mux.HandleFunc("GET /agent_versions/{version_id}/configure/architecture", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r,
			"/agent_versions/"+r.PathValue("version_id")+"/configure/architecture/flows",
			http.StatusMovedPermanently)
	})
}
