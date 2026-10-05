package handler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// Configurator Agents tab (spec § REST — BO configurator): the version's
// agent_tool_enabled flag and its sub-agent bindings. Writes answer with HTML
// fragments only — on failure an #agents-error fragment carrying statusFor's
// status, which the vendored htmx response-targets extension swaps into the
// tab's error slot. These handlers never call Error (JSON).

// WithSubAgents injects the sub-agent repository the Agents tab reads and
// writes through. Returns h for chaining at construction sites.
func (h *ConfiguratorHandler) WithSubAgents(r *repository.SubAgentRepository) *ConfiguratorHandler {
	h.subAgents = r
	return h
}

// agentTargetGroup is one <optgroup> of the target picker.
type agentTargetGroup struct {
	AgentName string
	Targets   []repository.BindableTarget
}

type agentsTabData struct {
	configPageData
	Enabled      bool
	Bindings     []repository.LabeledBinding
	TargetGroups []agentTargetGroup
	Locked       bool // no writes: status ∉ {INITIALIZING, DRAFT} or eval/training active
	EvalBlocked  bool // Locked because of active eval or training work (banner)
	Saved        bool
	OOB          bool // agents_toggle: render with hx-swap-oob (refused-toggle snap-back)
}

// errAgentsFormInvalid is the sentinel behind every Agents-tab form-shape
// error; statusFor maps it to 400.
var errAgentsFormInvalid = errors.New("invalid agents form")

// errAgentsBadForm is a form-shape error local to the Agents tab carrying a
// user-facing message; it unwraps to errAgentsFormInvalid.
func errAgentsBadForm(msg string) error {
	return &agentsFormError{msg: msg}
}

type agentsFormError struct{ msg string }

func (e *agentsFormError) Error() string { return e.msg }
func (e *agentsFormError) Unwrap() error { return errAgentsFormInvalid }

// renderAgentsError writes statusFor(err) and the #agents-error fragment.
// 5xx errors are logged and rendered as a generic "internal error".
func (h *ConfiguratorHandler) renderAgentsError(w http.ResponseWriter, err error) {
	h.renderAgentsErrorWith(w, err, nil)
}

