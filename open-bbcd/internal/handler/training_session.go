package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/DACdigital/OpenBBC/open-bbcd/web"
)

// TrainingSessionStore is the narrow interface TrainingSessionHandler uses.
// Adapter in training_session_store_adapter.go forwards to concrete repos.
type TrainingSessionStore interface {
	Create(ctx context.Context, sourceEvalID, parentVersionID string) (string, error)
	GetByID(ctx context.Context, id string) (*types.TrainingSession, error)
	GetActiveByEval(ctx context.Context, evalID string) (*types.TrainingSession, error)
	List(ctx context.Context, limit, offset int) ([]*types.TrainingSession, error)
	EnrichRows(ctx context.Context, sessions []*types.TrainingSession) ([]repository.TrainingSessionRowView, error)
	Start(ctx context.Context, id string, epochs, patience int) error
	Complete(ctx context.Context, id string, promptsJSON []byte, trainingReport json.RawMessage, summary types.CompleteSummary) (string, error)
	Fail(ctx context.Context, id, errorMessage string) error
	// EvalForTraining returns the eval that would source a new session. Nil
	// (no error) means the eval doesn't exist; error means DB issue.
	EvalForTraining(ctx context.Context, evalID string) (*types.Eval, error)
}

type TrainingSessionHandler struct {
	store      TrainingSessionStore
	listTmpl   *template.Template
	detailTmpl *template.Template
}

func NewTrainingSessionHandler(store TrainingSessionStore) (*TrainingSessionHandler, error) {
	funcs := template.FuncMap{
		"statusClass": statusClass,
		"pct": func(f float64) string {
			return fmt.Sprintf("%.1f%%", f*100)
		},
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
	listTmpl, err := template.New("layout").Funcs(funcs).ParseFS(
		web.Assets,
		"templates/layout.html",
		"templates/training/list.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse training list template: %w", err)
	}
	detailTmpl, err := template.New("layout").Funcs(funcs).ParseFS(
		web.Assets,
		"templates/layout.html",
		"templates/training/detail.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse training detail template: %w", err)
	}
	return &TrainingSessionHandler{
		store:      store,
		listTmpl:   listTmpl,
		detailTmpl: detailTmpl,
	}, nil
}

func (h *TrainingSessionHandler) Create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	sourceEvalID := r.FormValue("source_eval_id")
	if sourceEvalID == "" {
		http.Error(w, "source_eval_id required", http.StatusBadRequest)
		return
	}
	e, err := h.store.EvalForTraining(r.Context(), sourceEvalID)
	if err != nil {
		Error(w, err)
		return
	}
	if e == nil {
		http.NotFound(w, r)
		return
	}
	if e.Status != types.EvalStatusDone {
		http.Error(w, "eval must be DONE to train from (status: "+string(e.Status)+")", http.StatusBadRequest)
		return
	}
	if e.Score != nil && *e.Score >= 1.0 {
		http.Error(w, "eval has a perfect score — no training needed", http.StatusBadRequest)
		return
	}
	active, err := h.store.GetActiveByEval(r.Context(), sourceEvalID)
	if err != nil {
		Error(w, err)
		return
	}
	if active != nil {
		http.Error(w, "an active training session already exists for this eval: "+active.ID, http.StatusConflict)
		return
	}
	id, err := h.store.Create(r.Context(), sourceEvalID, e.AgentVersionID)
	if err != nil {
		Error(w, err)
		return
	}
	http.Redirect(w, r, "/training-sessions/"+id, http.StatusSeeOther)
}

type startBody struct {
	Epochs   int `json:"epochs"`
	Patience int `json:"patience"`
}

func (h *TrainingSessionHandler) Start(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session_id")
	var body startBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Epochs < 1 || body.Patience < 1 {
		http.Error(w, "epochs and patience must be >= 1", http.StatusBadRequest)
		return
	}
	if err := h.store.Start(r.Context(), id, body.Epochs, body.Patience); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TrainingSessionHandler) Complete(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

type failBody struct {
	ErrorMessage string `json:"error_message"`
}

func (h *TrainingSessionHandler) Fail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session_id")
	var body failBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.Fail(r.Context(), id, body.ErrorMessage); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TrainingSessionHandler) JSONFetch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session_id")
	s, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}

func (h *TrainingSessionHandler) ReportJSON(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session_id")
	s, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if len(s.TrainingReport) == 0 {
		_, _ = w.Write([]byte("{}"))
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, s.TrainingReport, "", "  "); err != nil {
		_, _ = w.Write(s.TrainingReport)
		return
	}
	_, _ = w.Write(pretty.Bytes())
}

func (h *TrainingSessionHandler) UIList(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (h *TrainingSessionHandler) UIDetail(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
