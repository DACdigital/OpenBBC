package handler

import (
	"context"
	"encoding/json"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// trainingSessionStore implements TrainingSessionStore by forwarding to the
// concrete repositories. Keeps the handler free of repo types.
type trainingSessionStore struct {
	sessions *repository.TrainingSessionRepository
	versions *repository.AgentVersionRepository
	evals    *repository.EvalRepository
}

var _ TrainingSessionStore = (*trainingSessionStore)(nil)

func (s *trainingSessionStore) Create(ctx context.Context, sourceEvalID, parentVersionID string) (string, error) {
	return s.sessions.Create(ctx, sourceEvalID, parentVersionID)
}

func (s *trainingSessionStore) GetByID(ctx context.Context, id string) (*types.TrainingSession, error) {
	return s.sessions.GetByID(ctx, id)
}

func (s *trainingSessionStore) GetActiveByEval(ctx context.Context, evalID string) (*types.TrainingSession, error) {
	return s.sessions.GetActiveByEval(ctx, evalID)
}

func (s *trainingSessionStore) List(ctx context.Context, status string, limit int) ([]*types.TrainingSession, error) {
	return s.sessions.List(ctx, status, limit)
}

func (s *trainingSessionStore) EnrichRows(ctx context.Context, sessions []*types.TrainingSession) ([]repository.TrainingSessionRowView, error) {
	return s.sessions.EnrichRows(ctx, sessions)
}

func (s *trainingSessionStore) Start(ctx context.Context, id string, epochs, patience int) error {
	return s.sessions.Start(ctx, id, epochs, patience)
}

func (s *trainingSessionStore) Complete(ctx context.Context, id string, promptsJSON []byte, trainingReport json.RawMessage, summary types.CompleteSummary) (string, error) {
	return s.sessions.Complete(ctx, s.versions, id, promptsJSON, trainingReport, summary)
}

func (s *trainingSessionStore) Fail(ctx context.Context, id, errorMessage string) error {
	return s.sessions.Fail(ctx, id, errorMessage)
}

func (s *trainingSessionStore) EvalForTraining(ctx context.Context, evalID string) (*types.Eval, error) {
	return s.evals.GetByID(ctx, evalID)
}
