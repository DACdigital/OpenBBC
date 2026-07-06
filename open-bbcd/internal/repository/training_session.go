package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
	"github.com/lib/pq"
)

type TrainingSessionRepository struct {
	db *sql.DB
}

func NewTrainingSessionRepository(db *sql.DB) *TrainingSessionRepository {
	return &TrainingSessionRepository{db: db}
}

// Create inserts a PENDING training session. Returns ErrTrainingSessionConflict
// if an active (PENDING/IN_PROGRESS) session already exists for the eval — the
// partial unique index does the enforcement.
func (r *TrainingSessionRepository) Create(ctx context.Context, sourceEvalID, parentVersionID string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO training_sessions (source_eval_id, parent_version_id)
		VALUES ($1::uuid, $2::uuid)
		RETURNING id::text
	`, sourceEvalID, parentVersionID).Scan(&id)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return "", types.ErrTrainingSessionConflict
		}
		return "", err
	}
	return id, nil
}

// GetByID loads one session.
func (r *TrainingSessionRepository) GetByID(ctx context.Context, id string) (*types.TrainingSession, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id::text, source_eval_id::text, parent_version_id::text,
		       (new_version_id)::text, status,
		       requested_at, started_at, completed_at,
		       error_message, epochs, patience,
		       initial_score, final_score, total_epochs_run, stopped_reason,
		       training_report,
		       created_at, updated_at
		FROM training_sessions WHERE id = $1::uuid
	`, id)
	return scanTrainingSession(row)
}

// GetActiveByEval returns the PENDING or IN_PROGRESS session for an eval, or
// nil if none. The partial unique index guarantees at most one row satisfies.
func (r *TrainingSessionRepository) GetActiveByEval(ctx context.Context, evalID string) (*types.TrainingSession, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id::text, source_eval_id::text, parent_version_id::text,
		       (new_version_id)::text, status,
		       requested_at, started_at, completed_at,
		       error_message, epochs, patience,
		       initial_score, final_score, total_epochs_run, stopped_reason,
		       training_report,
		       created_at, updated_at
		FROM training_sessions
		WHERE source_eval_id = $1::uuid AND status IN ('PENDING','IN_PROGRESS')
		LIMIT 1
	`, evalID)
	s, err := scanTrainingSession(row)
	if errors.Is(err, types.ErrNotFound) {
		return nil, nil
	}
	return s, err
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

// scanTrainingSession consumes the exact column order emitted by the SELECTs
// above. Any drift between the SELECT column list and this function is a bug.
func scanTrainingSession(row rowScanner) (*types.TrainingSession, error) {
	var s types.TrainingSession
	var newVersionID sql.NullString
	var startedAt, completedAt sql.NullTime
	var epochs, patience, totalEpochsRun sql.NullInt64
	var initialScore, finalScore sql.NullFloat64
	var stoppedReason sql.NullString
	var trainingReport []byte

	err := row.Scan(
		&s.ID, &s.SourceEvalID, &s.ParentVersionID, &newVersionID, &s.Status,
		&s.RequestedAt, &startedAt, &completedAt,
		&s.ErrorMessage, &epochs, &patience,
		&initialScore, &finalScore, &totalEpochsRun, &stoppedReason,
		&trainingReport,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, types.ErrNotFound
		}
		return nil, err
	}
	if newVersionID.Valid {
		s.NewVersionID = &newVersionID.String
	}
	if startedAt.Valid {
		s.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		s.CompletedAt = &completedAt.Time
	}
	if epochs.Valid {
		v := int(epochs.Int64)
		s.Epochs = &v
	}
	if patience.Valid {
		v := int(patience.Int64)
		s.Patience = &v
	}
	if initialScore.Valid {
		v := initialScore.Float64
		s.InitialScore = &v
	}
	if finalScore.Valid {
		v := finalScore.Float64
		s.FinalScore = &v
	}
	if totalEpochsRun.Valid {
		v := int(totalEpochsRun.Int64)
		s.TotalEpochsRun = &v
	}
	if stoppedReason.Valid {
		s.StoppedReason = &stoppedReason.String
	}
	if len(trainingReport) > 0 {
		s.TrainingReport = json.RawMessage(trainingReport)
	}
	return &s, nil
}
