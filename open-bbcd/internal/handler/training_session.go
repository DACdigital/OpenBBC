package handler

import (
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

// Stubs — bodies filled in later tasks.

func (h *TrainingSessionHandler) Create(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (h *TrainingSessionHandler) Start(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (h *TrainingSessionHandler) Complete(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (h *TrainingSessionHandler) Fail(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (h *TrainingSessionHandler) JSONFetch(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (h *TrainingSessionHandler) ReportJSON(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (h *TrainingSessionHandler) UIList(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (h *TrainingSessionHandler) UIDetail(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
