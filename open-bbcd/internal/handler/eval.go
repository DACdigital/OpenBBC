package handler

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/eval"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"gopkg.in/yaml.v3"
)

// EvalHandler owns /evals and /agent_versions/{id}/evals routes.
type EvalHandler struct {
	repo       *repository.EvalRepository
	dataset    *repository.DatasetRepository
	adapter    eval.Store
	listTmpl   *template.Template
	detailTmpl *template.Template
	modalTmpl  *template.Template
	perAVTmpl  *template.Template
}

func NewEvalHandler(
	repo *repository.EvalRepository,
	dataset *repository.DatasetRepository,
	adapter eval.Store,
	webFS fs.FS,
) (*EvalHandler, error) {
	funcs := template.FuncMap{
		"statusClass": statusClass,
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
		"pct":         func(f float64) string { return formatPercent(f) },
	}
	listTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/eval/list.html",
	)
	if err != nil {
		return nil, err
	}
	detailTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/eval/detail.html",
	)
	if err != nil {
		return nil, err
	}
	modalTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/eval/new_modal.html",
	)
	if err != nil {
		return nil, err
	}
	perAVTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/eval/list_per_agent_version.html",
	)
	if err != nil {
		return nil, err
	}
	return &EvalHandler{
		repo: repo, dataset: dataset, adapter: adapter,
		listTmpl: listTmpl, detailTmpl: detailTmpl, modalTmpl: modalTmpl, perAVTmpl: perAVTmpl,
	}, nil
}

// Create handles POST /agent_versions/{version_id}/evals.
// Accepts JSON `{"dataset_version_id":"..."}` or a form field of the same
// name. JSON callers get the eval row back; form callers 303 to detail.
func (h *EvalHandler) Create(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	var datasetVersionID string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			DatasetVersionID string `json:"dataset_version_id"`
		}
		if err := DecodeJSON(r, &body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		datasetVersionID = body.DatasetVersionID
	} else {
		if err := r.ParseForm(); err != nil {
			Error(w, err)
			return
		}
		datasetVersionID = r.FormValue("dataset_version_id")
	}
	if datasetVersionID == "" {
		http.Error(w, "dataset_version_id required", http.StatusBadRequest)
		return
	}
	dv, err := h.dataset.GetVersion(r.Context(), datasetVersionID)
	if err != nil {
		Error(w, err)
		return
	}
	if dv.Status != types.DatasetVersionClosed {
		Error(w, types.ErrDatasetVersionNotClosed)
		return
	}
	missing, err := h.dataset.CountMissingCriteria(r.Context(), datasetVersionID)
	if err != nil {
		Error(w, err)
		return
	}
	if missing > 0 {
		Error(w, types.ErrDatasetMissingCriteria)
		return
	}
	e, err := h.repo.Create(r.Context(), versionID, datasetVersionID)
	if err != nil {
		Error(w, err)
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		JSON(w, http.StatusCreated, e)
		return
	}
	http.Redirect(w, r, "/evals/"+e.ID, http.StatusSeeOther)
}

