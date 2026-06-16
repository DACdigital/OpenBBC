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
	"regexp"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/yuin/goldmark"
	"gopkg.in/yaml.v3"
)

// ConfigStore is the narrow interface the configurator depends on.
//
// GetWithAgent returns both the version row (for status/bundle) and its owning
// Agent (for name/flow_map_config). The flow-map config lives on the Agent
// (per-agent), while status and bundle live on the AgentVersion. Other methods
// are scoped: GetFlowMapConfig / UpdateFlowMapConfig take an agent_id;
// UpdateStatus takes a version_id.
type ConfigStore interface {
	GetWithAgent(ctx context.Context, versionID string) (*types.AgentVersion, *types.Agent, error)
	GetFlowMapConfig(ctx context.Context, agentID string) (cfg []byte, parseErr string, err error)
	UpdateFlowMapConfig(ctx context.Context, agentID string, cfg []byte) error
	UpdateStatus(ctx context.Context, versionID, expectedFrom, to string) error
}

type ConfiguratorHandler struct {
	repo                                                              ConfigStore
	schema                                                            *types.WizardSchema
	flowsTmpl, skillsTmpl, capabilitiesTmpl, finalizeTmpl, inputsTmpl *template.Template
}

func NewConfiguratorHandler(repo ConfigStore, schema *types.WizardSchema, webFS fs.FS) (*ConfiguratorHandler, error) {
	funcs := template.FuncMap{
		"renderMarkdown": renderMarkdown,
		"statusClass":    statusClass,
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
	capabilitiesTmpl, err := parse("capabilities")
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
	return &ConfiguratorHandler{
		repo:             repo,
		schema:           schema,
		flowsTmpl:        flowsTmpl,
		skillsTmpl:       skillsTmpl,
		capabilitiesTmpl: capabilitiesTmpl,
		finalizeTmpl:     finalizeTmpl,
		inputsTmpl:       inputsTmpl,
	}, nil
}

// proseRelLink matches `../<section>/<id>.md` inside a markdown link
// destination, where <section> is one of the .flow-map directory names
// the discovery skill emits.
var proseRelLink = regexp.MustCompile(`\.\./(skills|capabilities|flows)/([^)\s]+?)\.md`)

// renderMarkdown converts a prose markdown blob (from the discovery
// skill) to HTML. Relative links into the .flow-map directory layout
// are rewritten to the configurator routes (scoped by version_id) so
// they actually navigate. Trusted input — prose is generated, not
// user-typed.
func renderMarkdown(versionID, md string) template.HTML {
	if md == "" {
		return ""
	}
	if versionID != "" {
		base := "/agent_versions/" + versionID + "/configure/"
		md = proseRelLink.ReplaceAllString(md, base+"$1/$2")
	}
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(md), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(md))
	}
	return template.HTML(buf.String())
}

type configPageData struct {
	Active            string
	VersionID         string // URL path param value (a version row's ID)
	AgentID           string // stable agent ID, used for back-link to the version list
	AgentName         string
	AgentStatus       string // version's status (lives on AgentVersion now)
	ReadOnly          bool   // true for non-INITIALIZING versions (DRAFT, TRAINING, READY, DEPLOYED)
	HasBundle         bool   // true when this version has a generated bundle (Run is enabled)
	Tab               string // "flows" | "skills" | "capabilities" | "inputs"
	Config            types.FlowMapConfig
	ParseError        string
	DiscoveryFilePath string
	WizardFields      []wizardFieldView // populated for the Inputs tab
	SelectedFlow      *types.Flow
	SelectedSkill     *types.Skill
	SelectedCap       *types.Capability
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
	cfgBytes, parseErr, err := h.repo.GetFlowMapConfig(r.Context(), agent.ID)
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
		Active:            "agents",
		VersionID:         versionID,
		AgentID:           agent.ID,
		AgentName:         agent.Name,
		AgentStatus:       version.Status,
		ReadOnly:          version.Status != "INITIALIZING",
		HasBundle:         len(version.Bundle) > 0,
		Config:            cfg,
		ParseError:        parseErr,
		DiscoveryFilePath: agent.DiscoveryFilePath,
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
	data.WizardFields = h.buildWizardFieldViews(data.Config, data.DiscoveryFilePath)
	renderTemplate(w, h.inputsTmpl, "layout", data)
}

