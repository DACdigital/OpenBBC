package handler

import (
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// BackendsHandler serves the /mcp CRUD pages for tool backends.
type BackendsHandler struct {
	repo     *repository.ToolBackendRepository
	wiring   *repository.VersionWiringRepository
	listTmpl *template.Template
	formTmpl *template.Template
	editTmpl *template.Template
}

// NewBackendsHandler parses templates once at construction time (matching UIHandler pattern).
func NewBackendsHandler(repo *repository.ToolBackendRepository, wiring *repository.VersionWiringRepository, webFS fs.FS) (*BackendsHandler, error) {
	funcs := template.FuncMap{
		"statusClass": statusClass,
	}
	listTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/backends/list.html",
	)
	if err != nil {
		return nil, err
	}
	formTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/backends/form_http.html",
	)
	if err != nil {
		return nil, err
	}
	editTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/backends/edit.html",
	)
	if err != nil {
		return nil, err
	}
	return &BackendsHandler{repo, wiring, listTmpl, formTmpl, editTmpl}, nil
}

// listRow is the rendering shape: backend + computed UsageCount + PrimaryURL.
type listRow struct {
	*types.ToolBackend
	UsageCount int
	PrimaryURL string
}

func primaryURL(b *types.ToolBackend) string {
	switch b.Kind {
	case types.ToolBackendKindHTTPEndpoint:
		var c types.HTTPBackendConfig
		_ = json.Unmarshal(b.Config, &c)
		return c.BaseURL
	case types.ToolBackendKindMCPClient:
		var c types.MCPBackendConfig
		_ = json.Unmarshal(b.Config, &c)
		return c.URL
	}
	return ""
}

// List renders GET /mcp — the backends table.
func (h *BackendsHandler) List(w http.ResponseWriter, r *http.Request) {
	all, err := h.repo.List(r.Context())
	if err != nil {
		Error(w, err)
		return
	}
	counts, err := h.wiring.UsageCounts(r.Context())
	if err != nil {
		Error(w, err)
		return
	}
	rows := make([]listRow, 0, len(all))
	for _, b := range all {
		rows = append(rows, listRow{b, counts[b.ID], primaryURL(b)})
	}
	renderTemplate(w, h.listTmpl, "layout", map[string]any{
		"Active": "mcp",
		"Rows":   rows,
	})
}

// New renders GET /mcp/new — the create form.
// MCP backend form is added in Task 14; only http_endpoint is supported now.
func (h *BackendsHandler) New(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "http_endpoint"
	}
	if kind != "http_endpoint" {
		http.Error(w, "MCP backend form not yet available (Task 14)", http.StatusNotImplemented)
		return
	}
	renderTemplate(w, h.formTmpl, "layout", map[string]any{
		"Active": "mcp",
		"Kind":   kind,
	})
}

// Create handles POST /mcp — persist a new backend and redirect.
func (h *BackendsHandler) Create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		Error(w, types.ErrToolBackendNameRequired)
		return
	}
	kind := types.ToolBackendKind(r.FormValue("kind"))

	var cfgJSON json.RawMessage
	switch kind {
	case types.ToolBackendKindHTTPEndpoint:
		cfg := types.HTTPBackendConfig{
			BaseURL:          r.FormValue("base_url"),
			DefaultHeaders:   parseHeaderRows(r, "default_headers"),
			ForwardedHeaders: r.Form["forwarded_headers"],
		}
		cfgJSON, _ = json.Marshal(cfg)
	case types.ToolBackendKindMCPClient:
		// Added in Task 14.
		http.Error(w, "MCP backend not yet supported in form (Task 14)", http.StatusNotImplemented)
		return
	default:
		Error(w, types.ErrToolBackendKindInvalid)
		return
	}

	be := &types.ToolBackend{Name: name, Kind: kind, Config: cfgJSON}
	if err := h.repo.Create(r.Context(), be); err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/mcp", http.StatusSeeOther)
}

// Edit renders GET /mcp/{id} — the edit form pre-populated with current values.
func (h *BackendsHandler) Edit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	be, err := h.repo.Get(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	counts, err := h.wiring.UsageCounts(r.Context())
	if err != nil {
		Error(w, err)
		return
	}
	var cfg types.HTTPBackendConfig
	_ = json.Unmarshal(be.Config, &cfg)

	type hdrOption struct {
		Name    string
		Checked bool
	}
	defaults := []string{"Authorization", "Cookie", "X-User-Id"}
	already := map[string]bool{}
	for _, hdr := range cfg.ForwardedHeaders {
		already[hdr] = true
	}
	opts := []hdrOption{}
	seen := map[string]bool{}
	for _, d := range defaults {
		opts = append(opts, hdrOption{Name: d, Checked: already[d]})
		seen[d] = true
	}
	for _, hdr := range cfg.ForwardedHeaders {
		if !seen[hdr] {
			opts = append(opts, hdrOption{Name: hdr, Checked: true})
			seen[hdr] = true
		}
	}

	renderTemplate(w, h.editTmpl, "layout", map[string]any{
		"Active":                 "mcp",
		"Backend":                be,
		"Cfg":                    cfg,
		"UsageCount":             counts[be.ID],
		"ForwardedHeaderOptions": opts,
	})
}

// Update handles POST /mcp/{id} — save changes and redirect.
func (h *BackendsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	be, err := h.repo.Get(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		Error(w, types.ErrToolBackendNameRequired)
		return
	}

	switch be.Kind {
	case types.ToolBackendKindHTTPEndpoint:
		cfg := types.HTTPBackendConfig{
			BaseURL:          r.FormValue("base_url"),
			DefaultHeaders:   parseHeaderRows(r, "default_headers"),
			ForwardedHeaders: r.Form["forwarded_headers"],
		}
		cfgJSON, _ := json.Marshal(cfg)
		be.Name = name
		be.Config = cfgJSON
	case types.ToolBackendKindMCPClient:
		// Added in Task 14.
		http.Error(w, "MCP backend update not yet supported (Task 14)", http.StatusNotImplemented)
		return
	}
	if err := h.repo.Update(r.Context(), be); err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/mcp", http.StatusSeeOther)
}

// Delete handles POST /mcp/{id}/delete — remove a backend (HTML forms can't DELETE).
func (h *BackendsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		if errors.Is(err, types.ErrToolBackendInUse) {
			Error(w, err) // sentinel maps to 409
			return
		}
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/mcp", http.StatusSeeOther)
}

// parseHeaderRows reads paired "<prefix>_key" / "<prefix>_value" multi-value
// form inputs into a map. Empty keys are skipped.
func parseHeaderRows(r *http.Request, prefix string) map[string]string {
	keys := r.Form[prefix+"_key"]
	vals := r.Form[prefix+"_value"]
	out := map[string]string{}
	for i := range keys {
		if i < len(vals) && keys[i] != "" {
			out[keys[i]] = vals[i]
		}
	}
	return out
}
