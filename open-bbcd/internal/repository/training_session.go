package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
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
		if isUniqueViolation(err) {
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
	// The partial unique index idx_ts_one_active_per_eval guarantees at most one
	// PENDING/IN_PROGRESS row per source_eval_id; LIMIT 1 is an extra guard.
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

// CompleteSummary carries the scalars we extract from the training-report
// JSON so the Complete tx can UPDATE them into typed columns as well as store
// the full report blob.
type CompleteSummary struct {
	InitialScore   float64
	FinalScore     float64
	TotalEpochsRun int
	StoppedReason  string
}

// Start marks a PENDING session IN_PROGRESS, stamps started_at, and records
// the epochs/patience config the script is about to use.
func (r *TrainingSessionRepository) Start(ctx context.Context, id string, epochs, patience int) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE training_sessions
		SET status = 'IN_PROGRESS',
		    started_at = now(),
		    epochs = $2,
		    patience = $3,
		    updated_at = now()
		WHERE id = $1::uuid AND status = 'PENDING'
	`, id, epochs, patience)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Distinguish 404 (row missing) from 409 (wrong status) so callers
		// can map to correct HTTP codes.
		var status string
		if err := r.db.QueryRowContext(ctx, `SELECT status FROM training_sessions WHERE id = $1::uuid`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return types.ErrNotFound
			}
			return err
		}
		return types.ErrTrainingSessionConflict
	}
	return nil
}

// Complete forks a READY agent_version and marks the session DONE in one tx.
// Returns the new version id. The `versionRepo` parameter provides access to
// the tx-scoped insert helper — both operations share the same *sql.Tx so
// either everything lands or nothing does.
func (r *TrainingSessionRepository) Complete(
	ctx context.Context,
	versionRepo *AgentVersionRepository,
	id string,
	promptsJSON []byte,
	trainingReport json.RawMessage,
	summary CompleteSummary,
) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	// Guard status + capture parent_version_id inside the tx.
	var parentVersionID, status string
	if err := tx.QueryRowContext(ctx, `
		SELECT parent_version_id::text, status FROM training_sessions WHERE id = $1::uuid
	`, id).Scan(&parentVersionID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", types.ErrNotFound
		}
		return "", err
	}
	if status != string(types.TrainingSessionStatusInProgress) {
		return "", types.ErrTrainingSessionConflict
	}

	// Fork the READY version using the shared tx-scoped helper.
	newVersionID, err := versionRepo.insertVersionFromPromptsTx(ctx, tx, parentVersionID, promptsJSON, types.AgentStatusReady)
	if err != nil {
		return "", err
	}

	// Update the session row atomically.
	_, err = tx.ExecContext(ctx, `
		UPDATE training_sessions
		SET status = 'DONE',
		    completed_at = now(),
		    new_version_id = $2::uuid,
		    initial_score = $3,
		    final_score = $4,
		    total_epochs_run = $5,
		    stopped_reason = $6,
		    training_report = $7::jsonb,
		    updated_at = now()
		WHERE id = $1::uuid
	`, id, newVersionID, summary.InitialScore, summary.FinalScore, summary.TotalEpochsRun, summary.StoppedReason, []byte(trainingReport))
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return newVersionID, nil
}

// Fail transitions PENDING or IN_PROGRESS → FAILED with the given message.
func (r *TrainingSessionRepository) Fail(ctx context.Context, id, errorMessage string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE training_sessions
		SET status = 'FAILED',
		    completed_at = now(),
		    error_message = $2,
		    updated_at = now()
		WHERE id = $1::uuid AND status IN ('PENDING','IN_PROGRESS')
	`, id, errorMessage)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var s string
		if err := r.db.QueryRowContext(ctx, `SELECT status FROM training_sessions WHERE id = $1::uuid`, id).Scan(&s); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return types.ErrNotFound
			}
			return err
		}
		return types.ErrTrainingSessionConflict
	}
	return nil
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
