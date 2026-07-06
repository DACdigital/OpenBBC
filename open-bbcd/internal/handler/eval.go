package handler

import (
	"context"
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

// EvalDetailStore is the narrow interface EvalHandler uses for the detail
// page to look up the active training session for an eval.
type EvalDetailStore interface {
	// GetActiveTrainingSessionForEval returns the PENDING or IN_PROGRESS
	// training session for this eval, or nil if none. Used to decide whether
	// the Train button should show or a "Training pending" link instead.
	GetActiveTrainingSessionForEval(ctx context.Context, evalID string) (*types.TrainingSession, error)
}

// EvalHandler owns /evals and /agent_versions/{id}/evals routes.
type EvalHandler struct {
	repo          *repository.EvalRepository
	dataset       *repository.DatasetRepository
	adapter       eval.Store
	trainingStore EvalDetailStore
	listTmpl      *template.Template
	detailTmpl    *template.Template
	modalTmpl     *template.Template
	perAVTmpl     *template.Template
}

func NewEvalHandler(
	repo *repository.EvalRepository,
	dataset *repository.DatasetRepository,
	adapter eval.Store,
	trainingStore EvalDetailStore,
	webFS fs.FS,
) (*EvalHandler, error) {
	funcs := template.FuncMap{
		"statusClass": statusClass,
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
		"pct":         func(f float64) string { return formatPercent(f) },
		"deref": func(v any) any {
			switch p := v.(type) {
			case *string:
				if p == nil {
					return ""
				}
				return *p
			case *int:
				if p == nil {
					return 0
				}
				return *p
			case *float64:
				if p == nil {
					return 0.0
				}
				return *p
			}
			return v
		},
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
		repo: repo, dataset: dataset, adapter: adapter, trainingStore: trainingStore,
		listTmpl: listTmpl, detailTmpl: detailTmpl, modalTmpl: modalTmpl, perAVTmpl: perAVTmpl,
	}, nil
}

// Create handles POST /agent_versions/{version_id}/evals.
// Accepts JSON `{"dataset_version_id":"..."}` or a form field of the same
// name. JSON callers get the eval row back; form callers 303 to detail.
func (h *EvalHandler) Create(w http.ResponseWriter, r *http.Request) {
	versionID := r.PathValue("version_id")
	// Body fields (JSON or form).
	var (
		datasetVersionID string
		mockMCP          = true // default checked
		headerOverrides  = map[string]string{}
	)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			DatasetVersionID string            `json:"dataset_version_id"`
			MockMCPTools     *bool             `json:"mock_mcp_tools,omitempty"`
			HeaderOverrides  map[string]string `json:"header_overrides,omitempty"`
		}
		if err := DecodeJSON(r, &body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		datasetVersionID = body.DatasetVersionID
		if body.MockMCPTools != nil {
			mockMCP = *body.MockMCPTools
		}
		if body.HeaderOverrides != nil {
			headerOverrides = body.HeaderOverrides
		}
	} else {
		if err := r.ParseForm(); err != nil {
			Error(w, err)
			return
		}
		datasetVersionID = r.FormValue("dataset_version_id")
		mockMCP = r.FormValue("mock_mcp_tools") != "" // HTML checkbox: absent when unchecked
		if raw := r.FormValue("header_overrides_json"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &headerOverrides); err != nil {
				http.Error(w, "invalid header_overrides_json", http.StatusBadRequest)
				return
			}
		}
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
	e, err := h.repo.Create(r.Context(), versionID, datasetVersionID, mockMCP, headerOverrides)
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

// evalSessionView wraps EvalSession with pre-parsed structured views of
// the JSONB transcript + judgments so templates render as tables instead
// of raw JSON dumps.
type evalSessionView struct {
	*types.EvalSession
	Judgments []judgmentView
	Turns     []turnView
}

type judgmentView struct {
	MessageID string
	Criterion string
	Passed    bool
	Reason    string
}

type turnView struct {
	Role      string
	Text      string
	ToolCalls []turnToolCallView
}

type turnToolCallView struct {
	Name       string
	ArgsJSON   string
	ResultJSON string
	Source     string
}

// UIDetail handles GET /evals/{eval_id}.
func (h *EvalHandler) UIDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("eval_id")
	e, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	enriched, err := h.repo.EnrichRows(r.Context(), []*types.Eval{e})
	if err != nil {
		Error(w, err)
		return
	}
	var agentName, datasetName string
	var agentVersionNum, datasetVersionNum int
	if len(enriched) > 0 {
		agentName = enriched[0].AgentName
		agentVersionNum = enriched[0].AgentVersionNum
		datasetName = enriched[0].DatasetName
		datasetVersionNum = enriched[0].DatasetVersionNum
	}
	rows, err := h.repo.ListSessions(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	sessions := make([]evalSessionView, 0, len(rows))
	for _, s := range rows {
		sessions = append(sessions, evalSessionView{
			EvalSession: s,
			Judgments:   parseJudgments(s.Judgments),
			Turns:       parseTranscript(s.Transcript),
		})
	}
	activeTraining, err := h.trainingStore.GetActiveTrainingSessionForEval(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	renderTemplate(w, h.detailTmpl, "layout", map[string]any{
		"Active":                "evals",
		"Eval":                  e,
		"AgentName":             agentName,
		"AgentVersionNum":       agentVersionNum,
		"DatasetName":           datasetName,
		"DatasetVersionNum":     datasetVersionNum,
		"Sessions":              sessions,
		"ActiveTrainingSession": activeTraining,
	})
}

// parseJudgments decodes the JSONB judgments column into a display slice.
// Tolerates malformed rows — returns nil rather than erroring the page.
func parseJudgments(raw []byte) []judgmentView {
	if len(raw) == 0 {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]judgmentView, 0, len(arr))
	for _, m := range arr {
		out = append(out, judgmentView{
			MessageID: asString(m["message_id"]),
			Criterion: asString(m["criterion"]),
			Passed:    asBool(m["passed"]),
			Reason:    asString(m["reason"]),
		})
	}
	return out
}

// parseTranscript decodes the JSONB transcript column into a display slice.
// Each turn extracts a best-effort text summary (Anthropic content-block
// arrays get concatenated to their text-typed blocks) and lifts tool_calls
// out for compact rendering.
func parseTranscript(raw []byte) []turnView {
	if len(raw) == 0 {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]turnView, 0, len(arr))
	for _, m := range arr {
		v := turnView{
			Role: asString(m["role"]),
			Text: extractText(m["content"]),
		}
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, c := range calls {
				cm, _ := c.(map[string]any)
				if cm == nil {
					continue
				}
				args, _ := json.Marshal(cm["args"])
				result, _ := json.Marshal(cm["result"])
				v.ToolCalls = append(v.ToolCalls, turnToolCallView{
					Name:       asString(cm["name"]),
					ArgsJSON:   string(args),
					ResultJSON: string(result),
					Source:     asString(cm["source"]),
				})
			}
		}
		out = append(out, v)
	}
	return out
}

// extractText coerces a content field into human-readable text. Handles:
// plain string, Anthropic content-block array ({type:text,text:...}), or
// anything else via best-effort JSON dump.
func extractText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, item := range t {
			blk, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if asString(blk["type"]) == "text" {
				parts = append(parts, asString(blk["text"]))
			}
		}
		return strings.Join(parts, "\n")
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
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
	// Set of dataset_version_id → true, for the current agent version,
	// where an eval is already PENDING or IN_PROGRESS.
	inflight := map[string]bool{}
	existing, err := h.repo.ListByAgentVersion(r.Context(), versionID)
	if err == nil {
		for _, e := range existing {
			if e.Status == types.EvalStatusPending || e.Status == types.EvalStatusInProgress {
				inflight[e.DatasetVersionID] = true
			}
		}
	}
	renderTemplate(w, h.modalTmpl, "eval_new_modal", map[string]any{
		"VersionID": versionID,
		"Rows":      rows,
		"Inflight":  inflight,
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