// Start handles POST /evals/{eval_id}/start.
func (h *EvalHandler) Start(w http.ResponseWriter, r *http.Request) {
	if err := h.repo.Start(r.Context(), r.PathValue("eval_id")); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Export handles GET /evals/{eval_id}/export.yaml.
func (h *EvalHandler) Export(w http.ResponseWriter, r *http.Request) {
	payload, err := eval.Build(r.Context(), h.adapter, r.PathValue("eval_id"))
	if err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(payload); err != nil {
		Error(w, err)
		return
	}
	_ = enc.Close()
}

// Result handles POST /evals/{eval_id}/result — accepts eval-result.json.
func (h *EvalHandler) Result(w http.ResponseWriter, r *http.Request) {
	var result types.EvalResult
	if err := DecodeJSON(r, &result); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if result.Status != types.EvalStatusDone && result.Status != types.EvalStatusFailed {
		http.Error(w, "status must be DONE or FAILED", http.StatusBadRequest)
		return
	}
	if err := h.repo.Submit(r.Context(), r.PathValue("eval_id"), &result); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Fail handles POST /evals/{eval_id}/fail — convenience terminal.
func (h *EvalHandler) Fail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ErrorMessage string `json:"error_message"`
	}
	_ = DecodeJSON(r, &body)
	if err := h.repo.Fail(r.Context(), r.PathValue("eval_id"), body.ErrorMessage); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UploadResult handles POST /evals/{eval_id}/upload-result via a multipart file.
func (h *EvalHandler) UploadResult(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, _, err := r.FormFile("result")
	if err != nil {
		http.Error(w, "missing result file", http.StatusBadRequest)
		return
	}
	defer f.Close()
	var result types.EvalResult
	if err := json.NewDecoder(f).Decode(&result); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.repo.Submit(r.Context(), r.PathValue("eval_id"), &result); err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/evals/"+r.PathValue("eval_id"), http.StatusSeeOther)
}

// UIList handles GET /evals — the full list, paginated. Uses the repo's
// EnrichRows for a single-query label join (agent + dataset names).
func (h *EvalHandler) UIList(w http.ResponseWriter, r *http.Request) {
	all, err := h.repo.ListAll(r.Context())
	if err != nil {
		Error(w, err)
		return
	}
	pr := ParsePageRequest(r)
	total := len(all)
	page := slicePageEvals(all, pr.Offset(), pr.Limit())
	rows, err := h.repo.EnrichRows(r.Context(), page)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.listTmpl, "layout", map[string]any{
		"Active":   "evals",
		"Rows":     rows,
		"Page":     NewPageView(pr, total),
		"BasePath": r.URL.Path,
	})
}

// UIListByAgentVersion handles GET /agent_versions/{version_id}/evals.
func (h *EvalHandler) UIListByAgentVersion(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	all, err := h.repo.ListByAgentVersion(r.Context(), versionID)
	if err != nil {
		Error(w, err)
		return
	}
	pr := ParsePageRequest(r)
	total := len(all)
	page := slicePageEvals(all, pr.Offset(), pr.Limit())
	rows, err := h.repo.EnrichRows(r.Context(), page)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.perAVTmpl, "layout", map[string]any{
		"Active":    "agents",
		"Rows":      rows,
		"Page":      NewPageView(pr, total),
		"BasePath":  r.URL.Path,
		"VersionID": versionID,
	})
}

// UIDetail handles GET /evals/{eval_id}.
func (h *EvalHandler) UIDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("eval_id")
	e, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	sessions, err := h.repo.ListSessions(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.detailTmpl, "layout", map[string]any{
		"Active":   "evals",
		"Eval":     e,
		"Sessions": sessions,
	})
}

// UINewModal handles GET /agent_versions/{version_id}/evals/new.
func (h *EvalHandler) UINewModal(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	datasets, err := h.dataset.List(r.Context())
	if err != nil {
		Error(w, err)
		return
	}
	type dsRow struct {
		Dataset  *types.Dataset
		Versions []*types.DatasetVersion
	}
	rows := make([]dsRow, 0, len(datasets))
	for _, d := range datasets {
		versions, _ := h.dataset.ListVersions(r.Context(), d.ID)
		closed := make([]*types.DatasetVersion, 0, len(versions))
		for _, v := range versions {
			if v.Status == types.DatasetVersionClosed {
				closed = append(closed, v)
			}
		}
		if len(closed) > 0 {
			rows = append(rows, dsRow{Dataset: d, Versions: closed})
		}
	}
	renderTemplate(w, h.modalTmpl, "eval_new_modal", map[string]any{
		"VersionID": versionID,
		"Rows":      rows,
	})
}

func slicePageEvals(all []*types.Eval, off, lim int) []*types.Eval {
	if off >= len(all) {
		return nil
	}
	end := off + lim
	if end > len(all) {
		end = len(all)
	}
	return all[off:end]
}

func formatPercent(f float64) string {
	s := fmt.Sprintf("%.1f", f*100)
	if strings.HasSuffix(s, ".0") {
		s = strings.TrimSuffix(s, ".0")
	}
	return s + "%"
}
