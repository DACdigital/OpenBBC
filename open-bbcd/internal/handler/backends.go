package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/llm/tools"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// BackendsHandler serves the /mcp CRUD pages for tool backends.
type BackendsHandler struct {
	repo        *repository.ToolBackendRepository
	wiring      *repository.VersionWiringRepository
	listTmpl    *template.Template
	formTmpl    *template.Template
	formMCPTmpl *template.Template
	editTmpl    *template.Template
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
	formMCPTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/backends/form_mcp.html",
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
	return &BackendsHandler{repo, wiring, listTmpl, formTmpl, formMCPTmpl, editTmpl}, nil
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
func (h *BackendsHandler) New(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "http_endpoint"
	}
	switch kind {
	case "http_endpoint":
		renderTemplate(w, h.formTmpl, "layout", map[string]any{
			"Active": "mcp",
			"Kind":   kind,
		})
	case "mcp_client":
		renderTemplate(w, h.formMCPTmpl, "layout", map[string]any{
			"Active": "mcp",
			"Kind":   kind,
		})
	default:
		http.Error(w, "unknown kind", http.StatusBadRequest)
	}
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
		cfg := types.MCPBackendConfig{
			URL:            r.FormValue("url"),
			Transport:      r.FormValue("transport"),
			DefaultHeaders: parseHeaderRows(r, "default_headers"),
		}
		if cfg.Transport == "" {
			cfg.Transport = "streamable_http"
		}
		cfgJSON, _ = json.Marshal(cfg)
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
	var mcpCfg types.MCPBackendConfig
	if be.Kind == types.ToolBackendKindMCPClient {
		_ = json.Unmarshal(be.Config, &mcpCfg)
	} else {
		_ = json.Unmarshal(be.Config, &cfg)
	}

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
		"MCPCfg":                 mcpCfg,
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
		cfg := types.MCPBackendConfig{
			URL:            r.FormValue("url"),
			Transport:      r.FormValue("transport"),
			DefaultHeaders: parseHeaderRows(r, "default_headers"),
		}
		if cfg.Transport == "" {
			cfg.Transport = "streamable_http"
		}
		cfgJSON, _ := json.Marshal(cfg)
		be.Name = name
		be.Config = cfgJSON
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

// TestConnection builds an ephemeral MCPClientBackend from form fields and
// calls Tools(ctx) with a 5s timeout. Returns an HTML fragment listing the
// discovered tools or the error message.
func (h *BackendsHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderTestResult(w, "", "invalid form: "+err.Error())
		return
	}
	cfg := tools.MCPBackendCfg{
		URL:            r.FormValue("url"),
		Transport:      r.FormValue("transport"),
		DefaultHeaders: parseHeaderRows(r, "default_headers"),
	}
	if cfg.URL == "" {
		renderTestResult(w, "", "URL is required")
		return
	}

	backend := tools.NewMCPClientBackend("test", "test-ephemeral", cfg)
	defer backend.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	defs, err := backend.Tools(ctx)
	if err != nil {
		renderTestResult(w, "", err.Error())
		return
	}

	renderTestResult(w, "Connection OK — "+strconv.Itoa(len(defs))+" tools discovered.", "")
}

// renderTestResult emits an HTML fragment with either a success message or an
// error. Inline minimal markup; we don't need a full template for this.
func renderTestResult(w http.ResponseWriter, ok, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errMsg != "" {
		fmt.Fprintf(w, `<div class="config-banner config-banner-error">Test failed: %s</div>`, html.EscapeString(errMsg))
		return
	}
	fmt.Fprintf(w, `<div class="config-banner config-banner-success">%s</div>`, html.EscapeString(ok))
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
