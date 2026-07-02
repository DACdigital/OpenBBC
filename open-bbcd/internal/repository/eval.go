package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

// EvalRepository owns evals + eval_sessions.
type EvalRepository struct{ db *sql.DB }

func NewEvalRepository(db *sql.DB) *EvalRepository { return &EvalRepository{db: db} }

const evalColumns = `id::text, agent_version_id::text, dataset_version_id::text, status,
	score, total_criteria, passed_criteria, error_message, aikdm_meta,
	created_at, started_at, completed_at`

func scanEval(s scanner) (*types.Eval, error) {
	e := &types.Eval{}
	var status string
	var score sql.NullFloat64
	var total, passed sql.NullInt64
	var startedAt, completedAt sql.NullTime
	var meta []byte
	if err := s.Scan(
		&e.ID, &e.AgentVersionID, &e.DatasetVersionID, &status,
		&score, &total, &passed, &e.ErrorMessage, &meta,
		&e.CreatedAt, &startedAt, &completedAt,
	); err != nil {
		return nil, err
	}
	e.Status = types.EvalStatus(status)
	if score.Valid {
		v := score.Float64
		e.Score = &v
	}
	if total.Valid {
		v := int(total.Int64)
		e.TotalCriteria = &v
	}
	if passed.Valid {
		v := int(passed.Int64)
		e.PassedCriteria = &v
	}
	if startedAt.Valid {
		t := startedAt.Time
		e.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		e.CompletedAt = &t
	}
	e.AikdmMeta = meta
	return e, nil
}

// Create inserts a PENDING eval. Callers are responsible for validating
// dataset-version-closed / criteria-complete before calling.
func (r *EvalRepository) Create(ctx context.Context, agentVersionID, datasetVersionID string) (*types.Eval, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO evals (agent_version_id, dataset_version_id, status)
		VALUES ($1::uuid, $2::uuid, 'PENDING')
		RETURNING `+evalColumns,
		agentVersionID, datasetVersionID,
	)
	return scanEval(row)
}

func (r *EvalRepository) GetByID(ctx context.Context, id string) (*types.Eval, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+evalColumns+` FROM evals WHERE id = $1::uuid`, id)
	e, err := scanEval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, types.ErrNotFound
	}
	return e, err
}

func (r *EvalRepository) ListAll(ctx context.Context) ([]*types.Eval, error) {
	return r.query(ctx, `SELECT `+evalColumns+` FROM evals ORDER BY created_at DESC`)
}

func (r *EvalRepository) ListByAgentVersion(ctx context.Context, agentVersionID string) ([]*types.Eval, error) {
	return r.query(ctx,
		`SELECT `+evalColumns+` FROM evals WHERE agent_version_id = $1::uuid ORDER BY created_at DESC`,
		agentVersionID,
	)
}

func (r *EvalRepository) ListPendingOrInProgressForPair(ctx context.Context, agentVersionID, datasetVersionID string) ([]*types.Eval, error) {
	return r.query(ctx,
		`SELECT `+evalColumns+` FROM evals
		 WHERE agent_version_id = $1::uuid AND dataset_version_id = $2::uuid
		   AND status IN ('PENDING','IN_PROGRESS')
		 ORDER BY created_at DESC`,
		agentVersionID, datasetVersionID,
	)
}

func (r *EvalRepository) query(ctx context.Context, sqlStr string, args ...any) ([]*types.Eval, error) {
	rows, err := r.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.Eval
	for rows.Next() {
		e, err := scanEval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Start transitions PENDING → IN_PROGRESS and stamps started_at. Returns
// ErrEvalNotPending if the current status is anything else.
func (r *EvalRepository) Start(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE evals SET status='IN_PROGRESS', started_at=now()
		WHERE id = $1::uuid AND status = 'PENDING'
	`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return r.classifyMissingOrConflict(ctx, id, types.ErrEvalNotPending)
	}
	return nil
}

// Submit persists the terminal result. On DONE, inserts every session row
// and stamps score+counts. On FAILED, only stamps status + error_message.
// Refuses if the eval is already DONE or FAILED.
func (r *EvalRepository) Submit(ctx context.Context, id string, result *types.EvalResult) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM evals WHERE id = $1::uuid`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == string(types.EvalStatusDone) || status == string(types.EvalStatusFailed) {
		return types.ErrEvalAlreadyFinal
	}

	if result.Status == types.EvalStatusFailed {
		if _, err := tx.ExecContext(ctx, `
			UPDATE evals
			SET status='FAILED', error_message=$2, aikdm_meta=$3::jsonb, completed_at=now()
			WHERE id = $1::uuid
		`, id, result.ErrorMessage, marshalOrEmpty(result.AikdmMeta)); err != nil {
			return err
		}
		return tx.Commit()
	}

	for _, s := range result.Sessions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO eval_sessions
				(eval_id, session_id, score, total_criteria, passed_criteria, transcript, judgments)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7::jsonb)
		`,
			id, s.SessionID, s.Score, s.TotalCriteria, s.PassedCriteria,
			marshalOrEmpty(s.Transcript), marshalOrEmpty(s.Judgments),
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE evals
		SET status='DONE', score=$2, total_criteria=$3, passed_criteria=$4,
		    aikdm_meta=$5::jsonb, completed_at=now()
		WHERE id = $1::uuid
	`, id, result.Score, result.TotalCriteria, result.PassedCriteria, marshalOrEmpty(result.AikdmMeta)); err != nil {
		return err
	}
	return tx.Commit()
}

// Fail is a script-friendly shortcut for the error-only terminal state.
func (r *EvalRepository) Fail(ctx context.Context, id, errMsg string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM evals WHERE id = $1::uuid`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == string(types.EvalStatusDone) || status == string(types.EvalStatusFailed) {
		return types.ErrEvalAlreadyFinal
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE evals SET status='FAILED', error_message=$2, completed_at=now() WHERE id = $1::uuid
	`, id, errMsg); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *EvalRepository) ListSessions(ctx context.Context, evalID string) ([]*types.EvalSession, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, eval_id::text, session_id::text, score, total_criteria, passed_criteria, transcript, judgments
		FROM eval_sessions WHERE eval_id = $1::uuid ORDER BY score ASC
	`, evalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*types.EvalSession
	for rows.Next() {
		s := &types.EvalSession{}
		var transcript, judgments []byte
		if err := rows.Scan(&s.ID, &s.EvalID, &s.SessionID, &s.Score,
			&s.TotalCriteria, &s.PassedCriteria, &transcript, &judgments); err != nil {
			return nil, err
		}
		s.Transcript = transcript
		s.Judgments = judgments
		out = append(out, s)
	}
	return out, rows.Err()
}

// AverageScoreByAgentVersion returns the plain mean of DONE eval scores
// for the given agent version. Returns 0.0 and hasEvals=false when none
// exist — templates render "—" in that case.
func (r *EvalRepository) AverageScoreByAgentVersion(ctx context.Context, agentVersionID string) (avg float64, count int, err error) {
	err = r.db.QueryRowContext(ctx, `
		SELECT COALESCE(AVG(score), 0), COUNT(*)
		FROM evals
		WHERE agent_version_id = $1::uuid AND status = 'DONE' AND score IS NOT NULL
	`, agentVersionID).Scan(&avg, &count)
	return
}

func (r *EvalRepository) classifyMissingOrConflict(ctx context.Context, id string, conflictErr error) error {
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT status FROM evals WHERE id = $1::uuid`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return types.ErrNotFound
	}
	if err != nil {
		return err
	}
	return conflictErr
}

func marshalOrEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return []byte(raw)
}