// buildWizardFieldViews maps each schema field to its current value pulled
// from the FlowMapConfig (for text/textarea) or the agent row (for file).
// Field order follows the schema's `order` so the layout matches the wizard.
func (h *ConfiguratorHandler) buildWizardFieldViews(cfg types.FlowMapConfig, discoveryFilePath string) []wizardFieldView {
	if h.schema == nil {
		return nil
	}
	wizardValues := map[string]string{
		"name":            cfg.Name,
		"scope":           cfg.Scope,
		"should_do":       cfg.ShouldDo,
		"should_not_do":   cfg.ShouldNotDo,
		"business_domain": cfg.BusinessDomain,
		"discovery_file":  discoveryFilePath,
	}
	out := make([]wizardFieldView, 0, len(h.schema.Wizard))
	for _, of := range h.schema.OrderedFields() {
		out = append(out, wizardFieldView{
			Key:   of.Key,
			Label: of.Field.Label,
			Type:  of.Field.Type,
			Value: wizardValues[of.Key],
		})
	}
	return out
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
// configurator accepts edits. Returns the agent's stable ID so callers can
// route subsequent config CRUD against the per-agent flow_map_config.
// Used as a guard at the top of every mutating handler so a stale tab or
// hand-crafted request can't change a finalized version.
func (h *ConfiguratorHandler) requireEditable(ctx context.Context, versionID string) (agentID string, err error) {
	version, agent, err := h.repo.GetWithAgent(ctx, versionID)
	if err != nil {
		return "", err
	}
	if version.Status != "INITIALIZING" {
		return "", types.ErrInvalidAgentStatus
	}
	return agent.ID, nil
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

	versionID := r.PathValue("version_id")
	flowID := r.PathValue("flowId")
	agentID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
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

	// Re-render the flow row so htmx can swap it in place. Template's
	// VersionID drives the htmx URL for the toggle/back actions.
	renderTemplate(w, h.flowsTmpl, "flow_row", map[string]any{
		"VersionID":  versionID,
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
	versionID := r.PathValue("version_id")
	agentID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
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
	agentID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
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

// SkillNew renders an empty skill_new_form for creating a custom skill.
// The form's submit URL is /skills (no skillId), creating a new row
// instead of updating an existing one.
func (h *ConfiguratorHandler) SkillNew(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	_, agent, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	cfg, err := h.loadConfig(r.Context(), agent.ID)
	if err != nil {
		Error(w, err)
		return
	}
	blank := types.Skill{Origin: "custom", Role: "read"}
	renderTemplate(w, h.skillsTmpl, "skill_new_form", map[string]any{
		"VersionID":    versionID,
		"Skill":        blank,
		"Capabilities": cfg.Capabilities,
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
	agentID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
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

	if err := h.saveConfig(r.Context(), agentID, cfg); err != nil {
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
//   - clean=true: emit a filtered view — flows with included=false dropped,
//     and capabilities not referenced by any remaining skill dropped. The
//     full view (default) preserves everything for audit and round-trip.
//
// Available for any agent (no status gate at the handler level — the link
// in /agents/ui only appears for non-INITIALIZING agents). Returns 404 if
// the agent doesn't exist or has no config persisted yet.
func (h *ConfiguratorHandler) DownloadYAML(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	_, agent, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cfg, err := h.loadConfig(r.Context(), agent.ID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

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
//   - capabilities not referenced by any remaining skill are dropped
//
// Skills are not filtered (external skills remain — the agent needs to
// know about them to redirect users). The full config remains in the
// database; this function only shapes the YAML for export.
func filterAgentConfig(cfg types.FlowMapConfig) types.FlowMapConfig {
	keptFlows := make([]types.Flow, 0, len(cfg.Flows))
	for _, f := range cfg.Flows {
		if f.Included {
			keptFlows = append(keptFlows, f)
		}
	}

	referenced := make(map[string]struct{}, len(cfg.Skills))
	for _, s := range cfg.Skills {
		if s.CapabilityRef != "" {
			referenced[s.CapabilityRef] = struct{}{}
		}
	}
	keptCaps := make([]types.Capability, 0, len(cfg.Capabilities))
	for _, c := range cfg.Capabilities {
		if _, ok := referenced[c.Name]; ok {
			keptCaps = append(keptCaps, c)
		}
	}

	cfg.Flows = keptFlows
	cfg.Capabilities = keptCaps
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
	_, agent, err := h.repo.GetWithAgent(r.Context(), versionID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cfgBytes, parseErr, err := h.repo.GetFlowMapConfig(r.Context(), agent.ID)
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
	agentID, err := h.requireEditable(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
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
		"VersionID":    versionID,
		"Skill":        *cur,
		"Capabilities": cfg.Capabilities,
	})
}