// renderAgentsErrorWith is renderAgentsError followed by extra (already
// rendered) out-of-band fragments; extra is dropped if the error fragment
// itself fails to render.
func (h *ConfiguratorHandler) renderAgentsErrorWith(w http.ResponseWriter, err error, extra []byte) {
	status := statusFor(err)
	msg := err.Error()
	if status >= http.StatusInternalServerError {
		slog.Error("configurator agents tab", slog.Any("error", err))
		msg = "internal error"
	}
	var buf bytes.Buffer
	if terr := h.agentsTmpl.ExecuteTemplate(&buf, "agents_error", map[string]any{"Message": msg, "OOB": false}); terr != nil {
		slog.Error("template execution failed", slog.String("template", "agents_error"), slog.Any("error", terr))
		buf.Reset()
		buf.WriteString(`<div id="agents-error" role="alert">internal error</div>`)
	} else {
		buf.Write(extra)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// renderAgentsFragment renders a success fragment followed by an
// out-of-band empty #agents-error that clears any earlier error.
func (h *ConfiguratorHandler) renderAgentsFragment(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	err := h.agentsTmpl.ExecuteTemplate(&buf, name, data)
	if err == nil && name != "tab_content" {
		err = h.agentsTmpl.ExecuteTemplate(&buf, "agents_error", map[string]any{"Message": "", "OOB": true})
	}
	if err != nil {
		h.renderAgentsError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// agentsVersionID returns the path's version id, or ErrNotFound when it is
// not a canonical UUID (Postgres would reject it as a 500).
func agentsVersionID(r *http.Request) (string, error) {
	id := r.PathValue("version_id")
	if !validUUID(id) {
		return "", types.ErrNotFound
	}
	return id, nil
}

// agentsTabData loads everything the tab renders.
func (h *ConfiguratorHandler) agentsTabData(ctx context.Context, versionID string) (*agentsTabData, error) {
	if h.subAgents == nil {
		return nil, errors.New("sub-agent repository not configured")
	}
	version, agent, err := h.repo.GetWithAgent(ctx, versionID)
	if err != nil {
		return nil, err
	}
	enabled, _, err := h.subAgents.GetAgentToolConfig(ctx, versionID)
	if err != nil {
		return nil, err
	}
	bindings, err := h.subAgents.ListBindingsWithLabels(ctx, versionID)
	if err != nil {
		return nil, err
	}
	block := h.subAgents.ConfigWriteBlock(ctx, versionID)
	if block != nil && !errors.Is(block, types.ErrVersionLocked) && !errors.Is(block, types.ErrEvalOrTrainingActive) {
		return nil, block
	}
	d := &agentsTabData{
		configPageData: configPageData{
			Active:           "agents",
			VersionID:        version.ID,
			AgentID:          agent.ID,
			AgentName:        agent.Name,
			AgentStatus:      version.Status,
			ReadOnly:         version.Status != string(types.AgentStatusInitializing),
			HasBundle:        len(agent.Architecture) > 0 && len(version.Prompts) > 0,
			Tab:              "agents",
			AgentToolEnabled: enabled,
		},
		Enabled:     enabled,
		Bindings:    bindings,
		Locked:      block != nil,
		EvalBlocked: errors.Is(block, types.ErrEvalOrTrainingActive),
	}
	d.VersionNum, _ = h.repo.GetVersionNum(ctx, version.ID)
	if !d.Locked {
		targets, err := h.subAgents.ListBindableTargets(ctx, versionID)
		if err != nil {
			return nil, err
		}
		d.TargetGroups = groupTargets(targets)
	}
	return d, nil
}

// groupTargets splits the (agent name, version num)-ordered picker list into
// one group per agent.
func groupTargets(targets []repository.BindableTarget) []agentTargetGroup {
	var out []agentTargetGroup
	lastAgent := ""
	for _, t := range targets {
		if len(out) == 0 || t.AgentID != lastAgent {
			out = append(out, agentTargetGroup{AgentName: t.AgentName})
			lastAgent = t.AgentID
		}
		g := &out[len(out)-1]
		g.Targets = append(g.Targets, t)
	}
	return out
}

// AgentsTab handles GET /agent_versions/{version_id}/configure/agents.
func (h *ConfiguratorHandler) AgentsTab(w http.ResponseWriter, r *http.Request) {
	versionID, err := agentsVersionID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := h.agentsTabData(r.Context(), versionID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Error("configurator agents tab", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	renderTemplate(w, h.agentsTmpl, "layout", data)
}

// agentsWrite runs the shared write preamble: version id check, form parse,
// repo presence. Returns false after rendering an error.
func (h *ConfiguratorHandler) agentsWritePreamble(w http.ResponseWriter, r *http.Request) (string, bool) {
	versionID, err := agentsVersionID(r)
	if err != nil {
		h.renderAgentsError(w, err)
		return "", false
	}
	if err := r.ParseForm(); err != nil {
		h.renderAgentsError(w, errAgentsBadForm("invalid form"))
		return "", false
	}
	if h.subAgents == nil {
		h.renderAgentsError(w, errors.New("sub-agent repository not configured"))
		return "", false
	}
	return versionID, true
}

// ToggleAgentTool handles POST …/architecture/agents/toggle (form
// enabled=on|off). Any "on" value enables: the checkbox form posts a hidden
// "off" ahead of the box's "on".
func (h *ConfiguratorHandler) ToggleAgentTool(w http.ResponseWriter, r *http.Request) {
	versionID, ok := h.agentsWritePreamble(w, r)
	if !ok {
		return
	}
	vals := r.PostForm["enabled"]
	if len(vals) == 0 {
		h.renderAgentsError(w, errAgentsBadForm("enabled must be on or off"))
		return
	}
	enabled := false
	for _, v := range vals {
		switch v {
		case "on":
			enabled = true
		case "off":
		default:
			h.renderAgentsError(w, errAgentsBadForm("enabled must be on or off"))
			return
		}
	}
	if err := h.subAgents.SetAgentToolEnabled(r.Context(), versionID, enabled); err != nil {
		h.renderRefusedToggle(w, r.Context(), versionID, err)
		return
	}
	h.renderAgentsAfterWrite(w, r.Context(), versionID, "agents_toggle", false)
}

// renderRefusedToggle answers a refused toggle with the error fragment plus
// an out-of-band #agents-toggle carrying the persisted state, so the checkbox
// the user just flipped snaps back. Falls back to the plain error when the
// reload or render fails.
func (h *ConfiguratorHandler) renderRefusedToggle(w http.ResponseWriter, ctx context.Context, versionID string, cause error) {
	data, err := h.agentsTabData(ctx, versionID)
	if err != nil {
		h.renderAgentsError(w, cause)
		return
	}
	data.OOB = true
	var buf bytes.Buffer
	if terr := h.agentsTmpl.ExecuteTemplate(&buf, "agents_toggle", data); terr != nil {
		slog.Error("template execution failed", slog.String("template", "agents_toggle"), slog.Any("error", terr))
		h.renderAgentsError(w, cause)
		return
	}
	h.renderAgentsErrorWith(w, cause, buf.Bytes())
}

// AddSubAgentBinding handles POST …/architecture/agents (form name,
// target_version_id, note) and answers with the bindings table.
func (h *ConfiguratorHandler) AddSubAgentBinding(w http.ResponseWriter, r *http.Request) {
	versionID, ok := h.agentsWritePreamble(w, r)
	if !ok {
		return
	}
	target := r.PostFormValue("target_version_id")
	if !validUUID(target) {
		h.renderAgentsError(w, errAgentsBadForm("choose a target version"))
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if err := h.subAgents.AddBinding(r.Context(), versionID, name, target, r.PostFormValue("note")); err != nil {
		h.renderAgentsError(w, err)
		return
	}
	h.renderAgentsAfterWrite(w, r.Context(), versionID, "agents_table", false)
}

// UpdateSubAgentNotes handles POST …/architecture/agents/notes (form
// note[<name>] per binding) and answers with the whole tab, Saved=true.
// Names that are not bound are ignored by the repository.
func (h *ConfiguratorHandler) UpdateSubAgentNotes(w http.ResponseWriter, r *http.Request) {
	versionID, ok := h.agentsWritePreamble(w, r)
	if !ok {
		return
	}
	notes := map[string]string{}
	const prefix = "note["
	for key, vals := range r.PostForm {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, "]") || len(vals) == 0 {
			continue
		}
		if name := key[len(prefix) : len(key)-1]; name != "" {
			notes[name] = vals[0]
		}
	}
	if err := h.subAgents.UpdateNotes(r.Context(), versionID, notes); err != nil {
		h.renderAgentsError(w, err)
		return
	}
	h.renderAgentsAfterWrite(w, r.Context(), versionID, "tab_content", true)
}

// DeleteSubAgentBinding handles POST …/architecture/agents/{name}/delete and
// answers with the bindings table; 404 when name is not bound.
func (h *ConfiguratorHandler) DeleteSubAgentBinding(w http.ResponseWriter, r *http.Request) {
	versionID, ok := h.agentsWritePreamble(w, r)
	if !ok {
		return
	}
	if err := h.subAgents.DeleteBinding(r.Context(), versionID, r.PathValue("name")); err != nil {
		h.renderAgentsError(w, err)
		return
	}
	h.renderAgentsAfterWrite(w, r.Context(), versionID, "agents_table", false)
}

// renderAgentsAfterWrite reloads the tab state and renders the named
// fragment. Rendering after the commit reflects the authoritative DB state.
func (h *ConfiguratorHandler) renderAgentsAfterWrite(w http.ResponseWriter, ctx context.Context, versionID, fragment string, saved bool) {
	data, err := h.agentsTabData(ctx, versionID)
	if err != nil {
		h.renderAgentsError(w, err)
		return
	}
	data.Saved = saved
	h.renderAgentsFragment(w, fragment, data)
}
