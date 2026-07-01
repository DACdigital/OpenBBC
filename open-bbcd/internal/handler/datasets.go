package handler

import (
	"errors"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// DatasetsHandler serves /datasets — list, detail, create, close.
type DatasetsHandler struct {
	repo                *repository.DatasetRepository
	listTmpl            *template.Template
	newModalTmpl        *template.Template
	detailTmpl          *template.Template
	closeConfirmTmpl    *template.Template
	removeSessionTmpl   *template.Template
}

func NewDatasetsHandler(repo *repository.DatasetRepository, webFS fs.FS) (*DatasetsHandler, error) {
	funcs := template.FuncMap{
		"statusClass": statusClass,
		"dict":        tplDict,
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
	}
	listTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/datasets/list.html",
	)
	if err != nil {
		return nil, err
	}
	newModalTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/datasets/new_modal.html",
	)
	if err != nil {
		return nil, err
	}
	detailTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/layout.html",
		"templates/datasets/detail.html",
	)
	if err != nil {
		return nil, err
	}
	closeConfirmTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/datasets/close_confirm_modal.html",
	)
	if err != nil {
		return nil, err
	}
	removeSessionTmpl, err := template.New("").Funcs(funcs).ParseFS(webFS,
		"templates/datasets/remove_session_modal.html",
	)
	if err != nil {
		return nil, err
	}
	return &DatasetsHandler{
		repo:              repo,
		listTmpl:          listTmpl,
		newModalTmpl:      newModalTmpl,
		detailTmpl:        detailTmpl,
		closeConfirmTmpl:  closeConfirmTmpl,
		removeSessionTmpl: removeSessionTmpl,
	}, nil
}

// datasetListRow is the rendering shape for the /datasets table.
type datasetListRow struct {
	Dataset      *types.Dataset
	HasDraft     bool
	LatestNum    int
	LatestStatus types.DatasetVersionStatus
	SessionCount int
}

// List renders GET /datasets — the datasets table.
func (h *DatasetsHandler) List(w http.ResponseWriter, r *http.Request) {
	datasets, err := h.repo.List(r.Context())
	if err != nil {
		Error(w, err)
		return
	}
	rows := make([]datasetListRow, 0, len(datasets))
	for _, d := range datasets {
		versions, _ := h.repo.ListVersions(r.Context(), d.ID)
		if len(versions) == 0 {
			rows = append(rows, datasetListRow{Dataset: d})
			continue
		}
		latest := versions[0]
		refs, _ := h.repo.GetVersionSessions(r.Context(), latest.ID)
		hasDraft := false
		for _, v := range versions {
			if v.Status == types.DatasetVersionDraft {
				hasDraft = true
				break
			}
		}
		rows = append(rows, datasetListRow{
			Dataset:      d,
			HasDraft:     hasDraft,
			LatestNum:    latest.VersionNum,
			LatestStatus: latest.Status,
			SessionCount: len(refs),
		})
	}
	renderTemplate(w, h.listTmpl, "layout", map[string]any{
		"Active": "datasets",
		"Rows":   rows,
	})
}

// New renders GET /datasets/new — the create-dataset modal fragment.
func (h *DatasetsHandler) New(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, h.newModalTmpl, "dataset_new_modal", nil)
}

// Create handles POST /datasets — persist a new dataset, redirect to detail.
func (h *DatasetsHandler) Create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	name := r.FormValue("name")
	description := r.FormValue("description")
	d, err := h.repo.Create(r.Context(), name, description)
	if err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/datasets/"+d.ID, http.StatusSeeOther)
}

// Detail renders GET /datasets/{dataset_id}?version_id=... — the versions
// strip and selected version's session list.
func (h *DatasetsHandler) Detail(w http.ResponseWriter, r *http.Request) {
	datasetID := r.PathValue("dataset_id")
	dataset, err := h.repo.GetByID(r.Context(), datasetID)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		Error(w, err)
		return
	}
	versions, err := h.repo.ListVersions(r.Context(), datasetID)
	if err != nil {
		Error(w, err)
		return
	}
	var draft *types.DatasetVersion
	for _, v := range versions {
		if v.Status == types.DatasetVersionDraft {
			draft = v
			break
		}
	}
	var selected *types.DatasetVersion
	if q := r.URL.Query().Get("version_id"); q != "" {
		for _, v := range versions {
			if v.ID == q {
				selected = v
				break
			}
		}
	}
	if selected == nil {
		if draft != nil {
			selected = draft
		} else if len(versions) > 0 {
			selected = versions[0]
		}
	}
	var sessions []*types.DatasetSessionRef
	if selected != nil {
		sessions, err = h.repo.GetVersionSessions(r.Context(), selected.ID)
		if err != nil {
			Error(w, err)
			return
		}
	}
	renderTemplate(w, h.detailTmpl, "layout", map[string]any{
		"Active":          "datasets",
		"Dataset":         dataset,
		"Versions":        versions,
		"SelectedVersion": selected,
		"DraftVersion":    draft,
		"Sessions":        sessions,
	})
}

// CloseConfirm renders GET /datasets/{dataset_id}/close-draft/confirm — modal fragment.
func (h *DatasetsHandler) CloseConfirm(w http.ResponseWriter, r *http.Request) {
	datasetID := r.PathValue("dataset_id")
	dataset, err := h.repo.GetByID(r.Context(), datasetID)
	if err != nil {
		Error(w, err)
		return
	}
	versions, err := h.repo.ListVersions(r.Context(), datasetID)
	if err != nil {
		Error(w, err)
		return
	}
	var draft *types.DatasetVersion
	for _, v := range versions {
		if v.Status == types.DatasetVersionDraft {
			draft = v
			break
		}
	}
	if draft == nil {
		http.Error(w, "no draft to close", http.StatusConflict)
		return
	}
	sessions, _ := h.repo.GetVersionSessions(r.Context(), draft.ID)
	renderTemplate(w, h.closeConfirmTmpl, "dataset_close_confirm_modal", map[string]any{
		"Dataset":      dataset,
		"Version":      draft,
		"SessionCount": len(sessions),
	})
}

// CloseDraft handles POST /datasets/{dataset_id}/close-draft
func (h *DatasetsHandler) CloseDraft(w http.ResponseWriter, r *http.Request) {
	datasetID := r.PathValue("dataset_id")
	if err := r.ParseForm(); err != nil {
		Error(w, err)
		return
	}
	note := r.FormValue("close_note")
	versions, err := h.repo.ListVersions(r.Context(), datasetID)
	if err != nil {
		Error(w, err)
		return
	}
	var draftID string
	for _, v := range versions {
		if v.Status == types.DatasetVersionDraft {
			draftID = v.ID
			break
		}
	}
	if draftID == "" {
		Error(w, types.ErrDatasetVersionClosed)
		return
	}
	if err := h.repo.CloseDraft(r.Context(), draftID, note); err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/datasets/"+datasetID, http.StatusSeeOther)
}

// RemoveSessionConfirm renders GET /datasets/{dataset_id}/sessions/{session_id}/remove-confirm
// The modal's confirm button posts DELETE to the existing chat-scoped
// unassign endpoint, then reloads on success.
func (h *DatasetsHandler) RemoveSessionConfirm(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	versionID := r.URL.Query().Get("version_id")
	title := r.URL.Query().Get("title")
	renderTemplate(w, h.removeSessionTmpl, "dataset_remove_session_modal", map[string]any{
		"SessionID":      sessionID,
		"AgentVersionID": versionID,
		"SessionTitle":   title,
	})
}
